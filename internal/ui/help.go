package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"
)

// helpMaxCols caps the newspaper layout. Past four or five columns the eye
// stops tracking a row across the page, and the sheet has 51 bindings.
const helpMaxCols = 5

// helpMinDescW is the floor a truncated description keeps. Below this the row
// is unreadable anyway, and a terminal that narrow has already lost.
const helpMinDescW = 12

// HelpOverlay shows a keybindings cheat sheet: grouped into sections, and
// filterable as you type.
type HelpOverlay struct {
	visible bool
	width   int
	height  int
	scroll  int // top grid row when the sheet is taller than the screen
	filter  textinput.Model
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay() *HelpOverlay {
	ti := NewTextInput()
	ti.Placeholder = "type to filter"
	ti.SetWidth(28)
	ti.Focus()
	return &HelpOverlay{filter: ti}
}

func (h *HelpOverlay) Show() {
	h.visible = true
	h.scroll = 0
	h.filter.SetValue("")
	h.filter.Focus()
}
func (h *HelpOverlay) Hide()           { h.visible = false }
func (h *HelpOverlay) IsVisible() bool { return h.visible }

// SetSize records the terminal size and re-clamps the scroll offset, so it
// owns the scroll invariant on resize and View() can stay read-only.
func (h *HelpOverlay) SetSize(w, ht int) {
	h.width, h.height = w, ht
	h.clampScroll()
}

func (h *HelpOverlay) clampScroll() {
	h.scroll = clampInt(h.scroll, 0, h.layout().maxScroll)
}

// Update filters, scrolls, or closes.
//
// A printable key types rather than closing, which is a deliberate break with
// the sheet's old "press any key to close" contract: it holds 51 bindings, and
// on anything narrower than ~160 columns most of them are off-screen, so typing
// is the way through it. The cost is that j/k/g/G are literal characters now —
// scrolling lives on the arrows, PgUp/PgDn and Home/End. esc and ? close.
func (h *HelpOverlay) Update(msg tea.Msg) (*HelpOverlay, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		// Paste and the caret's blink tick both land here.
		var cmd tea.Cmd
		h.filter, cmd = h.filter.Update(msg)
		h.clampScroll()
		return h, cmd
	}

	lay := h.layout()
	switch key.String() {
	case "esc":
		// One press never discards both the filter and the sheet.
		if h.filter.Value() != "" {
			h.filter.SetValue("")
			h.scroll = 0
			return h, nil
		}
		h.Hide()
		return h, nil
	case "?", "ctrl+c":
		// ? is the key that opened it. ctrl+c closed it before this change too,
		// and swallowing it entirely would leave no way out of the sheet.
		h.Hide()
		return h, nil
	case "up":
		h.scroll--
	case "down":
		h.scroll++
	case "pgup":
		h.scroll -= lay.pageStep
	case "pgdown":
		h.scroll += lay.pageStep
	case "home":
		h.scroll = 0
	case "end":
		h.scroll = lay.maxScroll
	default:
		var cmd tea.Cmd
		h.filter, cmd = h.filter.Update(msg)
		// The result set just changed under the offset; anchoring to the top is
		// the only position that means the same thing for every query.
		h.scroll = 0
		h.clampScroll()
		return h, cmd
	}
	h.clampScroll()
	return h, nil
}

// helpRow is one rendered line of the grid: a binding or a section header.
type helpRow struct {
	Key     string
	Desc    string
	Section string
	Tag     string // section label folded onto the row, filtering only
	Header  bool
	KeyIdx  []int // matched rune indexes into Key, filtering only
	DescIdx []int // matched rune indexes into Desc, filtering only
}

func (r helpRow) binding() bool { return !r.Header }

