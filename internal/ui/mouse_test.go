package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/frost"
	"github.com/brizzai/fleet/internal/session"
)

// sidebarRows builds n selectable session rows plus the layout rect a frame
// would have left behind, so the hit-testing tests describe a real screen.
func mouseTestHome(rows, visibleRows int) *Home {
	h := &Home{cfg: &config.Config{}, actionLog: NewActionLog(100)}
	for i := 0; i < rows; i++ {
		s := session.NewSession("s", "/tmp/mouse-test")
		h.flatItems = append(h.flatItems, SidebarItem{Session: s})
	}
	h.layout.sidebar = mouseRect{x: 1, y: sidebarContentTop, w: 40, h: visibleRows}
	// syncViewport/scrollSidebar size the viewport off the panel, not the rect.
	h.height = visibleRows + 4 + 1 + 1
	h.width = 100
	return h
}

// TestMouseEventsThatChangeNothingSkipTheRepaint is the guard that replaced
// "never turn mouse reporting on" (#268). Reporting is on now, so the storm is
// held off one layer in: Bubble Tea v2 renders after every message and offers no
// way to decline, so an event nothing acted on has to leave the frame cache
// standing or it costs a full compose apiece — ~300 of them per trackpad flick.
func TestMouseEventsThatChangeNothingSkipTheRepaint(t *testing.T) {
	h := mouseTestHome(3, 20) // every row fits: there is nothing to scroll

	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"motion", tea.MouseMotionMsg{X: 5, Y: 5}},
		{"release", tea.MouseReleaseMsg{X: 5, Y: 5, Button: tea.MouseLeft}},
		{"wheel outside the sidebar", tea.MouseWheelMsg{X: 90, Y: 5, Button: tea.MouseWheelDown}},
		{"wheel with nothing to scroll", tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown}},
		{"click on empty space below the rows", tea.MouseClickMsg{X: 5, Y: sidebarContentTop + 10, Button: tea.MouseLeft}},
		{"right click", tea.MouseClickMsg{X: 5, Y: sidebarContentTop, Button: tea.MouseRight}},
	} {
		h.viewDirty = true
		if _, _ = h.Update(tc.msg); h.viewDirty {
			t.Errorf("%s: marked the frame dirty; an ignored mouse event must not buy a View()", tc.name)
		}
	}

	// The counterpart: a message that is not a mouse event always repaints,
	// because any of them may have changed the screen.
	h.viewDirty = false
	if _, _ = h.Update(shellOutputMsg{}); !h.viewDirty {
		t.Error("a non-mouse message left the frame clean; only mouse events may skip a repaint")
	}
}

// TestScreenServesTheCachedFrame pins the other half: skipping the repaint only
// helps if View actually hands back the frame it already built.
func TestScreenServesTheCachedFrame(t *testing.T) {
	h := mouseTestHome(3, 20)
	h.frameCache = "the frame that is on screen"

	h.viewDirty = false
	if got := h.screen(); got != "the frame that is on screen" {
		t.Errorf("screen() recomposed a clean frame: got %q", got)
	}

	// Nothing asserts the dirty path here on purpose: composing needs a fully
	// built Home, and the pair is already closed from the other side — Update
	// marks every non-mouse message dirty (asserted above), so the only way to
	// reach screen() with a clean frame is a mouse event that changed nothing.
}

// TestWheelScrollsTheViewportNotTheCursor is a behavioural guard, not a style
// one. Binding the wheel to the cursor looks equivalent and is not: every cursor
// move fires fetchPreviewForSelected, which forks a `tmux capture-pane`, so one
// flick would fork hundreds of processes.
func TestWheelScrollsTheViewportNotTheCursor(t *testing.T) {
	h := mouseTestHome(50, 10)
	h.cursor = 4

	if _, _ = h.Update(tea.MouseWheelMsg{X: 5, Y: sidebarContentTop + 2, Button: tea.MouseWheelDown}); h.viewOffset == 0 {
		t.Fatal("wheel-down over the sidebar did not scroll it")
	}
	if h.cursor != 4 {
		t.Errorf("wheel moved the cursor to %d: scrolling must not fetch a preview per event", h.cursor)
	}

	// Clamped at both ends, so a burst past the end stops buying frames.
	h.viewOffset = 0
	if h.scrollSidebar(-1) {
		t.Error("scrolled above the first row")
	}
	for i := 0; i < 500; i++ {
		h.scrollSidebar(1)
	}
	if h.scrollSidebar(1) {
		t.Error("scrolled past the last row")
	}
}

