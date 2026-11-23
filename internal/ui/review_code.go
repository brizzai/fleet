package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

// The diff's row renderers, as free functions over a review.Stream.
//
// Lifted out of a dialog so that two surfaces can draw the same code the same
// way: the Review view's stop card renders only the rows one decision is about
// (RenderHunk), and the full diff renders every row with a cursor. Neither owns
// a copy of the colour rules — the two channels (row tint = added/deleted,
// foreground = syntax) are decided here and in design_review.go, once.

// diffTabWidth fixes what a tab is worth. Terminals disagree, and a tab
// rendered at the terminal's own width shears every column after it —
// including the diff's own gutter on the row below.
const diffTabWidth = 4

// dispRow is one rendered row of a diff panel.
//
// A cursor and a scroll position should index THIS list rather than the
// stream, because side-by-side pairs two stream lines onto one row and unified
// does not — an index that meant a stream line would move under the user every
// time the layout toggled. Toggle by remapping through the stream index.
type dispRow struct {
	left  int  // stream index, or -1
	right int  // stream index in split mode, or -1
	full  bool // spans the whole panel (headers, folds, notes)
}

// primary is the stream index the row stands on.
func (r dispRow) primary() int {
	if r.left >= 0 {
		return r.left
	}
	return r.right
}

// buildRows flattens the whole stream into rendered rows.
func buildRows(s review.Stream, split bool) []dispRow {
	return buildRowsRange(s, 0, len(s.Lines), split)
}

// buildRowsRange flattens the stream rows [from, to) into rendered rows.
//
// In split mode a run of deletions is paired with the run of additions that
// follows it, index for index, so the two sides stay level and your eye tracks
// straight across. The shorter run is padded with empty cells rather than
// letting the sides drift apart, which is the whole reason side-by-side is
// worth having.
func buildRowsRange(s review.Stream, from, to int, split bool) []dispRow {
	from, to = max(from, 0), min(to, len(s.Lines))
	if from >= to {
		return nil
	}
	rows := make([]dispRow, 0, to-from)
	if !split {
		for i := from; i < to; i++ {
			rows = append(rows, dispRow{left: i, right: -1, full: true})
		}
		return rows
	}

	i := from
	for i < to {
		switch s.Lines[i].Kind {
		case review.LineDel:
			delStart := i
			for i < to && s.Lines[i].Kind == review.LineDel {
				i++
			}
			addStart := i
			for i < to && s.Lines[i].Kind == review.LineAdd {
				i++
			}
			nDel, nAdd := addStart-delStart, i-addStart
			for k := range max(nDel, nAdd) {
				r := dispRow{left: -1, right: -1}
				if k < nDel {
					r.left = delStart + k
				}
				if k < nAdd {
					r.right = addStart + k
				}
				rows = append(rows, r)
			}

		case review.LineAdd:
			// An addition with no deletion before it: right side only.
			rows = append(rows, dispRow{left: -1, right: i})
			i++

		case review.LineContext:
			rows = append(rows, dispRow{left: i, right: i})
			i++

		default:
			// Headers, folds and notes span the whole panel.
			rows = append(rows, dispRow{left: i, right: -1, full: true})
			i++
		}
	}
	return rows
}

// rowOfStream maps a stream index back to the display row that shows it, or
// the nearest row before it when this layout does not show it.
func rowOfStream(rows []dispRow, streamIdx int) int {
	for i, r := range rows {
		if r.left == streamIdx || r.right == streamIdx {
			return i
		}
	}
	best := 0
	for i, r := range rows {
		if r.primary() <= streamIdx {
			best = i
		}
	}
	return best
}

// diffRenderOpts is what varies between surfaces drawing the same rows.
type diffRenderOpts struct {
	// cursor is the display row the keys act from, or -1 for none (a stop
	// card is read, not navigated row by row).
	cursor int
	// caret reserves a one-column caret gutter on the left of every row and
	// draws FocusCaret's ▸ in it on the cursor row.
	caret bool
	// query is the search needle to light up, or "".
	query string
	// expandHint is what the fold marker offers ("e expand"). Empty draws the
	// marker without a verb.
	expandHint string
}

