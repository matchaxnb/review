package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"review/internal/filetree"
	"review/internal/gitstatus"
	"review/internal/highlight"
	"review/internal/safepath"
	"review/internal/store"
)

type handlers struct {
	store       *store.Store
	rootDir     string
	base        gitstatus.Base
	highlighter *highlight.Cache
	// blame remembers which commit each line of a file came from, so naming the
	// commit behind a diff hunk costs one stat per file read rather than a git
	// subprocess per hunk shown.
	blame *gitstatus.BlameCache
	// rev, when non-empty, is the exact revision (commit) whose content the
	// file/tree endpoints serve. It powers line-by-line history review: the
	// frontend selects a commit and the server shows that commit's tree,
	// diffed against its parent. Empty = working tree (the original mode).
	rev string
	// sideBySide reports whether the frontend offers a side-by-side view of
	// the diff hunk under the pointer.
	sideBySide bool
}

func (h *handlers) handleTree(w http.ResponseWriter, r *http.Request) {
	if rev := r.URL.Query().Get("rev"); rev != "" && validRev(rev) {
		files, err := gitstatus.TreeAt(h.rootDir, rev)
		if err != nil {
			jsonError(w, "failed to read tree at revision", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, filetree.FromPaths(files))
		return
	}
	tree, err := filetree.Walk(h.rootDir)
	if err != nil {
		jsonError(w, "failed to walk directory", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, tree)
}

// validRev rejects revisions that could be read as a git option or path. The
// value is passed as a git argument (never a shell), but a leading dash or a
// ".." range separator is never a valid single commit, so refusing them keeps
// the surface tight.
func validRev(rev string) bool {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return false
	}
	if strings.ContainsAny(rev, " \t\n") {
		return false
	}
	return true
}

type commitsResponse struct {
	Base    string             `json:"base"` // revision the history starts after
	Commits []gitstatus.Commit `json:"commits"`
}

func (h *handlers) handleCommits(w http.ResponseWriter, r *http.Request) {
	commits, err := gitstatus.History(h.rootDir, h.base)
	if err != nil {
		jsonError(w, "failed to read history", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, commitsResponse{Base: h.base.Rev, Commits: commits})
}

// maxFileSize is the largest file the server reads into memory and hands to
// the highlighter.
const maxFileSize = 2 << 20 // 2 MiB

// binarySniffLen is how much of a file is examined to tell code from binary
// data. Git looks at the same amount.
const binarySniffLen = 8000

type fileResponse struct {
	HTML          string                       `json:"html"`
	Language      string                       `json:"language"`
	DiffLines     map[int]gitstatus.LineChange `json:"diffLines,omitempty"`
	DiffHunks     []gitstatus.DiffHunk         `json:"diffHunks,omitempty"`
	DiffDeletions []gitstatus.DiffDeletion     `json:"diffDeletions,omitempty"`
}

func (h *handlers) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		jsonError(w, "path parameter required", http.StatusBadRequest)
		return
	}

	// Historical mode: serve the file's content at a commit and diff it
	// against that commit's parent (its virtual tree, no checkout).
	if rev := r.URL.Query().Get("rev"); rev != "" {
		if !validRev(rev) {
			jsonError(w, "invalid rev", http.StatusBadRequest)
			return
		}
		absPath, ok := h.resolvePath(path)
		if !ok {
			jsonError(w, "invalid path", http.StatusBadRequest)
			return
		}
		content, err := gitstatus.Show(h.rootDir, rev, path)
		if err != nil {
			// Not present at this revision; fall back to the working-tree
			// content so a path from the tree still opens.
			content, err = os.ReadFile(absPath)
			if err != nil {
				jsonError(w, "file not found", http.StatusNotFound)
				return
			}
		}
		if len(content) > maxFileSize {
			jsonError(w, "file is too large to display", http.StatusRequestEntityTooLarge)
			return
		}
		if isBinary(content) {
			jsonError(w, "binary file", http.StatusBadRequest)
			return
		}
		hl := h.highlighter.Highlight(path, string(content))
		parent := gitstatus.ParentOf(h.rootDir, rev)
		diff := gitstatus.GetFileDiffAt(h.rootDir, parent, rev, path, content)
		// The hunk was put there by whatever the file's history at this
		// revision says, so the blame is taken at the same revision.
		gitstatus.AttributeHunks(diff.Hunks, h.blame.Get(h.rootDir, rev, path))
		jsonResponse(w, fileResponse{
			HTML:          hl.HTML,
			Language:      hl.Language,
			DiffLines:     diff.Lines,
			DiffHunks:     diff.Hunks,
			DiffDeletions: diff.Deletions,
		})
		return
	}

	// Prevent path traversal
	absPath, ok := h.resolvePath(path)
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}
	if info.Size() > maxFileSize {
		jsonError(w, "file is too large to display", http.StatusRequestEntityTooLarge)
		return
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}
	if isBinary(content) {
		jsonError(w, "binary file", http.StatusBadRequest)
		return
	}

	hl := h.highlighter.Highlight(path, string(content))
	diff := gitstatus.GetFileDiff(h.rootDir, h.base, path, content)
	// The working tree is what is being reviewed, so the blame is taken over
	// it: a hunk whose lines are not committed yet has no commit to name, which
	// is the answer for a change being made right now.
	gitstatus.AttributeHunks(diff.Hunks, h.blame.Get(h.rootDir, "", path))
	resp := fileResponse{
		HTML:          hl.HTML,
		Language:      hl.Language,
		DiffLines:     diff.Lines,
		DiffHunks:     diff.Hunks,
		DiffDeletions: diff.Deletions,
	}
	jsonResponse(w, resp)
}

