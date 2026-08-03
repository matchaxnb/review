package safepath

import (
	"path/filepath"
	"testing"
)

func TestResolveAccepts(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "review")

	cases := map[string]string{
		"main.go":              filepath.Join(root, "main.go"),
		"src/app/main.go":      filepath.Join(root, "src", "app", "main.go"),
		"[...slug].astro":      filepath.Join(root, "[...slug].astro"),
		"a..b/c..d.go":         filepath.Join(root, "a..b", "c..d.go"),
		"./main.go":            filepath.Join(root, "main.go"),
		"src/../main.go":       filepath.Join(root, "main.go"),
		"src/nested/../app.go": filepath.Join(root, "src", "app.go"),
	}

	for path, want := range cases {
		got, ok := Resolve(root, path)
		if !ok {
			t.Errorf("%q was rejected", path)
			continue
		}
		if got != want {
			t.Errorf("%q resolved to %q, want %q", path, got, want)
		}
	}
}

func TestResolveRejects(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "review")

	for _, path := range []string{
		"",
		"..",
		"../outside.go",
		"../../etc/passwd",
		"src/../../outside.go",
	} {
		if got, ok := Resolve(root, path); ok {
			t.Errorf("%q was accepted as %q", path, got)
		}
	}
}

// TestResolveContainsAbsolutePath verifies that an absolute path is taken as
// relative to the root rather than as a way out of it.
func TestResolveContainsAbsolutePath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "review")
	abs := filepath.Join(string(filepath.Separator), "etc", "passwd")

	got, ok := Resolve(root, abs)
	if !ok {
		t.Fatalf("%q was rejected", abs)
	}
	if want := filepath.Join(root, "etc", "passwd"); got != want {
		t.Errorf("%q resolved to %q, want %q", abs, got, want)
	}
}
