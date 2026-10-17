package ui

import (
	"strings"

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
// requested for; it only lands if the user is still there.
type previewScrollMsg struct {
	sessionID string
	want      int
	offset    int // want clamped to the history
	history   int
	content   string
	err       error
}

// scrollPreview handles a wheel notch over the preview (delta -1 up, +1 down).
func (h *Home) scrollPreview(delta int) (tea.Model, tea.Cmd) {
	sc := &h.previewScroll
	next := sc.offset - delta*previewScrollLines
	if sc.history > 0 {
		next = min(next, sc.history)
	}
	next = max(next, 0)
	if next == sc.offset {
		return h, nil
	}
	h.clearPreviewSelection() // its rows would now point at different text
	if next == 0 {
		h.resetPreviewScroll()
		return h, tea.Batch(h.fetchPreviewForSelected(), h.markMouseRepaint())
	}
	sc.offset = next
	return h, tea.Batch(h.fetchPreviewScroll(), h.markMouseRepaint())
}

// fetchPreviewScroll captures the view at the current offset, one capture at a
// time: a trackpad flick sends dozens of notches, and the result handler asks
// again for wherever the wheel ended up.
func (h *Home) fetchPreviewScroll() tea.Cmd {
	sel := h.selectedSession()
	if sel == nil || h.previewScroll.inFlight {
		return nil
	}
	h.previewScroll.inFlight = true
	id, ts, want, rows := sel.ID, sel.GetTmuxSession(), h.previewScroll.offset, h.layout.previewFit[1]
	if rows <= 0 {
		rows = h.layout.previewText.h
	}
	return func() tea.Msg {
		content, offset, history, err := ts.CapturePaneRange(want, rows)
		return previewScrollMsg{sessionID: id, want: want, offset: offset, history: history, content: content, err: err}
	}
}

// handlePreviewScroll applies a scrolled capture if the user is still at the
// offset it was taken for, otherwise fetches wherever they are now.
func (h *Home) handlePreviewScroll(msg previewScrollMsg) (tea.Model, tea.Cmd) {
	sc := &h.previewScroll
	sc.inFlight = false
	sel := h.selectedSession()
	if sc.offset == 0 || sel == nil || sel.ID != msg.sessionID {
		return h, nil // scrolled back down or moved on meanwhile
	}
	if msg.err != nil {
		h.resetPreviewScroll()
		return h, nil
	}
	if msg.want != sc.offset {
		return h, h.fetchPreviewScroll()
	}
	sc.history = msg.history
	if msg.offset == 0 { // no history to scroll into
		h.resetPreviewScroll()
		return h, nil
	}
	sc.offset, sc.content, sc.sessionID = msg.offset, msg.content, msg.sessionID
	h.viewDirty = true
	return h, nil
}

// resetPreviewScroll returns the preview to the live bottom.
func (h *Home) resetPreviewScroll() {
	if h.previewScroll.offset == 0 && h.previewScroll.content == "" {
		return
	}
	inFlight := h.previewScroll.inFlight
	h.previewScroll = previewScrollState{inFlight: inFlight}
	h.viewDirty = true
}

// paintPreviewScrollbar draws herdr's scrollbar down the last column of the
// preview while it is scrolled up: ▕ for the track, ▐ for the thumb.
func (h *Home) paintPreviewScrollbar(inner string, w int) string {
	sc := h.previewScroll
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
