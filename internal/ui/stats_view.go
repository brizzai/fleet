package ui

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/stats"
)

// statsRangeMsg asks the wiring layer for the Report of a newly selected range.
// The view keeps rendering whatever it already holds for that range (or a
// "computing" line) until SetReport delivers it.
type statsRangeMsg struct{ Range stats.Range }

// statsOpenRecapMsg asks the wiring layer to open the weekly recap reel over
// the Stats screen (`w` inside it).
type statsOpenRecapMsg struct{}

// statsFooterPromise is the privacy line the screen always carries.
const statsFooterPromise = "computed here, never sent"

// StatsView is the full-screen "Stats" screen (`i` / Ctrl+K → "Your Stats").
//
// It is a pure renderer of stats.Report: every number is computed off the
// Update goroutine and delivered through SetReport. The range tabs are a MODE
// (ModeOn, never filled) — switching them does not move the keyboard, they ask
// for a different Report via statsRangeMsg.
type StatsView struct {
	visible       bool
	width, height int
	rng           stats.Range
	// reports caches the last Report per range, so flipping the tab back is
	// instant and a range still being computed can say so instead of blanking.
	reports     map[stats.Range]stats.Report
	progress    stats.Progress
	hasProgress bool
	scroll      int
	// covered is set while the recap reel sits on top, so exactly one accent
	// frame is on screen. hasRecap hides the `w` hint when last week's recap
	// has nothing to show (the key would only toast).
	covered, hasRecap bool
	// gen is bumped whenever the data behind the page changes; with the size
	// and range it keys the laid-out lines, which clampScroll and View would
	// otherwise rebuild (several full grid layouts) on every keypress.
	gen   int
	cache statsLinesCache
}

type statsLinesCache struct {
	ok        bool
	w, h, gen int
	rng       stats.Range
	lines     []string
	tabs      int // rows the range tabs take above the page (tabRows)
}

// NewStatsView creates the (hidden) Stats screen on the 7d range.
func NewStatsView() *StatsView {
	return &StatsView{rng: stats.Range7d, reports: map[stats.Range]stats.Report{}}
}

// Show opens the screen at the top. The range survives across opens.
func (v *StatsView) Show()           { v.visible = true; v.scroll = 0 }
func (v *StatsView) Hide()           { v.visible = false }
func (v *StatsView) IsVisible() bool { return v.visible }

// SetSize records the terminal size and re-clamps the scroll offset.
func (v *StatsView) SetSize(w, h int) {
	v.width, v.height = w, h
	v.clampScroll()
}

// SetCovered marks the screen as sitting under another panel (the recap reel).
func (v *StatsView) SetCovered(c bool) { v.covered = c }

// SetHasRecap says whether last week's recap has cards, for the `w` hint.
func (v *StatsView) SetHasRecap(ok bool) { v.hasRecap = ok }

// Range is the range the tabs currently select.
func (v *StatsView) Range() stats.Range { return v.rng }

// SetReport stores a Report under its own Range. A report for a range the tabs
// have since moved off is kept for when the user comes back to it.
func (v *StatsView) SetReport(r stats.Report) {
	if v.reports == nil {
		v.reports = map[stats.Range]stats.Report{}
	}
	v.reports[r.Range] = r
	v.gen++
	v.clampScroll()
}

// SetProgress feeds the first-open scan screen.
func (v *StatsView) SetProgress(p stats.Progress) {
	v.progress = p
	v.hasProgress = true
	v.gen++
	v.clampScroll()
}

// Update handles range cycling, scrolling and closing.
func (v *StatsView) Update(msg tea.Msg) (*StatsView, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}
	page := max(v.bodyHeight()-2, 1)
	switch key.String() {
	case "esc", "q", "i":
		v.Hide()
		return v, nil
	case "w":
		return v, func() tea.Msg { return statsOpenRecapMsg{} }
	case "[", "left", "h":
		return v, v.cycleRange(-1)
	case "]", "right", "l":
		return v, v.cycleRange(1)
	case "1", "2", "3":
		return v, v.pickRange(int(key.String()[0] - '1'))
	case "j", "down":
		v.scroll++
	case "k", "up":
		v.scroll--
	case "pgdown", "space", "ctrl+d":
		v.scroll += page
	case "pgup", "ctrl+u":
		v.scroll -= page
	case "home", "g":
		v.scroll = 0
	case "end", "G":
		v.scroll = math.MaxInt32
	}
	v.clampScroll()
	return v, nil
}

// cycleRange moves the tabs one step (wrapping) and asks for that range.
func (v *StatsView) cycleRange(dir int) tea.Cmd {
	idx := 0
	for i, r := range stats.Ranges {
		if r == v.rng {
			idx = i
		}
	}
	n := len(stats.Ranges)
	return v.pickRange(((idx+dir)%n + n) % n)
}

// pickRange selects the idx-th range (the `1` `2` `3` keys) and asks for its
// Report. The range already selected asks for nothing and keeps the scroll.
func (v *StatsView) pickRange(idx int) tea.Cmd {
	if idx < 0 || idx >= len(stats.Ranges) || stats.Ranges[idx] == v.rng {
		return nil
	}
	v.rng = stats.Ranges[idx]
	v.scroll = 0
	rng := v.rng
	return func() tea.Msg { return statsRangeMsg{Range: rng} }
}

func (v *StatsView) innerHeight() int { return max(v.height-2, 1) }

// tabRows is how many rows the pinned range tabs take above the page: the tab
// row, plus a blank row under it when the page fits without it scrolling —
// the row is air, and never worth shrinking the page for. The whole-screen
// scan and first-run states have no range to pick, so none.
func (v *StatsView) tabRows() int {
	v.contentLines()
	return v.cache.tabs
}

// bodyHeight is the scrolling page's height: the inner height less the tabs.
func (v *StatsView) bodyHeight() int { return max(v.innerHeight()-v.tabRows(), 1) }

// contentWidth is the usable width inside the border and its 1-column
// margins. Uncapped on purpose: a capped grid floated inside a full-width
// frame whose title and hints no longer shared its edges.
func (v *StatsView) contentWidth() int { return max(v.width-4, 1) }

func (v *StatsView) maxScroll() int {
	return max(len(v.contentLines())-v.bodyHeight(), 0)
}

func (v *StatsView) clampScroll() {
	v.scroll = clampInt(v.scroll, 0, v.maxScroll())
}

// statsScreen names which of the four whole-screen states is showing.
type statsScreen int

const (
	statsScreenReport statsScreen = iota
	statsScreenScan
	statsScreenFirstRun
	statsScreenLoading
)

func (v *StatsView) screen() (statsScreen, stats.Report) {
	r, ok := v.reports[v.rng]
	scanning := v.hasProgress && !v.progress.Finished
	switch {
	case scanning && (!ok || statsReportEmpty(r)):
		return statsScreenScan, r
	case !ok:
		return statsScreenLoading, r
	case statsReportEmpty(r):
		return statsScreenFirstRun, r
	}
	return statsScreenReport, r
}

// statsReportEmpty is "nothing worth a dashboard yet": no backfilled turn, no
// measured reply, and no day in the heatmap's year with a fleet action or agent
// time. Coverage can't answer it — status changes start the recording clock and
// loading the sidebar notes every session, both before the user has done
// anything. A range that is merely quiet on an install with history is NOT
// empty (the heatmap spans the year): it renders the normal screen with dashes.
func statsReportEmpty(r stats.Report) bool {
	if !r.Coverage.TurnsSince.IsZero() || r.Hero.AgentTime > 0 || r.Hero.ReplyN > 0 {
		return false
	}
	for _, d := range r.Days {
		if d.Actions > 0 || d.AgentTime > 0 {
			return false
		}
	}
	return true
}

func (v *StatsView) contentLines() []string {
	c := &v.cache
	if c.ok && c.w == v.width && c.h == v.height && c.gen == v.gen && c.rng == v.rng {
		return c.lines
	}
	tabs, inner := 0, v.innerHeight()
	switch scr, _ := v.screen(); scr {
	case statsScreenScan, statsScreenFirstRun:
	default:
		tabs = 2
	}
	lines := v.buildContentLines(max(inner-tabs, 1))
	if tabs == 2 && len(lines) > inner-tabs {
		tabs = 1
		lines = v.buildContentLines(max(inner-tabs, 1))
	}
	*c = statsLinesCache{ok: true, w: v.width, h: v.height, gen: v.gen, rng: v.rng, lines: lines, tabs: tabs}
	return lines
}

// buildContentLines lays the page out for a body bodyH rows tall.
func (v *StatsView) buildContentLines(bodyH int) []string {
	cw := v.contentWidth()
	scr, r := v.screen()
	switch scr {
	case statsScreenScan:
		return statsCenterBlock(statsScanLines(v.progress, cw), cw, bodyH)
	case statsScreenFirstRun:
		return statsCenterBlock(statsFirstRunLines(cw), cw, bodyH)
	case statsScreenLoading:
		return statsCenterBlock([]string{DimStyle.Render("computing your stats…")}, cw, bodyH)
	}
	availH := bodyH - 1 // a row of air above the bottom border
	var note []string
	if v.hasProgress && !v.progress.Finished {
		note = []string{"", DimStyle.Render(ansi.Truncate(fmt.Sprintf(
			"still reading %s / %s conversations — numbers fill in as it goes",
			statsInt(v.progress.Done), statsInt(v.progress.Total)), cw, "…"))}
	}
	return statsReportLines(r, cw, availH, note)
}

