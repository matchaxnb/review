package gitstatus

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a git repository with one committed file.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	return root
}

// TestGet_UntrackedInNewDirectory verifies that files in a directory git does
// not know about yet are reported individually, the way the file tree lists
// them.
func TestGet_UntrackedInNewDirectory(t *testing.T) {
	root := initRepo(t)

	if err := os.MkdirAll(filepath.Join(root, "newdir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"newdir/a.go", "newdir/sub/b.go"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	statuses := Get(root, Base{})
	if statuses == nil {
		t.Fatal("expected a status map")
	}
	for _, rel := range []string{"newdir/a.go", "newdir/sub/b.go"} {
		if got := statuses[rel]; got != StatusUntracked {
			t.Errorf("expected %q to be untracked, got %q (map: %v)", rel, got, statuses)
		}
	}
}

// TestGet_WorkingTreeStates covers the ordinary statuses of tracked files.
func TestGet_WorkingTreeStates(t *testing.T) {
	root := initRepo(t)

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statuses := Get(root, Base{})
	if got := statuses["tracked.txt"]; got != StatusModified {
		t.Errorf("expected tracked.txt to be modified, got %q", got)
	}
}
