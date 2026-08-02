package highlight

import (
	"bytes"
	stdhtml "html"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// CSS returns the Chroma CSS classes for syntax highlighting.
//
// The light and dark GitHub palettes are each scoped to a prefers-color-scheme
// media query so exactly one is active at a time. This mirrors the page's
// automatic dark mode and prevents light-theme token colors (which set explicit
// dark text colors) from leaking onto the dark background, where tokens that the
// dark theme leaves at its base color would otherwise stay unreadable.
func CSS() string {
	var buf bytes.Buffer
	buf.WriteString("@media (prefers-color-scheme: light) {\n")
	buf.WriteString(styleCSS("github"))
	buf.WriteString("\n}\n")
	buf.WriteString("@media (prefers-color-scheme: dark) {\n")
	buf.WriteString(styleCSS("github-dark"))
	buf.WriteString("\n}\n")
	return buf.String()
}

// styleCSS returns the class-based Chroma CSS for the named style, falling back
// to the built-in default when the style is unknown.
func styleCSS(name string) string {
	style := styles.Get(name)
	if style == nil {
		style = styles.Fallback
	}
	formatter := html.New(html.WithClasses(true))
	var buf bytes.Buffer
	formatter.WriteCSS(&buf, style)
	return buf.String()
}

// Result contains the highlighted HTML and detected language.
type Result struct {
	HTML     string `json:"html"`
	Language string `json:"language"`
}

// Highlight returns syntax-highlighted HTML for the given file content.
func Highlight(filename, content string) Result {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	lang := lexer.Config().Name

	formatter := html.New(
		html.WithLineNumbers(true),
		html.WithLinkableLineNumbers(true, "L"),
		html.WithClasses(true),
		html.WrapLongLines(true),
	)

	style := styles.Get("github")
	if style == nil {
		style = styles.Fallback
	}

	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return plainResult(content)
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return plainResult(content)
	}

	return Result{HTML: buf.String(), Language: lang}
}

// plainResult renders content as escaped, unhighlighted HTML. It is used when
// the file cannot be tokenised or formatted, where returning the source as it
// is would let the browser interpret it as markup.
func plainResult(content string) Result {
	return Result{
		HTML:     "<pre>" + stdhtml.EscapeString(content) + "</pre>",
		Language: "plaintext",
	}
}
