package gitstatus

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Status represents the git status of a file.
type Status string

const (
	StatusNone      Status = ""
	StatusModified  Status = "modified"  // changed in working tree
	StatusStaged    Status = "staged"    // staged for commit
	StatusUntracked Status = "untracked" // not tracked by git
	StatusAdded     Status = "added"     // new file staged
	StatusDeleted   Status = "deleted"   // deleted
	StatusConflict  Status = "conflict"  // merge conflict
)

// FileStatuses maps relative file paths to their git status.
type FileStatuses map[string]Status

// Base identifies the commit a review is compared against.
// The zero value compares against the working tree's HEAD.
type Base struct {
	Rev    string // revision as given by the user, empty when comparing against HEAD
	Commit string // commit the diffs are taken from, empty when comparing against HEAD
}

// rev returns the revision to hand to git diff.
func (b Base) rev() string {
	if b.Commit == "" {
		return "HEAD"
	}
	return b.Commit
}

// ResolveBase determines the commit to compare a review against for a
// user-supplied revision such as a branch name, tag or commit ID. The merge
// base of that revision and HEAD is used, so commits made on the base branch
// after branching off are not reported as changes. Falls back to the revision
// itself when the two have no common ancestor. Returns an error if the
// directory is no git repository or the revision is unknown to it.
func ResolveBase(dir, rev string) (Base, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return Base{}, fmt.Errorf("%s is not a git repository", dir)
	}

	cmd = exec.Command("git", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return Base{}, fmt.Errorf("unknown revision %q", rev)
	}
	commit := strings.TrimSpace(string(out))

	cmd = exec.Command("git", "merge-base", commit, "HEAD")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		if mb := strings.TrimSpace(string(out)); mb != "" {
			commit = mb
		}
	}

	return Base{Rev: rev, Commit: commit}, nil
}

// Get returns the git status for all files in the given directory. With a base
// commit set, files are reported by how they differ from that commit instead of
// from HEAD. Returns nil if the directory is not a git repository.
func Get(dir string, base Base) FileStatuses {
	// Check if this is a git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return nil
	}

	if base.Commit != "" {
		return statusesSince(dir, base)
	}

	// Get porcelain status. core.quotepath=false keeps non-ASCII paths
	// (e.g. umlauts) unquoted so they match the paths reported by the file tree.
	// Untracked files are listed individually because the file tree shows them
	// individually too; a collapsed directory entry would match none of them.
	cmd = exec.Command("git", "-c", "core.quotepath=false", "status", "--porcelain", "-uall")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	result := make(FileStatuses)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 4 {
			continue
		}

		x := line[0] // index (staging area) status
		y := line[1] // working tree status
		path := strings.TrimSpace(line[3:])

		// Handle renames: "R  old -> new"
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}

		// Make path relative and clean
		path = filepath.Clean(path)

		status := classifyStatus(x, y)
		if status != StatusNone {
			result[path] = status
		}
	}

	return result
}

// statusesSince reports how the working tree differs from the base commit,
// covering both committed and uncommitted changes. Untracked files are listed
// as well since they are part of what is under review.
func statusesSince(dir string, base Base) FileStatuses {
	result := make(FileStatuses)

	// core.quotepath=false keeps non-ASCII paths (e.g. umlauts) unquoted so they
	// match the paths reported by the file tree.
	cmd := exec.Command("git", "-c", "core.quotepath=false", "diff", "--name-status", base.Commit)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		// Renames and copies report both the old and the new path
		path := fields[len(fields)-1]
		status := classifyDiffStatus(fields[0][0])
		if status != StatusNone {
			result[filepath.Clean(path)] = status
		}
	}

	cmd = exec.Command("git", "-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			if path := scanner.Text(); path != "" {
				result[filepath.Clean(path)] = StatusUntracked
			}
		}
	}

	return result
}

// classifyDiffStatus maps a git diff --name-status letter to a Status.
func classifyDiffStatus(code byte) Status {
	switch code {
	case 'A':
		return StatusAdded
	case 'D':
		return StatusDeleted
	case 'M', 'R', 'C', 'T':
		return StatusModified
	}
	return StatusNone
}

func classifyStatus(x, y byte) Status {
	// Untracked
	if x == '?' && y == '?' {
		return StatusUntracked
	}
	// Conflicts
	if x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D') {
		return StatusConflict
	}
	// Staged changes take priority display
	if x == 'A' {
		return StatusAdded
	}
	if x == 'D' {
		return StatusDeleted
	}
	if x == 'M' || x == 'R' || x == 'C' {
		// If also modified in working tree, show as modified (more urgent)
		if y == 'M' || y == 'D' {
			return StatusModified
		}
		return StatusStaged
	}
	// Working tree changes
	if y == 'M' {
		return StatusModified
	}
	if y == 'D' {
		return StatusDeleted
	}
	return StatusNone
}

// DirStatus computes an aggregate status for a directory path.
// It returns the "most important" status of any file under that directory.
func (fs FileStatuses) DirStatus(dirPath string) Status {
	if fs == nil {
		return StatusNone
	}
	prefix := dirPath + "/"
	best := StatusNone
	for path, status := range fs {
		if strings.HasPrefix(path, prefix) || path == dirPath {
			if statusPriority(status) > statusPriority(best) {
				best = status
			}
		}
	}
	return best
}

func statusPriority(s Status) int {
	switch s {
	case StatusConflict:
		return 5
	case StatusModified:
		return 4
	case StatusUntracked:
		return 3
	case StatusAdded:
		return 2
	case StatusStaged:
		return 1
	default:
		return 0
	}
}
