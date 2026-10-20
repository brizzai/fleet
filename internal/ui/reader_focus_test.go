package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

func tabKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab} }

// ⇥ moves the keyboard between the two panels, the same relationship the
// sidebar and preview have on the main screen.
func TestTabMovesFocusBetweenTreeAndDiff(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	if d.treeFocus {
		t.Fatal("the reader must open with the diff holding the keyboard")
	}
	d.Update(tabKey())
	if !d.treeFocus {
		t.Fatal("tab did not move focus to the tree")
	}
	d.Update(tabKey())
	if d.treeFocus {
		t.Error("tab did not move focus back to the diff")
	}
}

// Taking focus must never move you: the tree's selection starts on the file the
// diff is already showing.
func TestFocusStartsOnTheFileYouWereReading(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.jumpStream(d.doc.Stream.FileStarts[1])
	want := d.doc.Stream.Files[1]

	d.Update(tabKey())
	if got := d.treeSelected(); got != want {
		t.Errorf("tree selection landed on %q, want the file already on screen %q", got, want)
	}
}

// The list drives the panel beside it — that is what makes this the
// sidebar-and-preview pattern rather than a second cursor you have to commit.
func TestTreeSelectionScrollsTheDiff(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	// Walk to the next FILE: directories are selectable now and deliberately
	// do not move the diff, so stopping on one proves nothing either way.
	sel := ""
	for range len(d.treeView) {
		d.Update(keyOf("down"))
		if sel = d.treeSelected(); sel != "" && sel != d.doc.Stream.Files[0] {
			break
		}
	}
	if sel == "" {
		t.Fatal("never reached a file")
	}
	if got := d.doc.Stream.FileAt(d.streamAt(d.cursor)); got != sel {
		t.Errorf("diff is showing %q while the tree selects %q — the two disagree", got, sel)
	}
}

// The tree and the diff must never disagree about which file you are on. A
// directory selection carries the diff to the FIRST file under it rather than
// leaving it somewhere else — a folder row still answers "where am I".
func TestTreeAndDiffAlwaysNameTheSameFile(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())

	implied := func() string {
		n := d.treeView[d.treeCursor]
		if !n.IsDir {
			return n.Path
		}
		for _, f := range d.doc.Stream.Files {
			if strings.HasPrefix(f, n.Path+"/") {
				return f
			}
		}
		return ""
	}

	var sawDir bool
	for range len(d.treeView) {
		d.Update(keyOf("down"))
		if d.treeView[d.treeCursor].IsDir {
			sawDir = true
		}
		want := implied()
		if want == "" {
			continue
		}
		if got := d.doc.Stream.FileAt(d.streamAt(d.cursor)); got != want {
			t.Fatalf("tree row %q implies %q but the diff is showing %q",
				d.treeView[d.treeCursor].Label, want, got)
		}
	}
	if !sawDir {
		t.Fatal("never landed on a directory — they should be selectable")
	}
}

// Driving the DIFF drags the tree's selection along, so the left panel is
// always a picture of where you are — not just after you press ⇥.
func TestTreeSelectionFollowsTheDiff(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	for _, key := range []string{"space", "]", "."} {
		for range 3 {
			if key == "space" {
				d.Update(spaceKey())
			} else {
				d.Update(keyOf(key))
			}
			cur := d.doc.Stream.FileAt(d.streamAt(d.cursor))
			if cur == "" {
				continue
			}
			sel := d.treeView[d.treeCursor]
			if sel.IsDir || sel.Path != cur {
				t.Fatalf("after %q the diff is on %q but the tree selects %q",
					key, cur, sel.Path)
			}
		}
	}
}

// Holds at the ends, like every other jump in the reader.
func TestTreeSelectionHoldsAtTheEnds(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	for range len(d.treeView) + 5 {
		d.Update(keyOf("up"))
	}
	first := d.treeCursor
	d.Update(keyOf("up"))
	if d.treeCursor != first {
		t.Errorf("selection moved past the top: %d -> %d", first, d.treeCursor)
	}
}

