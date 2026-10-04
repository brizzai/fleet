package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/tmux"
)

// Drag-to-select in the preview pane. The terminal's own selection runs across
// the whole screen row, sidebar included; this one stays inside the pane. Press
// records an anchor, a drag highlights from it, release copies, and the next
// click or key press clears it. While a selection exists the preview stops
// refreshing (see previewMsg), so agent output can't scroll text out from
// under the highlight.

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

// notePreview records the frame's preview geometry and lines (for hit-testing
// and copying), then paints the selection over them. inner is the rendered
// preview at exactly w×hgt, placed at screen cell (x, y).
func (h *Home) notePreview(inner string, x, y, w, hgt int) string {
	h.layout.previewText = mouseRect{x: x + previewIndent, y: y, w: w - previewIndent, h: hgt}
	h.previewLines = strings.Split(inner, "\n")
	if !h.previewSel.shown {
		return inner
	}
	lines := append([]string(nil), h.previewLines...)
	h.forEachSelectedRow(func(row, c0, c1 int) {
		line := lines[row]
		lines[row] = ansi.Truncate(line, c0, "") +
			selectionStyle.Render(ansi.Strip(ansi.Cut(line, c0, c1))) +
			ansi.TruncateLeft(line, c1, "")
	})
	return strings.Join(lines, "\n")
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

// endPreviewSelection copies the selection on release. The highlight stays up
// so it is clear what went to the clipboard.
func (h *Home) endPreviewSelection() (tea.Model, tea.Cmd) {
	h.previewSel.dragging = false
	if !h.previewSel.shown {
		return h, nil
	}
	text := h.selectedPreviewText()
	if strings.TrimSpace(text) == "" {
		return h, nil
	}
	lines := strings.Count(text, "\n") + 1
	return h, func() tea.Msg {
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
