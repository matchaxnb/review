package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ContextRadius is the number of source lines kept above and below an
// annotated line to recognise the code again after it moved.
const ContextRadius = 3

// Comment is one message in a line's review thread.
//
// A comment is never edited in place: answering one appends a further comment
// naming it in ReplyTo, so the file and the store both only ever grow. ID is
// the Unix time the comment was written, which orders the file by append and
// gives a reply something stable to name. Deleting a comment marks it as
// deleted — and with it everything answering it — which the next open or close
// of the tool rewrites out of the file.
type Comment struct {
	ID         int64  `json:"id"`
	ReplyTo    int64  `json:"replyTo,omitempty"`    // ID of the comment being answered, 0 for a new thread
	Comment    string `json:"comment"`              // the reviewer's prose, empty for a pure suggestion
	Suggestion string `json:"suggestion,omitempty"` // replacement lines for the range, empty when none
	Deleted    bool   `json:"-"`                    // tombstone: dropped from the file on the next purge
}

// Thread holds the comments written about one line or run of lines, together
// with the source context the thread was taken against.
//
// A thread can cover a range of lines. It is keyed by the last line of that
// range, which is what the line header names, and StartLine holds the first.
// A thread on a single line has StartLine equal to its key, the same shape
// Gerrit's CommentRange takes for a one-line comment.
type Thread struct {
	StartLine   int        `json:"startLine"` // first line of the range (1-based)
	Context     []string   `json:"-"`         // stored context lines (without line-number prefix)
	ContextFrom int        `json:"-"`         // first line number of context block
	Outdated    bool       `json:"outdated"`  // true if context no longer matches source
	Comments    []*Comment `json:"comments"`  // in the order they were appended
}

// StartFor returns the first line a thread covers, given the end line it is
// keyed by. A thread written before ranges were recorded carries no start line,
// and covers the end line alone.
func (t *Thread) StartFor(endLine int) int {
	if t.StartLine >= 1 && t.StartLine <= endLine {
		return t.StartLine
	}
	return endLine
}

// Live returns the comments that still stand, in order. Deleted ones are kept
// only until the next purge, and are never shown or handed out.
func (t *Thread) Live() []*Comment {
	live := make([]*Comment, 0, len(t.Comments))
	for _, c := range t.Comments {
		if !c.Deleted {
			live = append(live, c)
		}
	}
	return live
}

// Covers reports whether the thread spans lineNum.
func (t *Thread) Covers(lineNum, endLine int) bool {
	return lineNum >= t.StartFor(endLine) && lineNum <= endLine
}

