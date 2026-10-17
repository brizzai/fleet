package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/tmux"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Wheel scrolling in the preview, herdr's "host scroll": the wheel walks back
// through the agent's tmux history three lines a notch, a one-column scrollbar
// shows where the view is, and any key — or wheeling back down — returns to the
// live bottom. While scrolled up the live refresh is skipped, so the view holds
// still as the agent keeps printing.
//
// ponytail: wheel events are not forwarded to agents that run on the alternate
// screen with mouse reporting (Codex/opencode TUIs); their history is not in
// tmux, so the wheel does nothing there. Claude Code prints to the normal
// screen and scrolls fine.

// previewScrollLines is herdr's default lines per wheel notch.
const previewScrollLines = 3

type previewScrollState struct {
	offset    int    // rows above the live bottom; 0 = live
	sessionID string // whose history content shows
	history   int    // tmux history size at the last fetch, to clamp the wheel
	content   string // the captured scrolled view (offset > 0)
	inFlight  bool   // a capture is running; the next waits for it
}

// previewScrollMsg carries a scrolled capture. want is the offset it was
// requested for; it only lands if the user is still there. id is the session
// ID for the preview, or the shell's tmux name for the drawer.
type previewScrollMsg struct {
	drawer  bool
	id      string
	want    int
	offset  int // want clamped to the history
	history int
	content string
	err     error
}

// scrollPreview handles a wheel notch over the preview (delta -1 up, +1 down).
func (h *Home) scrollPreview(delta int) (tea.Model, tea.Cmd) {
	next, ok := nextScrollOffset(h.previewScroll, delta)
	if !ok {
		return h, nil
	}
	h.clearPreviewSelection() // its rows would now point at different text
	if next == 0 {
		h.resetPreviewScroll()
		return h, tea.Batch(h.fetchPreviewForSelected(), h.markMouseRepaint())
	}
	h.previewScroll.offset = next
	return h, tea.Batch(h.fetchPreviewScroll(), h.markMouseRepaint())
}

// scrollDrawer handles a wheel notch over the terminal drawer's body. Live is
// the emulator, so scrolling back down needs no capture.
func (h *Home) scrollDrawer(delta int) (tea.Model, tea.Cmd) {
	next, ok := nextScrollOffset(h.drawerScroll, delta)
	if !ok {
		return h, nil
	}
	if next == 0 {
		h.resetScroll(&h.drawerScroll)
		return h, h.markMouseRepaint()
	}
	h.drawerScroll.offset = next
	return h, tea.Batch(h.fetchDrawerScroll(), h.markMouseRepaint())
}

// nextScrollOffset moves sc one wheel notch, clamped to the known history, and
// reports whether that changed anything.
func nextScrollOffset(sc previewScrollState, delta int) (int, bool) {
	next := sc.offset - delta*previewScrollLines
	if sc.history > 0 {
		next = min(next, sc.history)
	}
	next = max(next, 0)
	return next, next != sc.offset
}

// fetchPreviewScroll captures the preview at its current offset.
func (h *Home) fetchPreviewScroll() tea.Cmd {
	sel := h.selectedSession()
	if sel == nil {
		return nil
	}
	rows := h.layout.previewFit[1]
	if rows <= 0 {
		rows = h.layout.previewText.h
	}
	return fetchScroll(&h.previewScroll, false, sel.ID, sel.GetTmuxSession(), rows)
}

// fetchDrawerScroll captures the active shell at the drawer's current offset.
func (h *Home) fetchDrawerScroll() tea.Cmd {
	sh := h.activeShell()
	if sh == nil {
		return nil
	}
	return fetchScroll(&h.drawerScroll, true, sh.TmuxName(), sh.Tmux(), h.drawerInnerH)
}

// fetchScroll captures the view at sc's offset, one capture at a time: a
// trackpad flick sends dozens of notches, and the result handler asks again
// for wherever the wheel ended up.
func fetchScroll(sc *previewScrollState, drawer bool, id string, ts *tmux.Session, rows int) tea.Cmd {
	if sc.inFlight {
		return nil
	}
	sc.inFlight = true
	want := sc.offset
	return func() tea.Msg {
		content, offset, history, err := ts.CapturePaneRange(want, rows)
		return previewScrollMsg{drawer: drawer, id: id, want: want, offset: offset, history: history, content: expandTabs(content), err: err}
	}
}

// expandTabs turns the literal tabs capture-pane emits into spaces up to the
// next 8-column stop; a raw tab has no width to the renderer, so the row came
// out short and pushed the border and scrollbar off it.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		var b strings.Builder
		for j, part := range strings.Split(line, "\t") {
			if j > 0 {
				w := ansi.StringWidth(b.String())
				b.WriteString(strings.Repeat(" ", 8-w%8))
			}
			b.WriteString(part)
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// handlePreviewScroll applies a scrolled capture if the user is still at the
// offset it was taken for, otherwise fetches wherever they are now.
func (h *Home) handlePreviewScroll(msg previewScrollMsg) (tea.Model, tea.Cmd) {
	sc, current, refetch := &h.previewScroll, "", h.fetchPreviewScroll
	if msg.drawer {
		sc, refetch = &h.drawerScroll, h.fetchDrawerScroll
		if sh := h.activeShell(); sh != nil {
			current = sh.TmuxName()
		}
	} else if sel := h.selectedSession(); sel != nil {
		current = sel.ID
	}
	sc.inFlight = false
	if sc.offset == 0 || current != msg.id {
		return h, nil // scrolled back down or moved on meanwhile
	}
	if msg.err != nil {
		h.resetScroll(sc)
		return h, nil
	}
	if msg.want != sc.offset {
		return h, refetch()
	}
	sc.history = msg.history
	if msg.offset == 0 { // no history to scroll into
		h.resetScroll(sc)
		return h, nil
	}
	sc.offset, sc.content, sc.sessionID = msg.offset, msg.content, msg.id
	h.viewDirty = true
	return h, nil
}

// resetPreviewScroll returns the preview to the live bottom.
func (h *Home) resetPreviewScroll() { h.resetScroll(&h.previewScroll) }

func (h *Home) resetScroll(sc *previewScrollState) {
	if sc.offset == 0 && sc.content == "" {
		return
	}
	*sc = previewScrollState{inFlight: sc.inFlight}
	h.viewDirty = true
}

// paintScrollbar draws herdr's scrollbar down the last column of a pane while
// it is scrolled up: ▕ for the track, ▐ for the thumb. inner must be padded to
// w columns.
func paintScrollbar(sc previewScrollState, inner string, w int) string {
	if sc.offset == 0 || sc.content == "" || w < 2 {
		return inner
	}
	// Built per call: the theme reassigns the palette at runtime.
	track := lipgloss.NewStyle().Foreground(ColorTextDim).Render("▕")
	thumbBar := lipgloss.NewStyle().Foreground(ColorAccent).Render("▐")
	lines := strings.Split(inner, "\n")
	rows := len(lines)
	total := sc.history + rows
	thumb := max(1, rows*rows/total)
	top := min(rows-thumb, (sc.history-sc.offset)*rows/total)
	for i, line := range lines {
		bar := track
		if i >= top && i < top+thumb {
			bar = thumbBar
		}
		lines[i] = ansi.Truncate(line, w-1, "") + bar
	}
	return strings.Join(lines, "\n")
}