// TestSidebarItemAtSkipsTheScrollIndicators: the `… N more above` and
// `… N more below` rows are painted inside the same rect as the session rows,
// and the below-indicator sits directly on top of the first item the window
// excludes — the exact index a naive viewOffset+row would return.
func TestSidebarItemAtSkipsTheScrollIndicators(t *testing.T) {
	h := mouseTestHome(50, 10)
	h.viewOffset = 5

	win := sidebarWindow(len(h.flatItems), h.viewOffset, h.layout.sidebar.h)
	if !win.Above || !win.Below {
		t.Fatalf("precondition: want both indicators drawn, got %+v", win)
	}

	top := h.layout.sidebar.y
	if got := h.sidebarItemAt(5, top); got != -1 {
		t.Errorf("click on the `more above` row mapped to item %d, want none", got)
	}
	if got := h.sidebarItemAt(5, top+1); got != h.viewOffset {
		t.Errorf("first painted row mapped to %d, want %d", got, h.viewOffset)
	}
	if got := h.sidebarItemAt(5, top+2); got != h.viewOffset+1 {
		t.Errorf("second painted row mapped to %d, want %d", got, h.viewOffset+1)
	}
	last := top + 1 + (win.End - win.Start) - 1
	if got := h.sidebarItemAt(5, last); got != win.End-1 {
		t.Errorf("last painted row mapped to %d, want %d", got, win.End-1)
	}
	if got := h.sidebarItemAt(5, last+1); got != -1 {
		t.Errorf("click on the `more below` row mapped to item %d, want none", got)
	}
	if got := h.sidebarItemAt(h.layout.sidebar.x+h.layout.sidebar.w, top+1); got != -1 {
		t.Error("a click past the sidebar's right edge hit a row")
	}
}

// TestClickSelectsAndDoubleClickActivates: one click moves the cursor, a second
// on the same row inside the window activates it exactly as Enter would.
func TestClickSelectsAndDoubleClickActivates(t *testing.T) {
	h := mouseTestHome(20, 10)
	h.cursor = 0
	row := sidebarContentTop + 3 // viewOffset is 0 and no indicator is drawn

	armed := h.flatItems[3].Session.ID

	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	if h.cursor != 3 {
		t.Fatalf("click landed the cursor on %d, want 3", h.cursor)
	}
	if h.lastClickTarget.sessionID != armed {
		t.Fatalf("first click did not arm a double-click, latch=%+v", h.lastClickTarget)
	}

	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	if h.lastClickTarget != (contextMenuTarget{}) {
		t.Error("second click inside the window was not treated as a double-click")
	}

	// Outside the window it is two single clicks, not one double.
	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	h.lastClickAt = time.Now().Add(-2 * mouseDoubleClickWindow)
	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	if h.lastClickTarget.sessionID != armed {
		t.Error("a slow second click was treated as a double-click")
	}
}

// TestClickIgnoresSpacerRows: a spacer is the blank line between origin groups.
// The keyboard skips it (NextSelectableItem), so the pointer must too rather
// than parking the cursor somewhere no key could have put it.
func TestClickIgnoresSpacerRows(t *testing.T) {
	h := mouseTestHome(5, 10)
	h.flatItems[2] = SidebarItem{IsSpacer: true}
	h.cursor = 0

	h.Update(tea.MouseClickMsg{X: 5, Y: sidebarContentTop + 2, Button: tea.MouseLeft})
	if h.cursor != 0 {
		t.Errorf("click on a spacer moved the cursor to %d", h.cursor)
	}
}

