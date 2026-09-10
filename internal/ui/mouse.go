package ui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Mouse support.
//
// fleet reports the mouse in cell-motion mode (DECSET 1002 + SGR 1006), which
// is the smallest mode Bubble Tea v2 offers — there is no click-only variant,
// so the wheel comes with it whether or not anything handles it. That is what
// made this expensive enough to be switched off once (issue #268): v2's event
// loop calls View() after *every* message, so one trackpad flick over the
// window became ~300 ignored MouseWheelMsg/sec, each buying a full render of a
// screen that had not changed — measured at 220% CPU.
//
// Two things keep that from coming back, and both live here rather than in the
// handlers, so a future mouse handler cannot forget them:
//
//   - A mouse message that changes nothing skips the repaint entirely
//     (Home.viewDirty, consumed by Home.screen). Update cannot decline to
//     render, but View can hand back the frame it already built.
//   - A mouse message that *does* change something repaints at most once per
//     mouseRepaintInterval, with a trailing tick so the settled state still
//     lands (markMouseRepaint). A burst then costs ~60 renders/sec, which is
//     all the renderer flushes anyway.
//
// The cost of reporting is that the terminal stops drag-selecting inside
// fleet's own UI; copying text out of the sidebar needs Shift (Option in
// iTerm2). Selection *inside* an attached agent pane is unaffected — that is a
// full-screen tmux client with its own `mouse on` and copy-command. Set
// FLEET_NO_MOUSE to opt out, the same escape hatch FLEET_NO_COPY_COMMAND and
// FLEET_NO_EXTENDED_KEYS offer for the other terminal modes fleet turns on.

// mouseRepaintInterval bounds how often a wheel burst buys a View(). 16ms is
// one frame at the renderer's own 60fps ceiling, so nothing visible is lost.
const mouseRepaintInterval = 16 * time.Millisecond

// mouseDoubleClickWindow is how long a second click on the same row still
// counts as a double-click. Matches the slot double-tap window (handleSlotJump)
// so "press twice quickly to attach" means one thing across the app.
const mouseDoubleClickWindow = 400 * time.Millisecond

// mouseEnabled reports whether fleet asks the terminal for mouse reporting.
// Read once per call rather than cached: it is only consulted from chrome(),
// which already runs per frame, and a package-level cache would need an init
// that the tests would have to work around.
func mouseEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_NO_MOUSE"))) {
	case "1", "true", "yes", "y", "on":
		return false
	default:
		return true
	}
}

// mouseRect is a screen-space rectangle in cells, covering
// [x, x+w) × [y, y+h). The zero value contains nothing.
type mouseRect struct{ x, y, w, h int }

func (r mouseRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// screenLayout records where the last frame put each panel's *inner* content
// area — inside the border, so a rect's row 0 is the panel's first content row.
//
// It is written by renderBody and read by the mouse handlers, which is the only
// honest order: the layout is a function of the terminal size, the layout mode,
// whether the drawer is open and how tall it ended up, and recomputing any of
// that at click time is how a pointer starts acting on a panel that is not
// under it. A click that arrives before the first frame hits the zero value and
// lands nowhere, which is correct.
type screenLayout struct {
	sidebar mouseRect

	// The topmost dropdown or palette, if one is up. Recorded by composeScreen,
	// which is where the box's x/y are already known — the dialogs themselves
	// only ever return a bare box and have no idea where it was composited.
	overlay     mouseRect
	overlayRows clickableOverlay
}

// clickableOverlay is a dropdown or palette whose rows can be picked with the
// pointer.
type clickableOverlay interface {
	// ClickRowAt moves the selection onto the row at (dx, dy) — offsets from the
	// box's top-left corner — and returns the key that a click means on that
	// row, or 0 when it landed on nothing pickable.
	//
	// The key comes back from the dialog rather than being decided by the
	// caller because "activate this row" is not one thing: it fires the entry in
	// a menu (enter) but ticks a box in the allowed-accounts editor, where enter
	// saves and closes — a click that saved the whole dialog because the user
	// aimed at a checkbox would be a different feature. Chrome (the border, the
	// title, a `⋮` scroll marker) and disabled rows return 0, so the click is
	// swallowed rather than firing whatever was selected before.
	ClickRowAt(dx, dy int) rune
}

// overlayRowMap maps a line inside an overlay's box to the item drawn on it.
//
// Built by the dialog's own View as it emits each row, never re-derived from
// the item slice: the command palette interleaves section headers, a `recent`
// label and blank spacer rows with its results, so the only thing that reliably
// knows which line a row landed on is the loop that wrote it. The simple
// dropdowns could compute it — and would drift the first time one of them grows
// a header.
type overlayRowMap struct {
	top  int         // lines of chrome above the first content line (border + padding)
	rows map[int]int // line within the box → item index
}

// set records that item i was drawn on content line `line`.
func (m *overlayRowMap) set(line, i int) {
	if m.rows == nil {
		m.rows = map[int]int{}
	}
	m.rows[line+m.top] = i
}

// reset clears the map for a fresh render, recording how much chrome the box
// draws above its first content line.
func (m *overlayRowMap) reset(top int) {
	m.top = top
	m.rows = map[int]int{}
}

func (m overlayRowMap) at(dy int) (int, bool) {
	i, ok := m.rows[dy]
	return i, ok
}

// handleMouse routes a mouse message. It returns without marking the frame
// dirty unless something actually changed, so an ignored event is free.
func (h *Home) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	m := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		return h.handleWheel(m)
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft {
			return h, nil
		}
		return h.handleClick(m)
	}
	// Release and motion carry nothing fleet acts on. Drag lands here too:
	// cell-motion reports it, and swallowing it is what stops a drag over the
	// sidebar from walking the cursor.
	return h, nil
}

