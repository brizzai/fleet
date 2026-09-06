package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
)

func loadPatch(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func demoFiles(t *testing.T) []github.PRFile {
	sess, errs := loadPatch(t, "session.patch"), loadPatch(t, "errors.patch")
	return []github.PRFile{
		{Path: "internal/session/session.go", Status: "modified", Additions: 11, Deletions: 0, Patch: sess},
		{Path: "internal/github/errors.go", Status: "modified", Additions: 1, Deletions: 1, Patch: errs},
		{Path: "internal/ui/tips.go", Status: "added", Additions: 22, Deletions: 0, Patch: errs},
	}
}

func newDemoReader(t *testing.T, w, h int) *ReaderDialog {
	t.Helper()
	var d ReaderDialog
	d.SetSize(w, h)
	files := demoFiles(t)
	folded := append(append([]github.PRFile(nil), files...),
		github.PRFile{Path: "go.sum", Status: "modified", Additions: 40, Deletions: 4, Patch: loadPatch(t, "errors.patch")})
	d.Show(283, "repair hooks pointing at a deleted binary", "hayke102", "brizzai/fleet", "",
		files, folded, map[string]bool{"internal/ui/tips.go": true}, nil)
	return &d
}

// The reader is full-screen, so every row must be exactly the terminal's width
// and there must be exactly as many rows as it is tall. A row one column short
// leaves a ragged edge down the side of the screen; one column long wraps and
// shifts everything below it by a row.
func TestReaderRendersExactSize(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{124, 30}, {180, 30}, {80, 24}, {200, 40}, {90, 12}} {
		d := newDemoReader(t, tc.w, tc.h)
		lines := strings.Split(d.View(), "\n")
		if len(lines) != tc.h {
			t.Errorf("%dx%d: rendered %d rows, want %d", tc.w, tc.h, len(lines), tc.h)
			continue
		}
		for i, l := range lines {
			if got := lipgloss.Width(l); got != tc.w {
				t.Errorf("%dx%d: row %d is %d columns, want %d: %q",
					tc.w, tc.h, i, got, tc.w, ansi.Strip(l))
			}
		}
	}
}

// TestReaderDump is an eyeball harness, not an assertion: READER_DUMP=1 prints
// the reader at a few sizes so a change to the layout can be looked at.
func TestReaderDump(t *testing.T) {
	if os.Getenv("READER_DUMP") == "" {
		t.Skip("set READER_DUMP=1 to print")
	}
	w, h := 124, 26
	if s := os.Getenv("READER_W"); s != "" {
		_, _ = fmtSscan(s, &w)
	}
	if s := os.Getenv("READER_H"); s != "" {
		_, _ = fmtSscan(s, &h)
	}
	d := newDemoReader(t, w, h)
	switch os.Getenv("READER_STATE") {
	case "split":
		if !d.split {
			d.Update(keyOf("|"))
		}
	case "unified":
		if d.split {
			d.Update(keyOf("|"))
		}
	case "comment":
		for range 6 {
			d.Update(keyOf("j"))
		}
		d.Update(keyOf("c"))
		for _, r := range "these two need to clear together" {
			d.Update(keyOf(string(r)))
		}
	case "saved":
		for range 6 {
			d.Update(keyOf("j"))
		}
		d.Update(keyOf("c"))
		d.Update(keyOf("tab"))
		for _, r := range "reads as a count of frames; it counts readings" {
			d.Update(keyOf(string(r)))
		}
		d.Update(keyOf("enter"))
	case "search":
		d.Update(keyOf("/"))
		for _, r := range "paneFinished" {
			d.Update(keyOf(string(r)))
		}
	}
	out := d.View()
	if os.Getenv("READER_PLAIN") != "" {
		out = ansi.Strip(out)
	}
	os.Stdout.WriteString("\n" + out + "\n")
}

func fmtSscan(s string, v *int) (int, error) { return fmt.Sscan(s, v) }

func keyOf(s string) tea.KeyPressMsg {
	if len([]rune(s)) == 1 {
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Text: s}
}

