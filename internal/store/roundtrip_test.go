package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoundTrip_LongLines ensures files with very long lines, such as minified
// or generated code, can still be annotated and followed.
func TestRoundTrip_LongLines(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 200_000)
	if err := os.WriteFile(filepath.Join(dir, "bundle.js"), []byte("first\n"+long+"\nlast\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, "REVIEW.md")

	st, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("bundle.js", 2, "this line is huge"); err != nil {
		t.Fatal(err)
	}
	if got := len(st.data["bundle.js"][2].Context); got != 3 {
		t.Errorf("expected 3 context lines, got %d", got)
	}

	reloaded, err := Load(mdPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	ann := reloaded.data["bundle.js"][2]
	if ann == nil {
		t.Fatal("annotation lost")
	}
	if len(ann.Context) != 3 || ann.Context[1] != long {
		t.Errorf("long context line not read back (%d lines)", len(ann.Context))
	}
}
