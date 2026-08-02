package highlight

import (
	"strings"
	"testing"
)

// TestHighlight_EscapesMarkup ensures file content can never reach the browser
// as markup, whatever the file contains.
func TestHighlight_EscapesMarkup(t *testing.T) {
	for _, name := range []string{"page.html", "notes.txt", "weird.unknownext"} {
		t.Run(name, func(t *testing.T) {
			res := Highlight(name, `<script>alert("x")</script>`)
			if strings.Contains(res.HTML, "<script>") {
				t.Errorf("unescaped markup in output: %s", res.HTML)
			}
			if !strings.Contains(res.HTML, "&lt;") {
				t.Errorf("expected escaped markup, got: %s", res.HTML)
			}
		})
	}
}

// TestPlainResult covers the fallback used when highlighting fails.
func TestPlainResult(t *testing.T) {
	res := plainResult("<b>&</b>")
	if strings.Contains(res.HTML, "<b>") {
		t.Errorf("unescaped markup in fallback: %s", res.HTML)
	}
	if res.Language != "plaintext" {
		t.Errorf("expected plaintext, got %q", res.Language)
	}
}
