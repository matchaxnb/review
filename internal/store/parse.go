package store

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// contextMarker appears in the info string of a context block's fence and
// tells it apart from a code sample written in the comment above it.
const contextMarker = "context"

// suggestionMarker is the info string of the fence around a suggested
// replacement. It is a word of its own rather than a marker beside the file's
// language, so a suggestion reads like the same block does in Gerrit and in
// GitHub's suggestion comments.
const suggestionMarker = "suggestion"

var (
	// Every form of line heading carries the id of the comment it introduces
	// after the @. A comment of one line reads "#### Line 42 @1729532520", a
	// range "#### Lines 10-20 @1729532520", and a deleted one — which only
	// survives in a file left behind by a tool that was killed — adds the
	// (outdated) and (deleted) marks to either. One pattern covers the four,
	// so the two heading forms never drift apart.
	lineHeadingRe = regexp.MustCompile(`^#### Lines? (\d+)(?:-(\d+))?(?: @(\d+))?(.*)$`)

	// Existing marks on a heading, read back off the trailing text.
	outdatedMarkRe = regexp.MustCompile(`\(outdated\)`)
	deletedMarkRe  = regexp.MustCompile(`\(deleted\)`)

	// replyLineRe matches the line naming the comment an answer replies to. It
	// is written like the review's own header fields, and is the one such line
	// a comment may begin with without being escaped.
	replyLineRe = regexp.MustCompile(`^_In reply to: (\d+)_$`)

	fileHeaderRe  = regexp.MustCompile("^## `(.+)`$")
	contextLineRe = regexp.MustCompile(`^(\d+):(?: (.*))?$`)
	startedRe     = regexp.MustCompile(`^_Started: (.+)_$`)
	baseRe        = regexp.MustCompile(`^_Base: (.+)_$`)
	createdRe     = regexp.MustCompile(`^_Created: (.+)_$`)
	modifiedRe    = regexp.MustCompile(`^_Modified: (.+)_$`)
	fenceRe       = regexp.MustCompile("^(`{3,}|~{3,})")
)

// lineHeaderRe matches either form of line heading, so a structural check
// covers both without repeating them.
var lineHeaderRe = regexp.MustCompile(`^#### Lines? `)

// reviewMeta is what a REVIEW.md records about the review itself: where it was
// taken from and when it was begun and last written. All fields are empty for
// a file that records none of them.
type reviewMeta struct {
	data     map[string]map[int]*Thread
	started  string
	base     string
	created  string
	modified string
}