// RenderHunk renders the stream rows [fromRow, toRow) of doc — typically a
// review.BindRange span — at exactly width columns per line, unified or side
// by side. No cursor, no caret: this is the stop card's read-only slice.
//
// Every returned line is exactly width cells wide, so a caller can box it
// without re-measuring.
func RenderHunk(doc *review.Doc, fromRow, toRow, width int, sideBySide bool) []string {
	if doc == nil || width <= 0 {
		return nil
	}
	rows := buildRowsRange(doc.Stream, fromRow, toRow, sideBySide)
	if len(rows) == 0 {
		return nil
	}
	return renderDiffRows(doc.Stream, rows, width, sideBySide,
		diffRenderOpts{cursor: -1, expandHint: "e more context"})
}

// renderDiffRows draws display rows at exactly width columns each.
func renderDiffRows(s review.Stream, rows []dispRow, width int, split bool, o diffRenderOpts) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = renderDiffRow(s, r, width, split, i == o.cursor, o)
	}
	return out
}

// renderDiffRow draws one display row at exactly width columns.
func renderDiffRow(s review.Stream, r dispRow, width int, split, onCursor bool, o diffRenderOpts) string {
	lead := ""
	if o.caret {
		lead = " "
		if onCursor {
			// The caret is drawn on EVERY row kind, including the ones that
			// carry no code. A cursor that vanishes on a file header is a
			// cursor you lose.
			lead = FocusCaret().Render("▸")
		}
		width--
	}
	if width <= 0 {
		return lead
	}

	if r.full || !split {
		l, ok := streamLine(s, r.primary())
		if !ok {
			return lead + fillRow(onCursor, width)
		}
		return lead + renderFullRow(l, width, onCursor, o)
	}

	// Side by side: two independent cells with a rule between them. The
	// highlight spans BOTH, because the cursor is on a row, not on a column.
	half := (width - 1) / 2
	rule := DimStyle.Render("│")
	if onCursor {
		rule = cursorFill(review.LineBlank).Render("│")
	}
	left := renderCell(s, r.left, half, onCursor, true, o.query)
	right := renderCell(s, r.right, width-1-half, onCursor, false, o.query)
	return lead + left + rule + right
}

func streamLine(s review.Stream, i int) (review.Line, bool) {
	if i < 0 || i >= len(s.Lines) {
		return review.Line{}, false
	}
	return s.Lines[i], true
}

func fillRow(onCursor bool, width int) string {
	blank := strings.Repeat(" ", max(width, 0))
	if onCursor {
		return DiffCursorRowStyle.Render(blank)
	}
	return blank
}

// cursorFill is the background for the row the keys act from.
//
// Derived from the row's own tint rather than replacing it: the row still has
// to say added or deleted while it says "you are here".
func cursorFill(kind review.LineKind) lipgloss.Style {
	switch kind {
	case review.LineAdd:
		return DiffCursorAddRowStyle
	case review.LineDel:
		return DiffCursorDelRowStyle
	}
	return DiffCursorRowStyle
}

// renderFullRow draws a row that spans the whole panel: a header, fold, note
// or blank — or, in unified mode, a line of code with both line numbers.
func renderFullRow(l review.Line, width int, onCursor bool, o diffRenderOpts) string {
	band := func(style lipgloss.Style, text string) string {
		return padStyled(style, " "+ansi.Truncate(text, max(width-1, 0), "…"), width)
	}
	switch l.Kind {
	case review.LineBlank:
		return fillRow(onCursor, width)
	case review.LineFileHeader:
		// A band rather than a plain row. It is the only thing separating two
		// files in one continuous stream, and a bold line of text scrolls past
		// looking like a comment about the code above it.
		return band(FileBandStyle, l.Text)
	case review.LineHunkHeader:
		if onCursor {
			return band(DiffCursorRowStyle.Foreground(ColorBlue), l.Text)
		}
		return band(DiffHunkStyle, l.Text)
	case review.LineNote:
		if onCursor {
			return band(DiffCursorRowStyle.Foreground(ColorTextDim), l.Text)
		}
		return band(DimStyle, l.Text)
	case review.LineExpand:
		base := DiffHunkStyle
		if onCursor {
			base = DiffCursorRowStyle
		}
		return renderExpandRow(l, width, base, o.expandHint)
	}
	return renderCodeLine(l, width, true, true, onCursor, o.query)
}

// renderCell draws one side of a side-by-side row. An index of -1 is the
// empty cell that keeps the two sides level.
func renderCell(s review.Stream, streamIdx, width int, onCursor, isLeft bool, query string) string {
	l, ok := streamLine(s, streamIdx)
	if !ok {
		return fillRow(onCursor, width)
	}
	return renderCodeLine(l, width, false, isLeft, onCursor, query)
}