// View renders the full-screen panel: exactly width × height.
func (v *StatsView) View() string {
	w, h := v.width, v.height
	if w < 12 || h < 4 {
		return DimStyle.Render(ansi.Truncate("stats", max(w, 0), ""))
	}
	cw := v.contentWidth()
	lines := v.contentLines()
	bodyH := v.bodyHeight()
	scroll := clampInt(v.scroll, 0, max(len(lines)-bodyH, 0))
	end := min(scroll+bodyH, len(lines))
	var rows []string
	if n := v.tabRows(); n > 0 {
		rows = append(rows, v.rangeTabs(cw))
		for range n - 1 {
			rows = append(rows, "")
		}
	}
	for i := scroll; i < end; i++ {
		rows = append(rows, ansi.Truncate(lines[i], cw, ""))
	}
	var b strings.Builder
	for i, l := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteByte(' ')
		b.WriteString(l)
	}

	hints := "1 2 3 range · esc close"
	if v.hasRecap {
		hints = "1 2 3 range · w recap · esc close"
	}
	switch {
	case end < len(lines):
		hints = "↓ more · j/k scroll · " + hints
	case scroll > 0:
		hints = "↑ j/k scroll · " + hints
	}
	right := statsFooterPromise
	if lipgloss.Width(hints)+lipgloss.Width(right)+8 > w {
		right = "" // the keys outrank the promise when both can't fit
	}
	return RenderBorderedPanelInsets(b.String(), "Stats", "", hints, right, w, h, !v.covered)
}

// statsRangeTabLabel is a range as its tab spells it out.
func statsRangeTabLabel(r stats.Range) string {
	switch r {
	case stats.Range7d:
		return "7 days"
	case stats.Range30d:
		return "30 days"
	}
	return "all time"
}

// rangeTabs is the tab row pinned at the top of the page — "7 days   30 days
// all time · Sep 24 – Sep 30" — with the active range as a MODE (accent, bold,
// underlined, never filled) and the report's own window dim beside it. It
// used to be a "‹ 7d 30d all ›" chip inset in the frame's top-right border,
// where nobody noticed it (and a desktop notification could cover it).
func (v *StatsView) rangeTabs(cw int) string {
	parts := make([]string, 0, len(stats.Ranges))
	for _, r := range stats.Ranges {
		if r == v.rng {
			parts = append(parts, ModeOn().Render(statsRangeTabLabel(r)))
		} else {
			parts = append(parts, ModeOff().Render(statsRangeTabLabel(r)))
		}
	}
	row := " " + strings.Join(parts, "   ")
	// A range still computing says so in the page below, not here as well.
	if r, ok := v.reports[v.rng]; ok && !r.From.IsZero() && !r.To.IsZero() {
		row += DimStyle.Render("  · " + r.From.Local().Format("Jan 2") + " – " + r.To.Local().Format("Jan 2"))
	}
	return ansi.Truncate(row, cw, "")
}

// ---------------------------------------------------------------------------
// Whole-screen states
// ---------------------------------------------------------------------------

func statsScanLines(p stats.Progress, cw int) []string {
	txt := lipgloss.NewStyle().Foreground(ColorText)
	var lines []string
	if p.Total <= 0 {
		lines = append(lines, txt.Render("looking for your fleet conversations…"))
	} else {
		lines = append(lines, txt.Render(fmt.Sprintf("reading %s/%s conversations",
			statsInt(p.Done), statsInt(p.Total))))
		barW := clampInt(cw-12, 4, 40)
		frac := float64(p.Done) / float64(p.Total)
		lines = append(lines, "",
			DimStyle.Render("▕")+statsBar(frac, barW, lipgloss.NewStyle().Foreground(ColorText))+
				DimStyle.Render("▏ ")+DimStyle.Render(fmt.Sprintf("%3.0f%%", 100*math.Min(frac, 1))))
	}
	var sub []string
	if p.Tokens > 0 {
		sub = append(sub, statsTokens(p.Tokens)+" tokens")
	}
	if p.Turns > 0 {
		sub = append(sub, statsInt(p.Turns)+" turns")
	}
	if len(sub) > 0 {
		lines = append(lines, "", DimStyle.Render(strings.Join(sub, " · ")))
	}
	lines = append(lines, "", "",
		DimStyle.Render("only numbers are read — never your prompts or code"),
		DimStyle.Render("esc closes · the scan keeps going"))
	return lines
}

func statsFirstRunLines(cw int) []string {
	txt := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	body := "fleet keeps a private log of what happens here — how many agents " +
		"run at once, how long they wait on you, which keys you reach for. " +
		"Come back after a few sessions."
	lines := []string{txt.Render("◷ Stats start recording now."), ""}
	// Pad the paragraph to its own width so it centres as a block and keeps a
	// straight left edge, instead of centring each ragged line.
	para := strings.Split(ansi.Wrap(body, min(cw, 60), ""), "\n")
	paraW := 0
	for _, l := range para {
		paraW = max(paraW, lipgloss.Width(l))
	}
	for _, l := range para {
		lines = append(lines, DimStyle.Render(statsPadRight(l, paraW)))
	}
	lines = append(lines, "", DimStyle.Render("computed on this machine · never sent"))
	return lines
}

// statsCenterBlock centres a block of lines in a cw × h area (each line is
// centred on its own; the block as a whole sits at the vertical middle).
func statsCenterBlock(block []string, cw, h int) []string {
	top := max((h-len(block))/2, 0)
	out := make([]string, 0, top+len(block))
	for range top {
		out = append(out, "")
	}
	for _, l := range block {
		pad := max((cw-lipgloss.Width(l))/2, 0)
		out = append(out, strings.Repeat(" ", pad)+l)
	}
	return out
}

// ---------------------------------------------------------------------------
// The report: a grid of bordered cards
// ---------------------------------------------------------------------------

// statsChartMinRows / statsChartMaxRows bound the concurrency chart. Past ten
// rows the bars stop gaining resolution and turn into a wall of thin spikes, so
// rows a tall screen has left over are not spent on it.
const (
	statsChartMinRows = 4
	statsChartMaxRows = 10
)

// statsBigChartRows is the chart height the big hero figures must leave: two
// extra rows of digits are not worth a chart squeezed to its floor.
const statsBigChartRows = 6

// statsReportLines lays the report out as card rows that tile cw columns. It
// picks the richest style that fits availH — big hero figures and a row of
// padding inside every card, then padding alone, then neither — and grows the
// concurrency chart into the rows that leaves, up to statsChartMaxRows. What a
// tall screen still has spare is not stretched into empty cards: a row of air
// between the bands, the rest under the page (statsBalance). When even the
// plainest style overflows, the page scrolls.
func statsReportLines(r stats.Report, cw, availH int, note []string) []string {
	foot := statsFootnoteLines(r, cw)
	if len(foot) > 0 {
		foot = append([]string{""}, foot...)
	}
	foot = append(foot, note...) // the scan's progress line rides under the footnote
	_, hasChart := statsConcurrencyCard(r, statsChartMinRows)
	chart := func(st statsStyle, rows int) []string {
		if card, ok := statsConcurrencyCard(r, rows); ok {
			return statsGrid([]statsColumn{{card}}, cw, st.pad)
		}
		return nil
	}
	build := func(st statsStyle) (hero, lower []string, room int) {
		hero = statsHeroRows(r, cw, st)
		// Rows past what the full-height chart leaves shrink the chart;
		// rows past what its floor leaves scroll the page.
		rest := availH - len(hero) - len(foot)
		lower = statsBestGrid(statsLowerLayouts(r, cw), cw, st.pad,
			rest-len(chart(st, statsChartMaxRows)), rest-len(chart(st, statsChartMinRows)))
		room = availH - len(hero) - len(foot) - len(lower) - len(chart(st, statsChartMinRows))
		return hero, lower, room
	}
	for _, st := range []statsStyle{{pad: 1, big: true}, {pad: 1}, {pad: 0}} {
		hero, lower, room := build(st)
		if room < 0 || (st.big && hasChart && room < statsBigChartRows-statsChartMinRows) {
			continue
		}
		bands := [][]string{hero}
		if hasChart {
			bands = append(bands, chart(st, min(statsChartMinRows+room, statsChartMaxRows)))
		}
		return statsBalance(append(bands, lower), foot, availH)
	}
	hero, lower, _ := build(statsStyle{})
	return slices.Concat(hero, chart(statsStyle{}, statsChartMinRows), lower, foot)
}

// statsBalance lays the bands out in availH rows, anchored to the top: the
// page starts right under the range tabs, spare rows open at most one blank
// row between the bands, and whatever is left falls below the page. Centred,
// a short page floated in the middle of a tall screen with a band of empty
// rows between the tabs and the first card. The footnote stays attached one
// row under the grid: pinned to the bottom, it left a band of empty rows
// between the cards and their footnote that read as the page stretched
// around nothing.
func statsBalance(bands [][]string, foot []string, availH int) []string {
	spare := availH - len(foot)
	for _, b := range bands {
		spare -= len(b)
	}
	gap := 0
	if n := len(bands) - 1; n > 0 && spare >= n {
		gap = 1
	}
	out := make([]string, 0, availH)
	for i, b := range bands {
		if i > 0 {
			for range gap {
				out = append(out, "")
			}
		}
		out = append(out, b...)
	}
	out = append(out, foot...)
	for len(out) < availH {
		out = append(out, "")
	}
	return out
}

// statsStyle is how generously the report is drawn: pad rows inside every
// card, and whether the hero figures use the big three-row digits.
type statsStyle struct {
	pad int
	big bool
}