// Enter hands the keyboard back; the diff already moved as the selection did.
func TestEnterInTreeReturnsToTheDiff(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	// Land on a file: on a directory ⏎ folds instead, which is its own test.
	sel := d.treeSelected()
	for range len(d.treeView) {
		if sel != "" {
			break
		}
		d.Update(keyOf("down"))
		sel = d.treeSelected()
	}
	if sel == "" {
		t.Fatal("no file to select")
	}
	d.Update(keyOf("enter"))
	if d.treeFocus {
		t.Error("enter did not hand the keyboard back to the diff")
	}
	if got := d.doc.Stream.FileAt(d.streamAt(d.cursor)); got != sel {
		t.Errorf("diff moved off %q on the way back", sel)
	}
}

// Esc steps back one level before it leaves, the way it does on the main
// screen. Ctrl+Q always quits, from either panel.
func TestEscLeavesFocusBeforeItLeavesTheReader(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.treeFocus {
		t.Error("esc did not drop tree focus")
	}
	if !d.Visible() {
		t.Fatal("esc quit the reader instead of stepping back one level")
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.Visible() {
		t.Error("a second esc did not leave the reader")
	}

	d2 := newDemoReader(t, 124, 30)
	d2.Update(tabKey())
	d2.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if d2.Visible() {
		t.Error("ctrl+q must quit from the tree too — it is the one key that always leaves")
	}
}

// The accent border is fleet's only border-level focus signal (design-system
// §4), so it has to follow the keyboard rather than sit on the diff forever.
func TestAccentBorderFollowsTheKeyboard(t *testing.T) {
	rgbSeq := func(c interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}) string {
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
	}
	accent, muted := rgbSeq(ColorAccent), rgbSeq(ColorBorder)

	d := newDemoReader(t, 124, 14)
	// The tree panel's left border opens every body row, so the row's leading
	// escape sequence is that border's colour.
	lead := func() string { return strings.SplitN(strings.Split(d.View(), "\n")[2], "│", 2)[0] }

	if got := lead(); !strings.Contains(got, muted) {
		t.Errorf("with the diff focused the tree border should be muted, got %q", got)
	}
	d.Update(tabKey())
	if got := lead(); !strings.Contains(got, accent) {
		t.Errorf("with the tree focused its border should be accent, got %q", got)
	}
	d.Update(tabKey())
	if got := lead(); !strings.Contains(got, muted) {
		t.Errorf("handing the keyboard back left the tree border accented: %q", got)
	}
}

// Below the width where a tree is drawn there is nothing to focus, so the key
// must say so rather than appear to do nothing.
func TestFocusRefusedWhenThereIsNoTree(t *testing.T) {
	d := newDemoReader(t, 70, 24)
	if d.treeWidth() != 0 {
		t.Skip("70 columns still draws a tree")
	}
	d.Update(tabKey())
	if d.treeFocus {
		t.Fatal("focused a tree that is not on screen")
	}
	if d.toast == "" {
		t.Error("refused silently — the reader must say why")
	}
}

// The footer names what the keys do NOW, so it has to change with focus.
func TestFooterFollowsFocus(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	diffKeys := ansi.Strip(d.footer())
	d.Update(tabKey())
	treeKeys := ansi.Strip(d.footer())
	if diffKeys == treeKeys {
		t.Fatal("the footer says the same thing in both panels")
	}
	if !strings.Contains(treeKeys, "back to diff") {
		t.Errorf("the tree's footer does not say how to get back: %q", treeKeys)
	}
	if lipgloss.Width(d.footer()) > d.width {
		t.Error("the tree footer overflows the terminal")
	}
}

