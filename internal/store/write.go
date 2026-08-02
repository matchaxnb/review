package store

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2/lexers"
)

// startedFormat is the layout of the review's start date in REVIEW.md.
const startedFormat = "2006-01-02"

// maxLineLength bounds a single line when reading source or review files, so
// that a generated or minified file is still read to the end.
const maxLineLength = 1 << 20

// serialize converts the annotation map to a markdown string. The context
// blocks are written from the context stored with each annotation, which is
// the code as it looked when the annotation was last in sync with the source.
func serialize(data map[string]map[int]*Annotation, started string) string {
	var b strings.Builder

	b.WriteString("# Code Review\n\n")
	b.WriteString(fmt.Sprintf("_Started: %s_\n", started))

	// Sort file paths
	paths := make([]string, 0, len(data))
	for p, lines := range data {
		if len(lines) > 0 {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, filePath := range paths {
		lines := data[filePath]
		if len(lines) == 0 {
			continue
		}

		b.WriteString("\n---\n\n")
		b.WriteString(fmt.Sprintf("## `%s`\n", filePath))

		// Sort line numbers
		lineNums := make([]int, 0, len(lines))
		for n := range lines {
			lineNums = append(lineNums, n)
		}
		sort.Ints(lineNums)

		for _, lineNum := range lineNums {
			ann := lines[lineNum]
			if ann.Outdated {
				b.WriteString(fmt.Sprintf("\n#### Line %d (outdated)\n\n", lineNum))
			} else {
				b.WriteString(fmt.Sprintf("\n#### Line %d\n\n", lineNum))
			}
			b.WriteString(ann.Comment)
			b.WriteString("\n")

			if len(ann.Context) > 0 {
				lang := detectLang(filePath)
				b.WriteString(fmt.Sprintf("\n```%s\n", lang))
				b.WriteString(formatContext(ann.Context, ann.ContextFrom))
				b.WriteString("```\n")
			}
		}
	}

	return b.String()
}

// formatContext renders stored context lines with their line numbers, the form
// they take inside the fenced context block.
func formatContext(lines []string, from int) string {
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d: %s\n", from+i, line)
	}
	return b.String()
}

// contextAround returns the lines surrounding lineNum together with the line
// number the block starts at. Line numbers are 1-based.
func contextAround(lines []string, lineNum, radius int) ([]string, int) {
	if lineNum < 1 || lineNum > len(lines) {
		return nil, 0
	}
	start := lineNum - radius
	if start < 1 {
		start = 1
	}
	end := lineNum + radius
	if end > len(lines) {
		end = len(lines)
	}
	return append([]string(nil), lines[start-1:end]...), start
}

// readFileLines reads all lines from a source file and returns them (0-indexed).
func readFileLines(srcRoot, relPath string) ([]string, error) {
	absPath := filepath.Join(srcRoot, relPath)
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLength)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// detectLang returns a language identifier for the code fence.
func detectLang(filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		return ""
	}
	name := strings.ToLower(lexer.Config().Name)
	// Map common names to short fence tags
	switch name {
	case "go":
		return "go"
	case "python", "python 3":
		return "python"
	case "javascript":
		return "javascript"
	case "typescript":
		return "typescript"
	case "java":
		return "java"
	case "c":
		return "c"
	case "c++":
		return "cpp"
	case "c#":
		return "csharp"
	case "ruby":
		return "ruby"
	case "rust":
		return "rust"
	case "shell", "bash":
		return "bash"
	case "sql":
		return "sql"
	case "html":
		return "html"
	case "css":
		return "css"
	case "json":
		return "json"
	case "yaml":
		return "yaml"
	case "markdown":
		return "markdown"
	default:
		return name
	}
}
