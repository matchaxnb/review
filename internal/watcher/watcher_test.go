package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"review/internal/store"
)

// newTestWatcher sets up a store and a running watcher over a temporary tree.
func newTestWatcher(t *testing.T) (*store.Store, *Watcher, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("l1\nl2\nl3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Load(filepath.Join(dir, "REVIEW.md"), dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	t.Cleanup(w.Stop)
	time.Sleep(100 * time.Millisecond) // let the watch settle

	return st, w, dir
}

// TestOwnWriteIsNotReported verifies that saving a comment does not come back
// as an external change to REVIEW.md.
func TestOwnWriteIsNotReported(t *testing.T) {
	st, w, _ := newTestWatcher(t)

	if err := st.Set("a.go", 2, "a comment"); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		t.Errorf("own write reported as %q", ev.Type)
	case <-time.After(1500 * time.Millisecond):
	}
}

// TestForeignWriteIsReported verifies that a change made outside the tool is
// still picked up.
func TestForeignWriteIsReported(t *testing.T) {
	_, w, dir := newTestWatcher(t)

	content := "# Code Review\n\n_Started: 2020-01-01_\n\n---\n\n## `a.go`\n\n#### Line 2\n\n> edited by hand\n"
	if err := os.WriteFile(filepath.Join(dir, "REVIEW.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		if ev.Type != "review-reloaded" {
			t.Errorf("expected review-reloaded, got %q", ev.Type)
		}
	case <-time.After(3 * time.Second):
		t.Error("external change to REVIEW.md was not reported")
	}
}