// statsCardSpec is one bordered card: a title inset in the top border, an
// optional dim note on its right, and a body rendered for the width it gets.
type statsCardSpec struct {
	title, right string
	// minW/maxW bound the card's OUTER width; maxW 0 means no cap. wantW is
	// the width it reads best at: a row's spare columns go first to cards
	// still short of it, then by weight. It never decides what fits in a row.
	minW, maxW, wantW, weight int
	// body renders the card's lines for an inner width (card width less the
	// border and a one-column margin each side).
	body func(w int) []string
	// fit, when set, re-renders the body for the rows the card was stretched
	// to, so a list shows more of itself rather than a band of blank rows.
	fit func(w, h int) []string
	// fullW is the outer width past which a bar card's extra columns only
	// stretch its bars (a 150-column bar chart of six counts); statsBestGrid
	// counts them as half-wasted. 0 means never.
	fullW int
}

// statsColumn is a stack of cards sharing one width; its cards share the
// stretch so every column in a grid row ends on the same line.
type statsColumn []statsCardSpec

const statsCardGap = 1 // columns between two cards in a row

// statsMaxBlankShare is the most of a card's body, in percent, that may be
// blank rows before statsGridWaste penalises the layout that left it so.
const statsMaxBlankShare = 30

// statsCard renders one card exactly w × h: rounded ColorBorder frame, title
// dim and bold in the top border, body inset one column (and pad rows).
func statsCard(c statsCardSpec, body []string, w, h, pad int) []string {
	inner := max(w-4, 1)
	lines := make([]string, 0, len(body)+pad)
	for range pad {
		lines = append(lines, "")
	}
	for _, l := range body {
		lines = append(lines, " "+ansi.Truncate(l, inner, ""))
	}
	title := PaletteSectionStyle.Render(c.title)
	right := ""
	if c.right != "" {
		right = DimStyle.Render(c.right)
	}
	return strings.Split(renderBorderedPanel(strings.Join(lines, "\n"), title, right, "", "", w, h, false), "\n")
}

// statsGrid packs columns into rows left to right — a column that doesn't fit
// beside the previous ones starts a new row — shares each row's spare width
// by weight, and pads every column of a row to the row's height.
func statsGrid(cols []statsColumn, cw, pad int) []string {
	lines, _ := statsGridWaste(cols, cw, pad)
	return lines
}

// statsBestGrid renders the arrangement that strands the fewest blank cells
// inside its cards. Height is nearly free within soft rows (a light tiebreak
// toward the squatter one), costs half a row of blank cells per line past it —
// each shortens the chart — and is dear past hard, where every line scrolls.
//
// A layout is a list of rows, each a list of columns; a row too wide for cw
// still wraps inside statsGrid.
func statsBestGrid(layouts [][][]statsColumn, cw, pad, soft, hard int) []string {
	var best []string
	bestCost := math.MaxInt
	for _, rows := range layouts {
		var lines []string
		waste := 0
		for _, cols := range rows {
			l, w := statsGridWaste(cols, cw, pad)
			lines, waste = append(lines, l...), waste+w
		}
		cost := waste + len(lines)*cw/32 + max(len(lines)-soft, 0)*cw/4 + max(len(lines)-hard, 0)*cw*2
		if cost < bestCost {
			best, bestCost = lines, cost
		}
	}
	return best
}

// statsGridWaste is statsGrid that also counts the blank body cells its cards
// were stretched with: rows under a short body and columns beside a narrow one.
func statsGridWaste(cols []statsColumn, cw, pad int) ([]string, int) {
	waste := 0
	type colDim struct{ minW, maxW, wantW, weight int }
	dims := make([]colDim, len(cols))
	for i, col := range cols {
		d := colDim{weight: 1}
		// A stack is capped only as wide as its widest card wants: capping it
		// at its narrowest squeezed a fun-facts card to the records beside it.
		uncapped := false
		for _, c := range col {
			d.minW = max(d.minW, c.minW)
			uncapped = uncapped || c.maxW == 0
			d.maxW = max(d.maxW, c.maxW)
			d.weight = max(d.weight, c.weight)
			d.wantW = max(d.wantW, c.wantW)
		}
		if uncapped {
			d.maxW = 0
		}
		d.minW = min(d.minW, cw)
		dims[i] = d
	}
	// Row breaks, greedily. A last row left holding one column alone steals
	// the previous row's last column when the two fit together: a lone card
	// stretched across the whole width (a 150-column bar chart of six
	// counts) reads worse than two balanced rows.
	var breaks []int // end index of each row
	for start := 0; start < len(cols); {
		end, used := start+1, dims[start].minW
		for end < len(cols) && used+statsCardGap+dims[end].minW <= cw {
			used += statsCardGap + dims[end].minW
			end++
		}
		breaks = append(breaks, end)
		start = end
	}
	if k := len(breaks); k >= 2 {
		prevStart := 0
		if k >= 3 {
			prevStart = breaks[k-3]
		}
		last := len(cols) - 1
		if breaks[k-1]-breaks[k-2] == 1 && breaks[k-2]-prevStart >= 3 &&
			dims[last-1].minW+statsCardGap+dims[last].minW <= cw {
			breaks[k-2]--
		}
	}
	var out []string
	for start, bi := 0, 0; start < len(cols); bi++ {
		end, used := breaks[bi], -statsCardGap
		for i := start; i < end; i++ {
			used += statsCardGap + dims[i].minW
		}
		n := end - start
		widths := make([]int, n)
		spare := cw - used
		for i := range n {
			widths[i] = dims[start+i].minW
		}
		// A lone column fills the row whatever its cap: a row that stops short
		// of the others reads as a mistake.
		capped := func(i int) bool {
			return n > 1 && dims[start+i].maxW > 0 && widths[i] >= dims[start+i].maxW
		}
		for i := range n {
			if !capped(i) {
				want := dims[start+i].wantW
				if m := dims[start+i].maxW; m > 0 {
					want = min(want, m)
				}
				give := min(max(want-widths[i], 0), spare)
				widths[i] += give
				spare -= give
			}
		}
		for spare > 0 {
			best := -1
			for i := range n {
				if capped(i) {
					continue
				}
				// Give the next column to whoever has received least per weight.
				if best < 0 || (widths[i]-dims[start+i].minW)*dims[start+best].weight <
					(widths[best]-dims[start+best].minW)*dims[start+i].weight {
					best = i
				}
			}
			if best < 0 {
				widths[n-1] += spare // everything capped: the last card absorbs it
				break
			}
			widths[best]++
			spare--
		}

		// Render bodies, then stretch each column's last card to the row height.
		bodies := make([][][]string, n)
		heights := make([]int, n)
		rowH := 0
		for i := range n {
			col := cols[start+i]
			bodies[i] = make([][]string, len(col))
			for k, c := range col {
				bodies[i][k] = c.body(max(widths[i]-4, 1))
				heights[i] += len(bodies[i][k]) + 2 + 2*pad
			}
			rowH = max(rowH, heights[i])
		}
		block := make([][]string, n)
		for i := range n {
			col := cols[start+i]
			// A shorter column's slack is shared evenly by its cards: all of it
			// in the last one left a box with two lines and five blank rows.
			slack, nc := rowH-heights[i], len(col)
			for k, c := range col {
				h := len(bodies[i][k]) + 2 + 2*pad + slack/nc
				if k >= nc-slack%nc {
					h++
				}
				body := bodies[i][k]
				if c.fit != nil {
					body = c.fit(max(widths[i]-4, 1), h-2-2*pad)
				}
				// Blank rows under the body, and blank columns beside it.
				innerH, innerW := h-2-2*pad, max(widths[i]-4, 1)
				blankRows := max(innerH-len(body), 0)
				waste += blankRows*innerW +
					max(widths[i]-4-statsNaturalW(body), 0)*len(body)
				// A card left mostly empty is the look to avoid, whatever the
				// total says: blank rows past statsMaxBlankShare of its body cost
				// double again, so one hollow card outweighs a little waste
				// spread across several.
				if excess := blankRows - innerH*statsMaxBlankShare/100; excess > 0 {
					waste += 2 * excess * innerW
				}
				if c.fullW > 0 {
					waste += max(widths[i]-c.fullW, 0) * len(body) / 2
				}
				block[i] = append(block[i], statsCard(c, body, widths[i], h, pad)...)
			}
		}
		for y := range rowH {
			var b strings.Builder
			for i := range n {
				if i > 0 {
					b.WriteString(strings.Repeat(" ", statsCardGap))
				}
				b.WriteString(block[i][y])
			}
			out = append(out, b.String())
		}
		start = end
	}
	return out, waste
}

// --- hero -------------------------------------------------------------------

type statsTile struct {
	value, unit, label, sub string
	short                   string // sub for a narrow tile; "" = sub
	tag                     string // label for the compact one-row strip
}