// helpRows flows the bindings into one list of lines: section headers
// interleaved at rest, or a flat fuzzy result while filtering.
//
// Headers are dropped while filtering, following the command palette's
// rationale — grouping a scored result fragments a handful of rows across six
// headers. The section folds onto the row instead, and here that is
// load-bearing rather than decorative: `, Enter and PgUp/PgDn each appear in
// two sections meaning different things, so a filtered list without it shows
// duplicate keys that contradict each other.
func (h *HelpOverlay) helpRows() []helpRow {
	all := HelpOverlayBindings()
	query := strings.TrimSpace(h.filter.Value())

	if query == "" {
		counts := map[string]int{}
		for _, e := range all {
			counts[e.Section]++
		}
		rows := make([]helpRow, 0, len(all)+len(helpSections))
		prev := ""
		for _, e := range all {
			if e.Section != prev {
				// The count says how much sits under a header you are about to
				// scroll past — worth more here than in the palette, since below
				// ~120 columns the sheet is one column and most of it is off-screen.
				rows = append(rows, helpRow{
					Header:  true,
					Section: e.Section,
					Desc:    fmt.Sprintf("%s  %d", helpSectionLabel(e.Section), counts[e.Section]),
				})
				prev = e.Section
			}
			rows = append(rows, helpRow{Key: e.Key, Desc: e.Desc, Section: e.Section})
		}
		return rows
	}

	// Haystack is `Key + " " + Desc` so "ctrl" finds Ctrl+T and "snooze" finds z.
	haystacks := make([]string, len(all))
	for i, e := range all {
		haystacks[i] = e.Key + " " + e.Desc
	}
	var rows []helpRow
	for _, m := range fuzzy.Find(query, haystacks) {
		e := all[m.Index]
		// fuzzy reports byte offsets; every bound below counts runes, and the
		// arrows in `Shift+↑/↓` are three bytes each.
		idx := runeIndexes(haystacks[m.Index], m.MatchedIndexes)
		keyLen := runeLen(e.Key)
		rows = append(rows, helpRow{
			Key:     e.Key,
			Desc:    e.Desc,
			Section: e.Section,
			Tag:     helpSectionLabel(e.Section),
			KeyIdx:  filterShiftIndexes(idx, 0, keyLen, 0),
			DescIdx: filterShiftIndexes(idx, keyLen+1, keyLen+1+runeLen(e.Desc), keyLen+1),
		})
	}
	return rows
}

// helpChunk is one newspaper column, sized to its own contents.
type helpChunk struct {
	rows  []helpRow
	keyW  int
	descW int // only used while filtering, to line the section tags up
	w     int
}

// newHelpChunk measures a column. keyW is the true widest key in *this* column,
// never capped — the cell pads to it, so a capped value would overflow.
func newHelpChunk(rows []helpRow) helpChunk {
	ch := helpChunk{rows: rows}
	for _, r := range rows {
		if r.binding() {
			ch.keyW = max(ch.keyW, lipgloss.Width(r.Key))
			ch.descW = max(ch.descW, lipgloss.Width(r.Desc))
		}
	}
	for _, r := range rows {
		switch {
		case r.Header:
			ch.w = max(ch.w, lipgloss.Width(r.Desc))
		default:
			w := ch.keyW + 2 + lipgloss.Width(r.Desc)
			if r.Tag != "" {
				// Tags start at a common column, so they read as a column
				// rather than as a ragged suffix on each description.
				w = ch.keyW + 2 + ch.descW + 2 + lipgloss.Width(r.Tag)
			}
			ch.w = max(ch.w, w)
		}
	}
	return ch
}

// cell renders one line of this column.
func (ch helpChunk) cell(r int) string {
	if r < 0 || r >= len(ch.rows) {
		return ""
	}
	row := ch.rows[r]
	if row.Header {
		return PaletteSectionStyle.Render(row.Desc)
	}
	// Pad the raw text, then style — padding a styled string counts the ANSI
	// bytes and the columns come out ragged.
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	hit := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	cell := highlightWith(pad(row.Key, ch.keyW), row.KeyIdx, keyStyle, hit) +
		"  " + highlightMatches(row.Desc, row.DescIdx)
	if row.Tag != "" {
		cell += strings.Repeat(" ", max(0, ch.descW-lipgloss.Width(row.Desc)))
		cell += DimStyle.Render("  " + row.Tag)
	}
	return cell
}

