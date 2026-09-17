package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/github"
)

// twoHunks leaves a real gap between them: the first ends at new line 5, the
// second starts at 80, so lines 6..79 are unchanged and foldable — wide enough
// that one expand step on each side does NOT close it.
const twoHunks = `@@ -1,4 +1,5 @@
 package demo
 
+import "time"
 
 func first() {
@@ -79,3 +80,4 @@ func second() {
 	a := 1
-	b := 2
+	b := 3
 }`

// addedLine is the new-side number of the sole added row in the second hunk:
// the hunk opens at 80 with one context row, so the `+` lands on 81.
const addedLine = 81

// allGaps returns every fold in the document, not just the first — a two-hunk
// patch has a trailing gap as well, and a test that expands one and asserts on
// the total is really asserting the other one does not exist.
func allGaps(d *Doc) []*Gap {
	var out []*Gap
	for _, l := range d.Stream.Lines {
		if l.Kind == LineExpand {
			out = append(out, l.Gap)
		}
	}
	return out
}

func worktreeWith(t *testing.T, path string, lines int) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString("line ")
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString("\n")
	}
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func kinds(s Stream, k LineKind) []Line {
	var out []Line
	for _, l := range s.Lines {
		if l.Kind == k {
			out = append(out, l)
		}
	}
	return out
}

func TestExpandRevealsWorktreeLines(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, wt, 40)

	gaps := kinds(d.Stream, LineExpand)
	if len(gaps) == 0 {
		t.Fatal("a 34-line gap between hunks must render an expand row")
	}
	g := gaps[0].Gap
	before := len(d.Stream.Lines)

	if !d.Expand(g, false) {
		t.Fatal("Expand reported no change")
	}
	if len(d.Stream.Lines) <= before {
		t.Fatalf("stream did not grow: %d -> %d", before, len(d.Stream.Lines))
	}

	// Every revealed row is real content from the worktree, numbered on the
	// new side, and flagged so the reader never offers to comment on it.
	var revealed int
	for _, l := range d.Stream.Lines {
		if l.Expanded {
			revealed++
			if l.New == 0 || l.Text == "" {
				t.Fatalf("revealed row is not a real line: %+v", l)
			}
		}
	}
	if revealed != 2*expandStep {
		t.Errorf("revealed %d lines, want %d (one step each way)", revealed, 2*expandStep)
	}
	if g.Hidden() == 0 {
		t.Error("a 74-line gap should still be folded after one step each way")
	}
}

func TestExpandAllClosesTheGap(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, wt, 40)
	for _, g := range allGaps(d) {
		d.Expand(g, true)
	}
	if n := len(kinds(d.Stream, LineExpand)); n != 0 {
		t.Errorf("expand-all left %d fold markers", n)
	}
}

// The frontiers meeting must close the gap outright. Leaving a marker over
// zero hidden lines renders "⋯ 0 lines hidden", which is a control that does
// nothing sitting where a control that did something used to be.
func TestExpandStepsClosePreciselyWhenTheyMeet(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, wt, 40)
	g := kinds(d.Stream, LineExpand)[0].Gap
	for range 20 {
		if !d.Expand(g, false) {
			break
		}
	}
	if g.Hidden() != 0 {
		t.Errorf("gap still hides %d lines after repeated expansion", g.Hidden())
	}
	for _, l := range d.Stream.Lines {
		if l.Kind == LineExpand && l.Gap == g {
			t.Error("closed gap still draws a marker")
		}
	}
}

// With no worktree there are no bytes to reveal, so the reader must not offer
// a control it cannot honour.
func TestNoWorktreeDrawsNoExpandRows(t *testing.T) {
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, "", 40)
	if n := len(kinds(d.Stream, LineExpand)); n != 0 {
		t.Errorf("drew %d expand rows with no source to expand from", n)
	}
	if d.CanExpand() {
		t.Error("CanExpand true with no worktree")
	}
}

func TestCommentRendersAtItsAnchor(t *testing.T) {
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, "", 40)
	d.AddComment(Comment{File: "demo.go", Line: addedLine, Kind: CommentNit, Body: "b is a poor name for a counter"})

	var anchorRow, boxRow = -1, -1
	for i, l := range d.Stream.Lines {
		if l.Kind == LineAdd && l.New == addedLine {
			anchorRow = i
		}
		if l.Kind == LineCommentTop {
			boxRow = i
		}
	}
	if anchorRow < 0 {
		t.Fatalf("no added row at new line %d", addedLine)
	}
	if boxRow != anchorRow+1 {
		t.Errorf("comment box at row %d, want directly under its anchor at %d", boxRow, anchorRow+1)
	}
	if d.Pending() != 1 {
		t.Errorf("pending = %d, want 1", d.Pending())
	}
}

func TestCommentBodyWrapsAndSurvivesDelete(t *testing.T) {
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, "", 24)
	id := d.AddComment(Comment{File: "demo.go", Line: addedLine, Body: strings.Repeat("word ", 20)})
	if n := len(kinds(d.Stream, LineCommentBody)); n < 4 {
		t.Errorf("a 100-char comment at width 24 wrapped to %d rows", n)
	}
	d.DeleteComment(id)
	if n := len(kinds(d.Stream, LineCommentTop)); n != 0 {
		t.Errorf("deleted comment still renders %d boxes", n)
	}
}

// The tree beside the diff is in path order. Any other diff order makes the
// two panels disagree about where you are.
func TestFilesOrderedByPath(t *testing.T) {
	d := NewDoc([]github.PRFile{
		{Path: "z/last.go", Patch: twoHunks},
		{Path: "a/first.go", Patch: twoHunks},
		{Path: "m/mid.go", Patch: twoHunks},
	}, "", 40)
	want := []string{"a/first.go", "m/mid.go", "z/last.go"}
	for i, w := range want {
		if d.Stream.Files[i] != w {
			t.Errorf("file %d = %s, want %s", i, d.Stream.Files[i], w)
		}
	}
}

func TestWordMarksReachTheStream(t *testing.T) {
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, "", 40)
	var marked int
	for _, l := range d.Stream.Lines {
		if len(l.Marks) > 0 {
			marked++
		}
	}
	// `b := 2` → `b := 3` is one paired edit: both sides carry a mark.
	if marked != 2 {
		t.Errorf("%d rows carry word marks, want 2 (the paired −/+)", marked)
	}
}

func TestSearchFindsCodeAndNotChrome(t *testing.T) {
	d := NewDoc([]github.PRFile{{Path: "demo.go", Patch: twoHunks}}, "", 40)
	d.AddComment(Comment{File: "demo.go", Line: addedLine, Body: "package"})
	hits := d.Stream.Search("package")
	if len(hits) != 1 {
		t.Fatalf("%d hits for 'package', want only the code row", len(hits))
	}
	if d.Stream.Lines[hits[0]].Kind != LineContext {
		t.Errorf("hit landed on %v, want a code row", d.Stream.Lines[hits[0]].Kind)
	}
}

// A single-line hunk header has no comma. Reading its length as zero puts the
// following gap on top of the hunk it follows.
func TestSingleLineHunkHasLengthOne(t *testing.T) {
	if _, _, n := parseHunkRange("@@ -1 +1 @@"); n != 1 {
		t.Errorf("newCount = %d, want 1", n)
	}
	if _, _, n := parseHunkRange("@@ -1,4 +1,5 @@"); n != 5 {
		t.Errorf("newCount = %d, want 5", n)
	}
}