// The space bar reports its String() as the word "space", so any handler that
// tests String() for a single rune drops every space the user types. That is a
// comment box you cannot write a sentence in, and it shipped once.
func TestComposerAcceptsSpaces(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	for range 6 {
		d.Update(keyOf("j"))
	}
	d.Update(keyOf("c"))
	if d.mode != modeCompose {
		t.Fatal("c did not open the composer on a code row")
	}
	for _, r := range "two words" {
		d.Update(keyOf(string(r)))
	}
	if d.draft.Body != "two words" {
		t.Errorf("draft = %q, want %q", d.draft.Body, "two words")
	}
}

// A Ctrl chord is a command, not text — some terminals set Text on it anyway.
func TestComposerIgnoresChords(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	for range 6 {
		d.Update(keyOf("j"))
	}
	d.Update(keyOf("c"))
	d.Update(tea.KeyPressMsg{Code: 'g', Text: "g", Mod: tea.ModCtrl})
	if d.draft.Body != "" {
		t.Errorf("chord typed %q into the draft", d.draft.Body)
	}
}

// readerWithWorktree builds a checkout the reader can expand context out of.
func readerWithWorktree(t *testing.T, w, h int) *ReaderDialog {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/internal/github", 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 90; i++ {
		fmt.Fprintf(&b, "// line %d of the real file on disk\n", i)
	}
	if err := os.WriteFile(dir+"/internal/github/errors.go", []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []github.PRFile{{
		Path: "internal/github/errors.go", Status: "modified",
		Additions: 1, Deletions: 1, Patch: loadPatch(t, "errors.patch"),
	}}
	var d ReaderDialog
	d.SetSize(w, h)
	d.Show(283, "repair hooks", "hayke102", "brizzai/fleet", dir, files, files, nil, nil)
	return &d
}

// A fold is the one control in the reader you can walk past without noticing,
// so it must both exist and open — and it must open from the worktree, with no
// API call, which is the whole reason fleet can offer it where tuicr pays a
// round trip.
func TestExpandOpensFromTheWorktree(t *testing.T) {
	d := readerWithWorktree(t, 124, 30)

	fold := -1
	for i := range d.rows {
		if l, ok := d.lineAt(i); ok && l.Kind == review.LineExpand {
			fold = i
			break
		}
	}
	if fold < 0 {
		t.Fatal("no fold marker with a worktree behind the reader")
	}

	d.moveTo(fold)
	before := len(d.rows)
	d.Update(keyOf("enter"))
	if len(d.rows) <= before {
		t.Fatalf("enter on a fold did not open it: %d -> %d rows", before, len(d.rows))
	}

	var sawReal bool
	for _, l := range d.doc.Stream.Lines {
		if l.Expanded && strings.Contains(l.Text, "of the real file on disk") {
			sawReal = true
		}
	}
	if !sawReal {
		t.Error("expanded rows do not carry the worktree's own bytes")
	}
}

// GitHub anchors a review comment to a position inside the diff it was sent, so
// a line we revealed out of the worktree has nothing to attach to. Refusing and
// saying why beats a comment that silently lands on a different line.
func TestNoCommentOnExpandedContext(t *testing.T) {
	d := readerWithWorktree(t, 124, 30)
	for i := range d.rows {
		if l, ok := d.lineAt(i); ok && l.Kind == review.LineExpand {
			d.moveTo(i)
			d.Update(keyOf("enter"))
			break
		}
	}
	for i := range d.rows {
		l, ok := d.lineAt(i)
		if !ok || !l.Expanded {
			continue
		}
		d.moveTo(i)
		d.Update(keyOf("c"))
		if d.mode == modeCompose {
			t.Fatal("composer opened on a line GitHub cannot anchor to")
		}
		if d.toast == "" {
			t.Fatal("refused silently — the reader must say why")
		}
		return
	}
	t.Fatal("no expanded row to test against")
}

