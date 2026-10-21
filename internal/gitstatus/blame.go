package gitstatus

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BlameLine records the commit that last touched a line of a file.
//
// The zero value stands for a line git has no commit for: one that is only in
// the working tree, which is also what Uncommitted marks.
type BlameLine struct {
	Commit      string `json:"commit,omitempty"` // full hash, empty when uncommitted
	Short       string `json:"short,omitempty"`  // abbreviated hash, as git prints it
	Subject     string `json:"subject,omitempty"`
	Author      string `json:"author,omitempty"`
	Date        string `json:"date,omitempty"` // RFC 3339, in the author's own zone
	Uncommitted bool   `json:"uncommitted,omitempty"`
}

// blameTimeFormat is how a blamed line's date is sent to clients.
const blameTimeFormat = time.RFC3339

// BlameLines blames a file and returns the commit behind each line, keyed by
// line number.
//
// rev names the revision to blame; an empty rev blames the working tree, which
// is what the code view shows when the review is not pinned to a commit. The
// line numbers are those of the revision blamed, so they match the lines the
// caller is showing.
func BlameLines(dir, rev, filePath string) (map[int]*BlameLine, error) {
	args := []string{"--no-pager", "blame", "--porcelain"}
	if rev != "" {
		args = append(args, rev)
	}
	args = append(args, "--", filePath)

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git blame %s: %w", filePath, err)
	}
	return parseBlame(out), nil
}

// parseBlame turns git blame --porcelain output into a line-to-commit map.
//
// A commit's details are printed once, with the first group it appears in, and
// later groups carry only the header line, so the details are remembered per
// hash as the output is read.
func parseBlame(out []byte) map[int]*BlameLine {
	lines := make(map[int]*BlameLine)
	known := make(map[string]*BlameLine)

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), maxDiffLine)

	var (
		current *BlameLine
		hash    string
		first   int
		last    int
		at      int64
		zone    *time.Location
	)

	// commit returns the record for a hash, taking the details of an earlier
	// group when this one carries none of its own.
	commit := func() *BlameLine {
		if b, ok := known[hash]; ok {
			current = b
			return b
		}
		b := &BlameLine{Commit: hash, Short: shortHash(hash)}
		if hash == zeroHash {
			// git reports the working tree with a hash of its own and says so
			// in the author line; nothing was committed for these lines.
			b = &BlameLine{Uncommitted: true}
		}
		known[hash] = b
		current = b
		return b
	}

	// flush records the group being read against every line it covers.
	flush := func() {
		if current == nil {
			return
		}
		if !current.Uncommitted {
			current.Date = formatBlameTime(at, zone)
		}
		for n := first; n <= last; n++ {
			lines[n] = current
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		if isBlameHeader(line) {
			flush()
			fields := strings.Fields(line)
			hash = fields[0]
			first, _ = strconv.Atoi(fields[2])
			last = first
			if len(fields) > 3 {
				count, _ := strconv.Atoi(fields[3])
				last = first + count - 1
			}
			commit()
			continue
		}
		if current == nil {
			continue
		}

		switch {
		case line[0] == '\t':
			// The line's text, which is not what is being looked up here.
		case strings.HasPrefix(line, "summary "):
			current.Subject = strings.TrimPrefix(line, "summary ")
		case strings.HasPrefix(line, "author "):
			if name := strings.TrimPrefix(line, "author "); name != notCommittedAuthor {
				current.Author = name
			}
		case strings.HasPrefix(line, "author-time "):
			at, _ = strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64)
		case strings.HasPrefix(line, "author-tz "):
			zone = parseBlameZone(strings.TrimPrefix(line, "author-tz "))
		}
	}
	flush()

	return lines
}

// zeroHash is git's all-zero object name, used for lines that are only in the
// working tree.
const zeroHash = "0000000000000000000000000000000000000000"

// notCommittedAuthor is the author git prints for those same lines.
const notCommittedAuthor = "Not Committed Yet"

// isBlameHeader reports whether a line is a group header: a hash, then the
// first and last line numbers it covers.
func isBlameHeader(line string) bool {
	if len(line) < len(zeroHash)+1 {
		return false
	}
	for i := range len(zeroHash) {
		if !isHex(line[i]) {
			return false
		}
	}
	return line[len(zeroHash)] == ' '
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// shortHash abbreviates a hash the way git does, to the length git shows by
// default.
func shortHash(hash string) string {
	const short = 7
	if len(hash) <= short {
		return hash
	}
	return hash[:short]
}

// formatBlameTime renders a blame timestamp in the author's own zone, so the
// date reads as it did when the commit was made.
func formatBlameTime(unix int64, zone *time.Location) string {
	if unix == 0 {
		return ""
	}
	if zone == nil {
		zone = time.UTC
	}
	return time.Unix(unix, 0).In(zone).Format(blameTimeFormat)
}

// parseBlameZone turns a git timezone such as "+0200" into a location.
func parseBlameZone(tz string) *time.Location {
	if len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return nil
	}
	hours, err1 := strconv.Atoi(tz[1:3])
	minutes, err2 := strconv.Atoi(tz[3:5])
	if err1 != nil || err2 != nil {
		return nil
	}
	offset := (hours*60 + minutes) * 60
	if tz[0] == '-' {
		offset = -offset
	}
	return time.FixedZone(tz, offset)
}