// renderExpandRow draws a fold marker and says what opens it.
//
// The key is named on the row itself rather than only in the footer: a fold
// is the one control in a diff you can walk past without noticing, and a row
// that reads "⋯ 31 lines hidden" with no verb is a statement, not an offer.
func renderExpandRow(l review.Line, width int, base lipgloss.Style, hint string) string {
	body := "  " + l.Text
	if lipgloss.Width(body) > width {
		body = ansi.Truncate(body, width, "…")
	}
	if hint != "" {
		hint = "  " + hint + " "
	}
	pad := width - lipgloss.Width(body) - lipgloss.Width(hint)
	if pad < 1 {
		hint = ""
		pad = max(width-lipgloss.Width(body), 0)
	}
	return base.Foreground(ColorBlue).Render(body) +
		base.Render(strings.Repeat(" ", pad)) +
		base.Foreground(ColorAccent).Render(hint)
}

// renderCodeLine draws one line of code: gutter, sign, then the code itself,
// padded to exactly width.
//
// The gutter cell carries a saturated tint and the code area a quiet wash, so
// the change rail reads at a glance while the code stays legible. The
// foreground is never spent on add/delete — it belongs to syntax.
func renderCodeLine(l review.Line, width int, bothNums, isLeft, onCursor bool, query string) string {
	rowStyle := lipgloss.NewStyle()
	gutStyle := DiffNumStyle
	signStyle := DimStyle

	sign := " "
	switch l.Kind {
	case review.LineAdd:
		rowStyle, gutStyle, signStyle = DiffAddRowStyle, DiffAddGutterStyle, DiffAddSignStyle
		sign = "+"
	case review.LineDel:
		rowStyle, gutStyle, signStyle = DiffDelRowStyle, DiffDelGutterStyle, DiffDelSignStyle
		// ASCII, deliberately: U+2212 MINUS SIGN is East-Asian-Ambiguous and
		// some terminals give it two cells, which shears the whole gutter.
		sign = "-"
	}

	var gutter string
	switch {
	case bothNums:
		gutter = fmt.Sprintf("%4s %4s ", lineNum(l.Old), lineNum(l.New))
	case isLeft:
		gutter = fmt.Sprintf("%5s ", lineNum(l.Old))
	default:
		gutter = fmt.Sprintf("%5s ", lineNum(l.New))
	}
	// A cell too narrow for its own gutter keeps what fits rather than
	// overflowing into the neighbouring cell.
	if lipgloss.Width(gutter) > width {
		return gutStyle.Render(ansi.Truncate(gutter, width, ""))
	}
	gutW := lipgloss.Width(gutter)
	gutter = gutStyle.Render(gutter)

	if onCursor {
		// The gutter keeps its saturated block — it is the change rail, and the
		// cursor must not erase it — but everything else lifts.
		rowStyle = cursorFill(l.Kind)
		signStyle = rowStyle.Foreground(signColor(l.Kind)).Bold(true)
	}

	rest := width - gutW
	if rest < 2 {
		return gutter + rowStyle.Render(strings.Repeat(" ", rest))
	}
	code := renderCode(l, rest-2, onCursor, query)
	pad := max(rest-2-lipgloss.Width(code), 0)
	return gutter + signStyle.Render(sign) + rowStyle.Render(" ") + code +
		rowStyle.Render(strings.Repeat(" ", pad))
}

func signColor(kind review.LineKind) color.Color {
	switch kind {
	case review.LineAdd:
		return ColorGreen
	case review.LineDel:
		return ColorRed
	}
	return ColorTextDim
}