// handleWheel scrolls whatever the pointer is over.
func (h *Home) handleWheel(m tea.Mouse) (tea.Model, tea.Cmd) {
	delta := 0
	switch m.Button {
	case tea.MouseWheelUp:
		delta = -1
	case tea.MouseWheelDown:
		delta = 1
	default: // horizontal wheels: nothing here scrolls sideways
		return h, nil
	}
	// A dropdown or palette is up: the wheel walks its rows, and never reaches
	// the sidebar painted behind it. Nav goes through the dialog's own arrow
	// handling, so the wheel cannot land somewhere the arrows would refuse to —
	// past the end of the list, or on a disabled row.
	if h.layout.overlayRows != nil {
		if !h.layout.overlay.contains(m.X, m.Y) {
			return h, nil
		}
		key := tea.KeyUp
		if delta > 0 {
			key = tea.KeyDown
		}
		return h, tea.Batch(h.sendToModal(tea.KeyPressMsg{Code: key}), h.markMouseRepaint())
	}
	if !h.layout.sidebar.contains(m.X, m.Y) {
		return h, nil
	}
	if !h.scrollSidebar(delta) {
		return h, nil // already at the end — no frame to buy
	}
	return h, h.markMouseRepaint()
}

// scrollSidebar moves the sidebar viewport by delta rows without touching the
// cursor, and reports whether it moved.
//
// Scrolling the viewport rather than walking the cursor is what makes the wheel
// affordable: every cursor move fires fetchPreviewForSelected, which forks a
// `tmux capture-pane`, so a wheel bound to j/k would fork hundreds of processes
// per flick. It is also the better idiom — scroll to look, click to select —
// and needs no new state, since viewOffset is already the scroll position.
// The next cursor move calls syncViewport, which pulls the view back to the
// cursor, exactly as PgUp/PgDn already behave.
func (h *Home) scrollSidebar(delta int) bool {
	maxOffset := len(h.flatItems) - h.sidebarMinVisibleRows()
	if maxOffset < 0 {
		maxOffset = 0
	}
	next := clampInt(h.viewOffset+delta, 0, maxOffset)
	if next == h.viewOffset {
		return false
	}
	h.viewOffset = next
	return true
}

