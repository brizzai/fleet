package ui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

var updateReviewGolden = flag.Bool("update-review-golden", false, "rewrite internal/ui/testdata/review_*.golden")

// reviewCodeFixture is two hunks with a real gap between them, a paired
// replacement (word marks), a pure addition and a tab-indented line — the row
// shapes a stop card actually meets.
const reviewCodeFixture = `@@ -1,4 +1,5 @@
 package demo

+import "time"

 func first() {
@@ -40,4 +41,5 @@ func second() {
 	a := 1
-	retryDelay := 2 * time.Second
+	retryTimeout := 5 * time.Second
+	log.Printf("waiting %v", retryTimeout)
 	return a
 }`

func reviewCodeDoc(t *testing.T, worktree string) *review.Doc {
	t.Helper()
	return review.NewDoc([]review.File{
		{Path: "pkg/demo.go", Status: "modified", Additions: 3, Deletions: 1, Patch: reviewCodeFixture},
	}, worktree)
}

// plainLines strips styling and trailing spaces so a golden reads as text and
// survives a theme change; the colour rules have their own tests.
func plainLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strings.TrimRight(ansi.Strip(l), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateReviewGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-review-golden to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

func assertExactWidth(t *testing.T, lines []string, width int) {
	t.Helper()
	for i, l := range lines {
		if w := lipgloss.Width(l); w != width {
			t.Errorf("line %d is %d cells wide, want exactly %d: %q", i, w, width, ansi.Strip(l))
		}
	}
}

func TestRenderHunkUnifiedGolden(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	span, ok := review.BindRange(d, "pkg/demo.go", review.SideNew, 42, 43)
	if !ok || !span.Exact {
		t.Fatalf("bind: %+v ok=%v", span, ok)
	}
	lines := RenderHunk(d, span.From, span.To, 80, false)
	assertExactWidth(t, lines, 80)
	checkGolden(t, "review_hunk_unified_80.golden", plainLines(lines))
}

func TestRenderHunkSideBySideGolden(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	lines := RenderHunk(d, 0, len(d.Stream.Lines), 160, true)
	assertExactWidth(t, lines, 160)
	checkGolden(t, "review_hunk_split_160.golden", plainLines(lines))
}

// Side by side keeps a replaced line level with its replacement: the deletion
// and the first addition share one row, the extra addition gets a row with an
// empty left cell.
func TestRenderHunkSideBySidePairsReplacements(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	lines := RenderHunk(d, 0, len(d.Stream.Lines), 160, true)
	var paired, rightOnly bool
	for _, l := range lines {
		p := ansi.Strip(l)
		if strings.Contains(p, "retryDelay") && strings.Contains(p, "retryTimeout :=") {
			paired = true
		}
		if strings.Contains(p, "log.Printf") && !strings.Contains(p, "retryDelay") {
			left, _, _ := strings.Cut(p, "│")
			if strings.TrimSpace(left) == "" {
				rightOnly = true
			}
		}
	}
	if !paired {
		t.Error("the replaced line and its replacement are not on one row")
	}
	if !rightOnly {
		t.Error("the unpaired addition should sit alone on the right")
	}
}

// The renderer never overflows, however narrow the card gets — a line one
// column too wide wraps inside the card's border and shears the whole box.
func TestRenderHunkNeverOverflows(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	for _, w := range []int{1, 4, 9, 12, 20, 33, 61} {
		for _, split := range []bool{false, true} {
			lines := RenderHunk(d, 0, len(d.Stream.Lines), w, split)
			for i, l := range lines {
				if got := lipgloss.Width(l); got > w {
					t.Errorf("width %d split=%v line %d is %d wide", w, split, i, got)
				}
			}
		}
	}
	if RenderHunk(nil, 0, 1, 80, false) != nil || RenderHunk(d, 0, 1, 0, false) != nil {
		t.Error("nil doc or zero width should render nothing")
	}
	if RenderHunk(d, 5, 2, 80, false) != nil {
		t.Error("an empty span should render nothing")
	}
}

// A span bound with folds on its edges names the key that opens them.
func TestRenderHunkOffersExpandOnFolds(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	dir := t.TempDir()
	var src strings.Builder
	for i := 1; i <= 60; i++ {
		src.WriteString("// filler\n")
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "demo.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	d := reviewCodeDoc(t, dir)
	span, _ := review.BindRange(d, "pkg/demo.go", review.SideNew, 42, 42)
	out := plainLines(RenderHunk(d, span.From, span.To, 80, false))
	if !strings.Contains(out, "lines hidden") || !strings.Contains(out, "e more context") {
		t.Errorf("fold rows missing or mute:\n%s", out)
	}
}

// Tabs render at a fixed width, never as a raw tab the terminal expands.
func TestRenderCodeExpandsTabs(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	for _, l := range RenderHunk(d, 0, len(d.Stream.Lines), 80, false) {
		if strings.Contains(l, "\t") {
			t.Fatalf("raw tab in rendered row %q", ansi.Strip(l))
		}
	}
}

// A search hit outranks both colour channels on the run it covers.
func TestRenderCodeLightsSearchHits(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	l := review.Line{Kind: review.LineContext, Text: "return value", Toks: []review.Token{{Text: "return value"}}}
	with := renderCode(l, 40, false, "value")
	without := renderCode(l, 40, false, "")
	if with == without {
		t.Error("a query changed nothing on a row that contains it")
	}
	if ansi.Strip(with) != "return value" {
		t.Errorf("search changed the text: %q", ansi.Strip(with))
	}
}

// The cursor row keeps its add/delete tint (lifted), and the caret gutter
// costs exactly one column.
func TestRenderDiffRowCaret(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	d := reviewCodeDoc(t, "")
	rows := buildRows(d.Stream, false)
	o := diffRenderOpts{cursor: 2, caret: true}
	lines := renderDiffRows(d.Stream, rows, 60, false, o)
	assertExactWidth(t, lines, 60)
	if !strings.HasPrefix(ansi.Strip(lines[2]), "▸") {
		t.Errorf("cursor row has no caret: %q", ansi.Strip(lines[2]))
	}
	if strings.HasPrefix(ansi.Strip(lines[1]), "▸") {
		t.Error("caret drawn on a row that is not the cursor")
	}
}

func TestRowOfStreamSurvivesLayoutToggle(t *testing.T) {
	d := reviewCodeDoc(t, "")
	unified, split := buildRows(d.Stream, false), buildRows(d.Stream, true)
	for i, l := range d.Stream.Lines {
		if l.Kind != review.LineAdd {
			continue
		}
		r := split[rowOfStream(split, i)]
		if r.left != i && r.right != i {
			t.Errorf("stream row %d mapped to split row %+v", i, r)
		}
		if unified[rowOfStream(unified, i)].left != i {
			t.Errorf("stream row %d lost in unified", i)
		}
	}
}

// Every row kind, as the cursor row, in both layouts, stays exactly as wide
// as asked — the cursor lift must never change a row's width.
func TestEveryRowAsCursorKeepsItsWidth(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	for i := 1; i <= 60; i++ {
		src.WriteString("x := 1\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "demo.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	d := review.NewDoc([]review.File{
		{Path: "pkg/demo.go", Patch: reviewCodeFixture},
		{Path: "logo.png", Binary: true},
	}, dir)
	for _, split := range []bool{false, true} {
		rows := buildRows(d.Stream, split)
		for c := range rows {
			lines := renderDiffRows(d.Stream, rows, 90, split, diffRenderOpts{cursor: c, caret: true, expandHint: "e expand"})
			assertExactWidth(t, lines, 90)
		}
	}
}