func statsHeroTiles(r stats.Report) []statsTile {
	h := r.Hero
	var tiles []statsTile

	par := statsTile{value: "—", label: "in parallel", tag: "parallel"}
	if h.Parallel > 0 {
		par.value, par.unit = strconv.FormatFloat(h.Parallel, 'f', 1, 64), "×"
		if h.ParallelPrev > 0 {
			par.sub = statsArrow(h.Parallel-h.ParallelPrev) + " from " +
				strconv.FormatFloat(h.ParallelPrev, 'f', 1, 64) + "×"
		}
	}
	tiles = append(tiles, par)

	peak := statsTile{value: "—", label: "peak at once", tag: "peak"}
	if h.Peak > 0 {
		peak.value = strconv.Itoa(h.Peak)
		if !h.PeakAt.IsZero() {
			peak.sub = statsWhen(h.PeakAt, r.Range)
		}
	}
	tiles = append(tiles, peak)

	at := statsTile{value: "—", label: "agent time", tag: "agent time"}
	if h.AgentTime > 0 {
		at.value = statsDur(h.AgentTime)
		if h.AgentTimePrev > 0 {
			pct := 100 * (float64(h.AgentTime) - float64(h.AgentTimePrev)) / float64(h.AgentTimePrev)
			at.sub = fmt.Sprintf("%s %.0f%% vs prev", statsArrow(pct), math.Abs(pct))
			at.short = fmt.Sprintf("%s %.0f%%", statsArrow(pct), math.Abs(pct))
		}
	}
	tiles = append(tiles, at)

	reply := statsTile{value: "—", label: "median reply", tag: "reply"}
	if h.ReplyN >= stats.MinSamples && h.ReplyMedian > 0 {
		reply.value = statsDur(h.ReplyMedian)
		if h.ReplyP90 > 0 {
			reply.sub = "p90 " + statsDur(h.ReplyP90)
		}
	} else if h.ReplyN > 0 {
		// Not "3 of 5 replies", which reads as a result rather than a wait.
		reply.sub = fmt.Sprintf("needs %d replies · %d so far", stats.MinSamples, h.ReplyN)
		reply.short = fmt.Sprintf("after %d replies", stats.MinSamples)
	}
	tiles = append(tiles, reply)

	cost := statsTile{value: "—", label: "at API prices", tag: "cost"}
	if h.CostUSD > 0 {
		cost.value = statsMoney(h.CostUSD)
		if h.Tokens > 0 {
			cost.sub = statsTokens(h.Tokens) + " tokens"
		}
	}
	tiles = append(tiles, cost)
	return tiles
}

// statsHeroTileMin is the narrowest a hero tile gets in the full layout.
const statsHeroTileMin = 18

// statsHeroRows renders the big-number tiles. All five sit in one row when
// they fit at statsHeroTileMin; below that they become a compact strip —
// value only, short labels, still one row — rather than a 3 + 2 block that
// spent a third of an 80×24 screen on five numbers with ragged edges. Only
// when even the strip can't fit do they wrap.
func statsHeroRows(r stats.Report, cw int, st statsStyle) []string {
	tiles := statsHeroTiles(r)
	n := len(tiles)
	// A tile is never narrower than its label in the border needs, or the
	// border truncates it ("at API price").
	tileMin := func(t statsTile) int { return max(statsHeroTileMin, len(t.label)+6) }
	oneRow := (n - 1) * statsCardGap
	for _, t := range tiles {
		oneRow += tileMin(t)
	}
	if oneRow > cw {
		if cols, ok := statsHeroStrip(tiles, cw); ok {
			return statsGrid(cols, cw, st.pad)
		}
	}
	per := n
	if oneRow > cw {
		per = clampInt((cw+statsCardGap)/(statsHeroTileMin+1+statsCardGap), 1, n)
	}
	nRows := (n + per - 1) / per
	var out []string
	idx := 0
	for row := range nRows {
		k := (n - idx) / (nRows - row)
		if (n-idx)%(nRows-row) != 0 {
			k++
		}
		// Big digits only when every tile in the row takes them, so the row
		// never mixes a three-row figure with a one-line one.
		inner := (cw-(k-1)*statsCardGap)/k - 4
		big, anyNum := st.big, false
		for _, t := range tiles[idx : idx+k] {
			fig, ok := statsBigFigure(t.value + t.unit)
			big = big && ok && lipgloss.Width(fig[0]) <= inner
			anyNum = anyNum || t.value != "—"
		}
		big = big && anyNum // a row of dashes has no headline to blow up
		var cols []statsColumn
		for _, t := range tiles[idx : idx+k] {
			cols = append(cols, statsColumn{{
				title: t.label, minW: min(tileMin(t), cw), weight: 1,
				body: func(w int) []string {
					sub := t.sub
					if t.short != "" && lipgloss.Width(sub) > w {
						sub = t.short
					}
					sub = DimStyle.Render(ansi.Truncate(sub, w, "…"))
					// Centred in the tile: hugging the left edge of a wide tile
					// left most of it blank on a big screen.
					lines := []string{statsFigure(t.value + t.unit), sub}
					if big {
						fig, _ := statsBigFigure(t.value + t.unit)
						lines = append(fig, sub)
					}
					for i, l := range lines {
						lines[i] = recapCenter(l, w)
					}
					return lines
				},
			}})
		}
		out = append(out, statsGrid(cols, cw, st.pad)...)
		idx += k
	}
	return out
}

// statsHeroStrip is the compact hero row: each tile just wide enough for its
// short label in the border and its value, the spare shared out evenly.
func statsHeroStrip(tiles []statsTile, cw int) ([]statsColumn, bool) {
	var cols []statsColumn
	used := -statsCardGap
	for _, t := range tiles {
		minW := max(len(t.tag)+6, lipgloss.Width(t.value+t.unit)+4, 10)
		used += minW + statsCardGap
		cols = append(cols, statsColumn{{
			title: t.tag, minW: minW, weight: 1,
			body: func(int) []string { return []string{statsFigure(t.value + t.unit)} },
		}})
	}
	return cols, used <= cw
}

// statsFigure renders a headline number: digits (and the separators inside a
// number) bold in the accent — the top of the data ramp, so the hero figures
// are the only accent-coloured numbers on the page — and units and symbols
// ("h", "m", "×", "$") dim, so the eye lands on the magnitude.
func statsFigure(s string) string {
	num := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	var b strings.Builder
	for _, run := range statsFigureRuns(s) {
		if run.num {
			b.WriteString(num.Render(run.s))
		} else {
			b.WriteString(DimStyle.Render(run.s))
		}
	}
	return b.String()
}

type statsFigureRun struct {
	s   string
	num bool
}

// statsFigureRuns splits a figure into number and unit runs. A '.' or ','
// between two digits belongs to the number; a space never starts a new run.
func statsFigureRuns(s string) []statsFigureRun {
	var out []statsFigureRun
	var run strings.Builder
	runNum := false
	flush := func() {
		if run.Len() > 0 {
			out = append(out, statsFigureRun{run.String(), runNum})
			run.Reset()
		}
	}
	rs := []rune(s)
	isDigit := func(i int) bool { return i >= 0 && i < len(rs) && rs[i] >= '0' && rs[i] <= '9' }
	for i, c := range rs {
		isNum := isDigit(i) || ((c == '.' || c == ',') && isDigit(i-1) && isDigit(i+1))
		if c == ' ' {
			isNum = runNum
		}
		if isNum != runNum {
			flush()
			runNum = isNum
		}
		run.WriteRune(c)
	}
	flush()
	return out
}

// statsBigGlyphs is a small pixel font for the big figures, drawn two pixel
// rows per terminal row with the half blocks ▀ ▄ █ — solid cells a terminal
// draws seamlessly, unlike box-drawing strokes or braille, which Ghostty
// renders as separate dots. Six pixel rows make three terminal rows: digits
// stand on rows 0–4, so their baseline sits in the top half of the third
// row. There is no ',' (a one-pixel comma beside the digits read as a
// decimal point) and no '$' (drawn in six pixel rows it was a blob):
// statsBigFigure drops the one and sets the other small, as a superscript.
//
// Units are glyphs too, at the same scale: a unit drawn as an ordinary letter
// beside three-row digits shrank to a subscript, and "21h 00m" read as "2100".
// They are shaped to never pass for a digit — 's' is the round S, not the
// square 5 — and 'm' and '×' sit at x-height, below the digits' tops.
var statsBigGlyphs = map[rune][]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {"##.", ".#.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	'.': {".", ".", ".", ".", "#"},
	'h': {"#..", "#..", "###", "#.#", "#.#"},
	'm': {".....", ".....", "####.", "#.#.#", "#.#.#"},
	's': {".##", "#..", ".#.", "..#", "##."},
	'×': {"...", "...", "#.#", ".#.", "#.#"},
}

// statsBigFigure renders s three rows tall in statsBigGlyphs: the number in
// the accent, its units in the dim tone so the eye lands on the magnitude.
// Glyphs sit one column apart; a space in s opens a three-column gap, so
// "21h 00m" reads as two groups rather than one long number. A thousands
// comma is dropped ("$1,412" draws as 1412 — five digits need no separator,
// and a big comma read as a decimal point), and '$' is an ordinary dim
// character on the top row: a superscript reads as a price where a six-pixel
// '$' read as a blob. Every row is the same width. ok is false when s holds
// a character the font lacks — the caller then shows the ordinary one-line
// figure.
func statsBigFigure(s string) (rows []string, ok bool) {
	if s == "—" { // a missing value is not a headline: a plain dash, centred
		return []string{"", DimStyle.Render(s), ""}, true
	}
	num := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	var out [3]strings.Builder
	var cells [3]strings.Builder
	curNum, first, gap := false, true, 0
	flush := func() {
		st := DimStyle
		if curNum {
			st = num
		}
		for i := range out {
			if cells[i].Len() > 0 {
				out[i].WriteString(st.Render(cells[i].String()))
				cells[i].Reset()
			}
		}
	}
	blank := func(n int) {
		for i := range cells {
			cells[i].WriteString(strings.Repeat(" ", n))
		}
	}
	for _, run := range statsFigureRuns(s) {
		for _, c := range run.s {
			if c == ' ' {
				gap = 2
				continue
			}
			if c == ',' { // five digits need no separator; a big comma reads as '.'
				continue
			}
			if c == '$' { // a superscript currency sign, as a price tag sets it
				if run.num != curNum {
					flush()
					curNum = run.num
				}
				cells[0].WriteString("$")
				cells[1].WriteString(" ")
				cells[2].WriteString(" ")
				first = false
				continue
			}
			g, has := statsBigGlyphs[c]
			if !has {
				return nil, false
			}
			if !first {
				blank(1 + gap) // spaces stay in the current style: they carry no colour
			}
			first, gap = false, 0
			if run.num != curNum {
				flush()
				curNum = run.num
			}
			for x := range len(g[0]) {
				for row := range 3 {
					top := 2*row < len(g) && g[2*row][x] == '#'
					bot := 2*row+1 < len(g) && g[2*row+1][x] == '#'
					switch {
					case top && bot:
						cells[row].WriteString("█")
					case top:
						cells[row].WriteString("▀")
					case bot:
						cells[row].WriteString("▄")
					default:
						cells[row].WriteString(" ")
					}
				}
			}
		}
	}
	flush()
	return []string{out[0].String(), out[1].String(), out[2].String()}, true
}