// handleClick moves the cursor to the clicked sidebar row; a second click on
// the same row within mouseDoubleClickWindow activates it, exactly as Enter
// would (attach, resume, focus-split or toggle a group — see activateCursorRow).
func (h *Home) handleClick(m tea.Mouse) (tea.Model, tea.Cmd) {
	if h.layout.overlayRows != nil {
		return h.clickOverlay(m)
	}
	// While the drawer is typing or a split is focused, the keyboard belongs to
	// that pane and j/k cannot move the sidebar cursor at all. A click that moved
	// it anyway would leave the highlight and the typing target pointing at
	// different sessions — the one thing the design system's focus > selection
	// order exists to prevent — and would fire a preview capture behind the back
	// of someone who is mid-command in a shell. The wheel is still live there:
	// scrolling is looking, and it changes neither focus nor selection.
	if h.drawerHasFocus() || h.focusMode {
		return h, nil
	}
	idx := h.sidebarItemAt(m.X, m.Y)
	if idx < 0 {
		return h, nil
	}
	// A spacer is a blank row between origin groups; the keyboard skips over it
	// and so does the pointer, rather than parking the cursor somewhere no key
	// could have put it.
	if h.flatItems[idx].IsSpacer {
		return h, nil
	}

	doubleClick := idx == h.lastClickRow && time.Since(h.lastClickAt) < mouseDoubleClickWindow
	moved := h.cursor != idx
	h.cursor = idx
	h.syncViewport()
	h.viewDirty = true

	if doubleClick {
		h.lastClickRow = -1
		return h, h.activateCursorRow()
	}
	h.lastClickRow = idx
	h.lastClickAt = time.Now()
	if !moved {
		return h, nil // same row: the preview is already the right one
	}
	return h, h.fetchPreviewForSelected()
}

// sidebarItemAt maps a screen cell to a flatItems index, or -1 when the cell
// is not on a session row — outside the sidebar, on a scroll indicator, or
// past the last row.
func (h *Home) sidebarItemAt(x, y int) int {
	r := h.layout.sidebar
	if !r.contains(x, y) {
		return -1
	}
	win := sidebarWindow(len(h.flatItems), h.viewOffset, r.h)
	row := y - r.y
	if win.Above {
		row-- // the `… N more above` indicator owns the first row
	}
	if row < 0 {
		return -1
	}
	idx := win.Start + row
	if idx >= win.End {
		return -1 // the `… N more below` indicator, or blank space under a short list
	}
	return idx
}

// markMouseRepaint asks for a frame, at most one per mouseRepaintInterval.
// When it declines it schedules a single trailing tick, so the state the burst
// settled on is always what ends up on screen.
func (h *Home) markMouseRepaint() tea.Cmd {
	now := time.Now()
	if now.Sub(h.lastMousePaint) >= mouseRepaintInterval {
		h.lastMousePaint = now
		h.viewDirty = true
		return nil
	}
	if h.mouseSettleQueued {
		return nil
	}
	h.mouseSettleQueued = true
	return tea.Tick(mouseRepaintInterval, func(time.Time) tea.Msg { return mouseSettleMsg{} })
}

// mouseSettleMsg paints the frame a throttled mouse burst skipped.
type mouseSettleMsg struct{}

// clickOverlay routes a click while a dropdown or the palette is up.
//
// A click on a row does what that row's key does — one click on "Delete" in the
// context menu is what a menu promises, and making it take two would read as
// broken.
// A click outside the box closes it, which is what every dropdown anywhere
// does; both go through the dialog's own Enter and Esc handling rather than a
// second copy of it, so a menu cannot start behaving differently by pointer
// than by keyboard.
//
// Nothing falls through to the sidebar underneath. The overlays do not dim
// what is behind them (the dropdowns deliberately don't), so the sidebar is
// still painted and still has a live rect — without this, a click aimed past
// an open menu would move the cursor on the row the menu is about to act on.
func (h *Home) clickOverlay(m tea.Mouse) (tea.Model, tea.Cmd) {
	h.viewDirty = true
	r := h.layout.overlay
	if !r.contains(m.X, m.Y) {
		return h, h.sendToModal(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	key := h.layout.overlayRows.ClickRowAt(m.X-r.x, m.Y-r.y)
	if key == 0 {
		return h, nil // on the box but not on a pickable row: swallow it
	}
	return h, h.sendToModal(tea.KeyPressMsg{Code: key})
}

// sendToModal hands a synthetic keypress to whichever dialog owns the screen.
// The point is reuse, not simulation: activating a row means exactly what the
// dialog already does for Enter, including the analytics, the guards and the
// message it emits.
func (h *Home) sendToModal(key tea.KeyPressMsg) tea.Cmd {
	cmd, _ := h.routeToModal(key)
	return cmd
}
