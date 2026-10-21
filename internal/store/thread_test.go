package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newThreadStore returns a store over a small source file, ready for comments.
func newThreadStore(t *testing.T, src string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, "REVIEW.md")
	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	return st, mdPath
}

// TestThread_AppendOnly checks that commenting on the same lines again grows the
// thread rather than replacing what is there, and that the ids order the
// comments the way they were written.
func TestThread_AppendOnly(t *testing.T) {
	st, mdPath := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	first, err := st.AppendComment("a.go", 3, 3, 0, "first thought", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AppendComment("a.go", 3, 3, 0, "on reflection", "")
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Errorf("ids should order the comments: got %d then %d", first, second)
	}

	thread := st.GetFile("a.go")[3]
	if thread == nil || len(thread.Comments) != 2 {
		t.Fatalf("expected both comments in one thread, got %+v", thread)
	}
	if thread.Comments[0].Comment != "first thought" || thread.Comments[1].Comment != "on reflection" {
		t.Errorf("comments did not keep their order: %+v", thread.Comments)
	}

	// The ids are written down, so a reader of the file can tell the comments
	// apart and a reply has something to name.
	written := mustRead(t, mdPath)
	for _, id := range []int64{first, second} {
		if !strings.Contains(written, "@"+itoa(id)) {
			t.Errorf("id %d is not in the review file:\n%s", id, written)
		}
	}
}

// TestThread_Reply checks that a reply lands in the thread the comment it
// answers belongs to, even when it names other lines, and that it records what
// it answers.
func TestThread_Reply(t *testing.T) {
	st, mdPath := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n")

	root, err := st.AppendComment("a.go", 2, 2, 0, "root", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendComment("a.go", 7, 7, root, "a reply on another line", ""); err != nil {
		t.Fatal(err)
	}

	file := st.GetFile("a.go")
	if len(file) != 1 {
		t.Fatalf("a reply should not open a thread of its own, got %d threads", len(file))
	}
	thread := file[2]
	if len(thread.Comments) != 2 {
		t.Fatalf("expected the reply in the root's thread, got %+v", thread.Comments)
	}
	if thread.Comments[1].ReplyTo != root {
		t.Errorf("reply lost what it answers: %+v", thread.Comments[1])
	}
	if !strings.Contains(mustRead(t, mdPath), "_In reply to: "+itoa(root)+"_") {
		t.Errorf("reply marker missing from the file:\n%s", mustRead(t, mdPath))
	}
}

// TestThread_PurgeOnOpen checks the path a killed tool leaves behind: a review
// file still holding a comment deleted while it was open. Opening the review
// takes the comment and its answers out and writes the file back without them,
// which is what the tool does at startup.
func TestThread_PurgeOnOpen(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "REVIEW.md")
	content := "# Code Review\n\n_Started: 2026-01-01_\n\n---\n\n## `a.go`\n\n" +
		"#### Line 2 @100\n\nthe root\n\n" +
		"#### Line 2 @101 (deleted)\n\n_In reply to: 100_\n\ngone\n\n" +
		"#### Line 2 @102\n\n_In reply to: 101_\n\nalso gone\n\n" +
		"#### Line 4 @103\n\nstill here\n"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	// What the tool runs as it starts: the review is read, then cleaned up.
	if err := st.Purge(); err != nil {
		t.Fatal(err)
	}

	thread := st.GetFile("a.go")[2]
	if thread == nil || len(thread.Comments) != 1 || thread.Comments[0].Comment != "the root" {
		t.Fatalf("expected only the root comment to survive, got %+v", thread)
	}
	if _, ok := st.GetFile("a.go")[4]; !ok {
		t.Error("the untouched thread on line 4 was dropped")
	}

	written := mustRead(t, mdPath)
	if strings.Contains(written, "(deleted)") || strings.Contains(written, "also gone") {
		t.Errorf("deleted comments are still in the file:\n%s", written)
	}
	if !strings.Contains(written, "still here") {
		t.Errorf("the purge took a comment that was not deleted:\n%s", written)
	}
}