// statsArrow is a plain ▲/▼ — never green/red: colour means status in fleet,
// and "more agent time" is not a verdict.
func statsArrow(delta float64) string {
	switch {
	case delta > 0:
		return "▲"
	case delta < 0:
		return "▼"
	}
	return "="
}

// --- concurrency --------------------------------------------------------------

// statsConcShade is the ramp step for a concurrency level: one shade per
// "how many at once", so the chart's bands and the share bar under it use the
// same colour for the same thing. The three classes sit far apart on the ramp
// (solo, 2, 3+ — the share bar's own split) and the accent is reserved for
// the most parallel moments; a fourth class for 4+ was a near-identical
// neighbour that flattened the top of the chart into one block.
func statsConcShade(v float64) int {
	switch c := int(math.Ceil(v - 1e-9)); {
	case c <= 1:
		return 2
	case c == 2:
		return 4
	}
	return statsRampN - 1
}

// statsConcurrencyCard plots each bucket's PEAK: how many sessions were busy
// at once, the question this card and the hero's peak answer. A bucket's
// time-weighted mean sits well under 1 at 30d/all, which pressed the area
// against the floor and never reached the peak the hero reports.
func statsConcurrencyCard(r stats.Report, rows int) (statsCardSpec, bool) {
	peak := 0
	vals := make([]float64, len(r.ConcurrencyPeak))
	for i, c := range r.ConcurrencyPeak {
		peak = max(peak, c)
		vals[i] = float64(c)
	}
	if peak == 0 {
		return statsCardSpec{}, false
	}
	return statsCardSpec{
		title: "concurrency", right: "sessions busy at once", minW: 24, weight: 1,
		body: func(w int) []string {
			lw := len(strconv.Itoa(peak))
			// The plot stops a column short of the card's text edge: the bar for
			// the hour in progress otherwise ran up against the right border.
			chartW := max(w-lw-2, 4)
			unit := float64(rows) / float64(peak) // rows per level
			// Gridlines only where a level spans enough rows to read as a
			// band; tighter, they are just noise between the bars.
			var grid []float64
			if unit >= 3 {
				for l := 1; l < peak; l++ {
					grid = append(grid, float64(l))
				}
			}
			// Every level labelled when each gets two rows, else the peak,
			// the middle and 1.
			levels := []int{peak, (peak + 1) / 2, 1}
			if unit >= 2 {
				levels = levels[:0]
				for l := peak; l >= 1; l-- {
					levels = append(levels, l)
				}
			}
			labels := map[int]string{}
			for _, l := range levels {
				row := rows - int(math.Ceil(float64(l)*unit-1e-9))
				if _, taken := labels[row]; !taken && row >= 0 && row < rows {
					labels[row] = strconv.Itoa(l)
				}
			}
			chart := statsBlockChart(vals, chartW, rows, float64(peak), grid, statsConcShade)
			out := make([]string, 0, rows+5)
			for i, line := range chart {
				out = append(out, DimStyle.Render(fmt.Sprintf("%*s ", lw, labels[i]))+line)
			}
			indent := strings.Repeat(" ", lw+1)
			out = append(out, indent+lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", chartW)))
			// Ticks span the plotted slots only, past the chart's left padding.
			s, k, pad := statsChartSlots(len(vals), chartW)
			if ticks := statsTimeTicks(r.From, r.To, s*k); strings.TrimSpace(ticks) != "" {
				out = append(out, indent+strings.Repeat(" ", pad)+DimStyle.Render(ticks))
			}
			if share := statsShareLines(r, w, lw+1); len(share) > 0 {
				out = append(out, "")
				out = append(out, share...)
			}
			return out
		},
	}, true
}

// statsShareLines is one line under the chart: a caption saying what is
// measured, a short bar of busy time split solo / 2 / 3+ in the chart's own
// shades, and the shares named in those same shades, which is what ties each
// segment to its label. It used to be a full-width bar with a swatch legend
// and no caption; it read as an unexplained stripe, and at full width it
// passed for part of the chart's axis. indent lines it up with the plot.
func statsShareLines(r stats.Report, w, indent int) []string {
	total := r.ShareSolo + r.ShareTwo + r.ShareThreePlus
	if total <= 0 {
		return nil
	}
	segs := []struct {
		label string
		v     float64
		shade int
	}{
		{"solo", r.ShareSolo, statsConcShade(1)},
		{"2 at once", r.ShareTwo, statsConcShade(2)},
		{"3+ at once", r.ShareThreePlus, statsConcShade(3)},
	}
	var parts []string
	for _, s := range segs {
		if s.v <= 0 {
			continue
		}
		parts = append(parts, StatsRamp[s.shade].Render(s.label)+" "+
			lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("%.0f%%", 100*s.v/total)))
	}
	legend := strings.Join(parts, DimStyle.Render(" · "))
	caption := DimStyle.Render("when agents were busy  ")
	room := w - indent - lipgloss.Width(caption) - lipgloss.Width(legend) - 2
	barW := min(room, 40)
	if barW < 10 {
		// Too narrow for a bar: the named shares say it on their own.
		return []string{strings.Repeat(" ", indent) + ansi.Truncate(caption+legend, max(w-indent, 1), "…")}
	}

	// Whole cells per segment, largest remainder; a non-zero share keeps at
	// least one cell so a 3% sliver still shows.
	cells := make([]int, len(segs))
	used := 0
	for i, s := range segs {
		cells[i] = int(s.v / total * float64(barW))
		if s.v > 0 && cells[i] == 0 {
			cells[i] = 1
		}
		used += cells[i]
	}
	for used < barW {
		best := 0
		for i, s := range segs {
			if s.v/total*float64(barW)-float64(cells[i]) > segs[best].v/total*float64(barW)-float64(cells[best]) {
				best = i
			}
		}
		cells[best]++
		used++
	}
	for used > barW {
		big := 0
		for i := range cells {
			if cells[i] > cells[big] {
				big = i
			}
		}
		cells[big]--
		used--
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(caption)
	for i, s := range segs {
		b.WriteString(StatsRamp[s.shade].Render(strings.Repeat("█", cells[i])))
	}
	b.WriteString("  ")
	b.WriteString(legend)
	return []string{b.String()}
}

// statsChartSlots lays n samples onto w columns as equal-width slots: k
// columns per slot, s slots, and pad blank columns on the left. A column
// that covered one sample beside one covering two drew a lone burst as a
// 1-column sliver and split a solid one with 1-column gaps, which read as
// rendering glitches; with n < 2w every slot is therefore at least two
// columns wide, and each plots the peak of the samples it covers.
func statsChartSlots(n, w int) (s, k, pad int) {
	if n <= 0 || w <= 0 {
		return max(w, 0), 1, 0
	}
	k = 1
	if n < 2*w {
		k = min(max(2, w/n), w)
	}
	s = w / k
	return s, k, w - s*k
}

// statsBlockChart draws vals as solid block columns, w cells × rows cells:
// full blocks below, an eighth-block partial on top, so any row count steps
// cleanly. Samples are grouped into equal-width slots (statsChartSlots), each
// plotting the PEAK it covers so a one-hour burst survives resampling. shade
// maps a value to a StatsRamp step: a full cell takes the value at its middle,
// so a band boundary that falls inside a cell goes to the level filling most
// of it. A faint ┈ marks each grid level wherever no bar covers it.
func statsBlockChart(vals []float64, w, rows int, ymax float64, grid []float64, shade func(v float64) int) []string {
	if w < 1 || rows < 1 || ymax <= 0 {
		return nil
	}
	s, k, pad := statsChartSlots(len(vals), w)
	unit := float64(rows) / ymax // rows per unit of value
	eighths := make([]int, w)
	colV := make([]float64, w)
	for slot := range s {
		v := statsResampleMax(vals, slot, s)
		e := int(math.Round(v * unit * 8))
		if v > 0 && e == 0 {
			e = 1 // presence beats precision: a trickle is still activity
		}
		for x := pad + slot*k; x < pad+(slot+1)*k; x++ {
			colV[x] = v
			eighths[x] = min(e, rows*8)
		}
	}
	gridRow := map[int]bool{} // counted from the bottom
	for _, g := range grid {
		if g > 0 && g < ymax {
			gridRow[int(math.Ceil(g*unit-1e-9))-1] = true
		}
	}
	partials := [8]string{"", "▁", "▂", "▃", "▄", "▅", "▆", "▇"}
	const blank, faint = -2, -1
	out := make([]string, rows)
	for i := range rows {
		r := rows - 1 - i
		var b, run strings.Builder
		runKey := blank
		flush := func() {
			switch {
			case run.Len() == 0:
			case runKey == blank:
				b.WriteString(run.String())
			case runKey == faint:
				b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(run.String()))
			default:
				b.WriteString(StatsRamp[runKey].Render(run.String()))
			}
			run.Reset()
		}
		for x := range w {
			cell := clampInt(eighths[x]-r*8, 0, 8)
			key, ch := blank, " "
			switch {
			case cell == 8:
				key, ch = shade(math.Min((float64(r)+0.5)/unit, colV[x])), "█"
			case cell > 0:
				key, ch = shade(colV[x]), partials[cell]
			case gridRow[r] && x >= pad:
				key, ch = faint, "┈"
			}
			if key != runKey {
				flush()
				runKey = key
			}
			run.WriteString(ch)
		}
		flush()
		out[i] = b.String()
	}
	return out
}