// commentResponse is the JSON shape of one comment in a thread.
type commentResponse struct {
	ID         int64  `json:"id"`
	ReplyTo    int64  `json:"replyTo,omitempty"`
	Comment    string `json:"comment"`
	Suggestion string `json:"suggestion,omitempty"`
}

// annotationResponse is the JSON shape of the thread on one line or run of
// lines, keyed in a file's map by its end line.
type annotationResponse struct {
	StartLine int               `json:"startLine"` // first line the thread covers
	EndLine   int               `json:"endLine"`   // last line the thread covers
	Outdated  bool              `json:"outdated"`
	Comments  []commentResponse `json:"comments"`
}

// fileAnnotations converts one file's threads into the shape clients receive,
// keyed by line number.
func fileAnnotations(threads map[int]*store.Thread) map[string]annotationResponse {
	result := make(map[string]annotationResponse, len(threads))
	for line, thread := range threads {
		live := thread.Live()
		comments := make([]commentResponse, 0, len(live))
		for _, c := range live {
			comments = append(comments, commentResponse{
				ID:         c.ID,
				ReplyTo:    c.ReplyTo,
				Comment:    c.Comment,
				Suggestion: c.Suggestion,
			})
		}
		result[strconv.Itoa(line)] = annotationResponse{
			StartLine: thread.StartFor(line),
			EndLine:   line,
			Outdated:  thread.Outdated,
			Comments:  comments,
		}
	}
	return result
}

// allAnnotations converts the threads of every file into the shape clients
// receive, keyed by file path.
func allAnnotations(all map[string]map[int]*store.Thread) map[string]map[string]annotationResponse {
	result := make(map[string]map[string]annotationResponse, len(all))
	for file, threads := range all {
		result[file] = fileAnnotations(threads)
	}
	return result
}

func (h *handlers) handleGetAnnotations(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		jsonResponse(w, allAnnotations(h.store.All()))
		return
	}
	jsonResponse(w, fileAnnotations(h.store.GetFile(path)))
}

type annotationRequest struct {
	Path       string       `json:"path"`
	Line       int          `json:"line"`
	Range      *rangeEntity `json:"range,omitempty"`
	Comment    string       `json:"comment"`
	Suggestion string       `json:"suggestion,omitempty"`
	ReplyTo    int64        `json:"replyTo,omitempty"`
	ID         int64        `json:"id,omitempty"` // the comment to delete
}

// rangeEntity mirrors the fields of Gerrit's CommentRange that a whole-line
// comment uses. The character offsets alone are ignored: this tool comments on
// whole lines, so a range is always a run of lines.
type rangeEntity struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// lineRange returns the first and last line the request covers, in that order.
// A range, when given, wins over the single line field, and its end line is the
// line the annotation is keyed by, as in Gerrit.
func (r annotationRequest) lineRange() (start, end int, ok bool) {
	start, end = r.Line, r.Line
	if r.Range != nil {
		start, end = r.Range.StartLine, r.Range.EndLine
		if start < 1 {
			start = end
		}
	}
	if start > end {
		start, end = end, start
	}
	if start < 1 || end < 1 {
		return 0, 0, false
	}
	return start, end, true
}

