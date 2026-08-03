package highlight

import (
	"strings"
	"testing"
)

func TestCacheReusesResult(t *testing.T) {
	c := NewCache(1 << 20)

	first := c.Highlight("a.go", "package main\n")
	second := c.Highlight("a.go", "package main\n")

	if first.HTML != second.HTML || first.Language != second.Language {
		t.Error("cached result differs from the first one")
	}
	if len(c.entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(c.entries))
	}
}

func TestCacheSeesChangedContent(t *testing.T) {
	c := NewCache(1 << 20)

	before := c.Highlight("a.go", "package main\n")
	after := c.Highlight("a.go", "package other\n")

	if before.HTML == after.HTML {
		t.Error("changed content served the cached result")
	}
	if len(c.entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(c.entries))
	}
}

func TestCacheKeepsSameContentPerFile(t *testing.T) {
	c := NewCache(1 << 20)

	// The same text highlights differently depending on the file's language
	goRes := c.Highlight("a.go", "package main\n")
	txtRes := c.Highlight("a.txt", "package main\n")

	if goRes.Language == txtRes.Language {
		t.Errorf("both files highlighted as %q", goRes.Language)
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	// The bodies all highlight to the same size, so a limit of two and a half
	// of them holds exactly two entries
	one := len(Highlight("a.go", body("aa")).HTML)
	c := NewCache(2*one + one/2)

	c.Highlight("a.go", body("aa"))
	c.Highlight("b.go", body("bb"))
	c.Highlight("a.go", body("aa")) // makes a.go the recent one again
	c.Highlight("c.go", body("cc"))

	if _, ok := c.entries[contentKey("b.go", body("bb"))]; ok {
		t.Error("least recently used entry was kept")
	}
	for file, name := range map[string]string{"a.go": "aa", "c.go": "cc"} {
		if _, ok := c.entries[contentKey(file, body(name))]; !ok {
			t.Errorf("%s was dropped", file)
		}
	}
	if c.size > c.limit {
		t.Errorf("cache holds %d bytes over its limit of %d", c.size, c.limit)
	}
}

// TestCacheKeepsAnOversizedEntry verifies that a file larger than the whole
// cache is still served from it, since it is the one being looked at.
func TestCacheKeepsAnOversizedEntry(t *testing.T) {
	c := NewCache(16)

	c.Highlight("a.go", body("aa"))

	if len(c.entries) != 1 {
		t.Errorf("expected the entry to be kept, got %d entries", len(c.entries))
	}
}

// body returns a chunk of Go source that is distinct per name but always of the
// same length.
func body(name string) string {
	return "package main\n\n" + strings.Repeat("var "+name+" = 1\n", 20)
}