// comment returns the thread's comment with the given id, or nil.
func (t *Thread) comment(id int64) *Comment {
	for _, c := range t.Comments {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// equal reports whether two threads hold the same review of the same code.
func (t *Thread) equal(b *Thread) bool {
	if t.StartLine != b.StartLine ||
		t.Outdated != b.Outdated ||
		t.ContextFrom != b.ContextFrom ||
		len(t.Comments) != len(b.Comments) ||
		!slices.Equal(t.Context, b.Context) {
		return false
	}
	for i, c := range t.Comments {
		if !c.equal(b.Comments[i]) {
			return false
		}
	}
	return true
}

// equal reports whether two comments are the same message.
func (c *Comment) equal(b *Comment) bool {
	return c.ID == b.ID &&
		c.ReplyTo == b.ReplyTo &&
		c.Comment == b.Comment &&
		c.Suggestion == b.Suggestion &&
		c.Deleted == b.Deleted
}

// Store holds threads in memory and persists them to REVIEW.md.
type Store struct {
	mdPath   string
	srcRoot  string
	data     map[string]map[int]*Thread
	started  string // date the review was started, as recorded in REVIEW.md
	created  string // UTC time the review was begun, as recorded in REVIEW.md
	modified string // UTC time the review was last written, as recorded in REVIEW.md
	base     string // point in history the review was taken from, from the caller
	mu       sync.RWMutex
	onChange []func()
}

// Load reads the REVIEW.md file (if it exists) and returns a ready Store.
func Load(mdPath, srcRoot string) (*Store, error) {
	abs, err := filepath.Abs(mdPath)
	if err != nil {
		return nil, fmt.Errorf("resolve md path: %w", err)
	}
	srcAbs, err := filepath.Abs(srcRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve src root: %w", err)
	}

	meta, err := parse(abs)
	if err != nil {
		return nil, fmt.Errorf("parse REVIEW.md: %w", err)
	}

	return &Store{
		mdPath:   abs,
		srcRoot:  srcAbs,
		data:     meta.data,
		started:  meta.started,
		created:  meta.created,
		modified: meta.modified,
	}, nil
}

// SetBase records the point in history the review is taken from, so a review
// file left behind can be read back in context. It is refreshed on the next
// write, which happens as soon as an annotation is made.
func (s *Store) SetBase(base string) {
	s.mu.Lock()
	s.base = base
	s.mu.Unlock()
}

// OnChange registers a callback that fires after any mutation (Set/Delete).
func (s *Store) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

// notifyChange runs the registered callbacks. They are collected under the lock
// but called without it, since a callback is free to read the store.
func (s *Store) notifyChange() {
	s.mu.RLock()
	fns := slices.Clone(s.onChange)
	s.mu.RUnlock()

	for _, fn := range fns {
		fn()
	}
}

// AppendComment adds a comment to the thread on a line range and writes the
// review out. A thread on those lines is created if there is none, and its
// surrounding source lines are recorded with it so it can be followed when the
// code later moves. replyTo names the comment being answered, or is 0 for a new
// thread; the reply then lands on the thread that comment belongs to, whatever
// lines the caller named.
//
// The comment's id is its Unix time, which keeps the file in append order and
// gives a reply something to name. Two comments written in the same second take
// the same id, so the second is read back as part of the first; that is why the
// id is not asked to be unique.
func (s *Store) AppendComment(file string, startLine, endLine int, replyTo int64, comment, suggestion string) (int64, error) {
	comment = strings.TrimSpace(comment)
	suggestion = trimSuggestion(suggestion)
	if comment == "" && suggestion == "" {
		return 0, errors.New("comment and suggestion are both empty")
	}

	s.mu.Lock()

	if s.data[file] == nil {
		s.data[file] = make(map[int]*Thread)
	}
	// A reply belongs where the comment it answers is, whatever lines the
	// caller named, so the thread is looked up by that before anything else.
	thread := s.data[file][endLine]
	if replyTo != 0 {
		if from := s.threadOwning(file, replyTo); from != nil {
			thread = from
		}
	}
	if thread == nil {
		thread = &Thread{StartLine: startLine}
		if lines, err := readFileLines(s.srcRoot, file); err == nil {
			thread.Context, thread.ContextFrom = contextAroundRange(lines, startLine, endLine, ContextRadius)
		}
		s.data[file][endLine] = thread
	}

	thread.Comments = append(thread.Comments, &Comment{
		ID:         nextCommentID(s.data[file]),
		ReplyTo:    replyTo,
		Comment:    comment,
		Suggestion: suggestion,
	})
	id := thread.Comments[len(thread.Comments)-1].ID

	err := s.flush()
	s.mu.Unlock()

	if err == nil {
		s.notifyChange()
	}
	return id, err
}

// DeleteComment removes a comment, and everything answering it, from the
// review. The comment and its answers are marked deleted, which takes them off
// the screen at once, and the file is written out with them marked: nothing is
// dropped from a file that is being appended to while the review is open.
//
// The marks are cleared up — the comments rewritten out of the file for good —
// when the tool closes, and when a review holding one is opened. A file left
// behind by a tool that was killed is thus cleaned up by the next one.
func (s *Store) DeleteComment(file string, id int64) error {
	s.mu.Lock()

	changed := false
	if m, ok := s.data[file]; ok {
		for _, thread := range m {
			if thread.comment(id) == nil {
				continue
			}
			markDeleted(thread, id)
			changed = true
		}
	}
	if !changed {
		// Nothing to delete: deleting a comment twice is not an error, so a
		// client that fires the request again is not told it did something
		// wrong.
		s.mu.Unlock()
		return nil
	}

	err := s.flush()
	s.mu.Unlock()

	if err == nil {
		s.notifyChange()
	}
	return err
}

// Purge rewrites the review without the comments that were deleted while it was
// open. It is called when the tool closes, so a tombstone only has to survive
// the session that made it, and when a review holding one is opened, so a file
// left behind by a tool that was killed is cleaned up by the next one.
func (s *Store) Purge() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.purgeLocked()
}

// purgeLocked drops every deleted comment, writing the file only when there was
// one to drop. The caller holds the lock.
func (s *Store) purgeLocked() error {
	if !s.purgeData() {
		return nil
	}
	return s.flush()
}

// purgeData drops the deleted comments and the threads left empty by them, and
// reports whether that changed the review. The caller holds the lock.
func (s *Store) purgeData() bool {
	changed := false
	for file, threads := range s.data {
		for line, thread := range threads {
			// A comment whose parent was deleted goes with it, even when it
			// carries no mark of its own: that is how a review file left
			// behind by a killed tool comes to hold an orphan.
			for _, c := range thread.Comments {
				if c.Deleted {
					markDeleted(thread, c.ID)
				}
			}
			live := thread.Live()
			if len(live) == len(thread.Comments) {
				continue
			}
			changed = true
			if len(live) == 0 {
				delete(threads, line)
				continue
			}
			thread.Comments = live
		}
		if len(threads) == 0 {
			delete(s.data, file)
		}
	}
	return changed
}

// threadOwning returns the thread on file holding the comment with this id, or
// nil. The caller holds the lock.
func (s *Store) threadOwning(file string, id int64) *Thread {
	for _, thread := range s.data[file] {
		if thread.comment(id) != nil {
			return thread
		}
	}
	return nil
}

// markDeleted tombstones a comment and everything answering it, at any depth.
//
// The walk is repeated until it settles rather than recursing: a malformed
// thread — a comment answering itself, or a reply cycle written by hand — must
// not be able to spin here.
func markDeleted(thread *Thread, id int64) {
	for _, c := range thread.Comments {
		if c.ID == id {
			c.Deleted = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, c := range thread.Comments {
			if c.Deleted || c.ReplyTo == 0 {
				continue
			}
			if parent := thread.comment(c.ReplyTo); parent != nil && parent.Deleted {
				c.Deleted = true
				changed = true
			}
		}
	}
}

// nextCommentID returns the id a comment appended to this thread takes: the
// current Unix time, or a step past the last id already handed out in the file
// when that second has one, so ids order a file's comments and stay unique
// within it.
//
// Uniqueness is per file, not global, because that is what the callers need: a
// reply names the comment it answers, and a delete names the comment to drop,
// and both are looked up across the threads of one file. Two comments written
// in the same second in different files may share an id, and nothing reads
// anything into that.
func nextCommentID(threads map[int]*Thread) int64 {
	id := time.Now().Unix()
	var last int64
	for _, thread := range threads {
		for _, c := range thread.Comments {
			if c.ID > last {
				last = c.ID
			}
		}
	}
	if id <= last {
		return last + 1
	}
	return id
}

// trimSuggestion cleans a suggested replacement up: a single trailing newline
// is dropped, so the block is written back exactly as it was read.
func trimSuggestion(s string) string {
	if s == "" {
		return ""
	}
	return strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// GetFile returns a copy of one file's threads, holding only the comments that
// still stand.
func (s *Store) GetFile(file string) map[int]*Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return copyThreads(s.data[file])
}

// All returns a deep copy of every file's threads, holding only the comments
// that still stand.
func (s *Store) All() map[string]map[int]*Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cp := make(map[string]map[int]*Thread, len(s.data))
	for file, threads := range s.data {
		cp[file] = copyThreads(threads)
	}
	return cp
}

// copyThreads deep-copies one file's threads. Deleted comments are left out:
// they are on their way out of the review, and nothing outside the store has
// any use for them.
func copyThreads(threads map[int]*Thread) map[int]*Thread {
	cp := make(map[int]*Thread, len(threads))
	for line, thread := range threads {
		live := thread.Live()
		if len(live) == 0 {
			continue
		}
		t := *thread
		t.Comments = make([]*Comment, len(live))
		for i, c := range live {
			comment := *c
			t.Comments[i] = &comment
		}
		t.Context = slices.Clone(thread.Context)
		cp[line] = &t
	}
	return cp
}

// AnnotatedFiles returns a list of all file paths that have annotations.
func (s *Store) AnnotatedFiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	files := make([]string, 0, len(s.data))
	for f := range s.data {
		files = append(files, f)
	}
	return files
}