// fitWidth shrinks the description column until the widest row fits w. Only
// the single-column layout needs it — a wider one is rejected by the budget
// and falls back — and there the alternative is View()'s MaxWidth cutting from
// the right, which takes the section tag first. The tag is the half that can't
// be re-derived: `Enter`, “ ` “ and `PgUp/PgDn` each appear in two sections,
// so a filtered row without it reads as one key contradicting itself.
func (ch helpChunk) fitWidth(w int) helpChunk {
	if ch.w <= w {
		return ch
	}
	tagW := 0
	for _, r := range ch.rows {
		if r.binding() && r.Tag != "" {
			tagW = max(tagW, 2+lipgloss.Width(r.Tag))
		}
	}
	descW := max(helpMinDescW, w-ch.keyW-2-tagW)
	if descW >= ch.descW {
		return ch // the key column alone overflows; nothing here can fix that
	}
	rows := append([]helpRow(nil), ch.rows...)
	for i, r := range rows {
		if !r.binding() || lipgloss.Width(r.Desc) <= descW {
			continue
		}
		rows[i].Desc = truncRunes(r.Desc, descW)
		// Highlights past the cut have nothing left to paint.
		rows[i].DescIdx = filterShiftIndexes(r.DescIdx, 0, runeLen(rows[i].Desc), 0)
	}
	return newHelpChunk(rows)
}

// helpLayout holds the geometry computed from the terminal size, shared by
// Update (clamping/paging) and View (rendering) so they can't disagree.
type helpLayout struct {
	rows        []helpRow
	chunks      []helpChunk
	gutter      int
	rowsPerCol  int
	visibleRows int // grid rows shown at once
	pageStep    int
	maxScroll   int
}

// layout lays the bindings out as newspaper-style columns sized to the
// terminal. Each column is measured against its own contents rather than
// against the widest binding in the table: one 46-character description used to
// set the width of every column, so two columns needed ~130 terminal columns
// and the sheet was a single scrolling strip nearly everywhere.
func (h *HelpOverlay) layout() helpLayout {
	rows := h.helpRows()

	const (
		gutter  = 2 // space between columns
		marginH = 2 // breathing room from the screen edges
		indRows = 2 // ⋮ above / ⋮ below lines reserved when scrolling
		// Non-grid lines View() always emits.
		titleLines  = 2 // title + blank
		searchLines = 2 // filter input + blank
		hintLines   = 2 // blank + hint
	)
	// Frame overhead (border + padding) is read from DialogStyle, so the scroll
	// math follows automatically if the dialog is ever restyled.
	frameV := DialogStyle.GetVerticalFrameSize()
	frameH := DialogStyle.GetHorizontalFrameSize()

	availH := max(1, h.height-frameV-titleLines-searchLines-hintLines)
	availW := max(1, h.width-frameH-marginH)

	// Fewest columns that fit both budgets. Ascending matters: a two-hit filter
	// laid out as two one-row columns is what a width-only search produces, and
	// it is what the old code's colsByHeight term prevented. When nothing fits
	// the height, the loop keeps the widest candidate that fits the width, since
	// more columns is less scrolling. One column is always allowed — the box has
	// a MaxWidth net, and refusing to render is not an option.
	chunks := []helpChunk{newHelpChunk(nil)}
	best := -1
	for c := 1; c <= min(helpMaxCols, max(1, len(rows))); c++ {
		cand := chunkHelpRows(rows, ceilDiv(len(rows), c))
		if c > 1 && chunksWidth(cand, gutter) > availW {
			continue
		}
		h := chunksHeight(cand)
		if best < 0 || h < best {
			chunks, best = cand, h
		}
		if h <= availH {
			break
		}
	}

	if len(chunks) == 1 {
		chunks[0] = chunks[0].fitWidth(availW)
	}

	rowsPerCol := chunksHeight(chunks)

	visibleRows, maxScroll := rowsPerCol, 0
	if rowsPerCol > availH { // can't fit even at max columns → scroll
		visibleRows = max(1, availH-indRows)
		maxScroll = rowsPerCol - visibleRows
	}

	return helpLayout{
		rows:        rows,
		chunks:      chunks,
		gutter:      gutter,
		rowsPerCol:  rowsPerCol,
		visibleRows: visibleRows,
		pageStep:    max(1, visibleRows-1),
		maxScroll:   maxScroll,
	}
}