// The change count is the tree's only number, so the name has to be budgeted
// against every fixed column in the row. Getting it short truncates the count
// rather than the filename, and `+11` renders as `+1` — a wrong number, which
// is worse than a shortened name.
func TestTreeNeverTruncatesTheChangeCount(t *testing.T) {
	for _, w := range []int{90, 96, 104, 118, 124, 160, 200} {
		d := newDemoReader(t, w, 20)
		if d.treeWidth() == 0 {
			continue
		}
		rows := d.renderTree(d.treeWidth()-2, 12)
		for _, r := range rows {
			plain := strings.TrimRight(ansi.Strip(r), " ")
			if plain == "" || strings.HasSuffix(plain, "/") {
				continue
			}
			if lipgloss.Width(r) > d.treeWidth()-2 {
				t.Errorf("w=%d: tree row overflows its panel by %d: %q",
					w, lipgloss.Width(r)-(d.treeWidth()-2), plain)
			}
			// Every file row ends in its count; a truncated one loses digits.
			if i := strings.LastIndex(plain, "+"); i >= 0 {
				if got := plain[i:]; got == "+1" && strings.Contains(plain, "session.go") {
					t.Errorf("w=%d: session.go's count rendered as %q, want +11", w, got)
				}
			}
		}
	}
}

// dirRow puts the tree selection on the first directory row and returns it.
func dirRow(t *testing.T, d *ReaderDialog) int {
	t.Helper()
	for i, n := range d.treeView {
		if n.IsDir {
			d.treeCursor = i
			return i
		}
	}
	t.Fatal("the fixture has no directory rows")
	return 0
}

// Folding a directory hides everything beneath it.
func TestFoldingADirectoryHidesItsSubtree(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	i := dirRow(t, d)
	dir := d.treeView[i]
	before := len(d.treeView)

	d.Update(keyOf("enter"))
	if len(d.treeView) >= before {
		t.Fatalf("folding %q did not hide anything: %d -> %d rows", dir.Label, before, len(d.treeView))
	}
	for _, n := range d.treeView {
		if n.Path != dir.Path && strings.HasPrefix(n.Path, dir.Path+"/") {
			t.Errorf("%q is still visible under folded %q", n.Path, dir.Path)
		}
	}
	d.Update(keyOf("enter"))
	if len(d.treeView) != before {
		t.Errorf("unfolding did not restore the rows: %d, want %d", len(d.treeView), before)
	}
}

// Folding shifts every row below it, so the selection has to be restored by
// identity. Keeping the index would leave you standing on a different thing
// than the one you just folded.
func TestFoldingKeepsTheSelectionOnTheFolder(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	// Pick a nested directory, so folding it actually shifts the rows after it.
	target := -1
	for i, n := range d.treeView {
		if n.IsDir && n.Depth > 0 {
			target = i
			break
		}
	}
	if target < 0 {
		t.Skip("fixture has no nested directory")
	}
	d.treeCursor = target
	want := d.treeView[target].Path

	d.Update(keyOf("enter"))
	if got := d.treeView[d.treeCursor].Path; got != want {
		t.Errorf("after folding, the selection sits on %q, want %q", got, want)
	}
	if !d.collapsed[want] {
		t.Errorf("%q is not recorded as folded", want)
	}
}

// ← and → fold and unfold, the way every file tree does.
func TestArrowsFoldAndUnfold(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	i := dirRow(t, d)
	dir := d.treeView[i].Path

	d.Update(keyOf("left"))
	if !d.collapsed[dir] {
		t.Errorf("← did not fold %q", dir)
	}
	d.Update(keyOf("right"))
	if d.collapsed[dir] {
		t.Errorf("→ did not unfold %q", dir)
	}
}

// ← on a file jumps out to its folder — what ← means in a tree, and the fastest
// way to fold the folder you are standing in.
func TestLeftOnAFileClimbsToItsFolder(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	for i, n := range d.treeView {
		if !n.IsDir && n.Depth > 0 {
			d.treeCursor = i
			break
		}
	}
	if d.treeView[d.treeCursor].IsDir {
		t.Skip("fixture has no nested file")
	}
	depth := d.treeView[d.treeCursor].Depth

	d.Update(keyOf("left"))
	n := d.treeView[d.treeCursor]
	if !n.IsDir || n.Depth >= depth {
		t.Errorf("← landed on %q (dir=%v depth=%d), want the parent folder", n.Label, n.IsDir, n.Depth)
	}
}

