package gitstatus

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
)

// LineChange represents the type of change for a line.
type LineChange string

const (
	LineAdded    LineChange = "added"
	LineModified LineChange = "modified"
)

// diffContext is the number of unchanged lines git includes around a change.
// They are shown in the hunk tooltip.
const diffContext = 3

// maxDiffLine bounds a single line of diff output, so that a file with very
// long lines is still read completely.
const maxDiffLine = 1 << 20

// hunkRe matches unified diff hunk headers: @@ -old[,count] +new[,count] @@
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// FileDiffInfo contains all diff information for a single file, computed in one pass.
type FileDiffInfo struct {
	Lines     map[int]LineChange
	Hunks     []DiffHunk
	Deletions []DiffDeletion
}

// DiffDeletion marks a block of lines deleted between two lines of the new
// file. How many were removed is not recorded: the hunk the marker points at
// shows them.
type DiffDeletion struct {
	AfterLine int `json:"afterLine"` // deletion sits after this line (0 = top of file)
	HunkIndex int `json:"hunkIndex"` // index into DiffHunks for tooltip
}

// DiffHunk represents a single diff hunk with its affected line range and the
// lines it covers, laid out for a side-by-side view.
type DiffHunk struct {
	StartLine int        `json:"startLine"`        // first new-file line in hunk
	EndLine   int        `json:"endLine"`          // last new-file line in hunk
	Rows      []DiffRow  `json:"rows"`             // the hunk's lines, one row per line of either version
	Commit    *BlameLine `json:"commit,omitempty"` // commit that put this hunk there, when blame found one
}

// AttributeHunks records, on each hunk, the commit that put it there: the most
// recent commit behind any line the hunk added or replaced, which is the change
// the reader is looking at the diff of.
//
// Only lines the hunk added or replaced are asked about, so the answer is the
// change itself rather than the history of the lines around it. A hunk with
// nothing to attribute — a file git does not track, or a change not yet
// committed — is left as it is.
func AttributeHunks(hunks []DiffHunk, blame *Blame) {
	for i := range hunks {
		var changed []int
		for _, row := range hunks[i].Rows {
			// A line the hunk replaced shares one row with the line it stood
			// for; a line only the new side has is an addition. Either way the
			// line under review is the new file's, which is what was blamed.
			if row.NewNo != 0 && (row.OldNo == 0 || row.Old != row.New) {
				changed = append(changed, row.NewNo)
			}
		}
		if len(changed) == 0 {
			continue
		}
		hunks[i].Commit = blame.Newest(changed)
	}
}

// DiffRow is one line of a hunk as a side-by-side view reads it: a line of the
// old file and the line of the new file that stands opposite it. A row the old
// file does not have there (an added line) carries no old number, one the new
// file does not have (a removed line) no new number. A replaced line shares one
// row, removed on the left and added on the right.
type DiffRow struct {
	OldNo int    `json:"oldNo,omitempty"` // line number in the old file, 0 when the row has no old line
	NewNo int    `json:"newNo,omitempty"` // line number in the new file, 0 when the row has no new line
	Old   string `json:"old"`             // text of the old line, empty when the row has none
	New   string `json:"new"`             // text of the new line, empty when the row has none
}

// GetFileDiff returns all diff information for a file. With a base commit set,
// the file is diffed against that commit instead of against HEAD. The file's
// content is used to mark up a file git does not track yet.
//
// The diff and the check for an untracked file run at the same time, because
// which of the two answers is needed only shows once the diff is in.
func GetFileDiff(dir string, base Base, filePath string, content []byte) *FileDiffInfo {
	type result struct {
		out []byte
		err error
	}
	run := func(args ...string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			out, err := cmd.Output()
			ch <- result{out, err}
		}()
		return ch
	}

	diffCh := run("diff", base.rev(), "--unified="+strconv.Itoa(diffContext), "--no-color", "--", filePath)
	untrackedCh := run("ls-files", "--others", "--exclude-standard", "--", filePath)

	diff := <-diffCh
	untracked := <-untrackedCh

	if diff.err == nil && len(diff.out) > 0 {
		return parseDiff(diff.out)
	}
	if untracked.err == nil && len(untracked.out) > 0 {
		// The file is new to git, so all of it is new
		return allLinesAdded(content)
	}
	return &FileDiffInfo{}
}

// GetFileDiffAt diffs a file between two revisions (e.g. a commit and its
// parent). If the file does not exist at fromRev, every line is marked added.
// Unlike GetFileDiff it always uses the revision range, never the working tree.
func GetFileDiffAt(dir, fromRev, toRev, filePath string, content []byte) *FileDiffInfo {
	cmd := exec.Command("git", "diff", fromRev, toRev,
		"--unified="+strconv.Itoa(diffContext), "--no-color", "--", filePath)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return parseDiff(out)
	}
	// No diff output: added (absent at fromRev) or unchanged.
	if _, e := Show(dir, fromRev, filePath); e != nil {
		return allLinesAdded(content)
	}
	return &FileDiffInfo{}
}

