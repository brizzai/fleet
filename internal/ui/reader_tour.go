package ui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

// tourTitleLines caps how tall one step's row may grow. A route is meant to be
// readable at a glance; a step whose title needs four lines has stopped being a
// title.
const tourTitleLines = 2

// tourBandLines caps the narration above the code. The band explains where you
// are; the code is what you came for, and on an 80×24 terminal every line the
// band takes is a line of diff you cannot see.
const tourBandLines = 5

// renderTourPanel draws the route in the left panel.
//
// Same two states as the file tree, and for the same reason: unfocused it is a
// readout of where you are, focused it is the list you are driving. The
// selected step is the only one showing its stops.
func (d *ReaderDialog) renderTourPanel(width, rows int) []string {
	out := make([]string, rows)
	if width <= 0 || d.tour == nil {
		return out
	}

	// Keep the selection on screen. Items are multi-line, so this counts lines
	// rather than rows — a two-line title scrolled to its second line reads as
	// a different item.
	heights := make([]int, len(d.tourItems))
	for i := range d.tourItems {
		heights[i] = len(d.tourItemLines(i, width))
	}
	top, used := 0, 0
	for i := 0; i <= d.tourCur && i < len(heights); i++ {
		used += heights[i]
	}
	for used > rows && top < d.tourCur {
		used -= heights[top]
		top++
	}

	line := 0
	for i := top; i < len(d.tourItems) && line < rows; i++ {
		for _, l := range d.tourItemLines(i, width) {
			if line >= rows {
				break
			}
			out[line] = l
			line++
		}
	}
	return out
}

// tourItemLines renders one item, which may be a step (wrapping to two lines)
// or one of its stops.
func (d *ReaderDialog) tourItemLines(i, width int) []string {
	it := d.tourItems[i]
	if it.step >= len(d.tour.Steps) {
		return nil
	}
	st := d.tour.Steps[it.step]
	selected := i == d.tourCur

	if it.anchor >= 0 {
		if it.anchor >= len(st.Anchors) {
			return nil
		}
		return d.tourAnchorLines(st.Anchors[it.anchor], width, selected)
	}

	// The number is the route's spine — it is how "step 2 of 5" in the band
	// and the row you are standing on refer to the same thing.
	num := strconv.Itoa(it.step+1) + " "
	body := wrapTo(max(width-len(num)-2, 6), st.Title)
	if len(body) > tourTitleLines {
		body = body[:tourTitleLines]
		body[tourTitleLines-1] = ansi.Truncate(body[tourTitleLines-1], max(width-len(num)-3, 4), "…")
	}

	var out []string
	for j, text := range body {
		lead := num
		if j > 0 {
			lead = strings.Repeat(" ", len(num))
		}
		raw := " " + lead + text
		if selected {
			// Every line of a selected item carries the fill, so a wrapped
			// title reads as one highlighted block rather than two rows.
			raw += strings.Repeat(" ", max(width-lipgloss.Width(raw), 0))
			out = append(out, SelectionPill(d.treeFocus).Render(ansi.Truncate(raw, width, "")))
			continue
		}
		style := DimStyle
		if j == 0 {
			style = SessionItemStyle
		}
		out = append(out, style.Render(ansi.Truncate(raw, width, "…")))
	}
	return out
}

// tourAnchorLines renders one stop: where it is, and a handful of words on why.
func (d *ReaderDialog) tourAnchorLines(a review.Anchor, width int, selected bool) []string {
	where := shortPath(a.File)
	if a.Line > 0 {
		where += ":" + strconv.Itoa(a.Line)
	}
	// Indented under its step, and cut from the LEFT like every other path in
	// this reader — the basename is what identifies a file.
	where = truncFront(where, max(width-6, 8))

	raw := "   " + where
	if selected {
		raw += strings.Repeat(" ", max(width-lipgloss.Width(raw), 0))
		return []string{SelectionPill(d.treeFocus).Render(ansi.Truncate(raw, width, ""))}
	}
	out := []string{DiffNumStyle.Render(ansi.Truncate(raw, width, "…"))}
	if a.Note != "" {
		note := ansi.Truncate("     "+a.Note, width, "…")
		out = append(out, DimStyle.Render(note))
	}
	return out
}

// shortPath keeps the last two segments, which is what identifies a file
// without spending the panel's whole width on a monorepo prefix.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// tourBand is the narration that sits above the code, in the panel where there
// is room for a sentence.
//
// Above the diff rather than in the tour panel because the tour panel is a
// quarter of the screen: forty words there is fifteen wrapped lines and no
// route left to see. Here it sits with the thing it is describing.
func (d *ReaderDialog) tourBand(width int) []string {
	st, an, ok := d.tourSelection()
	if !ok || st == nil {
		return nil
	}
	body := st.Brief
	if an != nil && an.Note != "" {
		// On a stop, its own words lead — you moved to it deliberately, and the
		// step's brief is still one row up in the panel beside you.
		body = an.Note + " — " + st.Brief
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}

	label := strconv.Itoa(d.tourStepIndex()+1) + "/" + strconv.Itoa(len(d.tour.Steps)) +
		" · " + st.Title
	inner := width - 4
	if inner < 16 {
		return nil
	}
	lines := wrapTo(inner, body)
	if len(lines) > tourBandLines {
		lines = lines[:tourBandLines]
		lines[tourBandLines-1] = ansi.Truncate(lines[tourBandLines-1], inner, "…")
	}

	border := lipgloss.NewStyle().Foreground(ColorBorder)
	head := DimStyle.Render(ansi.Truncate(label, max(inner-2, 4), "…"))
	fill := max(inner-lipgloss.Width(head)-1, 0)

	out := []string{border.Render("╭─ ") + head + border.Render(" "+strings.Repeat("─", fill)+"╮")}
	for _, l := range lines {
		pad := strings.Repeat(" ", max(inner-lipgloss.Width(l), 0))
		out = append(out, border.Render("│ ")+SessionItemStyle.Render(l)+pad+border.Render(" │"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
	return out
}

// renderTourOverview fills the diff panel with a step that has nowhere to send
// you.
//
// An opening step that explains the shape of a change before any one file makes
// sense is worth more than a file it could have pointed at — and a diagram
// needs the wide side of the screen, not the quarter.
func (d *ReaderDialog) renderTourOverview(st *review.Step, width, rows int) []string {
	out := make([]string, rows)
	line := 0
	put := func(s string) {
		if line < rows {
			out[line] = s
			line++
		}
	}

	put("")
	put("  " + TitleStyle.Render(ansi.Truncate(st.Title, max(width-4, 8), "…")))
	put("")
	for _, l := range wrapTo(min(width-6, 76), st.Brief) {
		put("  " + SessionItemStyle.Render(l))
	}
	if st.Diagram != "" {
		put("")
		for _, l := range strings.Split(st.Diagram, "\n") {
			put("  " + DiffNumStyle.Render(ansi.Truncate(l, max(width-4, 8), "")))
		}
	}
	return out
}

// tourStatus is what the panel says while there is no route yet.
func (d *ReaderDialog) tourStatus() string {
	switch d.tourState {
	case tourWorking:
		return "reading the pull request…"
	case tourFailed:
		return "no tour"
	}
	return ""
}