func (h *handlers) handleSetAnnotation(w http.ResponseWriter, r *http.Request) {
	var req annotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	start, end, ok := req.lineRange()
	if req.Path == "" || !ok {
		jsonError(w, "path and line (>= 1) are required", http.StatusBadRequest)
		return
	}
	if _, ok := h.resolvePath(req.Path); !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Comment) == "" && strings.TrimSpace(req.Suggestion) == "" {
		jsonError(w, "a comment or a suggested change is required", http.StatusBadRequest)
		return
	}

	id, err := h.store.AppendComment(req.Path, start, end, req.ReplyTo, req.Comment, req.Suggestion)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to save: %v", err), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]any{"status": "ok", "id": id})
}

func (h *handlers) handleDeleteAnnotation(w http.ResponseWriter, r *http.Request) {
	var req annotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" || req.ID <= 0 {
		jsonError(w, "path and the id of the comment are required", http.StatusBadRequest)
		return
	}
	if _, ok := h.resolvePath(req.Path); !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteComment(req.Path, req.ID); err != nil {
		jsonError(w, fmt.Sprintf("failed to delete: %v", err), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "ok"})
}

// configResponse is the JSON shape for the review's global settings.
type configResponse struct {
	Base           string `json:"base"`                  // revision changes are compared against, empty when comparing against HEAD
	BaseShort      string `json:"baseShort,omitempty"`   // that revision abbreviated, as git prints it
	HistoryEnabled bool   `json:"historyEnabled"`        // whether starting a new review archives the old one instead of deleting it
	HistoryDest    string `json:"historyDest,omitempty"` // where the current review would be archived, relative to the review root
	SideBySide     bool   `json:"sideBySide"`            // whether the side-by-side view is available
}

func (h *handlers) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := configResponse{
		Base:           h.base.Rev,
		BaseShort:      h.base.Short(h.rootDir),
		HistoryEnabled: h.store.HistoryEnabled(),
		SideBySide:     h.sideBySide,
	}
	if dest, ok := h.store.NextHistoryDest(); ok {
		resp.HistoryDest = dest
	}
	jsonResponse(w, resp)
}

func (h *handlers) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	var statuses gitstatus.FileStatuses
	if rev := r.URL.Query().Get("rev"); rev != "" && validRev(rev) {
		parent := gitstatus.ParentOf(h.rootDir, rev)
		statuses = gitstatus.StatusBetween(h.rootDir, parent, rev)
	} else {
		statuses = gitstatus.Get(h.rootDir, h.base)
	}
	if statuses == nil {
		// Not a git repo — return empty object
		jsonResponse(w, map[string]string{})
		return
	}
	// Convert to string→string for JSON
	result := make(map[string]string, len(statuses))
	for path, status := range statuses {
		result[path] = string(status)
	}
	jsonResponse(w, result)
}

// newReviewResponse reports what starting a new review did with the old one.
type newReviewResponse struct {
	Status   string `json:"status"`
	Archived string `json:"archived,omitempty"` // path of the retired review, empty when it was deleted
}

func (h *handlers) handleDeleteReview(w http.ResponseWriter, r *http.Request) {
	dest, err := h.store.ArchiveReview()
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to start a new review: %v", err), http.StatusInternalServerError)
		return
	}

	resp := newReviewResponse{Status: "ok"}
	if dest != "" {
		// The path is relative to the review root, like every other path the
		// client sees.
		if rel, err := filepath.Rel(h.rootDir, dest); err == nil {
			resp.Archived = filepath.ToSlash(rel)
		} else {
			resp.Archived = dest
		}
	}
	jsonResponse(w, resp)
}

func (h *handlers) handleChromaCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css")
	w.Write([]byte(highlight.CSS()))
}

// isBinary reports whether content looks like binary data rather than text.
// A NUL byte early in the file is the same signal git uses.
func isBinary(content []byte) bool {
	if len(content) > binarySniffLen {
		content = content[:binarySniffLen]
	}
	return bytes.IndexByte(content, 0) >= 0
}

// resolvePath resolves a client-supplied relative path against the review root.
func (h *handlers) resolvePath(path string) (string, bool) {
	return safepath.Resolve(h.rootDir, path)
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