// statsResampleMax returns the largest source sample that slot x of n
// covers (the nearest sample when the source is sparser than the slots).
func statsResampleMax(vals []float64, x, n int) float64 {
	if len(vals) == 0 || n <= 0 {
		return 0
	}
	lo := x * len(vals) / n
	hi := (x + 1) * len(vals) / n
	if hi <= lo {
		return vals[min(lo, len(vals)-1)]
	}
	m := vals[lo]
	for _, v := range vals[lo+1 : hi] {
		m = max(m, v)
	}
	return m
}

// statsTimeTicks places time labels along a w-wide axis spanning [from, to):
// hours for a day, weekdays for a week, Mondays for a month, months beyond.
func statsTimeTicks(from, to time.Time, w int) string {
	if from.IsZero() || !to.After(from) || w < 4 {
		return ""
	}
	from, to = from.Local(), to.Local()
	span := to.Sub(from)
	type tick struct {
		at    time.Time
		label string
	}
	var ticks []tick
	centre := false
	midnight := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()) }
	switch {
	case span <= 36*time.Hour:
		step := 3 * time.Hour
		if w < 60 {
			step = 6 * time.Hour
		}
		for t := from.Truncate(time.Hour); t.Before(to); t = t.Add(time.Hour) {
			if t.Before(from) || t.Hour()%int(step.Hours()) != 0 {
				continue
			}
			ticks = append(ticks, tick{t, t.Format("15:04")})
		}
	case span <= 10*24*time.Hour:
		// Weekday names sit under the middle of their day, not its first hour.
		for t := midnight(from).Add(12 * time.Hour); t.Before(to); t = t.AddDate(0, 0, 1) {
			if !t.Before(from) {
				ticks = append(ticks, tick{t, t.Format("Mon")})
			}
		}
		centre = true
	case span <= 62*24*time.Hour:
		for t := midnight(from); t.Before(to); t = t.AddDate(0, 0, 1) {
			if t.Weekday() == time.Monday && !t.Before(from) {
				ticks = append(ticks, tick{t, t.Format("Jan 2")})
			}
		}
	default:
		for t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, from.Location()); t.Before(to); t = t.AddDate(0, 1, 0) {
			if t.Before(from) {
				continue
			}
			label := t.Format("Jan")
			if t.Month() == time.January {
				label = t.Format("2006")
			}
			ticks = append(ticks, tick{t, label})
		}
	}
	buf := []rune(strings.Repeat(" ", w))
	next := 0
	for _, tk := range ticks {
		x := int(float64(tk.at.Sub(from)) / float64(span) * float64(w))
		lr := []rune(tk.label)
		if centre {
			x -= len(lr) / 2
		}
		x = max(x, 0)
		if x < next || x+len(lr) > w {
			continue
		}
		copy(buf[x:], lr)
		next = x + len(lr) + 1
	}
	return strings.TrimRight(string(buf), " ")
}

// --- activity heatmap -----------------------------------------------------------

// statsHeatMinWeeks is the fewest weeks worth squeezing the heatmap for: it
// shares a row with repos/agents only when at least this many fit beside them.
const statsHeatMinWeeks = 16

// statsHeatShades are the ramp steps for heatmap levels 1–4 (level 0 is a
// border-tone square, so the grid reads as a grid rather than static).
var statsHeatShades = [5]int{0, 2, 4, 6, statsRampN - 1}

// statsDayLevels is each day's heatmap level (0–4). Fleet actions and agent
// time are bucketed separately, each by its own quartiles, and a day takes the
// higher of its two levels: agent turns are backfilled from transcripts long
// before fleet began recording actions, and scoring by actions alone once any
// day had one turned every backfilled day into a zero dot.
func statsDayLevels(days []stats.Day) []int {
	acts := make([]int, len(days))
	secs := make([]int, len(days))
	for i, d := range days {
		acts[i] = d.Actions
		secs[i] = int(d.AgentTime / time.Second)
	}
	la, lt := statsHeatLevels(acts), statsHeatLevels(secs)
	out := make([]int, len(days))
	for i := range days {
		out[i] = max(la[i], lt[i])
	}
	return out
}

// statsHeatLevels buckets scores by the quartiles of the non-zero days, the
// way GitHub's contribution graph does, so one monster day doesn't flatten the
// rest of the year into level 1.
func statsHeatLevels(scores []int) []int {
	var nz []int
	for _, s := range scores {
		if s > 0 {
			nz = append(nz, s)
		}
	}
	sort.Ints(nz)
	q := func(p float64) int {
		if len(nz) == 0 {
			return 0
		}
		return nz[min(int(p*float64(len(nz))), len(nz)-1)]
	}
	q1, q2, q3 := q(0.25), q(0.5), q(0.75)
	levels := make([]int, len(scores))
	for i, s := range scores {
		switch {
		case s <= 0:
			levels[i] = 0
		case s <= q1:
			levels[i] = 1
		case s <= q2:
			levels[i] = 2
		case s <= q3:
			levels[i] = 3
		default:
			levels[i] = 4
		}
	}
	return levels
}

// statsHeat is the heatmap's geometry, shared by the card's width and body.
type statsHeat struct {
	monday0         time.Time // the Monday of the first day in r.Days
	startCol, weeks int       // visible week columns
	// first is the first recorded day: days before it are drawn blank, not
	// as zero-activity squares claiming "nothing happened" when nothing was
	// being recorded.
	first time.Time
}

const statsHeatLabelW = 4

// Two columns per week ("■ "), always: at one column the cells touch and the
// grid reads as horizontal stripes, so a narrow card shows fewer weeks instead.
func (g statsHeat) width() int { return statsHeatLabelW + 2*g.weeks - 1 }

func statsHeatMaxWeeks(maxW int) int { return max((maxW-statsHeatLabelW+1)/2, 1) }

func statsHeatPos(monday0, d time.Time) (col, row int) {
	d = statsMidnight(d)
	days := int(math.Round(d.Sub(monday0).Hours() / 24))
	return days / 7, days % 7
}

func statsMidnight(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// statsHeatGeometry shows the weeks from the first recorded day, at most
// what maxW holds.
func statsHeatGeometry(r stats.Report, levels []int, maxW int) statsHeat {
	first := statsMidnight(r.Days[0].Date)
	g := statsHeat{monday0: first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))}
	lastCol, _ := statsHeatPos(g.monday0, r.Days[len(r.Days)-1].Date)
	ncols := lastCol + 1
	firstCol := lastCol
	for i, l := range levels {
		if l > 0 {
			firstCol, _ = statsHeatPos(g.monday0, r.Days[i].Date)
			g.first = statsMidnight(r.Days[i].Date)
			break
		}
	}
	for _, t := range []time.Time{r.Coverage.RecordingSince, r.Coverage.TurnsSince} {
		if !t.IsZero() && (g.first.IsZero() || t.Before(g.first)) {
			g.first = statsMidnight(t)
		}
	}
	if !g.first.IsZero() {
		c, _ := statsHeatPos(g.monday0, g.first)
		firstCol = clampInt(c, 0, firstCol)
	}
	// Only recorded weeks: padding a new install out to a fixed width drew
	// weeks of blank cells between the day labels and the first real week.
	g.weeks = clampInt(ncols-firstCol, 1, statsHeatMaxWeeks(maxW))
	g.startCol = ncols - g.weeks
	return g
}

func statsHeatmapCard(r stats.Report, maxW int) (statsCardSpec, bool) {
	if len(r.Days) == 0 {
		return statsCardSpec{}, false
	}
	levels := statsDayLevels(r.Days)
	g := statsHeatGeometry(r, levels, max(maxW-4, 8))
	title := "activity"
	if g.weeks >= 52 {
		title = "year"
	}
	right := fmt.Sprintf("streak %dd · best %dd", r.Streak, r.BestStreak)
	// Wide enough for the title and the streak note in the top border (the
	// border needs at least one dash between them, or it drops the note).
	w := min(max(g.width()+4, len(title)+lipgloss.Width(right)+9), max(maxW, 1))
	return statsCardSpec{
		title: title, right: right,
		minW: w, maxW: w, weight: 1,
		body: func(w int) []string { return statsHeatmapBody(r, g, levels, w) },
	}, true
}

