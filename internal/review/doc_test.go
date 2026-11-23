package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)

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
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
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
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
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
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	if n := len(kinds(d.Stream, LineExpand)); n != 0 {
		t.Errorf("drew %d expand rows with no source to expand from", n)
	}
	if d.CanExpand() {
		t.Error("CanExpand true with no worktree")
	}
}

// The tree beside the diff is in path order. Any other diff order makes the
// two panels disagree about where you are.
func TestFilesOrderedByPath(t *testing.T) {
	d := NewDoc([]File{
		{Path: "z/last.go", Patch: twoHunks},
		{Path: "a/first.go", Patch: twoHunks},
		{Path: "m/mid.go", Patch: twoHunks},
	}, "")
	want := []string{"a/first.go", "m/mid.go", "z/last.go"}
	for i, w := range want {
		if d.Stream.Files[i] != w {
			t.Errorf("file %d = %s, want %s", i, d.Stream.Files[i], w)
		}
	}
}

func TestWordMarksReachTheStream(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
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
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
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

// git diff output ends in a newline, GitHub's patches do not. The trailing
// empty string a split leaves must not become a phantom context row — it
// would add a blank line numbered one past the hunk.
func TestTrailingNewlineIsNotAContextRow(t *testing.T) {
	const p = "@@ -1,2 +1,2 @@\n a := 1\n-b := 2\n+b := 3"
	with := Build([]File{{Path: "demo.go", Patch: p + "\n"}})
	without := Build([]File{{Path: "demo.go", Patch: p}})
	if len(with.Lines) != len(without.Lines) {
		t.Fatalf("trailing newline changed the stream: %d rows vs %d", len(with.Lines), len(without.Lines))
	}
}

// A preamble (what `git diff` prints before the first @@) is skipped, not
// rendered as code.
func TestGitPreambleIsIgnored(t *testing.T) {
	pre := "diff --git a/demo.go b/demo.go\nindex 1..2 100644\n--- a/demo.go\n+++ b/demo.go\n"
	s := Build([]File{{Path: "demo.go", Patch: pre + twoHunks}})
	for _, l := range s.Lines {
		if l.Kind.IsCode() && strings.Contains(l.Text, "index 1..2") {
			t.Fatalf("preamble leaked into the stream: %+v", l)
		}
	}
	if len(s.HunkStarts) != 2 || len(s.HunkEnds) != 2 {
		t.Fatalf("hunks: starts=%v ends=%v", s.HunkStarts, s.HunkEnds)
	}
}

func TestBinaryFileSaysSo(t *testing.T) {
	s := Build([]File{{Path: "logo.png", Status: "modified", Binary: true}})
	var note string
	for _, l := range s.Lines {
		if l.Kind == LineNote {
			note = l.Text
		}
	}
	if !strings.Contains(note, "binary") {
		t.Errorf("binary file note = %q", note)
	}
}

// HunkEnds bound exactly the patch rows: the row at HunkEnds is never one of
// the hunk's own code rows.
func TestHunkEndsBoundThePatchRows(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
	s := d.Stream
	for h := range s.HunkStarts {
		if s.Lines[s.HunkStarts[h]].Kind != LineHunkHeader {
			t.Fatalf("hunk %d starts on %v", h, s.Lines[s.HunkStarts[h]].Kind)
		}
		for i := s.HunkStarts[h] + 1; i < s.HunkEnds[h]; i++ {
			if !s.Lines[i].Kind.IsCode() && s.Lines[i].Kind != LineNote {
				t.Errorf("hunk %d holds a %v row at %d", h, s.Lines[i].Kind, i)
			}
			if s.Lines[i].Expanded {
				t.Errorf("hunk %d holds an expanded row at %d", h, i)
			}
		}
		if s.HunkAt(s.HunkStarts[h]) != h {
			t.Errorf("HunkAt(header %d) = %d", h, s.HunkAt(s.HunkStarts[h]))
		}
	}
	if s.HunkAt(0) != -1 {
		t.Error("the file header belongs to no hunk")
	}
}

func TestExpandRowsOpensOnlyMarkersInRange(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
	if d.ExpandRows(0, 1, false) {
		t.Error("no marker on the header row, yet ExpandRows reported a change")
	}
	before := len(allGaps(d))
	if !d.ExpandRows(0, len(d.Stream.Lines), true) {
		t.Fatal("ExpandRows over the whole stream changed nothing")
	}
	if before == 0 || len(allGaps(d)) != 0 {
		t.Errorf("expand-all over the whole stream left %d of %d markers", len(allGaps(d)), before)
	}
}

// A diffed symlink's patch is its target path. Following it to expand context
// would show some other file's bytes — possibly from outside the worktree.
func TestSymlinkIsNotExpandedFrom(t *testing.T) {
	wt := worktreeWith(t, "real.go", 140)
	if err := os.Symlink(filepath.Join(wt, "real.go"), filepath.Join(wt, "demo.go")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
	if n := len(kinds(d.Stream, LineExpand)); n != 0 {
		t.Errorf("expanded from a symlink: %d fold rows", n)
	}
}

func TestPathClimbingOutOfWorktreeIsNotRead(t *testing.T) {
	outer := worktreeWith(t, "secret.go", 140)
	wt := filepath.Join(outer, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDoc([]File{{Path: "../secret.go", Patch: twoHunks}}, wt)
	if d.CanExpand() {
		t.Error("read a file outside the worktree")
	}
}

func TestFilesReturnsStreamOrder(t *testing.T) {
	d := NewDoc([]File{{Path: "b.go", Patch: twoHunks}, {Path: "a.go", Patch: twoHunks}}, "")
	fs := d.Files()
	if len(fs) != 2 || fs[0].Path != "a.go" || fs[1].Path != "b.go" {
		t.Errorf("Files() = %+v", fs)
	}
}