func lineNum(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// renderCode paints the code with syntax colours, brightening the runes that
// word-diff says actually changed and any that match query.
//
// Three segmentations over the same runes — tokens from the lexer, marks from
// the word diff, search hits — so it walks a per-rune class and coalesces
// equal neighbours. Done as runs rather than per rune because a row emitted one
// escape sequence per character is a row the terminal spends real time on,
// fifty times a frame.
func renderCode(l review.Line, width int, onCursor bool, query string) string {
	if width <= 0 {
		return ""
	}
	rs := []rune(l.Text)
	if len(rs) == 0 {
		return ""
	}

	class := make([]review.TokenClass, len(rs))
	at := 0
	for _, t := range l.Toks {
		for range []rune(t.Text) {
			if at < len(class) {
				class[at] = t.Class
				at++
			}
		}
	}
	marked := make([]bool, len(rs))
	for _, m := range l.Marks {
		for i := max(m.Start, 0); i < m.End && i < len(marked); i++ {
			marked[i] = true
		}
	}
	// Search hits outrank both: you asked for these by name.
	hit := make([]bool, len(rs))
	if query != "" {
		lower, needle := strings.ToLower(l.Text), strings.ToLower(query)
		for off := 0; off < len(lower); {
			j := strings.Index(lower[off:], needle)
			if j < 0 {
				break
			}
			start := len([]rune(lower[:off+j]))
			for i := start; i < start+len([]rune(needle)) && i < len(hit); i++ {
				hit[i] = true
			}
			off += j + len(needle)
		}
	}

	var b strings.Builder
	used := 0
	i := 0
	for i < len(rs) && used < width {
		j := i + 1
		for j < len(rs) && class[j] == class[i] && marked[j] == marked[i] && hit[j] == hit[i] {
			j++
		}
		text := strings.ReplaceAll(string(rs[i:j]), "\t", strings.Repeat(" ", diffTabWidth))
		if w := lipgloss.Width(text); used+w > width {
			text = ansi.Truncate(text, width-used, "…")
		}
		b.WriteString(codeRunStyle(l.Kind, class[i], marked[i], hit[i], onCursor).Render(text))
		used += lipgloss.Width(text)
		i = j
	}
	return b.String()
}

// wordRunStyle is the syntax style for a run sitting on a word-diff mark.
func wordRunStyle(c review.TokenClass) lipgloss.Style {
	switch c {
	case review.TokKeyword:
		return SynKeywordWordStyle
	case review.TokType:
		return SynTypeWordStyle
	case review.TokString:
		return SynStringWordStyle
	case review.TokNumber:
		return SynNumberWordStyle
	case review.TokComment:
		return SynCommentWordStyle
	case review.TokFunc:
		return SynFuncWordStyle
	}
	return SynPlainWordStyle
}

// codeRunStyle composes the two channels: background says what happened to the
// row, foreground says what the code is.
//
// Takes the row's KIND rather than its style, because a style carries no way to
// read a background back out — and a second table mapping styles to the colors
// they were built from is exactly how the two drift apart.
func codeRunStyle(kind review.LineKind, c review.TokenClass, marked, hit, onCursor bool) lipgloss.Style {
	if hit {
		// Search hits outrank both channels: a hit you cannot find on the row
		// is a search that did not work.
		return SearchHitStyle
	}

	s := SynPlainStyle
	switch c {
	case review.TokKeyword:
		s = SynKeywordStyle
	case review.TokType:
		s = SynTypeStyle
	case review.TokString:
		s = SynStringStyle
	case review.TokNumber:
		s = SynNumberStyle
	case review.TokComment:
		s = SynCommentStyle
	case review.TokFunc:
		s = SynFuncStyle
	}

	if marked {
		// A marked run swaps to the LIFTED syntax colour, because the word
		// background is carrying a third fact on top of the row's own and the
		// plain colours were picked against the quiet wash, not against a mark.
		// Keeping them made a comment on a changed word score a contrast ratio
		// of 1.13 against its background — invisible.
		switch kind {
		case review.LineAdd:
			return wordRunStyle(c).Background(ColorDiffAddWord)
		case review.LineDel:
			return wordRunStyle(c).Background(ColorDiffDelWord)
		}
	}
	if onCursor {
		switch kind {
		case review.LineAdd:
			return s.Background(ColorDiffCursorAddBg)
		case review.LineDel:
			return s.Background(ColorDiffCursorDelBg)
		}
		return s.Background(ColorDiffCursorBg)
	}
	switch kind {
	case review.LineAdd:
		return s.Background(ColorDiffAddBg)
	case review.LineDel:
		return s.Background(ColorDiffDelBg)
	}
	return s
}

// padStyled renders s in style and pads it, in the same style, to width — so
// a band's fill runs the full row rather than stopping where the text does.
func padStyled(style lipgloss.Style, s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		s += strings.Repeat(" ", width-w)
	} else if w > width {
		s = ansi.Truncate(s, width, "")
	}
	return style.Render(s)
}
