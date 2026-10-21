package gitstatus

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// blameRepo builds a repository with two commits touching one file, so that
// blame has to tell their lines apart.
func blameRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Each commit is dated a day after the one before it, so "newest" has an
	// answer to give.
	run := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	run("2026-01-01T10:00:00+02:00", "git", "init", "-q")
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("one\ntwo\n")
	run("2026-01-01T10:00:00+02:00", "git", "add", "a.go")
	run("2026-01-01T10:00:00+02:00", "git", "commit", "-q", "-m", "the first commit")

	write("one\ntwo\nthree\n")
	run("2026-01-02T10:00:00+02:00", "git", "add", "a.go")
	run("2026-01-02T10:00:00+02:00", "git", "commit", "-q", "-m", "add a third line")
	return dir
}

// TestBlameLines_NamesTheCommitPerLine checks that each line is attributed to
// the commit that last touched it, with the subject that goes with it.
func TestBlameLines_NamesTheCommitPerLine(t *testing.T) {
	dir := blameRepo(t)

	lines, err := BlameLines(dir, "", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 blamed lines, got %d: %v", len(lines), lines)
	}

	if got := lines[1].Subject; got != "the first commit" {
		t.Errorf("line 1 blamed on %q, want the first commit", got)
	}
	if got := lines[3].Subject; got != "add a third line" {
		t.Errorf("line 3 blamed on %q, want the commit that added it", got)
	}
	if lines[1].Commit == lines[3].Commit {
		t.Error("the two commits were not told apart")
	}
	if lines[3].Short == "" || len(lines[3].Short) >= len(lines[3].Commit) {
		t.Errorf("short hash not abbreviated: %q", lines[3].Short)
	}
	if got := lines[3].Author; got != "Ada" {
		t.Errorf("author: got %q, want Ada", got)
	}
	if got := lines[3].Date; got != "2026-01-02T10:00:00+02:00" {
		t.Errorf("date: got %q, want it in the author's own zone", got)
	}
}

// TestBlameLines_Uncommitted checks that a line only in the working tree is
// reported as such rather than as part of the last commit.
func TestBlameLines_Uncommitted(t *testing.T) {
	dir := blameRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := BlameLines(dir, "", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	got := lines[4]
	if got == nil {
		t.Fatal("the new line has no blame")
	}
	if !got.Uncommitted {
		t.Errorf("a working-tree line should read as uncommitted, got %+v", got)
	}
	if got.Commit != "" {
		t.Errorf("an uncommitted line should name no commit, got %q", got.Commit)
	}
}

// TestBlameCache_Memoises checks that the second read of a file is served from
// the cache, and that editing or committing the file invalidates it rather than
// handing out the blame taken before the change.
func TestBlameCache_Memoises(t *testing.T) {
	dir := blameRepo(t)
	cache := NewBlameCache()

	first := cache.Get(dir, "", "a.go")
	if len(first.Lines) != 3 {
		t.Fatalf("expected 3 blamed lines, got %d", len(first.Lines))
	}
	// The very same blame instance: the memo answered without running git again.
	if again := cache.Get(dir, "", "a.go"); again != first {
		t.Fatal("expected the cached blame back")
	}

	// A file that grew is blamed again, so the new line is in the answer.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := len(cache.Get(dir, "", "a.go").Lines); got != 4 {
		t.Errorf("after an edit: got %d lines, want 4 (stale blame served)", got)
	}
}

// TestBlameCache_PerRevision checks that the same file blamed at two revisions
// does not answer both from one entry.
func TestBlameCache_PerRevision(t *testing.T) {
	dir := blameRepo(t)
	cache := NewBlameCache()

	head := cache.Get(dir, "", "a.go")
	if len(head.Lines) != 3 {
		t.Fatalf("working tree: expected 3 lines, got %d", len(head.Lines))
	}
	// The parent of HEAD has one line fewer.
	parent := ParentOf(dir, "HEAD")
	at := cache.Get(dir, parent, "a.go")
	if len(at.Lines) != 2 {
		t.Fatalf("at the parent revision: expected 2 lines, got %d", len(at.Lines))
	}
	if len(cache.Get(dir, "", "a.go").Lines) != 3 {
		t.Error("blaming a revision disturbed the working tree's entry")
	}
}

// TestBlameLines_UntrackedFile checks that a file git does not know is reported
// as an error rather than as an empty blame, so the caller can leave the diff
// undecorated.
func TestBlameLines_UntrackedFile(t *testing.T) {
	dir := blameRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := BlameLines(dir, "", "new.go"); err == nil {
		t.Error("expected an error blaming an untracked file")
	}
	// The cache turns that into "no blame", which the caller can cope with.
	if got := NewBlameCache().Get(dir, "", "new.go"); len(got.Lines) != 0 {
		t.Errorf("expected no blame for an untracked file, got %v", got.Lines)
	}
}

// TestBlame_Newest checks that a hunk is attributed to the commit that most
// recently touched it, and that working-tree lines are passed over.
func TestBlame_Newest(t *testing.T) {
	dir := blameRepo(t)
	cache := NewBlameCache()
	blame := cache.Get(dir, "", "a.go")

	// Lines 1-2 are from the first commit, line 3 from the second.
	if got := blame.Newest([]int{1, 2}); got == nil || got.Subject != "the first commit" {
		t.Errorf("lines 1-2: got %+v, want the first commit", got)
	}
	if got := blame.Newest([]int{1, 2, 3}); got == nil || got.Subject != "add a third line" {
		t.Errorf("lines 1-3: got %+v, want the newest of them", got)
	}

	// A line that is only in the working tree names no commit, so a hunk made
	// of one has nothing to attribute it to.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	blame = cache.Get(dir, "", "a.go")
	if got := blame.Newest([]int{4}); got != nil {
		t.Errorf("an uncommitted-only hunk should have no commit, got %+v", got)
	}
}