// TestChromeMouseModeFollowsTheOptOut: FLEET_NO_MOUSE is the escape hatch for a
// terminal whose reporting misbehaves, matching FLEET_NO_COPY_COMMAND and
// FLEET_NO_EXTENDED_KEYS. It has to reach the View, since that is the only place
// v2 reads the mode from.
func TestChromeMouseModeFollowsTheOptOut(t *testing.T) {
	h := &Home{}

	t.Setenv("FLEET_NO_MOUSE", "")
	if got := h.chrome("x").MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v by default, want cell motion", got)
	}

	t.Setenv("FLEET_NO_MOUSE", "1")
	if got := h.chrome("x").MouseMode; got != tea.MouseModeNone {
		t.Errorf("MouseMode = %v with FLEET_NO_MOUSE set, want none", got)
	}
}

// boxLineOf finds which line of a rendered box holds needle, so the geometry
// tests below are anchored to what was actually drawn rather than to a
// hand-counted border-plus-padding offset. A row map that disagrees with its own
// renderer is the whole failure mode here, and arithmetic in the test would just
// be a second copy of the thing under test.
func boxLineOf(t *testing.T, box, needle string) int {
	t.Helper()
	for i, line := range strings.Split(box, "\n") {
		if strings.Contains(ansi.Strip(line), needle) {
			return i
		}
	}
	t.Fatalf("%q not found in rendered box:\n%s", needle, ansi.Strip(box))
	return -1
}

// TestContextMenuClickHitsTheRowUnderThePointer: the box draws a border and a
// title above its first row, so the pointer and the row map have to agree about
// where the rows begin.
func TestContextMenuClickHitsTheRowUnderThePointer(t *testing.T) {
	d := NewContextMenuDialog()
	d.SetSize(100, 40)
	d.SetAnchor(3, 5, 38)
	d.Show("Session", []ContextMenuItem{
		{ID: "attach", Label: "Attach", Enabled: true},
		{ID: "rename", Label: "Rename", Enabled: true},
		{ID: "fork", Label: "Fork", Enabled: false, Note: "Claude only"},
	})
	box := d.View() // the row map is built by the render, like a real frame

	if got := d.ClickRowAt(0, 0); got != 0 {
		t.Error("a click on the top border fired something")
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "Session")); got != 0 {
		t.Error("a click on the title fired something")
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "Attach")); got != tea.KeyEnter || d.cursor != 0 {
		t.Errorf("Attach: key=%q cursor=%d, want enter on row 0", got, d.cursor)
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "Rename")); got != tea.KeyEnter || d.cursor != 1 {
		t.Errorf("Rename: key=%q cursor=%d, want enter on row 1", got, d.cursor)
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "Fork")); got != 0 {
		t.Error("a disabled row fired: j/k skip it, and so must the pointer")
	}
	if d.cursor != 1 {
		t.Errorf("a refused click still moved the cursor to %d", d.cursor)
	}
	if got := d.ClickRowAt(0, 99); got != 0 {
		t.Error("a click below the box fired something")
	}
}

// TestAllowedAccountsClickTicksRatherThanSaves: enter saves and closes that
// dialog, so a click that meant enter would store whatever happened to be
// ticked the moment someone aimed at a checkbox.
func TestAllowedAccountsClickTicksRatherThanSaves(t *testing.T) {
	d := NewAllowedAccountsDialog()
	d.SetSize(100, 40)
	d.Show("origin:example.com/acme/api", "acme/api", []allowedAccountRow{
		{email: "first@x.com", label: "first@x.com"},
		{email: "second@x.com", label: "second@x.com"},
	}, nil)
	box := d.View()

	if got := d.ClickRowAt(0, boxLineOf(t, box, "second@x.com")); got != tea.KeySpace {
		t.Errorf("row click returned %q, want space — enter would save and close", got)
	}
	if d.cursor != 1 {
		t.Errorf("click landed the cursor on %d, want 1", d.cursor)
	}
}

