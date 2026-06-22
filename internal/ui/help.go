package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpOverlay shows a keybindings cheat sheet.
type HelpOverlay struct {
	visible bool
	width   int
	height  int
	scroll  int // top grid row when the sheet is taller than the screen
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{}
}

func (h *HelpOverlay) Show()           { h.visible = true; h.scroll = 0 }
func (h *HelpOverlay) Hide()           { h.visible = false }
func (h *HelpOverlay) IsVisible() bool { return h.visible }

// SetSize records the terminal size and re-clamps the scroll offset, so it
// owns the scroll invariant on resize and View() can stay read-only.
func (h *HelpOverlay) SetSize(w, ht int) {
	h.width, h.height = w, ht
	h.scroll = clampInt(h.scroll, 0, h.layout().maxScroll)
}

// Update scrolls when the sheet overflows; any non-scroll key closes it.
func (h *HelpOverlay) Update(msg tea.Msg) (*HelpOverlay, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return h, nil
	}
	lay := h.layout()
	if lay.maxScroll == 0 {
		// Whole sheet is visible — preserve the "any key closes" behaviour.
		h.Hide()
		return h, nil
	}
	switch key.String() {
	case "up", "k":
		h.scroll--
	case "down", "j":
		h.scroll++
	case "pgup":
		h.scroll -= lay.pageStep
	case "pgdown":
		h.scroll += lay.pageStep
	case "home", "g":
		h.scroll = 0
	case "end", "G":
		h.scroll = lay.maxScroll
	default:
		h.Hide()
		return h, nil
	}
	h.scroll = clampInt(h.scroll, 0, lay.maxScroll)
	return h, nil
}

// helpLayout holds the geometry computed from the terminal size, shared by
// Update (clamping/paging) and View (rendering) so they can't disagree.
type helpLayout struct {
	entries     []struct{ Key, Desc string }
	keyW        int
	colW        int
	gutter      int
	cols        int
	rowsPerCol  int
	visibleRows int // grid rows shown at once
	pageStep    int
	maxScroll   int
}

// layout lays the bindings out as newspaper-style columns sized to the
// terminal: a short/wide pane gets more columns so it fits without scrolling.
// When even the widest-possible column count is still too tall, the remaining
// rows scroll.
func (h *HelpOverlay) layout() helpLayout {
	var entries []struct{ Key, Desc string }
	for _, b := range HelpOverlayBindings() {
		if b.Key == "" { // drop section separators; columns provide the gaps
			continue
		}
		entries = append(entries, b)
	}
	n := len(entries)

	const (
		gutter  = 2 // space between columns
		marginH = 2 // breathing room from the screen edges
		indRows = 2 // ⋮ above / ⋮ below lines reserved when scrolling
		// Non-grid lines View() always emits: title+blank and blank+hint.
		titleLines = 2
		hintLines  = 2
	)
	// Frame overhead (border + padding) is read from DialogStyle, so the scroll
	// math follows automatically if the dialog is ever restyled.
	frameV := DialogStyle.GetVerticalFrameSize()
	frameH := DialogStyle.GetHorizontalFrameSize()

	// keyW is the true widest key (not capped), so colW always accounts for it —
	// lipgloss .Width is a minimum, so a capped value could be overflowed.
	keyW, descW := 0, 0
	for _, e := range entries {
		keyW = max(keyW, lipgloss.Width(e.Key))
		descW = max(descW, lipgloss.Width(e.Desc))
	}
	colW := keyW + 2 + descW

	availH := max(1, h.height-frameV-titleLines-hintLines)
	availW := max(colW, h.width-frameH-marginH)

	colsByWidth := max(1, (availW+gutter)/(colW+gutter))
	colsByHeight := ceilDiv(n, availH)
	cols := clampInt(colsByHeight, 1, colsByWidth)
	rowsPerCol := ceilDiv(n, cols)

	visibleRows, maxScroll := rowsPerCol, 0
	if rowsPerCol > availH { // can't fit even at max columns → scroll
		visibleRows = max(1, availH-indRows)
		maxScroll = rowsPerCol - visibleRows
	}

	return helpLayout{
		entries:     entries,
		keyW:        keyW,
		colW:        colW,
		gutter:      gutter,
		cols:        cols,
		rowsPerCol:  rowsPerCol,
		visibleRows: visibleRows,
		pageStep:    max(1, visibleRows-1),
		maxScroll:   maxScroll,
	}
}

// row renders one grid line: each column's cell joined by the gutter.
func (lay helpLayout) row(r int) string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Width(lay.keyW)
	var b strings.Builder
	for c := 0; c < lay.cols; c++ {
		if c > 0 {
			b.WriteString(strings.Repeat(" ", lay.gutter))
		}
		cell := ""
		if idx := c*lay.rowsPerCol + r; idx < len(lay.entries) {
			e := lay.entries[idx]
			cell = keyStyle.Render(e.Key) + "  " + e.Desc
		}
		if w := lipgloss.Width(cell); w < lay.colW {
			cell += strings.Repeat(" ", lay.colW-w)
		}
		b.WriteString(cell)
	}
	return b.String()
}

// View renders the keybinding cheat sheet.
func (h *HelpOverlay) View() string {
	lay := h.layout()

	var lines []string
	lines = append(lines, TitleStyle.Render("Keybindings"), "")

	if lay.maxScroll == 0 {
		for r := 0; r < lay.rowsPerCol; r++ {
			lines = append(lines, lay.row(r))
		}
	} else {
		above, below := lay.hiddenCounts(h.scroll)
		if above > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf("⋮ +%d above", above)))
		} else {
			lines = append(lines, "")
		}
		for r := h.scroll; r < h.scroll+lay.visibleRows; r++ {
			lines = append(lines, lay.row(r))
		}
		if below > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf("⋮ +%d below", below)))
		} else {
			lines = append(lines, "")
		}
	}

	hint := "Press any key to close"
	if lay.maxScroll > 0 {
		hint = "↑↓ scroll · any other key to close"
	}
	lines = append(lines, "", DimStyle.Render(hint))

	// Let the box auto-size to its widest line (the padded grid rows). Forcing
	// an explicit Width would make lipgloss count the horizontal padding against
	// the content area and wrap the longest binding.
	box := DialogStyle.Render(strings.Join(lines, "\n"))
	// Safety net so the box never bleeds past the screen. Below ~9 rows the
	// frame + title + one binding + hint can't all fit and MaxHeight drops the
	// hint — but that's sub-usable territory (the main sidebar/preview UI can't
	// render at that height either).
	box = lipgloss.NewStyle().MaxWidth(h.width).MaxHeight(h.height).Render(box)
	return lipgloss.Place(h.width, h.height, lipgloss.Center, lipgloss.Center, box)
}

// hiddenCounts returns how many bindings sit above and below the visible
// window, counting actual entries (the last column may be short).
func (lay helpLayout) hiddenCounts(scroll int) (above, below int) {
	end := scroll + lay.visibleRows
	for i := range lay.entries {
		row := i % lay.rowsPerCol
		if row < scroll {
			above++
		} else if row >= end {
			below++
		}
	}
	return
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
