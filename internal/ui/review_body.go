package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	// proseMeasure is how wide a paragraph is allowed to run.
	//
	// The preview pane is often 150 columns and prose is unreadable at that —
	// the same call the tour's narration band makes. Tables and code blocks are
	// deliberately NOT capped: they are laid out rather than read line by line,
	// and squeezing a four-column table into 90 columns to match a paragraph
	// wraps every cell for no reason.
	proseMeasure = 90

	// mdMinCell keeps a squeezed table column wide enough to hold a word.
	mdMinCell = 8
)

// reviewBodyLines renders a pull request description.
//
// Hand-rolled rather than a markdown library, and the tables are why: a general
// renderer wraps the whole document to one width and lays tables out without
// wrapping their cells, which is the opposite of what a preview pane needs. What
// actually appears in a pull request body is headings, paragraphs, bullets,
// fenced code and tables — five things, each of which wants its own width.
func reviewBodyLines(body string, width, budget int) []string {
	body = htmlComment.ReplaceAllString(body, "")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")

	prose := min(width, proseMeasure)
	var out []string
	blank := false
	emit := func(ls ...string) {
		for _, l := range ls {
			if len(out) < budget {
				out = append(out, l)
			}
		}
		blank = false
	}

	for i := 0; i < len(lines) && len(out) < budget; {
		line := strings.TrimRight(lines[i], " \t")

		switch {
		case strings.TrimSpace(line) == "":
			// One blank line between blocks, never the three a template leaves
			// behind: vertical space is the scarcest thing in a preview.
			if !blank && len(out) > 0 {
				out = append(out, "")
				blank = true
			}
			i++

		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			block, next := collectFence(lines, i)
			emit(renderMDCode(block, width)...)
			i = next

		case isTableRow(line) && i+1 < len(lines) && isTableRule(lines[i+1]):
			rows, aligns, next := collectTable(lines, i)
			emit(renderMDTable(rows, aligns, width)...)
			i = next

		case strings.HasPrefix(line, "#"):
			emit(renderMDHeading(line, prose))
			i++

		default:
			emit(renderMDParagraph(line, prose)...)
			i++
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// renderMDHeading styles by level: the top two are structure, deeper ones are
// labels on a paragraph and should not shout as loudly as the title above them.
func renderMDHeading(line string, width int) string {
	text := strings.TrimSpace(strings.TrimLeft(line, "#"))
	level := len(line) - len(strings.TrimLeft(line, "#"))
	style := TitleStyle
	if level >= 3 {
		style = lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	}
	return style.Render(ansi.Truncate(text, width, "…"))
}

// renderMDParagraph styles the WHOLE paragraph, then wraps it ANSI-aware.
//
// Wrapping first and styling each line separately is the obvious order and it is
// wrong: emphasis that spans a line break has its opening marker on one line and
// its closing marker on the next, so neither line contains a pair and both
// render their underscores raw. A real description hits this constantly — the
// markers are around a sentence, and a sentence is longer than the measure.
//
// It is safe here only because ansi.Wordwrap counts columns rather than bytes;
// wrapTo would measure the escape sequences as text.
func renderMDParagraph(line string, width int) []string {
	indent := ""
	body := line
	// A bullet keeps its marker and hangs its continuation underneath, so a
	// wrapped list still reads as a list.
	if t := strings.TrimLeft(line, " "); strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
		indent = "  "
		body = "• " + strings.TrimSpace(t[2:])
	}
	wrapped := ansi.Wordwrap(renderInlineMarkdown(body), max(width-len(indent), 8), "")
	var out []string
	for j, w := range strings.Split(wrapped, "\n") {
		lead := indent
		if j > 0 && indent != "" {
			lead = indent + "  "
		}
		out = append(out, lead+w)
	}
	return out
}

// collectFence returns a fenced block's inner lines and where it ended.
func collectFence(lines []string, i int) ([]string, int) {
	var block []string
	for j := i + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			return block, j + 1
		}
		block = append(block, lines[j])
	}
	// An unclosed fence is a real thing in a hand-written description; take the
	// rest rather than dropping it.
	return block, len(lines)
}

// renderMDCode draws a fenced block behind a rule, with the fence markers gone.
//
// Full width and never wrapped: the fences in a pull request body are shell
// transcripts and ASCII diagrams, and a diagram wrapped at a reading measure is
// no longer a diagram.
func renderMDCode(block []string, width int) []string {
	rule := lipgloss.NewStyle().Foreground(ColorBorder).Render("│ ")
	code := lipgloss.NewStyle().Foreground(ColorOrange)
	out := make([]string, 0, len(block))
	for _, l := range block {
		out = append(out, rule+code.Render(ansi.Truncate(l, max(width-2, 8), "…")))
	}
	return out
}

func isTableRow(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "|") }

// isTableRule recognises the |---|---| line that makes the row above a header.
// Without it, any line of prose containing pipes would become a table.
func isTableRule(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "|") {
		return false
	}
	seen := false
	for _, c := range t {
		switch c {
		case '-':
			seen = true
		case '|', ':', ' ':
		default:
			return false
		}
	}
	return seen
}

// mdAlign is a column's alignment, read from the rule row's colons.
type mdAlign int