func statsHeatmapBody(r stats.Report, g statsHeat, levels []int, w int) []string {
	type cell struct {
		set   bool
		level int
		today bool
		date  time.Time
	}
	grid := make([][]cell, 7)
	for i := range grid {
		grid[i] = make([]cell, g.weeks)
	}
	// Recorded days with nothing in them are honest zeros; days before
	// recording began stay blank.
	last := statsMidnight(r.Days[len(r.Days)-1].Date)
	for c := range g.weeks {
		for row := range 7 {
			d := g.monday0.AddDate(0, 0, (g.startCol+c)*7+row)
			if !d.After(last) && (g.first.IsZero() || !d.Before(g.first)) {
				grid[row][c] = cell{set: true, date: d}
			}
		}
	}
	for i, d := range r.Days {
		c, row := statsHeatPos(g.monday0, d.Date)
		if c < g.startCol || c-g.startCol >= g.weeks || row < 0 || row > 6 || !grid[row][c-g.startCol].set {
			continue
		}
		grid[row][c-g.startCol] = cell{set: true, level: levels[i], today: i == len(r.Days)-1, date: statsMidnight(d.Date)}
	}

	// Month labels over the first column holding a (recorded) month's 1st.
	// Labels may run past a young grid's right edge, up to the card's.
	gridW := max(g.width(), w) - statsHeatLabelW
	month := []rune(strings.Repeat(" ", gridW))
	next := 0
	for c := range g.weeks {
		for row := range 7 {
			cl := grid[row][c]
			if cl.set && cl.date.Day() == 1 {
				lbl := []rune(cl.date.Format("Jan"))
				x := c * 2
				if x >= next && x+len(lbl) <= gridW {
					copy(month[x:], lbl)
					next = x + len(lbl) + 1
				}
				break
			}
		}
	}
	// A grid too young to hold a month's 1st still says which month it is.
	if next == 0 {
		for row := range 7 {
			if cl := grid[row][0]; cl.set {
				if lbl := []rune(cl.date.Format("Jan")); len(lbl) <= gridW {
					copy(month, lbl)
				}
				break
			}
		}
	}
	out := []string{strings.Repeat(" ", statsHeatLabelW) + DimStyle.Render(strings.TrimRight(string(month), " "))}

	glyph := func(level int) string {
		if level == 0 {
			return StatsRamp[0].Render("■")
		}
		return StatsRamp[statsHeatShades[level]].Render("■")
	}
	// Today is the same square in the text colour, not a hue or glyph of its
	// own: ▣/□ are East-Asian-Ambiguous, which Ghostty draws two columns wide,
	// shearing the grid and swallowing the legend's space.
	today := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	rowLabels := [7]string{"M", "T", "W", "T", "F", "S", "S"}
	for row := range 7 {
		var b strings.Builder
		b.WriteString(DimStyle.Render(statsPadRight(rowLabels[row], statsHeatLabelW)))
		for c := range g.weeks {
			if c > 0 {
				b.WriteByte(' ')
			}
			cl := grid[row][c]
			switch {
			case !cl.set:
				b.WriteByte(' ')
			case cl.today:
				b.WriteString(today.Render("■"))
			default:
				b.WriteString(glyph(cl.level))
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}

	legend := DimStyle.Render("less  ")
	for lv := range 5 {
		legend += glyph(lv) + " "
	}
	legend += DimStyle.Render(" more")
	// The legend ends under the grid's right edge, or at the card's when a
	// young grid is narrower than the legend itself.
	if withToday := legend + "    " + today.Render("■") + DimStyle.Render(" today"); lipgloss.Width(withToday) <= w {
		legend = withToday
	}
	right := min(max(g.width(), lipgloss.Width(legend)), w)
	return append(out, strings.Repeat(" ", max(right-lipgloss.Width(legend), 0))+legend)
}

// --- the lower grid -------------------------------------------------------------

// statsLowerLayouts is everything under the chart, as the arrangements
// statsBestGrid chooses between: which cards share a row, and whether records
// and fun facts stack. No one arrangement suits every screen — records and
// facts stacked beside the heatmap set a row height the heatmap and the
// actions list can't fill, while in a row of their own on a narrow screen they
// save a scroll — so the grid tries each and keeps the one with the least
// blank card area. A card with nothing to say is left out entirely rather than
// drawn empty.
func statsLowerLayouts(r stats.Report, cw int) [][][]statsColumn {
	// The agents card earns its box only when it compares agents. With one
	// agent (or one with any turn data) it was a single "Claude 100%" line in
	// a tall empty box, so the split rides a line under the repos instead.
	withData := 0
	for _, a := range r.Agents {
		if a.AgentTime > 0 {
			withData++
		}
	}
	agentsCard := len(r.Agents) > 0 && (withData >= 2 || len(r.Repos) == 0)
	var who statsColumn
	whoMin := 0
	if len(r.Repos) > 0 {
		foot := []string(nil)
		if !agentsCard {
			foot = statsAgentSummary(r)
		}
		who = append(who, statsCardSpec{title: "repos", minW: 40, fullW: 100, weight: 3,
			body: func(w int) []string { return statsRepoLines(r, w, 6, foot) },
			fit:  func(w, h int) []string { return statsRepoLines(r, w, h, foot) }})
		whoMin = 40
	}
	if agentsCard {
		agentsW := statsNaturalW(statsAgentLines(r, 200)) + 4
		who = append(who, statsCardSpec{title: "agents", minW: agentsW, weight: 3,
			body: func(w int) []string { return statsAgentLines(r, w) }})
		whoMin = max(whoMin, agentsW)
	}

	var heat, notes, records, facts, actions statsColumn
	// The heatmap is sized for the width it would share with repos/agents
	// when a readable grid fits there: sized for the whole row, it claimed a
	// row band to itself at every width short of ~150 columns.
	heatMax := cw
	if len(who) > 0 {
		if share := cw - statsCardGap - whoMin; statsHeatMaxWeeks(share-4) >= statsHeatMinWeeks {
			heatMax = share
		}
	}
	if card, ok := statsHeatmapCard(r, heatMax); ok {
		heat = statsColumn{card}
	}

	// Records and fun facts are text: past their natural width a card only
	// grows a blank margin, so they are capped there and a row's spare width
	// goes to the bar cards, which spend it on longer bars.
	if len(r.Records) > 0 {
		recW := clampInt(statsNaturalW(statsRecordLines(r, 200))+4, 30, 72)
		records = statsColumn{{title: "records", minW: recW, maxW: recW + 2, weight: 3,
			body: func(w int) []string { return statsRecordLines(r, w) }}}
	}
	if len(r.FunFacts) > 0 {
		// Wants the width that keeps the longest fact on one line; it wraps
		// only when the row can't spare that much.
		factW := 0
		for _, f := range r.FunFacts {
			factW = max(factW, lipgloss.Width(f))
		}
		facts = statsColumn{{title: "fun facts", minW: 40, wantW: min(factW+6, 100), maxW: max(factW+6, 40), weight: 3,
			body: func(w int) []string { return statsFactLines(r, w, 0) },
			fit:  func(w, h int) []string { return statsFactLines(r, w, h) }}}
	}
	notes = slices.Concat(records, facts)
	if len(r.Activity) > 0 {
		total := 0
		for _, a := range r.Activity {
			total += a.N
		}
		actW := max(statsActivityNaturalW(r)+4, 30)
		actions = statsColumn{{title: "fleet actions", right: statsInt(total),
			minW: actW, fullW: actW + 40, weight: 1,
			// The top five and "+N more" for the long tail of single presses;
			// a card stretched taller by its row shows as many more as fit.
			body: func(w int) []string { return statsActivityLines(r, w, statsActionRows) },
			fit:  func(w, h int) []string { return statsActivityLines(r, w, max(statsActionRows, h)) }}}
	}

	// Which cards share a row is the choice: everything in one row on a wide
	// screen; the heatmap beside the short lists with the notes in a row of
	// their own; and so on. Each row still wraps by width inside statsGrid.
	row := func(cols ...statsColumn) []statsColumn {
		var out []statsColumn
		for _, c := range cols {
			if len(c) > 0 {
				out = append(out, c)
			}
		}
		return out
	}
	layout := func(rows ...[]statsColumn) [][]statsColumn {
		var out [][]statsColumn
		for _, r := range rows {
			if len(r) > 0 {
				out = append(out, r)
			}
		}
		return out
	}
	return [][][]statsColumn{
		layout(row(heat, who, records, facts, actions)),
		layout(row(heat, who, notes, actions)),
		layout(row(heat, who, actions), row(records, facts)),
		layout(row(heat, actions), row(who, records, facts)),
		layout(row(heat, actions), row(who, notes)),
		layout(row(heat, who), row(records, facts, actions)),
		layout(row(heat, who), row(notes, actions)),
		layout(row(heat, facts), row(who, records, actions)),
	}
}

// statsNaturalW is the widest of a card body's lines.
func statsNaturalW(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, lipgloss.Width(l))
	}
	return w
}

// statsRampFor maps a 0..1 magnitude onto the data steps of the ramp, so the
// largest bar in a card is the hottest and the smallest is still visible.
func statsRampFor(frac float64) lipgloss.Style {
	return StatsRamp[1+int(math.Round(math.Max(0, math.Min(1, frac))*float64(statsRampN-2)))]
}

// --- records & fun facts -----------------------------------------------------

func statsRecordLines(r stats.Report, w int) []string {
	labelW, valW := 0, 0
	for _, rec := range r.Records {
		labelW = max(labelW, lipgloss.Width(rec.Label))
		valW = max(valW, lipgloss.Width(rec.Value))
	}
	labelW = min(labelW, 22) + 2
	valW = min(valW, 16) + 2
	val := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	var out []string
	for _, rec := range r.Records {
		line := DimStyle.Render(statsPadRight(ansi.Truncate(rec.Label, labelW-2, "…"), labelW)) +
			statsPadRight(val.Render(ansi.Truncate(rec.Value, valW-2, "…")), valW) +
			DimStyle.Render(rec.Detail)
		out = append(out, ansi.Truncate(line, w, "…"))
	}
	return out
}