// Blame is what blame knows about one file: which commit each line came from,
// and when those commits were made, which is what puts a hunk's commits in
// order.
type Blame struct {
	Lines map[int]*BlameLine
	times map[string]int64 // commit hash to committer time
}

// For returns the blame of a line, or nil where the file has none for it.
func (b *Blame) For(line int) *BlameLine {
	if b == nil {
		return nil
	}
	return b.Lines[line]
}

// Newest returns the commit that most recently touched any of the given lines,
// which is the commit a hunk is attributed to. Lines only in the working tree
// are passed over: they are not in a commit, so they cannot be the answer, and
// a hunk made only of them has no commit at all.
func (b *Blame) Newest(lines []int) *BlameLine {
	var newest *BlameLine
	var newestAt int64 = -1
	for _, n := range lines {
		c := b.For(n)
		if c == nil || c.Uncommitted {
			continue
		}
		at := b.times[c.Commit]
		if at > newestAt {
			newest, newestAt = c, at
		}
	}
	return newest
}

// blameCacheLimit is how many files' blame is remembered. A review comes back
// to the same few files, and blaming one costs a git subprocess, so the ones
// being read stay in hand without the cache growing with the tree.
const blameCacheLimit = 64

// BlameCache remembers the blame of files, so that reading a diff hunk over and
// over costs one stat rather than a git subprocess each time.
//
// An entry is keyed by the file and the revision it was blamed at, together
// with the file's size and modification time where the working tree is being
// blamed: editing a file or committing it changes that key, so a stale blame is
// never handed out and the cache does not have to be invalidated by hand.
type BlameCache struct {
	mu      sync.Mutex
	entries map[blameKey]*Blame
	recent  []blameKey // least recently used first
}

type blameKey struct {
	rev      string // revision blamed, empty for the working tree
	path     string
	size     int64  // working tree only: the file's size when it was blamed
	modified string // working tree only: its modification time
}

// NewBlameCache returns an empty cache.
func NewBlameCache() *BlameCache {
	return &BlameCache{entries: make(map[blameKey]*Blame)}
}

// Get returns the blame of a file, taking it if it is not already known.
//
// A failure to blame — a file git does not track, or one with no history — is
// reported as no blame rather than as an error: the caller is decorating a diff
// it can already show without one.
func (c *BlameCache) Get(dir, rev, filePath string) *Blame {
	key := c.key(dir, rev, filePath)

	c.mu.Lock()
	if blame, ok := c.entries[key]; ok {
		c.touch(key)
		c.mu.Unlock()
		return blame
	}
	c.mu.Unlock()

	blame := loadBlame(dir, rev, filePath)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.entries[key] = blame
		c.recent = append(c.recent, key)
		for len(c.recent) > blameCacheLimit {
			oldest := c.recent[0]
			c.recent = c.recent[1:]
			delete(c.entries, oldest)
		}
	}
	return blame
}

// loadBlame blames a file and looks up when the commits it names were made, so
// a hunk's commits can be put in order.
func loadBlame(dir, rev, filePath string) *Blame {
	lines, err := BlameLines(dir, rev, filePath)
	if err != nil {
		lines = map[int]*BlameLine{}
	}
	return &Blame{Lines: lines, times: commitTimes(dir, lines)}
}

// commitTimes resolves the committer time of every commit the blamed lines
// name, in one git call for the file.
func commitTimes(dir string, lines map[int]*BlameLine) map[string]int64 {
	seen := make(map[string]bool, len(lines))
	var hashes []string
	for _, line := range lines {
		if line.Commit == "" || seen[line.Commit] {
			continue
		}
		seen[line.Commit] = true
		hashes = append(hashes, line.Commit)
	}
	if len(hashes) == 0 {
		return nil
	}

	args := append([]string{"log", "--no-walk", "--no-color", "--format=%H %ct", "--stdin"}, "--")
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(hashes, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	times := make(map[string]int64, len(hashes))
	for _, line := range strings.Split(string(out), "\n") {
		hash, at, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		seconds, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			continue
		}
		times[hash] = seconds
	}
	return times
}

// key builds the cache key for a file, reading the working tree's stat where
// that is what is being blamed.
func (c *BlameCache) key(dir, rev, filePath string) blameKey {
	key := blameKey{rev: rev, path: filePath}
	if rev != "" {
		// A revision does not change under us, so the path and revision are the
		// whole key.
		return key
	}
	// The working tree does change: the stat is what tells a blame taken before
	// an edit from one taken after it.
	if info, err := os.Stat(filepath.Join(dir, filePath)); err == nil {
		key.size = info.Size()
		key.modified = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return key
}

// touch marks a key as the most recently used one.
func (c *BlameCache) touch(key blameKey) {
	for i, k := range c.recent {
		if k == key {
			c.recent = append(c.recent[:i], c.recent[i+1:]...)
			c.recent = append(c.recent, key)
			return
		}
	}
}
