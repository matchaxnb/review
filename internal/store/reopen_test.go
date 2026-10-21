package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReopen covers what the tool does when it re-reads the review: a file
// edited outside it is adopted, a comment deleted since it was last written is
// written out for good, and the threads are checked against the source, which
// is a restart in everything but the process.
func TestReopen(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "a.go")
	if err := os.WriteFile(srcPath, []byte("l1\nl2\nl3\nl4\nl5\nl6\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, "REVIEW.md")

	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := st.AppendComment("a.go", 2, 2, 0, "this one will be deleted", "")
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := st.AppendComment("a.go", 4, 4, 0, "kept", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteComment("a.go", doomed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustRead(t, mdPath), "(deleted)") {
		t.Fatal("expected a tombstone in the file before the reopen")
	}

	// Outside the tool: the file is edited and the source the kept comment was
	// taken against moves, which is the drift half of a restart.
	editFile(t, mdPath, func(s string) string {
		return s + "\n#### Line 6 @" + "1000000000" + "\n\nwritten by hand\n"
	})
	if err := os.WriteFile(srcPath, []byte("new\nl1\nl2\nl3\nl4\nl5\nl6\n"), 0644); err != nil {
		t.Fatal(err)
	}

	drifted, changed, err := st.Reopen()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected the hand-written comment to be seen as a change")
	}
	if len(drifted) == 0 {
		t.Error("expected drift to be detected in a.go")
	}

	file := st.GetFile("a.go")
	if _, ok := file[2]; ok {
		t.Error("the deleted comment is still in the review")
	}
	if c, ok := file[6]; !ok || c.Comments[0].Comment != "written by hand" {
		t.Errorf("the hand-written comment was not adopted: %+v", file)
	}
	// The kept comment moved down one line with the insertion, so the file it
	// names is the one it was recognisable at.
	var found bool
	for line, thread := range file {
		if thread.Comments[0].ID == keeper && line == 5 {
			found = true
		}
	}
	if !found {
		t.Errorf("the kept comment did not follow its code: %+v", file)
	}

	// The tombstone is gone from the file, and what was written by hand stands.
	written := mustRead(t, mdPath)
	if strings.Contains(written, "(deleted)") {
		t.Errorf("the deleted comment is still in the file:\n%s", written)
	}
	if !strings.Contains(written, "written by hand") {
		t.Errorf("the hand-written comment was lost:\n%s", written)
	}
}

// TestReopen_BadFileKeepsTheReview checks that a review file that cannot be
// read leaves the review in hand alone: a running tool is not emptied because
// one read failed.
func TestReopen_BadFileKeepsTheReview(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "REVIEW.md")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("l1\nl2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendComment("a.go", 1, 1, 0, "still here", ""); err != nil {
		t.Fatal(err)
	}

	// A directory where the review file should be: opening it fails.
	if err := os.Remove(mdPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mdPath, 0755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := st.Reopen(); err == nil {
		t.Error("expected the failed read to be reported")
	}
	if got := st.GetFile("a.go"); len(got) != 1 {
		t.Errorf("the review in hand was lost to a failed read: %+v", got)
	}
}

// editFile rewrites a file through the given edit.
func editFile(t *testing.T, path string, edit func(string) string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(content))), 0644); err != nil {
		t.Fatal(err)
	}
}