// parseDiff turns unified diff output for a single file into the line markers,
// hunks and deletion markers the frontend draws.
//
// Added and modified lines are told apart by looking at the diff body rather
// than the hunk header: with context around a change, one hunk can hold both.
// A run of removed lines that is not replaced by added ones becomes a deletion
// marker sitting after the last line that survived.
func parseDiff(out []byte) *FileDiffInfo {
	info := &FileDiffInfo{}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), maxDiffLine)

	var (
		current *DiffHunk // hunk being read
		oldLine int       // next line number in the old file
		newLine int       // next line number in the new file
		removed []DiffRow // removed rows of the change block being read
		added   []DiffRow // added rows of the change block being read
	)

	// flushBlock ends the block of changed lines being read, turning it into
	// rows: the removed rows and the added ones are set against each other by
	// position, so a line replaced by another shares one row. What one side has
	// beyond the other keeps a row of its own, blank opposite it. Removed rows
	// that nothing was put in place of leave a deletion marker sitting after
	// the last line that survived.
	flushBlock := func() {
		pair := len(added)
		if len(removed) < pair {
			pair = len(removed)
		}
		for i := range pair {
			added[i].OldNo = removed[i].OldNo
			added[i].Old = removed[i].Old
		}
		current.Rows = append(current.Rows, added[:pair]...)
		if len(removed) > pair {
			after := newLine - 1
			if after < 0 {
				after = 0
			}
			info.Deletions = append(info.Deletions, DiffDeletion{
				AfterLine: after,
				HunkIndex: len(info.Hunks),
			})
			current.Rows = append(current.Rows, removed[pair:]...)
		} else {
			current.Rows = append(current.Rows, added[pair:]...)
		}
		removed = removed[:0]
		added = added[:0]
	}

	// endHunk finishes the hunk being read.
	endHunk := func() {
		if current == nil {
			return
		}
		flushBlock()
		info.Hunks = append(info.Hunks, *current)
		current = nil
	}

	mark := func(change LineChange) {
		if info.Lines == nil {
			info.Lines = make(map[int]LineChange)
		}
		info.Lines[newLine] = change
	}

	for scanner.Scan() {
		line := scanner.Text()

		if m := hunkRe.FindStringSubmatch(line); m != nil {
			endHunk()
			oldStart, _ := strconv.Atoi(m[1])
			newStart, _ := strconv.Atoi(m[3])
			newCount := 1
			if m[4] != "" {
				newCount, _ = strconv.Atoi(m[4])
			}
			current = &DiffHunk{StartLine: newStart, EndLine: newStart + newCount - 1}
			oldLine, newLine = oldStart, newStart
			removed, added = nil, nil
			continue
		}

		// Everything before the first hunk header is the file header
		if current == nil {
			continue
		}

		if line == "" {
			// An unchanged empty line is written without its leading space
			flushBlock()
			current.Rows = append(current.Rows, DiffRow{OldNo: oldLine, NewNo: newLine})
			oldLine++
			newLine++
			continue
		}

		switch line[0] {
		case ' ':
			flushBlock()
			body := line[1:]
			current.Rows = append(current.Rows, DiffRow{OldNo: oldLine, NewNo: newLine, Old: body, New: body})
			oldLine++
			newLine++
		case '-':
			removed = append(removed, DiffRow{OldNo: oldLine, Old: line[1:]})
			oldLine++
		case '+':
			// A line put where removed ones stood is a modification of them, a
			// line put where nothing was removed is one git added. The block
			// being read settles that, and the whole block is marked once when
			// it ends.
			if len(removed) > 0 {
				mark(LineModified)
			} else {
				mark(LineAdded)
			}
			added = append(added, DiffRow{NewNo: newLine, New: line[1:]})
			newLine++
		case '\\':
			// "\ No newline at end of file": no line of its own, and the row
			// above already carries the text.
		default:
			endHunk()
		}
	}
	endHunk()

	return info
}

// allLinesAdded marks every line of the given content as "added". Nothing is
// set against them, since the file did not exist before, so there is no hunk to
// show either side by side or as a diff.
func allLinesAdded(content []byte) *FileDiffInfo {
	n := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		n++ // file doesn't end with newline
	}
	if n == 0 {
		return &FileDiffInfo{}
	}
	result := make(map[int]LineChange, n)
	for i := 1; i <= n; i++ {
		result[i] = LineAdded
	}
	return &FileDiffInfo{Lines: result}
}
