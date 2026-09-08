package ui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

// tourPanelWidth is how wide the left panel may grow while the route is
// showing. See treeWidth for why the file tree's own cap is wrong here.
const tourPanelWidth = 48

// tourTitleLines caps how tall one step's row may grow. A route is meant to be
// readable at a glance; a step whose title needs four lines has stopped being a
// title.
const tourTitleLines = 2

// tourBandMeasure is the reading width of the narration.
//
// Wrapped to the panel instead, it ran to about 190 columns on a wide terminal —
// roughly three times the measure prose stays readable at, which is what turned
// a short paragraph into one endless line. The box stops where the text stops,
// so it reads as a note attached to the code rather than a banner across the
// screen.
const tourBandMeasure = 76

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

	// The number is the route's spine — it is how "step 2 of 5" in the band and
	// the row you are standing on refer to the same thing.
	num := strconv.Itoa(it.step+1) + "  "
	body := wrapTo(max(width-len(num)-2, 6), st.Title)
	if len(body) > tourTitleLines {
		body = body[:tourTitleLines]
		body[tourTitleLines-1] = ansi.Truncate(body[tourTitleLines-1], max(width-len(num)-3, 4), "…")
	}

	var out []string
	// A blank row before every step but the first. The route is a list of ideas
	// and the eye needs somewhere to break; without it seven wrapped titles run
	// together into one grey block, which is what made the panel unreadable.
	if it.step > 0 {
		out = append(out, "")
	}
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
		// The continuation of a title is still the title. Dimming it, as this
		// did, made every wrapped step look like a heading with a caption.
		out = append(out, SessionItemStyle.Render(ansi.Truncate(raw, width, "…")))
	}
	return out
}

// tourAnchorLines renders one stop on ONE line: where it is, and why.
//
// One line and not two, which is what it was. A stop split across a path row and
// a note row doubled the height of every step and left the panel with no shape —
// four greyish rows per stop, none of them clearly the heading of the others.
func (d *ReaderDialog) tourAnchorLines(a review.Anchor, width int, selected bool) []string {
	where := shortPath(a.File)
	if a.Line > 0 {
		where += ":" + strconv.Itoa(a.Line)
	}
	// The path gets at most half, so a deep monorepo path cannot crowd out the
	// words that say why you are being sent there. Cut from the LEFT like every
	// other path in this reader — the basename is what identifies a file.
	const lead = "     "
	pathW := max((width-len(lead))/2, 10)
	where = truncFront(where, pathW)

	raw := lead + where
	if a.Note != "" {
		pad := strings.Repeat(" ", max(pathW-lipgloss.Width(where)+1, 1))
		raw += pad + ansi.Truncate(a.Note, max(width-lipgloss.Width(raw)-len(pad)-1, 4), "…")
	}
	if selected {
		raw += strings.Repeat(" ", max(width-lipgloss.Width(raw), 0))
		return []string{SelectionPill(d.treeFocus).Render(ansi.Truncate(raw, width, ""))}
	}
	// Styled in parts: the path is a location and the note is prose, and
	// painting them one colour is what made the panel a wall.
	styled := DiffNumStyle.Render(lead + where)
	if a.Note != "" {
		styled += DimStyle.Render(raw[len(lead)+len(where):])
	}
	return []string{ansi.Truncate(styled, width, "…")}
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
	// The box stops where the text stops rather than spanning the panel: at 190
	// columns the same paragraph was one unreadable line, and a border stretched
	// across the whole screen reads as a banner rather than as a note about the
	// code underneath it.
	inner := min(width-4, tourBandMeasure)
	if inner < 16 {
		return nil
	}
	// Styled first, then wrapped ANSI-aware — the same order the description
	// needs, and for the same reason: emphasis around a phrase is longer than
	// the measure, and wrapping first leaves its markers stranded on two lines.
	lines := strings.Split(ansi.Wordwrap(renderInlineMarkdown(body), inner, ""), "\n")
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
		out = append(out, border.Render("│ ")+l+pad+border.Render(" │"))
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

	// The same renderer the pull request description gets. A step's brief is
	// prose written by the same model, in the same voice, with the same inline
	// code and emphasis in it — rendering one properly and the other as flat
	// text made the tour look like the draft of the page beside it.
	for _, para := range strings.Split(st.Brief, "\n") {
		if strings.TrimSpace(para) == "" {
			put("")
			continue
		}
		for _, l := range renderMDParagraph(para, min(width-4, proseMeasure)) {
			put("  " + l)
		}
	}

	if st.Diagram != "" {
		put("")
		// And the diagram is a code block, because that is what it is: the
		// rule down its left edge is what separates a drawing from the prose
		// above it without needing a caption to say so.
		for _, l := range renderMDCode(strings.Split(st.Diagram, "\n"), max(width-4, 8)) {
			put("  " + l)
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
