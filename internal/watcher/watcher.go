package watcher

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"review/internal/store"

	"github.com/fsnotify/fsnotify"
)

// Event represents a change detected by the watcher.
type Event struct {
	Type string `json:"type"`           // "file-changed", "review-deleted", "review-reloaded"
	Path string `json:"path,omitempty"` // relative path for file events
}

// Watcher monitors annotated source files and REVIEW.md for changes.
type Watcher struct {
	store      *store.Store
	fsw        *fsnotify.Watcher
	events     chan Event
	done       chan struct{}
	debounce   map[string]*time.Timer
	debounceMu sync.Mutex
	viewed     string // file currently open in the client, relative to the source root
	viewedMu   sync.Mutex
	watched    map[string]bool // directories currently registered with fsnotify
	watchedMu  sync.Mutex
}

// New creates a new file watcher. Call Start() to begin watching.
func New(st *store.Store) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		store:    st,
		fsw:      fsw,
		events:   make(chan Event, 64),
		done:     make(chan struct{}),
		debounce: make(map[string]*time.Timer),
		watched:  make(map[string]bool),
	}

	return w, nil
}

// Events returns the channel of file change events.
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Start begins watching files and processing events.
func (w *Watcher) Start() {
	w.syncWatches()

	// Follow the set of annotated files as it changes
	w.store.OnChange(w.syncWatches)

	go w.loop()
}

// Stop shuts down the watcher.
func (w *Watcher) Stop() {
	close(w.done)
	w.fsw.Close()
}

// WatchFile follows the file a client is currently looking at. Only one such
// file is watched at a time, and only inside the reviewed directory: the path
// comes from the client and is not to be trusted.
func (w *Watcher) WatchFile(relPath string) {
	if !within(w.store.SrcRoot(), relPath) {
		return
	}

	w.viewedMu.Lock()
	w.viewed = relPath
	w.viewedMu.Unlock()

	w.syncWatches()
}

// currentlyViewed returns the file a client last opened.
func (w *Watcher) currentlyViewed() string {
	w.viewedMu.Lock()
	defer w.viewedMu.Unlock()
	return w.viewed
}

// syncWatches makes the set of watched directories match what is needed right
// now: the directory holding REVIEW.md, the directories of annotated files and
// the directory of the file being viewed. Directories that are no longer of
// interest are dropped so watches do not pile up while browsing.
func (w *Watcher) syncWatches() {
	srcRoot := w.store.SrcRoot()
	want := map[string]bool{filepath.Dir(w.store.MdPath()): true}
	for _, relPath := range w.store.AnnotatedFiles() {
		want[filepath.Dir(filepath.Join(srcRoot, relPath))] = true
	}
	if viewed := w.currentlyViewed(); viewed != "" {
		want[filepath.Dir(filepath.Join(srcRoot, viewed))] = true
	}

	w.watchedMu.Lock()
	defer w.watchedMu.Unlock()
	for dir := range w.watched {
		if !want[dir] {
			w.fsw.Remove(dir)
			delete(w.watched, dir)
		}
	}
	for dir := range want {
		if w.watched[dir] {
			continue
		}
		if err := w.fsw.Add(dir); err != nil {
			log.Printf("cannot watch %s: %v", dir, err)
			continue
		}
		w.watched[dir] = true
	}
}

// within reports whether a relative path stays inside root.
func within(root, relPath string) bool {
	if relPath == "" {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Join(root, relPath))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (w *Watcher) loop() {
	mdPath := w.store.MdPath()
	srcRoot := w.store.SrcRoot()

	for {
		select {
		case <-w.done:
			return

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}

			absPath := ev.Name

			// Is this the REVIEW.md file?
			if absPath == mdPath || absPath == mdPath+".tmp" {
				// Ignore .tmp files (our own atomic writes)
				if absPath == mdPath+".tmp" {
					continue
				}
				if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
					// Atomic rename (flush) looks like a Rename event.
					// Check if the file still exists to distinguish real
					// deletion from an atomic replace.
					if _, err := os.Stat(mdPath); err != nil {
						w.emitDebounced(mdPath, Event{Type: "review-deleted"})
					} else {
						w.emitDebounced(mdPath, Event{Type: "review-reloaded"})
					}
				} else if ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) {
					w.emitDebounced(mdPath, Event{Type: "review-reloaded"})
				}
				continue
			}

			// Is this a source file we care about?
			if ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				relPath, err := filepath.Rel(srcRoot, absPath)
				if err != nil {
					continue
				}
				// Check if this file has annotations
				anns := w.store.GetFile(relPath)
				if len(anns) > 0 {
					w.emitDebounced(absPath, Event{Type: "file-changed", Path: relPath})
					continue
				}
				// Check if this is the file the client is looking at
				if relPath == w.currentlyViewed() {
					w.emitDebounced(absPath, Event{Type: "source-changed", Path: relPath})
				}
			}

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

func (w *Watcher) emitDebounced(key string, event Event) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()

	if t, ok := w.debounce[key]; ok {
		t.Stop()
	}

	w.debounce[key] = time.AfterFunc(500*time.Millisecond, func() {
		// For file-changed events, run drift detection first
		if event.Type == "file-changed" {
			drifted := w.store.CheckDrift(event.Path)
			// A client looking at the file needs the new content either way.
			// For any other file there is only something to report once drift
			// has moved an annotation.
			if !drifted && event.Path != w.currentlyViewed() {
				return
			}
		} else if event.Type == "review-reloaded" {
			changed, err := w.store.Reload()
			if err != nil {
				log.Printf("failed to reload REVIEW.md: %v", err)
				return
			}
			if !changed {
				return // our own write, or one that changed nothing
			}
		}

		select {
		case w.events <- event:
		default:
			// Channel full — drop event
		}

		w.debounceMu.Lock()
		delete(w.debounce, key)
		w.debounceMu.Unlock()
	})
}