// MdPath returns the absolute path to the REVIEW.md file.
func (s *Store) MdPath() string {
	return s.mdPath
}

// SrcRoot returns the absolute path to the source root directory.
func (s *Store) SrcRoot() string {
	return s.srcRoot
}

// Reload re-reads REVIEW.md from disk and replaces in-memory data. It reports
// whether the file differs from what was already in memory, which tells a
// write made by someone else from the store's own write being observed.
//
// It does not clear up the comments marked deleted: a review is appended to
// while it is open, and the marks are written out when the tool closes or when
// the review is next opened (see Purge). Doing it here would rewrite the file
// moments after every delete, which is the rewrite the marks exist to avoid.
func (s *Store) Reload() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := parse(s.mdPath)
	if err != nil {
		return false, fmt.Errorf("parse REVIEW.md: %w", err)
	}
	changed := !equalThreads(s.data, meta.data)
	s.data = meta.data
	s.started = meta.started
	// Adopt the times the file records, so a review written by someone else is
	// read back with its own history rather than this process's. The base is
	// not adopted: it belongs to the invocation, and overwriting it would
	// mislabel the very next write.
	s.created = meta.created
	s.modified = meta.modified
	return changed, nil
}

// Reopen reads REVIEW.md again and brings the review in line with what it now
// holds, which is what the tool does as it starts: the file is parsed afresh,
// the comments deleted since it was last written out are written out for good,
// and every thread is checked against the source it names.
//
// It reports the files drift moved, and whether the file differed from what was
// in memory — a caller that only wants to know whether anything happened can
// ignore both.
//
// A file that cannot be read is left alone rather than emptied: the review in
// hand is better than no review, and the caller is told what went wrong.
func (s *Store) Reopen() (drifted map[string]bool, changed bool, err error) {
	changed, err = s.Reload()
	if err != nil {
		return nil, false, err
	}
	if err := s.Purge(); err != nil {
		return nil, changed, err
	}
	drifted, err = s.CheckAllDrift()
	return drifted, changed, err
}

// equalThreads reports whether two thread maps hold the same review.
func equalThreads(a, b map[string]map[int]*Thread) bool {
	if len(a) != len(b) {
		return false
	}
	for file, aLines := range a {
		bLines, ok := b[file]
		if !ok || len(aLines) != len(bLines) {
			return false
		}
		for line, aThread := range aLines {
			bThread, ok := bLines[line]
			if !ok || !aThread.equal(bThread) {
				return false
			}
		}
	}
	return true
}

// Flush persists current state to REVIEW.md. Exported for use by drift detection.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}

// flush serialises the map and atomically writes REVIEW.md.
func (s *Store) flush() error {
	now := time.Now().UTC()
	if s.started == "" {
		s.started = now.Format(startedFormat)
	}
	if s.created == "" {
		s.created = now.Format(stampFormat)
	}
	s.modified = now.Format(stampFormat)

	info := ReviewInfo{Base: s.base, Created: s.created, Modified: s.modified}
	content := serialize(s.data, s.started, info)
	tmp := s.mdPath + ".tmp"

	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, s.mdPath); err != nil {
		return fmt.Errorf("rename to REVIEW.md: %w", err)
	}
	return nil
}
