package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// Drag-to-select in the preview pane. The terminal's own selection runs across
// the whole screen row, sidebar included; this one stays inside the pane. Press
// records an anchor, a drag highlights from it, and the next click or key press
// clears it. While a selection exists the preview stops
// refreshing (see previewMsg), so agent output can't scroll text out from
// under the highlight. Ctrl+C (or Ctrl+Shift+C) copies, like herdr with
// copy_on_select off.

// previewIndent is the two columns RenderPreview puts before every line. They
// are never text, so the selectable area starts after them.
const previewIndent = 2

type previewSelection struct {
	anchor, head tea.Mouse // screen cells, clamped to layout.previewText
	dragging     bool      // left button is down
	shown        bool      // something is highlighted (the pointer moved off the anchor)
}

// active reports whether the preview should hold still.
func (s previewSelection) active() bool { return s.dragging || s.shown }

var selectionStyle = lipgloss.NewStyle().Reverse(true)

// notePreview records the frame's preview geometry and lines (for hit-testing,
// copying and fitting the agent window), then paints the selection over them.
// inner is s's rendered preview at exactly w×hgt, placed at screen cell (x, y).
func (h *Home) notePreview(inner string, s *session.Session, x, y, w, hgt int) string {
	h.layout.previewText = mouseRect{x: x + previewIndent, y: y, w: w - previewIndent, h: hgt}
	rows := hgt
	if s != nil && s.FirstPrompt != "" {
		rows-- // RenderPreview's prompt strip
	}
	h.layout.previewFit = [2]int{w - previewIndent, rows}
	h.previewLines = strings.Split(inner, "\n")
	if !h.previewSel.shown {
		return h.paintPreviewScrollbar(inner, w)
	}
	lines := append([]string(nil), h.previewLines...)
	h.forEachSelectedRow(func(row, c0, c1 int) {
		line := lines[row]
		lines[row] = ansi.Truncate(line, c0, "") +
			selectionStyle.Render(ansi.Strip(ansi.Cut(line, c0, c1))) +
			ansi.TruncateLeft(line, c1, "")
	})
	return h.paintPreviewScrollbar(strings.Join(lines, "\n"), w)
}

// forEachSelectedRow calls fn for each preview line the selection covers, with
// the [c0, c1) column range inside that line. Rows run like a terminal
// selection: from the anchor to the end of its row, whole rows between, and up
// to the head on the last row, whichever way the drag went.
func (h *Home) forEachSelectedRow(fn func(row, c0, c1 int)) {
	r := h.layout.previewText
	a, b := h.previewSel.anchor, h.previewSel.head
	if b.Y < a.Y || (b.Y == a.Y && b.X < a.X) {
		a, b = b, a
	}
	left, right := previewIndent, previewIndent+r.w // text columns within a line
	for y := a.Y; y <= b.Y; y++ {
		row := y - r.y
		if row < 0 || row >= len(h.previewLines) {
			continue
		}
		c0, c1 := left, right
		if y == a.Y {
			c0 = left + a.X - r.x
		}
		if y == b.Y {
			c1 = left + b.X - r.x + 1
		}
		if c0 < c1 {
			fn(row, c0, c1)
		}
	}
}

// selectedPreviewText is the plain text under the selection, one line per row,
// without trailing blanks.
func (h *Home) selectedPreviewText() string {
	var rows []string
	h.forEachSelectedRow(func(row, c0, c1 int) {
		rows = append(rows, strings.TrimRight(ansi.Strip(ansi.Cut(h.previewLines[row], c0, c1)), " "))
	})
	return strings.Join(rows, "\n")
}

// clampToPreview pins a pointer cell inside the selectable area, so a drag that
// overshoots the pane selects up to its edge instead of into the sidebar.
func (h *Home) clampToPreview(m tea.Mouse) tea.Mouse {
	r := h.layout.previewText
	m.X = max(r.x, min(m.X, r.x+r.w-1))
	m.Y = max(r.y, min(m.Y, r.y+r.h-1))
	return m
}

// startPreviewSelection handles a left press inside the preview text.
func (h *Home) startPreviewSelection(m tea.Mouse) (tea.Model, tea.Cmd) {
	had := h.previewSel.shown
	h.previewSel = previewSelection{anchor: m, head: m, dragging: true}
	if had {
		return h, h.markMouseRepaint() // wipe the old highlight
	}
	return h, nil
}

// dragPreviewSelection moves the selection's head with the pointer.
func (h *Home) dragPreviewSelection(m tea.Mouse) (tea.Model, tea.Cmd) {
	m = h.clampToPreview(m)
	if m.X == h.previewSel.head.X && m.Y == h.previewSel.head.Y {
		return h, nil
	}
	h.previewSel.head = m
	h.previewSel.shown = m.X != h.previewSel.anchor.X || m.Y != h.previewSel.anchor.Y
	return h, h.markMouseRepaint()
}

// endPreviewSelection ends the drag; the highlight stays up until it is copied
// or cleared.
func (h *Home) endPreviewSelection() (tea.Model, tea.Cmd) {
	h.previewSel.dragging = false
	return h, nil
}

// copyPreviewSelection copies the highlighted text and clears the highlight.
func (h *Home) copyPreviewSelection() tea.Cmd {
	text := h.selectedPreviewText()
	h.clearPreviewSelection()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Count(text, "\n") + 1
	return func() tea.Msg {
		if err := tmux.CopyToClipboard(text); err != nil {
			return copySelectionMsg{err: fmt.Errorf("copy selection: %w", err)}
		}
		return copySelectionMsg{lines: lines}
	}
}

type copySelectionMsg struct {
	lines int
	err   error
}

// clearPreviewSelection drops the highlight; the preview resumes refreshing on
// its next tick.
func (h *Home) clearPreviewSelection() {
	if h.previewSel.active() {
		h.previewSel = previewSelection{}
		h.viewDirty = true
	}
}

// previewResize returns the step that fits s's tmux window to the preview, or a
// no-op when it already fits — the way herdr sizes each pane's terminal to the
// pane, so the agent redraws at the preview's width instead of being cut off.
// Called on the Update goroutine; the returned func runs inside the fetch Cmd.
//
// ponytail: only the session being previewed is fitted (others when selected),
// and one also attached in another terminal gets the preview's size there.
func (h *Home) previewResize(s *session.Session) func() {
	size := h.layout.previewFit
	if size[0] <= 0 || size[1] <= 0 || h.previewSizes[s.ID] == size {
		return func() {}
	}
	if h.previewSizes == nil {
		h.previewSizes = map[string][2]int{}
	}
	h.previewSizes[s.ID] = size
	ts := s.GetTmuxSession()
	return func() { _ = ts.ResizeWindow(size[0], size[1]) }
}