// TestThread_ReloadKeepsTombstones checks that reading the review back — which
// happens moments after every write, since the tool watches its own file — does
// not clear up the deleted comments. They are written out on close or the next
// open, not mid-session.
func TestThread_ReloadKeepsTombstones(t *testing.T) {
	st, _ := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	id, _ := st.AppendComment("a.go", 2, 2, 0, "to be deleted", "")
	if err := st.DeleteComment("a.go", id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reload(); err != nil {
		t.Fatal(err)
	}

	// Still marked in the file, and still out of the review handed to clients.
	if !strings.Contains(mustRead(t, st.MdPath()), "(deleted)") {
		t.Errorf("the tombstone was written out mid-session:\n%s", mustRead(t, st.MdPath()))
	}
	if len(st.GetFile("a.go")) != 0 {
		t.Errorf("a deleted comment is still handed out, got %+v", st.GetFile("a.go"))
	}
}

// TestThread_DeleteCascades checks that deleting a comment takes everything
// answering it, at any depth, and that a thread left with nothing goes away.
func TestThread_DeleteCascades(t *testing.T) {
	st, _ := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	root, _ := st.AppendComment("a.go", 2, 2, 0, "root", "")
	reply, _ := st.AppendComment("a.go", 2, 2, root, "answer", "")
	if _, err := st.AppendComment("a.go", 2, 2, reply, "answer to the answer", ""); err != nil {
		t.Fatal(err)
	}
	other, _ := st.AppendComment("a.go", 4, 4, 0, "its own thread", "")

	if err := st.DeleteComment("a.go", reply); err != nil {
		t.Fatal(err)
	}

	file := st.GetFile("a.go")
	thread := file[2]
	if thread == nil || len(thread.Comments) != 1 {
		t.Fatalf("expected only the root comment to stand, got %+v", thread)
	}
	if file[4] == nil || file[4].Comments[0].ID != other {
		t.Error("deleting one thread's comment touched another thread")
	}

	// Deleting the root tombstones it too, leaving the thread empty for the
	// purge that writes it out.
	if err := st.DeleteComment("a.go", root); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.GetFile("a.go")[2]; ok {
		t.Error("an emptied thread is still handed out")
	}
	if _, ok := st.GetFile("a.go")[4]; !ok {
		t.Error("the untouched thread was dropped")
	}

	// The tombstones are in the file as marks, not as plain comments: a review
	// that is being appended to keeps what it recorded, and the next purge —
	// closing the tool, or opening the review — takes them out.
	marked := mustRead(t, st.MdPath())
	if !strings.Contains(marked, "(deleted)") {
		t.Errorf("deleted comments were not written as tombstones:\n%s", marked)
	}

	// Purging writes them out, and only them.
	if err := st.Purge(); err != nil {
		t.Fatal(err)
	}
	if written := mustRead(t, st.MdPath()); strings.Contains(written, "root") || strings.Contains(written, "(deleted)") {
		t.Errorf("the deleted comments are still in the file:\n%s", written)
	}
	if written := mustRead(t, st.MdPath()); !strings.Contains(written, "its own thread") {
		t.Errorf("the purge took a comment that was not deleted:\n%s", written)
	}
}

// TestThread_DeleteTwiceIsQuiet checks that a repeated delete — a client firing
// the request again — is not an error, so the second one cannot take a live
// comment with it.
func TestThread_DeleteTwiceIsQuiet(t *testing.T) {
	st, _ := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	id, _ := st.AppendComment("a.go", 2, 2, 0, "to be deleted", "")
	keeper, _ := st.AppendComment("a.go", 2, 2, 0, "to be kept", "")

	if err := st.DeleteComment("a.go", id); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteComment("a.go", id); err != nil {
		t.Fatalf("deleting twice should be quiet: %v", err)
	}
	thread := st.GetFile("a.go")[2]
	if thread == nil || len(thread.Comments) != 1 || thread.Comments[0].ID != keeper {
		t.Fatalf("the repeated delete took a live comment with it: %+v", thread)
	}
}

// TestThread_SameSecond checks that two comments written in the same second
// still come back in the order they were written, which is all the id is asked
// to do.
func TestThread_SameSecond(t *testing.T) {
	st, mdPath := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	first, err := st.AppendComment("a.go", 2, 2, 0, "one", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AppendComment("a.go", 2, 2, 0, "two", "")
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(mdPath, st.SrcRoot())
	if err != nil {
		t.Fatal(err)
	}
	thread := reloaded.GetFile("a.go")[2]
	if thread == nil || len(thread.Comments) != 2 {
		t.Fatalf("expected both comments back, got %+v", thread)
	}
	if thread.Comments[0].ID != first || thread.Comments[1].ID != second {
		t.Errorf("ids were not read back as written: got %d, %d; want %d, %d",
			thread.Comments[0].ID, thread.Comments[1].ID, first, second)
	}
}

// TestThread_Suggestion checks that a suggested replacement survives a write and
// read, both on its own and under a comment, and that the comment's prose is not
// mistaken for code.
func TestThread_Suggestion(t *testing.T) {
	st, mdPath := newThreadStore(t, "l1\nl2\nl3\nl4\nl5\n")

	suggestion := "l2 replaced\nl2a extra"
	if _, err := st.AppendComment("a.go", 2, 2, 0, "this needs to be two lines", suggestion); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendComment("a.go", 4, 4, 0, "", "just the code"); err != nil {
		t.Fatal(err)
	}

	written := mustRead(t, mdPath)
	if !strings.Contains(written, "```suggestion\n"+suggestion+"\n```") {
		t.Errorf("suggested replacement not written as its own block:\n%s", written)
	}

	reloaded, err := Load(mdPath, st.SrcRoot())
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.GetFile("a.go")[2]
	if got == nil || len(got.Comments) != 1 {
		t.Fatalf("thread lost: %+v", got)
	}
	if got.Comments[0].Comment != "this needs to be two lines" {
		t.Errorf("comment prose changed: %q", got.Comments[0].Comment)
	}
	if got.Comments[0].Suggestion != suggestion {
		t.Errorf("suggestion changed:\n got: %q\nwant: %q", got.Comments[0].Suggestion, suggestion)
	}

	// A pure suggestion carries no prose, and is not read as an empty comment.
	bare := reloaded.GetFile("a.go")[4]
	if bare == nil || len(bare.Comments) != 1 || bare.Comments[0].Suggestion != "just the code" {
		t.Fatalf("a suggestion on its own was lost: %+v", bare)
	}
	if bare.Comments[0].Comment != "" {
		t.Errorf("a pure suggestion grew prose: %q", bare.Comments[0].Comment)
	}
}

// TestThread_LegacyFileNumbersComments checks that a review written before
// comments were numbered — every comment in the file, including several on one
// line — is still read, with ids handed out so the next write numbers it.
func TestThread_LegacyFileNumbersComments(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "REVIEW.md")
	content := "# Code Review\n\n_Started: 2026-01-01_\n\n---\n\n## `a.go`\n\n" +
		"#### Line 2\n\nfirst comment\n\n" +
		"#### Line 2\n\nsecond comment\n\n" +
		"#### Lines 4-5\n\na range\n"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	thread := st.GetFile("a.go")[2]
	if thread == nil || len(thread.Comments) != 2 {
		t.Fatalf("expected both comments in the thread, got %+v", thread)
	}
	if thread.Comments[0].ID == 0 || thread.Comments[0].ID >= thread.Comments[1].ID {
		t.Errorf("legacy comments were not numbered in order: %+v", thread.Comments)
	}
	if got := st.GetFile("a.go")[5]; got == nil || got.StartLine != 4 {
		t.Errorf("legacy range heading lost: %+v", got)
	}

	// A comment appended now takes an id past the ones just handed out.
	id, err := st.AppendComment("a.go", 2, 2, 0, "a new thought", "")
	if err != nil {
		t.Fatal(err)
	}
	if id <= thread.Comments[1].ID {
		t.Errorf("new comment id %d does not follow the legacy ids", id)
	}
}

// itoa renders an int64 for the assertions that look for an id in the file.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