// Taking focus while your file is hidden inside a folded folder must unfold it
// rather than dropping the selection somewhere else — that is the cursor moving
// on its own, which the design system forbids.
func TestTakingFocusUnfoldsOntoTheCurrentFile(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	for i, n := range d.treeView {
		if n.IsDir {
			d.treeCursor = i
			d.Update(keyOf("enter")) // fold everything
			break
		}
	}
	d.Update(tabKey()) // back to the diff

	want := d.doc.Stream.Files[1]
	for fi, f := range d.doc.Stream.Files {
		if f == want {
			d.jumpStream(d.doc.Stream.FileStarts[fi])
		}
	}
	d.Update(tabKey()) // and back to the tree

	if got := d.treeSelected(); got != want {
		t.Errorf("focus landed on %q, want the file already on screen %q", got, want)
	}
}

// The footer names what ⏎ does NOW, and ⏎ means two different things depending
// on whether a folder or a file carries the selection.
func TestFooterNamesFoldOnADirectory(t *testing.T) {
	d := newDemoReader(t, 124, 30)
	d.Update(tabKey())
	dirRow(t, d)
	if got := ansi.Strip(d.footer()); !strings.Contains(got, "fold") {
		t.Errorf("on a folder the footer does not offer folding: %q", got)
	}
	for i, n := range d.treeView {
		if !n.IsDir {
			d.treeCursor = i
			break
		}
	}
	if got := ansi.Strip(d.footer()); !strings.Contains(got, "read it") {
		t.Errorf("on a file the footer does not say what ⏎ does: %q", got)
	}
}

// The file list uses the SESSION LIST's two selection states, verbatim: the
// accent fill while it owns the keyboard, a muted band when it does not.
//
// Both halves matter. Without the first the focused list is quieter than every
// other focused list in fleet; without the second the tree stops showing which
// file you are in the moment you go back to reading, which is precisely when
// you need it. Asserted against SelectionPill itself rather than against
// literal colours, so this cannot drift from the sidebar.
func TestTreeSelectionMatchesTheSessionList(t *testing.T) {
	fill := func(style lipgloss.Style) string {
		rendered := style.Render("x")
		i := strings.Index(rendered, "48;2;")
		if i < 0 {
			t.Fatalf("expected a background fill in %q", rendered)
		}
		return rendered[i : i+strings.Index(rendered[i:], "m")]
	}
	focusedFill, blurredFill := fill(SelectionPill(true)), fill(SelectionPill(false))
	if focusedFill == blurredFill {
		t.Fatal("SelectionPill's two states are identical — this test proves nothing")
	}

	d := newDemoReader(t, 124, 20)

	// Blurred: the current file still carries the selection, muted.
	rows := d.renderTree(d.treeWidth()-2, 12)
	cur := d.doc.Stream.FileAt(d.streamAt(d.cursor))
	var blurred string
	for i, n := range d.treeView {
		if !n.IsDir && n.Path == cur && i < len(rows) {
			blurred = rows[i]
		}
	}
	if blurred == "" {
		t.Fatal("the current file is not on screen")
	}
	if !strings.Contains(blurred, blurredFill) {
		t.Errorf("unfocused, the current file does not use the sidebar's muted band: %q",
			ansi.Strip(blurred))
	}
	if strings.Contains(blurred, focusedFill) {
		t.Errorf("unfocused, the tree is accent-filled — it is competing with the diff")
	}

	// Focused: the accent fill, like any other list holding the keyboard.
	d.Update(tabKey())
	sel := d.renderTree(d.treeWidth()-2, 12)[d.treeCursor]
	if !strings.Contains(sel, focusedFill) {
		t.Errorf("focused, the selection is not accent-filled: %q", ansi.Strip(sel))
	}
	// A fill that stops mid-row reads as a rendering fault, not a selection.
	if got, want := lipgloss.Width(sel), d.treeWidth()-2; got != want {
		t.Errorf("selected row is %d columns, want the full %d", got, want)
	}
}