const (
	mdLeft mdAlign = iota
	mdCenter
	mdRight
)

// collectTable reads a table into cells, its alignments, and where it ended.
func collectTable(lines []string, i int) ([][]string, []mdAlign, int) {
	split := func(s string) []string {
		t := strings.TrimSpace(s)
		t = strings.TrimPrefix(t, "|")
		t = strings.TrimSuffix(t, "|")
		cells := strings.Split(t, "|")
		for k := range cells {
			cells[k] = strings.TrimSpace(cells[k])
		}
		return cells
	}

	rows := [][]string{split(lines[i])}
	var aligns []mdAlign
	for _, spec := range split(lines[i+1]) {
		switch {
		case strings.HasPrefix(spec, ":") && strings.HasSuffix(spec, ":"):
			aligns = append(aligns, mdCenter)
		case strings.HasSuffix(spec, ":"):
			aligns = append(aligns, mdRight)
		default:
			aligns = append(aligns, mdLeft)
		}
	}
	j := i + 2
	for ; j < len(lines) && isTableRow(lines[j]); j++ {
		rows = append(rows, split(lines[j]))
	}
	return rows, aligns, j
}

// renderMDTable draws a table with its cells wrapped inside their columns.
//
// Columns take their natural width where the panel allows it and are shrunk
// proportionally where it does not, never below a word. Wrapping rather than
// truncating is the whole point: in a before/after table the tail of the cell
// is usually the part that mattered.
func renderMDTable(rows [][]string, aligns []mdAlign, width int) []string {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 || len(rows) < 2 {
		return nil
	}

	natural := make([]int, cols)
	for _, r := range rows {
		for c, cell := range r {
			natural[c] = max(natural[c], lipgloss.Width(cell))
		}
	}
	// Every column costs "│ " before it plus a trailing "│", and one space
	// after the text — so the chrome is 3 per column plus 1.
	avail := width - (cols*3 + 1)
	widths := fitColumns(natural, max(avail, cols*mdMinCell))

	border := lipgloss.NewStyle().Foreground(ColorBorder)
	rule := func(l, m, r string) string {
		parts := make([]string, cols)
		for c := range parts {
			parts[c] = strings.Repeat("─", widths[c]+2)
		}
		return border.Render(l + strings.Join(parts, m) + r)
	}

	out := []string{rule("┌", "┬", "┐")}
	for i, r := range rows {
		out = append(out, renderTableRow(r, widths, aligns, cols, i == 0, border)...)
		if i == 0 {
			out = append(out, rule("├", "┼", "┤"))
		}
	}
	return append(out, rule("└", "┴", "┘"))
}

// renderTableRow wraps every cell, then lays the row out as many lines as its
// tallest cell needs.
func renderTableRow(r []string, widths []int, aligns []mdAlign, cols int,
	header bool, border lipgloss.Style) []string {

	wrapped := make([][]string, cols)
	height := 1
	for c := 0; c < cols; c++ {
		cell := ""
		if c < len(r) {
			cell = r[c]
		}
		// A cell carries inline markdown too — `git push` in a table read as
		// literal backticks until this ran on cells as well as paragraphs.
		// Styled first, then wrapped ANSI-aware, for the same reason a
		// paragraph is.
		styled := renderInlineMarkdown(cell)
		if header {
			styled = lipgloss.NewStyle().Bold(true).Foreground(ColorText).Render(cell)
		}
		wrapped[c] = strings.Split(ansi.Wordwrap(styled, widths[c], ""), "\n")
		height = max(height, len(wrapped[c]))
	}

	out := make([]string, height)
	for line := 0; line < height; line++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			text := ""
			if line < len(wrapped[c]) {
				text = wrapped[c][line]
			}
			b.WriteString(border.Render("│ "))
			// Padded by COLUMNS, not bytes: the cell is already styled, so
			// lipgloss.Width is the only honest measure of what it occupies.
			b.WriteString(padCell(text, widths[c], alignOf(aligns, c)))
			b.WriteString(" ")
		}
		b.WriteString(border.Render("│"))
		out[line] = b.String()
	}
	return out
}

func alignOf(aligns []mdAlign, c int) mdAlign {
	if c < len(aligns) {
		return aligns[c]
	}
	return mdLeft
}

func padCell(s string, width int, a mdAlign) string {
	pad := max(width-lipgloss.Width(s), 0)
	switch a {
	case mdRight:
		return strings.Repeat(" ", pad) + s
	case mdCenter:
		left := pad / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
	}
	return s + strings.Repeat(" ", pad)
}

// fitColumns gives every column its natural width when they all fit, and
// otherwise shrinks the wide ones first — a column holding one word should not
// lose half of it so a column holding a sentence can keep a little more.
func fitColumns(natural []int, avail int) []int {
	widths := append([]int(nil), natural...)
	total := 0
	for _, w := range widths {
		total += w
	}
	if total <= avail {
		return widths
	}
	for total > avail {
		widest, at := 0, -1
		for i, w := range widths {
			if w > widest && w > mdMinCell {
				widest, at = w, i
			}
		}
		if at < 0 {
			break // everything is at the floor; the table will overflow slightly
		}
		widths[at]--
		total--
	}
	return widths
}