// parse reads a REVIEW.md file and returns the threads together with the
// review's own record, which is empty for a file that carries none.
//
// Comments are kept as the reviewer wrote them, so the document's own
// structure is only recognised outside fenced code blocks: a heading in a
// comment's code sample is part of the comment. A horizontal rule separates
// sections only where a file heading follows it.
func parse(path string) (reviewMeta, error) {
	lines, err := readLines(path)
	if err != nil {
		if os.IsNotExist(err) {
			return reviewMeta{data: make(map[string]map[int]*Thread)}, nil
		}
		return reviewMeta{}, err
	}

	data := make(map[string]map[int]*Thread)

	// A document that marks its context blocks is read strictly, so a code
	// sample closing a comment is never taken for one. Files written before
	// the marker carry none; there the numbered source lines identify it.
	marked := usesContextMarker(lines)

	var (
		meta        reviewMeta
		currentFile string
		currentLine int
		startLine   int
		hasHeading  bool
		outdated    bool
		deleted     bool
		replyTo     int64
		body        []string
		collecting  bool
		fence       string

		// Comment ids walk upwards as the file is read, so a file whose
		// headings name no id — every review written before comments were
		// numbered — still comes back with them ordered, and the store has an
		// id to hand out next. A heading that names one keeps it: renumbering
		// a comment on every read would rewrite the whole file each time.
		lastID int64
		thisID int64
	)

	// save turns the lines collected since the last heading into a comment and
	// appends it to the thread on the lines the heading named. A heading with
	// no comment under it leaves the review as it was, which is what an
	// (outdated) mark standing alone in an older file comes down to.
	save := func() {
		if !collecting {
			return
		}
		comment, context, contextFrom, suggestion := splitBody(body, marked)
		if (comment != "" || suggestion != "") && currentFile != "" && hasHeading {
			t := data[currentFile][currentLine]
			if t == nil {
				t = &Thread{StartLine: startLine}
				data[currentFile][currentLine] = t
			}
			if contextFrom > 0 {
				t.Context, t.ContextFrom = context, contextFrom
			}
			if outdated {
				t.Outdated = true
			}
			// A heading that named an id keeps it; one that did not — every
			// comment in a review written before they were numbered — takes
			// the next in order, so the file comes back numbered.
			id := thisID
			if id == 0 {
				id = lastID + 1
			}
			if id > lastID {
				lastID = id
			}
			t.Comments = append(t.Comments, &Comment{
				ID:         id,
				ReplyTo:    replyTo,
				Comment:    comment,
				Suggestion: suggestion,
				Deleted:    deleted,
			})
		}
		body = nil
		collecting = false
	}

	for i, line := range lines {
		// Nothing inside a code fence is structure
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			if collecting {
				body = append(body, line)
			}
			continue
		}
		if marker := fenceMarker(line); marker != "" {
			fence = marker
			if collecting {
				body = append(body, line)
			}
			continue
		}

		if m := lineHeadingRe.FindStringSubmatch(line); m != nil && currentFile != "" {
			save()
			startLine, _ = strconv.Atoi(m[1])
			currentLine = startLine
			if m[2] != "" {
				currentLine, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				thisID, _ = strconv.ParseInt(m[3], 10, 64)
			} else {
				thisID = 0
			}
			outdated = outdatedMarkRe.MatchString(m[4])
			deleted = deletedMarkRe.MatchString(m[4])
			replyTo = 0
			hasHeading = true
			collecting = true
			continue
		}
		if m := fileHeaderRe.FindStringSubmatch(line); m != nil {
			save()
			currentFile = m[1]
			if data[currentFile] == nil {
				data[currentFile] = make(map[int]*Thread)
			}
			hasHeading = false
			continue
		}
		if strings.TrimSpace(line) == "---" && opensFileSection(lines[i+1:]) {
			save()
			hasHeading = false
			continue
		}

		if !collecting {
			readMeta(&meta, strings.TrimSpace(line))
			continue
		}
		if !hasText(body) {
			// Only the first line of a comment can be a reply marker: the id it
			// names is what puts the comment in the thread it belongs to, and a
			// reply has no context block or suggestion of its own. Blank lines
			// between the heading and the marker are skipped, since the marker
			// is written as its own paragraph.
			if m := replyLineRe.FindStringSubmatch(line); m != nil {
				replyTo, _ = strconv.ParseInt(m[1], 10, 64)
				continue
			}
		}
		body = append(body, unescapeStructure(line))
	}
	save()

	meta.data = data
	return meta, nil
}

// readMeta records a metadata line, of the form _Name: value_. Each field is
// kept at the first value seen, so the document's header wins over anything
// similar that follows.
func readMeta(meta *reviewMeta, line string) {
	for _, f := range []struct {
		re   *regexp.Regexp
		into *string
	}{
		{startedRe, &meta.started},
		{baseRe, &meta.base},
		{createdRe, &meta.created},
		{modifiedRe, &meta.modified},
	} {
		if *f.into != "" {
			continue
		}
		if m := f.re.FindStringSubmatch(line); m != nil {
			*f.into = m[1]
		}
	}
}

// splitBody separates a comment's prose from the blocks that follow it: the
// suggested replacement, when the comment carries one, and the source context
// block that closes it. Context comes back as the lines themselves together
// with the line number the block starts at, and 0 when there is none.
//
// With marked set, only a fence carrying the context marker counts as a
// context block; otherwise a closing block of numbered source lines does.
func splitBody(body []string, marked bool) (comment string, context []string, from int, suggestion string) {
	end := len(body)
	for end > 0 && strings.TrimSpace(body[end-1]) == "" {
		end--
	}

	open, close, ok := lastFencedBlock(body[:end])
	if ok && close == end-1 && (!marked || isContextFence(body[open])) {
		if ctx, at, ok := parseContext(body[open+1 : close]); ok {
			context, from = ctx, at
			end = open
		}
	}

	// What is left may end in a suggested replacement, which the comment's own
	// prose precedes the way it precedes the context block.
	suggestion, rest := splitSuggestion(body[:end])
	return joinComment(rest), context, from, suggestion
}