// rowFor renders the display row showing the given stream predicate.
//
// Checks BOTH sides of the row: in split mode a deletion and the addition that
// replaced it share one row, and lineAt reports only the left one — so a
// predicate on additions would never match, which is a bug in the test rather
// than in the reader.
func rowFor(t *testing.T, d *ReaderDialog, pred func(review.Line) bool) string {
	t.Helper()
	for i, r := range d.rows {
		for _, idx := range []int{r.left, r.right} {
			if l, ok := d.streamLine(idx); ok && pred(l) {
				return d.renderRow(i, d.diffWidth()-2)
			}
		}
	}
	t.Fatal("no row matched")
	return ""
}

func hasBG(s string, c interface {
	RGBA() (uint32, uint32, uint32, uint32)
}) bool {
	r, g, b, _ := c.RGBA()
	return strings.Contains(s, fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8))
}

func hasFG(s string, c interface {
	RGBA() (uint32, uint32, uint32, uint32)
}) bool {
	r, g, b, _ := c.RGBA()
	return strings.Contains(s, fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8))
}

// The two colour channels are the whole redesign: the BACKGROUND says added or
// deleted, the FOREGROUND says what the code is. Painting the foreground green
// to mean "added" is what made syntax highlighting and word diff impossible.
func TestDiffStateIsBackgroundAndSyntaxIsForeground(t *testing.T) {
	d := newDemoReader(t, 160, 30)
	add := rowFor(t, d, func(l review.Line) bool {
		return l.Kind == review.LineAdd && strings.Contains(l.Text, "return fmt.Errorf")
	})
	if !hasBG(add, ColorDiffAddBg) && !hasBG(add, ColorDiffAddWord) {
		t.Error("an added row carries no add tint in its background")
	}
	if !hasFG(add, ColorSynString) {
		t.Error("the string literal on an added row is not painted with the syntax string colour")
	}
	if !hasFG(add, ColorSynKeyword) && !hasFG(add, ColorSynType) {
		t.Error("`return` on an added row got no keyword colour")
	}
	// The crisp statement of the rule: the SAME token gets the SAME foreground
	// whether or not its line was added — only the background differs. If the
	// foreground still carried add/delete, an unchanged string literal would be
	// painted differently from an added one.
	ctx := rowFor(t, d, func(l review.Line) bool {
		return l.Kind == review.LineContext && strings.Contains(l.Text, "rate limit")
	})
	if !hasFG(ctx, ColorSynString) {
		t.Error("the string literal on an UNCHANGED row lost the syntax string colour")
	}
	if hasBG(ctx, ColorDiffAddBg) || hasBG(ctx, ColorDiffDelBg) {
		t.Error("an unchanged row carries a diff tint")
	}
}