// Exactly one row may carry the fill. Two would be two cursors.
func TestOnlyOneTreeRowIsFilled(t *testing.T) {
	d := newDemoReader(t, 124, 20)
	count := func() int {
		n := 0
		for _, r := range d.renderTree(d.treeWidth()-2, 12) {
			if strings.Contains(r, "48;2;") {
				n++
			}
		}
		return n
	}
	if got := count(); got != 1 {
		t.Errorf("unfocused tree has %d filled rows, want exactly 1", got)
	}
	d.Update(tabKey())
	if got := count(); got != 1 {
		t.Errorf("focused tree has %d filled rows, want exactly 1", got)
	}
}

// The cursor row is highlighted across the WHOLE line, not just marked in the
// gutter. A highlight that stops after the line numbers marks a column.
func TestCursorRowIsHighlightedFullWidth(t *testing.T) {
	for _, split := range []bool{false, true} {
		d := newDemoReader(t, 170, 24)
		if d.split != split {
			d.Update(keyOf("|"))
		}
		// Land on a real code row.
		for range 40 {
			if l, ok := d.lineAt(d.cursor); ok && l.Kind.IsCode() {
				break
			}
			d.moveTo(d.cursor + 1)
		}
		w := d.diffWidth() - 2
		row := d.renderRow(d.cursor, w)

		if lipgloss.Width(row) != w {
			t.Errorf("split=%v: cursor row is %d columns, want %d", split, lipgloss.Width(row), w)
		}
		// The fill has to survive all the way to the last column: render the
		// tail and check it still carries a background.
		plain := ansi.Strip(row)
		if strings.TrimSpace(plain) == "" {
			t.Fatalf("split=%v: cursor row is blank", split)
		}
		if !strings.Contains(row, "48;2;") {
			t.Errorf("split=%v: cursor row carries no background at all", split)
		}
		// Count the fills: the row should end inside one, not before it.
		last := strings.LastIndex(row, "48;2;")
		if tail := ansi.Strip(row[last:]); !strings.HasSuffix(row, "\x1b[m") && tail == "" {
			t.Errorf("split=%v: the highlight does not reach the end of the row", split)
		}
	}
}

// The cursor tint must not erase what the row already said. An added line under
// the cursor is still visibly an added line.
func TestCursorRowKeepsItsDiffState(t *testing.T) {
	bg := func(c interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}) string {
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
	}
	d := newDemoReader(t, 124, 30)
	for i := range d.rows {
		l, ok := d.lineAt(i)
		if !ok || l.Kind != review.LineAdd {
			continue
		}
		d.moveTo(i)
		row := d.renderRow(i, d.diffWidth()-2)
		if !strings.Contains(row, bg(ColorDiffCursorAddBg)) {
			t.Fatalf("an added row under the cursor lost its add tint: %q", ansi.Strip(row))
		}
		if strings.Contains(row, bg(ColorDiffCursorBg)) {
			t.Error("an added row under the cursor fell back to the neutral cursor tint")
		}
		return
	}
	t.Fatal("no added row in the fixture")
}

// The three cursor tints have to stay distinct from each other and from the
// resting tints they lift, in every theme — otherwise the cursor is invisible
// on some palettes and indistinguishable from a plain added line on others.
func TestCursorTintsAreDistinctInEveryTheme(t *testing.T) {
	defer ApplyPalette(PaletteFleetPink)
	key := func(c interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}) string {
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("%d,%d,%d", r>>8, g>>8, b>>8)
	}
	for _, p := range BuiltinPalettes {
		ApplyPalette(p)
		seen := map[string]string{}
		for _, c := range []struct {
			name string
			col  interface {
				RGBA() (uint32, uint32, uint32, uint32)
			}
		}{
			{"bg", p.Bg},
			{"add", ColorDiffAddBg}, {"del", ColorDiffDelBg},
			{"cursor", ColorDiffCursorBg},
			{"cursor+add", ColorDiffCursorAddBg}, {"cursor+del", ColorDiffCursorDelBg},
		} {
			k := key(c.col)
			if prev, dup := seen[k]; dup {
				t.Errorf("%s: %s and %s are the same colour (%s)", p.Name, prev, c.name, k)
			}
			seen[k] = c.name
		}
	}
}