// TestSnoozeClickFocusesTheTypedRowWithoutSubmitting: enter on the custom row
// means "snooze for what I typed", and the box is empty at the moment it is
// clicked — the dialog would refuse, so the click just takes the caret.
func TestSnoozeClickFocusesTheTypedRowWithoutSubmitting(t *testing.T) {
	d := NewSnoozeDialog()
	d.SetSize(100, 40)
	d.Show("Snooze session")
	box := d.View()

	first := boxLineOf(t, box, SnoozeDurations[0].Label)
	if got := d.ClickRowAt(0, first); got != tea.KeyEnter || d.focus != 0 {
		t.Errorf("first preset: key=%q focus=%d, want enter on preset 0", got, d.focus)
	}
	last := boxLineOf(t, box, SnoozeDurations[len(SnoozeDurations)-1].Label)
	if got := d.ClickRowAt(0, last); got != tea.KeyEnter || d.focus != len(SnoozeDurations)-1 {
		t.Errorf("last preset: key=%q focus=%d", got, d.focus)
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "or type:")); got != 0 {
		t.Errorf("custom-duration row returned %q, want no key", got)
	}
	if !d.inputFocused() {
		t.Error("clicking the custom-duration row did not focus it")
	}
}

// TestClickOutsideAnOverlayClosesItAndSparesTheSidebar runs through a real Home
// rather than the handler alone: closing goes through routeToModal, which walks
// every dialog in the app, and the point of the test is the gap between
// components — that a click past an open menu does not quietly move the sidebar
// cursor onto a different row than the one the menu is about to act on. The
// dropdowns do not dim what is behind them, so the sidebar is still painted and
// still has a live rect.
func TestClickOutsideAnOverlayClosesItAndSparesTheSidebar(t *testing.T) {
	storage, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	for i := 0; i < 20; i++ {
		h.flatItems = append(h.flatItems, SidebarItem{Session: session.NewSession("s", "/tmp/mouse-test")})
	}
	h.layout.sidebar = mouseRect{x: 1, y: sidebarContentTop, w: 40, h: 10}
	h.cursor = 0

	h.contextMenu.SetSize(120, 40)
	h.contextMenu.SetAnchor(3, 12, 38)
	h.contextMenu.Show("Session", []ContextMenuItem{{ID: "attach", Label: "Attach", Enabled: true}})

	// Compositing is what records the rect, so place it the way composeScreen
	// would — well below the row this click aims at.
	h.recordOverlay(h.contextMenu.View(), 3, 13, h.contextMenu)

	h.Update(tea.MouseClickMsg{X: 5, Y: sidebarContentTop + 4, Button: tea.MouseLeft})
	if h.cursor != 0 {
		t.Errorf("a click past an open menu moved the sidebar cursor to %d", h.cursor)
	}
	if h.contextMenu.IsVisible() {
		t.Error("a click outside the menu did not close it")
	}
}

// TestClickIsInertWhileAnotherSurfaceOwnsTheKeyboard: in the drawer's typing
// mode and in a focused split, j/k do not reach the sidebar at all, so a click
// must not move the cursor either — it would point the highlight at one session
// while the keys go to another.
func TestClickIsInertWhileAnotherSurfaceOwnsTheKeyboard(t *testing.T) {
	h := mouseTestHome(20, 10)
	h.cursor = 0
	h.focusMode = true

	h.Update(tea.MouseClickMsg{X: 5, Y: sidebarContentTop + 3, Button: tea.MouseLeft})
	if h.cursor != 0 {
		t.Errorf("click moved the cursor to %d while the split was focused", h.cursor)
	}

	// The wheel stays live: it is a look, not an act.
	h = mouseTestHome(50, 10)
	h.focusMode = true
	h.Update(tea.MouseWheelMsg{X: 5, Y: sidebarContentTop + 2, Button: tea.MouseWheelDown})
	if h.viewOffset == 0 {
		t.Error("wheel stopped scrolling while the split was focused")
	}
}