// chunkHelpRows slices the flowed rows into columns of at most per lines each.
func chunkHelpRows(rows []helpRow, per int) []helpChunk {
	if per <= 0 {
		per = 1
	}
	var out []helpChunk
	for start := 0; start < len(rows); start += per {
		seg := append([]helpRow(nil), rows[start:min(start+per, len(rows))]...)
		if start > 0 {
			// A section split across a column boundary repeats its header, so
			// the continued half still says what it belongs to.
			if len(seg) > 0 && seg[0].binding() {
				seg = append([]helpRow{{
					Header:  true,
					Section: seg[0].Section,
					Desc:    helpSectionLabel(seg[0].Section) + " (cont.)",
				}}, seg...)
			}
		}
		out = append(out, newHelpChunk(seg))
	}
	if len(out) == 0 {
		out = []helpChunk{newHelpChunk(nil)}
	}
	return out
}

// chunksHeight is the tallest column, which is what the grid has to render.
func chunksHeight(chunks []helpChunk) int {
	h := 0
	for _, ch := range chunks {
		h = max(h, len(ch.rows))
	}
	return h
}

func chunksWidth(chunks []helpChunk, gutter int) int {
	if len(chunks) == 0 {
		return 0
	}
	total := gutter * (len(chunks) - 1)
	for _, ch := range chunks {
		total += ch.w
	}
	return total
}

// row renders one grid line: each column's cell joined by the gutter.
func (lay helpLayout) row(r int) string {
	var b strings.Builder
	for c, ch := range lay.chunks {
		if c > 0 {
			b.WriteString(strings.Repeat(" ", lay.gutter))
		}
		cell := ch.cell(r)
		if w := lipgloss.Width(cell); w < ch.w {
			cell += strings.Repeat(" ", ch.w-w)
		}
		b.WriteString(cell)
	}
	return strings.TrimRight(b.String(), " ")
}

// View renders the keybinding cheat sheet.
func (h *HelpOverlay) View() string {
	lay := h.layout()

	var lines []string
	lines = append(lines, TitleStyle.Render("Keybindings"), "")
	lines = append(lines, h.filter.View(), "")

	switch {
	case len(lay.rows) == 0:
		lines = append(lines, DimStyle.Render("No binding matches — esc to clear"))
	case lay.maxScroll == 0:
		for r := 0; r < lay.rowsPerCol; r++ {
			lines = append(lines, lay.row(r))
		}
	default:
		above, below := lay.hiddenCounts(h.scroll)
		if above > 0 {
			note := fmt.Sprintf("⋮ +%d above", above)
			// In one column the section header scrolls off and "(cont.)" never
			// fires, because there is no column boundary to fire it at — which
			// is every terminal under ~120 columns. Past one column the grid
			// holds several sections per row and no single name would be true.
			if sec := lay.sectionAt(h.scroll); sec != "" {
				note += " · " + sec
			}
			lines = append(lines, DimStyle.Render(note))
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

	hint := "esc close"
	if lay.maxScroll > 0 {
		hint = "↑↓ scroll · esc close"
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

// hiddenCounts returns how many *bindings* sit above and below the visible
// window. Header rows are excluded: they are structure, and counting them would
// make "⋮ +12 below" promise more keys than are actually down there.
func (lay helpLayout) hiddenCounts(scroll int) (above, below int) {
	end := scroll + lay.visibleRows
	for _, ch := range lay.chunks {
		for r, row := range ch.rows {
			if !row.binding() {
				continue
			}
			if r < scroll {
				above++
			} else if r >= end {
				below++
			}
		}
	}
	return
}

// sectionAt names the section the top visible row belongs to, for the scroll
// indicator. Single-column only: with several columns the top row spans several
// sections at once and naming one of them would be a claim about the others.
func (lay helpLayout) sectionAt(scroll int) string {
	if len(lay.chunks) != 1 {
		return ""
	}
	rows := lay.chunks[0].rows
	for r := min(scroll, len(rows)-1); r >= 0; r-- {
		if rows[r].Section != "" {
			return strings.ToLower(helpSectionLabel(rows[r].Section))
		}
	}
	return ""
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