// splitSuggestion takes a trailing ```suggestion block off a comment and
// returns its lines together with what precedes it. A suggestion is written
// without line numbers: it is the code that should stand there, not a quote of
// code that does.
func splitSuggestion(body []string) (string, []string) {
	end := len(body)
	for end > 0 && strings.TrimSpace(body[end-1]) == "" {
		end--
	}
	open, close, ok := lastFencedBlock(body[:end])
	if !ok || close != end-1 || !isSuggestionFence(body[open]) {
		return "", body
	}
	return strings.TrimSuffix(strings.Join(body[open+1:close], "\n"), "\n"), body[:open]
}

// isSuggestionFence reports whether a fence line opens a suggested change.
func isSuggestionFence(line string) bool {
	return strings.EqualFold(fenceInfo(line), suggestionMarker)
}

// isContextFence reports whether a fence line opens a context block.
func isContextFence(line string) bool {
	return slices.Contains(strings.Fields(fenceInfo(line)), contextMarker)
}

// fenceInfo returns the info string of a code fence line, or an empty string
// when the line opens no fence.
func fenceInfo(line string) string {
	trimmed, indent := trimIndent(line)
	if indent > maxFenceIndent {
		return ""
	}
	if fenceRe.FindString(trimmed) == "" {
		return ""
	}
	return strings.TrimLeft(trimmed, "`~")
}

// usesContextMarker reports whether the document marks its context blocks.
func usesContextMarker(lines []string) bool {
	for _, line := range lines {
		if fenceMarker(line) != "" && isContextFence(line) {
			return true
		}
	}
	return false
}

// hasText reports whether any of the lines carries something other than
// whitespace, which is what tells a comment that has started from one that is
// still only blank lines.
func hasText(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// joinComment assembles the comment body, without the blank lines that set it
// apart from the headings around it.
func joinComment(lines []string) string {
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// parseContext reads numbered source lines, the form the context block takes,
// and reports whether the lines are such a block at all.
func parseContext(lines []string) ([]string, int, bool) {
	if len(lines) == 0 {
		return nil, 0, false
	}

	context := make([]string, 0, len(lines))
	from := 0
	for _, line := range lines {
		m := contextLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, 0, false
		}
		if from == 0 {
			from, _ = strconv.Atoi(m[1])
		}
		context = append(context, m[2])
	}
	return context, from, true
}

// lastFencedBlock returns the lines opening and closing the last complete
// fenced block in lines.
func lastFencedBlock(lines []string) (open, close int, ok bool) {
	fence := ""
	start := 0
	for i, line := range lines {
		if fence == "" {
			if marker := fenceMarker(line); marker != "" {
				fence, start = marker, i
			}
			continue
		}
		if closesFence(line, fence) {
			open, close, ok = start, i, true
			fence = ""
		}
	}
	return open, close, ok
}

// opensFileSection reports whether the next line with content starts a file
// section, which is what makes a horizontal rule a separator rather than text.
func opensFileSection(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return fileHeaderRe.MatchString(line)
	}
	return false
}

// fenceMarker returns the run of backticks or tildes opening a code fence on
// this line, or an empty string when the line opens none.
func fenceMarker(line string) string {
	trimmed, indent := trimIndent(line)
	if indent > maxFenceIndent {
		return ""
	}
	return fenceRe.FindString(trimmed)
}

// closesFence reports whether the line closes a fence opened with marker.
func closesFence(line, marker string) bool {
	trimmed, indent := trimIndent(line)
	if indent > maxFenceIndent {
		return false
	}
	trimmed = strings.TrimRight(trimmed, " ")
	if len(trimmed) < len(marker) {
		return false
	}
	return strings.Trim(trimmed, marker[:1]) == ""
}

// maxFenceIndent is how far a code fence may be indented before it is content
// of an enclosing block rather than a fence of its own.
const maxFenceIndent = 3

// trimIndent strips leading spaces and reports how many there were.
func trimIndent(line string) (string, int) {
	trimmed := strings.TrimLeft(line, " ")
	return trimmed, len(line) - len(trimmed)
}

// looksStructural reports whether a line would be read as one of the
// document's own headings rather than as text.
func looksStructural(line string) bool {
	return fileHeaderRe.MatchString(line) || lineHeaderRe.MatchString(line)
}

// unescapeStructure removes the backslash that escapeComment puts in front of
// a comment line which would otherwise read as a heading.
func unescapeStructure(line string) string {
	if rest, found := strings.CutPrefix(line, `\`); found && looksStructural(rest) {
		return rest
	}
	return line
}

// readLines reads a file into its lines, without the line terminators.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
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