// TestSidebarWindowAnnouncesTheLastItem: the upper indicator eats a row, so
// asking whether anything is below *before* reserving it reports "nothing" for a
// window that is about to lose that row. With 11 items at offset 1 in 10 rows,
// item 10 was neither drawn nor announced and the list looked one short.
//
// Harmless while viewOffset came only from syncViewport, which never parked
// there; the wheel can, which is what turned it into a bug a user hits.
func TestSidebarWindowAnnouncesTheLastItem(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		itemCount, offset, height int
		wantEnd                   int
		wantAbove, wantBelow      bool
	}{
		{"the reported case", 11, 1, 10, 9, true, true},
		{"unscrolled is unchanged", 11, 0, 10, 9, false, true},
		{"scrolled far enough to show the tail", 11, 2, 10, 11, true, false},
		{"everything fits", 10, 0, 10, 10, false, false},
	} {
		w := sidebarWindow(tc.itemCount, tc.offset, tc.height)
		if w.End != tc.wantEnd || w.Above != tc.wantAbove || w.Below != tc.wantBelow {
			t.Errorf("%s: got end=%d above=%v below=%v, want end=%d above=%v below=%v",
				tc.name, w.End, w.Above, w.Below, tc.wantEnd, tc.wantAbove, tc.wantBelow)
		}
		// The real invariant: every item is either drawn or announced.
		if w.End < tc.itemCount && !w.Below {
			t.Errorf("%s: items past %d are neither drawn nor announced", tc.name, w.End)
		}
	}
}

// TestSnoozeRowMapSurvivesALongTitle: the title is "Snooze " + a session title,
// which naming.GenerateTitle cuts at ~50 runes — so it is wider than the box's
// content area in the ordinary case, not the edge one. Truncating it to the
// outer width let it wrap, which pushed every preset one row below where
// clickRows said it was: clicking `30 minutes` snoozed for an hour.
func TestSnoozeRowMapSurvivesALongTitle(t *testing.T) {
	d := NewSnoozeDialog()
	d.SetSize(100, 40)
	d.Show("Snooze refactor the status detection pipeline end to end")
	box := d.View()

	for i, dur := range SnoozeDurations {
		line := boxLineOf(t, box, dur.Label)
		if got := d.ClickRowAt(0, line); got != tea.KeyEnter || d.focus != i {
			t.Errorf("%s is drawn on line %d but the map put focus on %d (key %q)",
				dur.Label, line, d.focus, got)
		}
	}
	if got := d.ClickRowAt(0, boxLineOf(t, box, "or type:")); got != 0 {
		t.Errorf("custom-duration row returned %q, want no key", got)
	}
}

// TestPaletteRowMapMatchesTheDrawnRowsAtEveryWidth. The box re-wraps anything
// wider than its content area, and the tab bar and search input are the two
// pieces that are not budgeted against it — so at 64 columns (an ordinary split
// pane) the tab bar takes a second line and a map built from written newlines
// fires the entry *below* the one clicked. Sweeping widths is the only way to
// see it: at 65 and above the arithmetic and the render agree by luck.
func TestPaletteRowMapMatchesTheDrawnRowsAtEveryWidth(t *testing.T) {
	items := []PaletteItem{
		{Kind: PaletteKindCommand, ID: "attach", Name: "Attach Session", Shortcut: "Enter", Haystack: "Attach Session"},
		{Kind: PaletteKindCommand, ID: "focus", Name: "Focus Preview", Shortcut: "Tab", Haystack: "Focus Preview"},
		{Kind: PaletteKindCommand, ID: "new", Name: "Renumber Panes", Shortcut: "a", Haystack: "Renumber Panes"},
	}
	// Needles, not full names: the narrow widths truncate the name column, and
	// what is being tested is which line a row landed on, not how it was cut.
	needles := []string{"Attach", "Focus", "Renumber"}

	for _, w := range []int{40, 50, 64, 65, 80, 120} {
		d := NewCommandPaletteDialog()
		d.SetSize(w, 40)
		d.Show(items, nil)
		box := d.View()

		for i, needle := range needles {
			line := boxLineOf(t, box, needle)
			d.cursor = -1
			key := d.ClickRowAt(0, line)
			if key != tea.KeyEnter || d.cursor != i {
				t.Errorf("width %d: %q is drawn on line %d but the map resolved cursor=%d (key %q)",
					w, needle, line, d.cursor, key)
			}
		}
	}
}