// statsFactLines renders the fun facts for an inner width. Given h rows by a
// row that stretched the card, it opens a blank line between facts when they
// all still fit: a short list spaced down its card reads as a list, where the
// same list packed at the top read as a card left mostly empty.
func statsFactLines(r stats.Report, w, h int) []string {
	star := StatsRamp[statsRampN-1]
	txt := lipgloss.NewStyle().Foreground(ColorText)
	var facts [][]string
	n := 0
	for _, f := range r.FunFacts {
		wrapped := statsBalancedWrap(f, max(w-2, 8))
		lines := make([]string, len(wrapped))
		for i, l := range wrapped {
			prefix := "  "
			if i == 0 {
				prefix = star.Render("✦") + " "
			}
			lines[i] = prefix + txt.Render(l)
		}
		facts = append(facts, lines)
		n += len(lines)
	}
	spaced := len(facts) > 1 && n+len(facts)-1 <= h
	var out []string
	for i, lines := range facts {
		if spaced && i > 0 {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

// statsBalancedWrap wraps s to at most w columns and, when that leaves an
// orphan — a last line under a third of the width — narrows the wrap just far
// enough to give it company. Narrowing all the way to evenly balanced lines
// drew every fact as a slim column down the left of a wide card.
func statsBalancedWrap(s string, w int) []string {
	lines := strings.Split(ansi.Wrap(s, w, ""), "\n")
	orphan := func(ls []string, n int) bool {
		return len(ls) > 1 && 3*lipgloss.Width(ls[len(ls)-1]) < n
	}
	for n := w - 1; n > w/2 && orphan(lines, n+1); n-- {
		next := strings.Split(ansi.Wrap(s, n, ""), "\n")
		if len(next) != len(lines) {
			break
		}
		lines = next
	}
	return lines
}

// --- fleet actions ---------------------------------------------------------------

// statsActivityNaturalW is the actions card's body width with a 12-cell bar:
// enough to compare, and it leaves the fun facts beside it room for a line
// each.
func statsActivityNaturalW(r stats.Report) int {
	labelW, maxN := 0, 0
	for _, a := range r.Activity[:min(len(r.Activity), 8)] {
		labelW = max(labelW, lipgloss.Width(statsActionLabel(a)))
		maxN = max(maxN, a.N)
	}
	return min(labelW, 20) + 2 + 12 + 1 + len(statsInt(maxN))
}

// statsActionRows is how many lines the actions card takes unstretched: the
// top five and a "+N more" line.
const statsActionRows = 6

// statsActivityLines lists the actions in at most rows lines. A list that
// doesn't fit ends in a dim "+N more" line in place of its tail.
func statsActivityLines(r stats.Report, w, rows int) []string {
	acts := r.Activity
	more := 0
	if rows = max(rows, 2); len(acts) > rows {
		acts, more = acts[:rows-1], len(acts)-(rows-1)
	}
	labelW, maxN := 0, 0
	for _, a := range r.Activity[:min(len(r.Activity), 8)] {
		labelW = max(labelW, lipgloss.Width(statsActionLabel(a)))
	}
	for _, a := range acts {
		maxN = max(maxN, a.N)
	}
	labelW = min(labelW, 20) + 2
	numW := len(statsInt(maxN))
	barW := max(w-labelW-numW-1, 1)
	num := lipgloss.NewStyle().Foreground(ColorText)
	var out []string
	for _, a := range acts {
		frac := 0.0
		if maxN > 0 {
			frac = float64(a.N) / float64(maxN)
		}
		out = append(out, DimStyle.Render(statsPadRight(ansi.Truncate(statsActionLabel(a), labelW-2, "…"), labelW))+
			statsBar(frac, barW, statsRampFor(frac))+" "+num.Render(fmt.Sprintf("%*s", numW, statsInt(a.N))))
	}
	if more > 0 {
		out = append(out, DimStyle.Render(fmt.Sprintf("+%d more", more)))
	}
	return out
}

func statsActionLabel(a stats.ActionCount) string {
	if a.Label != "" {
		return a.Label
	}
	return strings.ReplaceAll(a.Action, "_", " ")
}

// --- repos & agents -------------------------------------------------------------

// statsRepoLines lists the repos in at most rows lines (foot included,
// after a blank line), ending in "+N more" when they don't all fit.
func statsRepoLines(r stats.Report, w, rows int, foot []string) []string {
	if len(foot) > 0 {
		rows -= len(foot) + 1
	}
	repos := r.Repos
	more := 0
	if rows = max(rows, 2); len(repos) > rows {
		repos, more = repos[:rows-1], len(repos)-(rows-1)
	}
	nameW := 0
	var maxT time.Duration
	for _, rp := range repos {
		nameW = max(nameW, lipgloss.Width(rp.Name))
		maxT = max(maxT, rp.AgentTime)
	}
	nameW = min(nameW, 20) + 2
	const durW = 8
	barW := max(w-nameW-durW-1, 1)
	val := lipgloss.NewStyle().Foreground(ColorText)
	var out []string
	for _, rp := range repos {
		frac := 0.0
		if maxT > 0 {
			frac = float64(rp.AgentTime) / float64(maxT)
		}
		line := DimStyle.Render(statsPadRight(ansi.Truncate(rp.Name, nameW-2, "…"), nameW)) +
			statsBar(frac, barW, statsRampFor(frac)) + " " + val.Render(fmt.Sprintf("%*s", durW, statsDurOrDash(rp.AgentTime)))
		out = append(out, ansi.Truncate(line, w, ""))
	}
	if more > 0 {
		out = append(out, DimStyle.Render(fmt.Sprintf("+%d more", more)))
	}
	if len(foot) > 0 {
		out = append(out, "")
		for _, l := range foot {
			out = append(out, ansi.Truncate(l, w, "…"))
		}
	}
	return out
}

// statsAgentSummary is the agents card folded into one line under the repos,
// for when there is no split worth a card: "✻ Claude · 29 sessions".
func statsAgentSummary(r stats.Report) []string {
	var parts []string
	for _, a := range r.Agents {
		sess := fmt.Sprintf("%d session", a.Sessions)
		if a.Sessions != 1 {
			sess += "s"
		}
		parts = append(parts, AgentGlyphStyle.Render(agentGlyph(agent.Type(a.Agent)))+" "+
			DimStyle.Render(agent.Parse(a.Agent).DisplayName()+" · "+sess))
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, DimStyle.Render("   "))}
}

func statsAgentLines(r stats.Report, w int) []string {
	var total time.Duration
	for _, a := range r.Agents {
		total += a.AgentTime
	}
	val := lipgloss.NewStyle().Foreground(ColorText)
	var out []string
	for _, a := range r.Agents {
		// Turn data is Claude-only, so another agent's 0% is not a measurement:
		// say so rather than let it read as "never used".
		noData := a.AgentTime == 0 && r.Coverage.ClaudeOnly && agent.Parse(a.Agent) != agent.Claude
		share := ""
		if total > 0 {
			share = fmt.Sprintf("%3.0f%%", 100*float64(a.AgentTime)/float64(total))
		}
		if noData {
			share = "  —"
		}
		sess := fmt.Sprintf("%d session", a.Sessions)
		if a.Sessions != 1 {
			sess += "s"
		}
		tail := share + " · " + sess
		if noData {
			tail += " · no turn data"
		}
		line := AgentGlyphStyle.Render(agentGlyph(agent.Type(a.Agent))) + " " +
			val.Render(statsPadRight(agent.Parse(a.Agent).DisplayName(), 9)) +
			val.Render(fmt.Sprintf("%8s", statsDurOrDash(a.AgentTime))) + "  " +
			DimStyle.Render(tail)
		out = append(out, ansi.Truncate(line, w, ""))
	}
	return out
}

// --- footnotes ------------------------------------------------------------------

func statsFootnoteLines(r stats.Report, cw int) []string {
	// The window itself rides the range tabs, not this line.
	var parts []string
	c := r.Coverage
	if c.ClaudeOnly {
		parts = append(parts, "turn data: Claude sessions only")
	}
	if !c.RecordingSince.IsZero() {
		parts = append(parts, "recording since "+c.RecordingSince.Local().Format("Jan 2"))
	}
	if c.Sessions > 0 {
		s := statsInt(c.Sessions) + " fleet session"
		if c.Sessions != 1 {
			s += "s"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return nil
	}
	// Wrapped by part, so a line never opens on an orphaned separator, and
	// indented to the cards' text column rather than their borders.
	const indent = "  "
	cw = max(cw-len(indent), 1)
	var out []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case lipgloss.Width(cur)+3+lipgloss.Width(p) <= cw:
			cur += " · " + p
		default:
			out = append(out, indent+DimStyle.Render(ansi.Truncate(cur, cw, "…")))
			cur = p
		}
	}
	return append(out, indent+DimStyle.Render(ansi.Truncate(cur, cw, "…")))
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

// statsBar renders an eighth-block bar frac of w cells wide, padded with
// spaces to exactly w cells. The padding happens on the raw text, before the
// style, so the ANSI bytes never count as columns.
func statsBar(frac float64, w int, style lipgloss.Style) string {
	if w < 1 {
		return ""
	}
	frac = math.Max(0, math.Min(1, frac))
	eighths := int(math.Round(frac * float64(w) * 8))
	if frac > 0 && eighths == 0 {
		eighths = 1
	}
	partials := [8]string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	bar := strings.Repeat("█", eighths/8) + partials[eighths%8]
	pad := w - lipgloss.Width(bar)
	return style.Render(bar) + strings.Repeat(" ", max(pad, 0))
}

// statsPadRight pads a (possibly styled) string to w visible columns.
func statsPadRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func statsDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	case d < 100*time.Hour:
		d = d.Round(time.Minute)
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return statsInt(int(d/time.Hour)) + "h"
}

func statsDurOrDash(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return statsDur(d)
}

// statsInt formats n with thousands separators: 34584 → "34,584".
func statsInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func statsTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 0, 64) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func statsMoney(usd float64) string {
	switch {
	case usd < 10:
		return fmt.Sprintf("$%.2f", usd)
	case usd < 1000:
		return fmt.Sprintf("$%.0f", usd)
	}
	return "$" + statsInt(int(math.Round(usd)))
}

// statsWhen names a moment compactly for the range it sits in: a weekday is
// enough inside a week, a date beyond it.
func statsWhen(t time.Time, rng stats.Range) string {
	t = t.Local()
	if rng == stats.Range7d {
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2 15:04")
}
