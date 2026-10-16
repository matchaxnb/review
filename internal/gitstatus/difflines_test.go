package gitstatus

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseDiff_Lines(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want map[int]LineChange
	}{
		{
			name: "pure addition",
			diff: `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,6 @@ func existing()
 keep1
 keep2
 keep3
+line1
+line2
+line3
`,
			want: map[int]LineChange{
				13: LineAdded,
				14: LineAdded,
				15: LineAdded,
			},
		},
		{
			name: "modification replaces lines",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,2 +5,2 @@ package main
-oldA
-oldB
+newA
+newB
`,
			want: map[int]LineChange{
				5: LineModified,
				6: LineModified,
			},
		},
		{
			name: "pure deletion",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,4 +5,1 @@ package main
 keep
-old1
-old2
-old3
`,
			want: nil, // no new lines, nothing to highlight
		},
		{
			name: "addition and modification in one hunk",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,5 @@
 keep1
-old
+new
 keep2
+appended
`,
			want: map[int]LineChange{
				2: LineModified,
				4: LineAdded,
			},
		},
		{
			name: "single line no count",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1 +1 @@
-old
+new
`,
			want: map[int]LineChange{
				1: LineModified,
			},
		},
		{
			name: "unchanged empty line keeps numbering",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 keep

+added
 tail
`,
			want: map[int]LineChange{
				3: LineAdded,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiff([]byte(tt.diff)).Lines
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseDiff_Deletions(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []DiffDeletion
	}{
		{
			name: "deletion between kept lines",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,6 +1,3 @@
 keep1
 keep2
-old1
-old2
-old3
 keep3
`,
			want: []DiffDeletion{
				{AfterLine: 2, HunkIndex: 0},
			},
		},
		{
			name: "no deletion when lines are replaced",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,2 +5,2 @@ package main
-old
+new
`,
			want: nil,
		},
		{
			name: "deletion when fewer lines replace more",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,3 @@
 keep
-old1
-old2
-old3
+new1
 tail
`,
			want: []DiffDeletion{
				{AfterLine: 2, HunkIndex: 0},
			},
		},
		{
			name: "deletion at top of file",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,2 @@
-line1
-line2
 keep1
 keep2
`,
			want: []DiffDeletion{
				{AfterLine: 0, HunkIndex: 0},
			},
		},
		{
			name: "empty diff",
			diff: "",
			want: nil,
		},
		{
			name: "deletions in separate hunks",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -3,2 +3,1 @@
 keep
-removed1
@@ -10,3 +9,1 @@
 keep
-removed2
-removed3
`,
			want: []DiffDeletion{
				{AfterLine: 3, HunkIndex: 0},
				{AfterLine: 9, HunkIndex: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiff([]byte(tt.diff)).Deletions
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseDiff_Hunks(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 keep
-old
+new
+extra
@@ -20,2 +21,2 @@
 tail
-gone
+here
`
	hunks := parseDiff([]byte(diff)).Hunks
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d: %+v", len(hunks), hunks)
	}
	if hunks[0].StartLine != 1 || hunks[0].EndLine != 4 {
		t.Errorf("first hunk range: got %d-%d, want 1-4", hunks[0].StartLine, hunks[0].EndLine)
	}
	// The hunk pairs the removed line with the first line that replaced it and
	// leaves the extra added line a row of its own.
	wantRows := []DiffRow{
		{OldNo: 1, NewNo: 1, Old: "keep", New: "keep"},
		{OldNo: 2, NewNo: 2, Old: "old", New: "new"},
		{NewNo: 3, New: "extra"},
	}
	if !reflect.DeepEqual(hunks[0].Rows, wantRows) {
		t.Errorf("first hunk rows:\n got %+v\nwant %+v", hunks[0].Rows, wantRows)
	}
	if hunks[1].StartLine != 21 || hunks[1].EndLine != 22 {
		t.Errorf("second hunk range: got %d-%d, want 21-22", hunks[1].StartLine, hunks[1].EndLine)
	}
}

// TestParseDiff_Rows covers the side-by-side layout: which lines share a row,
// and which side a row with only one version belongs to. The rows of a hunk
// together have to account for every line of both files it spans, or the two
// columns drift apart.
func TestParseDiff_Rows(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []DiffRow
	}{
		{
			name: "addition and replacement in one hunk",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 keep
-old
+new
+extra
`,
			want: []DiffRow{
				{OldNo: 1, NewNo: 1, Old: "keep", New: "keep"},
				{OldNo: 2, NewNo: 2, Old: "old", New: "new"},
				{NewNo: 3, New: "extra"},
			},
		},
		{
			name: "more removals than additions leaves the extra ones old-only",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,2 @@
-old1
-old2
-old3
+new1
 keep
`,
			want: []DiffRow{
				{OldNo: 1, NewNo: 1, Old: "old1", New: "new1"},
				{OldNo: 2, Old: "old2"},
				{OldNo: 3, Old: "old3"},
				{OldNo: 4, NewNo: 2, Old: "keep", New: "keep"},
			},
		},
		{
			name: "no newline at end of file marker adds no row",
			diff: `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1 +1 @@
-old
\ No newline at end of file
+new
\ No newline at end of file
`,
			want: []DiffRow{
				{OldNo: 1, NewNo: 1, Old: "old", New: "new"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hunks := parseDiff([]byte(tt.diff)).Hunks
			if len(hunks) != 1 {
				t.Fatalf("expected 1 hunk, got %d", len(hunks))
			}
			if !reflect.DeepEqual(hunks[0].Rows, tt.want) {
				t.Errorf("rows:\n got %+v\nwant %+v", hunks[0].Rows, tt.want)
			}
		})
	}
}

// TestGetFileDiff_AgainstGit runs the parser over output git actually produces.
func TestGetFileDiff_AgainstGit(t *testing.T) {
	root := initRepo(t)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	write("code.go", "one\ntwo\nthree\nfour\nfive\nsix\n")
	git("add", "code.go")
	git("commit", "-m", "add code")

	// two -> TWO (modified), delete four and five, append seven
	write("code.go", "one\nTWO\nthree\nsix\nseven\n")

	info := GetFileDiff(root, Base{}, "code.go", nil)

	if got := info.Lines[2]; got != LineModified {
		t.Errorf("line 2: got %q, want modified", got)
	}
	if got := info.Lines[5]; got != LineAdded {
		t.Errorf("line 5: got %q, want added", got)
	}
	if len(info.Deletions) != 1 {
		t.Fatalf("expected 1 deletion, got %+v", info.Deletions)
	}
	if info.Deletions[0].AfterLine != 3 {
		t.Errorf("deletion: got %+v, want after line 3, count 2", info.Deletions[0])
	}
	if info.Deletions[0].HunkIndex < 0 || info.Deletions[0].HunkIndex >= len(info.Hunks) {
		t.Errorf("deletion points at hunk %d of %d", info.Deletions[0].HunkIndex, len(info.Hunks))
	}

	// An untracked file counts as entirely new
	write("fresh.go", "a\nb\nc\n")
	fresh := GetFileDiff(root, Base{}, "fresh.go", []byte("a\nb\nc\n"))
	if len(fresh.Lines) != 3 {
		t.Errorf("expected 3 added lines for an untracked file, got %v", fresh.Lines)
	}
	for line, change := range fresh.Lines {
		if change != LineAdded {
			t.Errorf("line %d: got %q, want added", line, change)
		}
	}

	// An unchanged file has nothing to report
	unchanged := GetFileDiff(root, Base{}, "tracked.txt", []byte("x\n"))
	if len(unchanged.Lines) != 0 || len(unchanged.Hunks) != 0 || len(unchanged.Deletions) != 0 {
		t.Errorf("expected an empty diff, got %+v", unchanged)
	}
}