// TestMouseIsInertDuringAFrostRun: handleKey swallows every key for the run, but
// View() returns the frost frame without passing through screen(), so h.layout
// still holds live geometry under the picture. Two clicks inside the
// double-click window would otherwise launch an attach from a screen that never
// showed what was picked.
func TestMouseIsInertDuringAFrostRun(t *testing.T) {
	h := mouseTestHome(50, 10)
	h.width, h.height = 80, 24
	h.cursor = 0
	h.frost = frost.New(frost.Freeze("x", h.width, h.height), 1)

	h.Update(tea.MouseClickMsg{X: 5, Y: sidebarContentTop + 4, Button: tea.MouseLeft})
	if h.cursor != 0 {
		t.Errorf("a click during a frost run moved the cursor to %d", h.cursor)
	}
	h.Update(tea.MouseWheelMsg{X: 5, Y: sidebarContentTop + 2, Button: tea.MouseWheelDown})
	if h.viewOffset != 0 {
		t.Errorf("a wheel during a frost run scrolled to %d", h.viewOffset)
	}
}

// TestClickedRowStaysUnderThePointer. syncViewport anchors on
// sidebarMinVisibleRows (contentHeight-4, which reserves both indicator rows
// whether or not they are drawn) while the rect the frame was drawn from is
// contentHeight-2. Re-anchoring on a click therefore scrolled the last two
// painted rows of a scrolled list out from under the pointer, so the second
// click of a double-click selected the neighbouring session instead — and a
// third attached it.
func TestClickedRowStaysUnderThePointer(t *testing.T) {
	for _, off := range []int{28, 29, 30, 31, 32} {
		h := mouseTestHome(50, 20)
		h.width, h.height = 80, 24
		h.viewOffset = off

		for row := 0; row < 20; row++ {
			y := h.layout.sidebar.y + row
			first := h.sidebarItemAt(5, y)
			if first < 0 {
				continue
			}
			h.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
			if again := h.sidebarItemAt(5, y); again != first {
				t.Fatalf("offset %d row %d: clicked item %d, the same cell now resolves to %d",
					off, row, first, again)
			}
			if h.cursor != first {
				t.Fatalf("offset %d row %d: clicked item %d but the cursor is on %d",
					off, row, first, h.cursor)
			}
		}
	}
}

// TestWheelInFocusModeRepaintsTheSidebar: in dual layout renderBody serves
// h.cachedSidebar while the split is focused and the sidebar is clean, so a
// scroll that did not mark it dirty moved viewOffset, bought a frame, and
// changed nothing on screen — the list then jumped the moment focus returned.
func TestWheelInFocusModeRepaintsTheSidebar(t *testing.T) {
	h := mouseTestHome(50, 10)
	h.focusMode = true
	h.sidebarDirty = false

	h.Update(tea.MouseWheelMsg{X: 5, Y: sidebarContentTop + 2, Button: tea.MouseWheelDown})
	if h.viewOffset == 0 {
		t.Fatal("wheel did not scroll in focus mode")
	}
	if !h.sidebarDirty {
		t.Error("the scroll left the sidebar cache standing — the frame would not change")
	}
}

// TestDoubleClickFollowsTheRowNotTheIndex. rebuildFlatItems runs on the ~2s tick
// and from adoptSessionsMsg, so a session arriving above the clicked row inside
// the 400ms window makes the same index a different session. Latching the index
// would read the second click as a double-click on a row clicked once, and
// attach something nobody pointed at.
func TestDoubleClickFollowsTheRowNotTheIndex(t *testing.T) {
	h := mouseTestHome(20, 10)
	row := sidebarContentTop + 3

	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	clicked := h.lastClickTarget
	if clicked.sessionID == "" {
		t.Fatal("first click armed nothing")
	}

	// A session appears above the clicked row: the same index is now someone else.
	h.flatItems = append([]SidebarItem{{Session: session.NewSession("newcomer", "/tmp/mouse-test")}}, h.flatItems...)

	h.Update(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	if h.lastClickTarget == (contextMenuTarget{}) {
		t.Error("the second click activated a row that had only been clicked once")
	}
	if h.lastClickTarget == clicked {
		t.Error("the latch did not follow the row under the pointer")
	}
}