func rgb(c interface {
	RGBA() (uint32, uint32, uint32, uint32)
}) (int, int, int) {
	r, g, b, _ := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

// Word-level diff is the reason the two channels had to be separated: on a line
// where one argument changed, the brightened runes ARE the review.
func TestWordDiffBrightensOnlyTheChangedRuns(t *testing.T) {
	d := newDemoReader(t, 160, 30)
	add := rowFor(t, d, func(l review.Line) bool {
		return l.Kind == review.LineAdd && len(l.Marks) > 0
	})
	if !hasBG(add, ColorDiffAddWord) {
		t.Fatal("a word-marked row carries no word-diff background")
	}
	// It must be a RUN, not the row: a marked row that is entirely bright says
	// nothing the row tint did not already say. So the quiet tint has to still
	// be present on the same row, around the mark.
	if !hasBG(add, ColorDiffAddBg) {
		t.Error("the whole added row is word-marked — the mark should be a run inside it")
	}
	// The deleted counterpart carries marks only where something was actually
	// removed. Here the edit only appended, so the old line is unmarked and
	// that is correct — the del side is covered by review.TestWordDiff*.
}

// Every theme has to work, and the review colours are derived rather than
// hand-picked precisely so a new palette gets a working diff for free.
func TestReviewColorsDeriveInEveryTheme(t *testing.T) {
	defer ApplyPalette(PaletteFleetPink)
	for _, p := range BuiltinPalettes {
		ApplyPalette(p)
		bg := []struct {
			name string
			c    interface {
				RGBA() (uint32, uint32, uint32, uint32)
			}
		}{
			{"add row", ColorDiffAddBg}, {"add gutter", ColorDiffAddGutter}, {"add word", ColorDiffAddWord},
			{"del row", ColorDiffDelBg}, {"del gutter", ColorDiffDelGutter}, {"del word", ColorDiffDelWord},
		}
		seen := map[string]string{}
		for _, b := range bg {
			r, g, bl := rgb(b.c)
			key := fmt.Sprintf("%d,%d,%d", r, g, bl)
			if prev, dup := seen[key]; dup {
				t.Errorf("%s: %s and %s derived to the same colour %s — the tint ladder collapsed",
					p.Name, prev, b.name, key)
			}
			seen[key] = b.name
		}
		// A tint indistinguishable from the ground is not a tint.
		br, bgc, bb := rgb(p.Bg)
		ar, ag, ab := rgb(ColorDiffAddBg)
		if abs(br-ar)+abs(bgc-ag)+abs(bb-ab) < 6 {
			t.Errorf("%s: the add tint is indistinguishable from the background", p.Name)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func spaceKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: ' ', Text: " "} }

// Space is the verb you press most in a review: this file is done, take me to
// the next one I have not read.
func TestSpaceMarksReadAndAdvances(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	files := d.doc.Stream.Files
	if len(files) < 3 {
		t.Fatalf("fixture has %d files, need 3", len(files))
	}
	// tips.go arrives already read, so spacing off the first file must skip it.
	if !d.read[files[2]] {
		t.Fatalf("fixture expected %s pre-read", files[2])
	}

	d.moveTo(0)
	d.Update(spaceKey())

	if !d.read[files[0]] {
		t.Errorf("space did not mark %s read", files[0])
	}
	if got := d.doc.Stream.FileAt(d.streamAt(d.cursor)); got != files[1] {
		t.Errorf("landed on %s, want the next unread file %s", got, files[1])
	}

	// From the second, the third is already read — so space should land there
	// anyway rather than swallowing the keystroke, and say nothing is left.
	d.Update(spaceKey())
	if !d.read[files[1]] {
		t.Errorf("space did not mark %s read", files[1])
	}
	if d.toast == "" {
		t.Error("space reported nothing about what is left")
	}
}

// Idempotent, because it means "done", not "toggle". A key that un-read a file
// every time you passed back over it would quietly undo your own progress.
func TestSpaceNeverUnmarks(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	first := d.doc.Stream.Files[0]
	d.moveTo(0)
	d.Update(spaceKey())
	d.jumpStream(d.doc.Stream.FileStarts[0])
	d.Update(spaceKey())
	if !d.read[first] {
		t.Error("pressing space twice on one file un-read it")
	}
}

// m is the correction: it toggles and does not move.
func TestMarkKeyTogglesInPlace(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	first := d.doc.Stream.Files[0]
	d.moveTo(0)
	at := d.cursor
	d.Update(keyOf("m"))
	if !d.read[first] || d.cursor != at {
		t.Errorf("m: read=%v cursor moved %d->%d", d.read[first], at, d.cursor)
	}
	d.Update(keyOf("m"))
	if d.read[first] {
		t.Error("m did not toggle back to unread")
	}
}

// KeyPressMsg.String() reports the key's NAME, so the space bar arrives as
// "space" and a `case " "` matches nothing. Space was bound that way and did
// nothing at all until this was found.
func TestSpaceIsMatchedByNameNotByCharacter(t *testing.T) {
	if got := spaceKey().String(); got != "space" {
		t.Fatalf("space stringifies as %q — the reader's case must match it", got)
	}
	d := newDemoReader(t, 124, 30)
	d.moveTo(0)
	before := d.cursor
	d.Update(spaceKey())
	if d.cursor == before {
		t.Error("space did not act — it is probably bound as \" \" again")
	}
}
