package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// helpMinInnerWidth is the floor for the dialog's text column; below this the
// sheet is unreadable, so we truncate rather than shrink further.
const helpMinInnerWidth = 24

// helpKeyColWidth is the fixed width of the key column. It must fit the widest
// key label (currently "= = then digit", 14 cells); padding/truncating to it
// keeps every binding on exactly one row so the height math stays exact.
const helpKeyColWidth = 14

// helpChromeHeight is the vertical space consumed by everything around the
// scrollable body: dialog border (2) + padding (2) + title block (title +
// blank = 2) + footer block (blank + footer = 2). The body therefore gets
// h.height - helpChromeHeight rows, which keeps the rendered box no taller
// than the terminal so lipgloss.Place never clips it.
const helpChromeHeight = 8

// HelpOverlay shows a keybindings cheat sheet.
type HelpOverlay struct {
	visible bool
	width   int
	height  int
	scroll  int
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{}
}

func (h *HelpOverlay) Show()             { h.visible = true; h.scroll = 0 }
func (h *HelpOverlay) Hide()             { h.visible = false }
func (h *HelpOverlay) IsVisible() bool   { return h.visible }
func (h *HelpOverlay) SetSize(w, ht int) { h.width = w; h.height = ht }

// bodyLines renders one line per keybinding (blank lines act as section
// separators), independent of the current scroll position.
func (h *HelpOverlay) bodyLines() []string {
	bindings := HelpOverlayBindings()
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	lines := make([]string, 0, len(bindings))
	for _, bind := range bindings {
		if bind.Key == "" {
			lines = append(lines, "")
			continue
		}
		// Pad/truncate the key text to a fixed cell BEFORE styling so lipgloss
		// can never wrap a long label onto a second line.
		key := keyStyle.Render(padOrTruncate(bind.Key, helpKeyColWidth))
		lines = append(lines, "  "+key+"  "+bind.Desc)
	}
	return lines
}

// visibleRows is how many body lines fit in the current terminal height.
func (h *HelpOverlay) visibleRows() int {
	return max(h.height-helpChromeHeight, 1)
}

// maxScroll is the largest valid scroll offset for the current size; 0 means
// the whole sheet fits and isn't scrollable.
func (h *HelpOverlay) maxScroll() int {
	if m := len(h.bodyLines()) - h.visibleRows(); m > 0 {
		return m
	}
	return 0
}

// Update scrolls when the sheet is taller than the screen; any non-scroll key
// (and any key at all when it already fits) dismisses the overlay.
func (h *HelpOverlay) Update(msg tea.Msg) (*HelpOverlay, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return h, nil
	}

	maxScroll := h.maxScroll()
	if maxScroll == 0 {
		// Everything fits — preserve the "any key closes" behaviour.
		h.Hide()
		return h, nil
	}

	page := h.visibleRows()
	switch key.String() {
	case "j", "down", "ctrl+n":
		h.scroll = clampInt(h.scroll+1, 0, maxScroll)
	case "k", "up", "ctrl+p":
		h.scroll = clampInt(h.scroll-1, 0, maxScroll)
	case "pgdown", "ctrl+d", " ":
		h.scroll = clampInt(h.scroll+page, 0, maxScroll)
	case "pgup", "ctrl+u":
		h.scroll = clampInt(h.scroll-page, 0, maxScroll)
	case "g", "home":
		h.scroll = 0
	case "G", "end":
		h.scroll = maxScroll
	default:
		h.Hide()
	}
	return h, nil
}

// innerWidth is the text column width: wide enough for the longest binding,
// but never wider than the terminal allows (border 2 + padding 4 = 6 cells of
// chrome). Lines wider than this are truncated so each binding is exactly one
// row, which keeps the height math exact and avoids vertical clipping.
func (h *HelpOverlay) innerWidth(lines []string) int {
	longest := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > longest {
			longest = w
		}
	}
	maxInner := max(h.width-6, helpMinInnerWidth)
	return clampInt(longest, helpMinInnerWidth, maxInner)
}

// View renders the keybinding cheat sheet, windowed to the terminal height.
func (h *HelpOverlay) View() string {
	lines := h.bodyLines()
	avail := h.visibleRows()
	inner := h.innerWidth(lines)

	var rows []string
	var footer string
	if len(lines) <= avail {
		rows = lines
		footer = "Press any key to close"
	} else {
		maxScroll := len(lines) - avail
		h.scroll = clampInt(h.scroll, 0, maxScroll)
		rows = lines[h.scroll : h.scroll+avail]
		footer = fmt.Sprintf("↑↓ scroll  ·  %d–%d of %d  ·  any other key closes",
			h.scroll+1, h.scroll+avail, len(lines))
	}

	// Truncate every line (and the footer) to the text column so nothing wraps.
	body := make([]string, len(rows))
	for i, r := range rows {
		body[i] = truncateToWidth(r, inner)
	}

	content := TitleStyle.Render("Keybindings") + "\n\n" +
		strings.Join(body, "\n") + "\n\n" +
		DimStyle.Render(truncateToWidth(footer, inner))
	box := DialogStyle.Width(inner + 4).Render(content)
	return lipgloss.Place(h.width, h.height, lipgloss.Center, lipgloss.Center, box)
}

// truncateToWidth shortens s (ANSI-aware) to at most width display cells.
func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// padOrTruncate fits s to exactly width display cells: right-pad with spaces if
// shorter, ANSI-aware truncate (with an ellipsis) if longer.
func padOrTruncate(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return ansi.Truncate(s, width, "…")
}

// clampInt constrains v to the inclusive range [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
