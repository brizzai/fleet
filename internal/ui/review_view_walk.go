package ui

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/selfreview"
)

// The Walkthrough tab: how the change works, chapter by chapter — the heart of
// it first, then what follows from it, the glue, the docs. The Decide tab asks;
// this one explains, so a person can answer with understanding rather than
// "is this ok?". Every changed file sits in exactly one chapter, with what
// happened to it: in a decision, safe to skip and why, or not reviewed.
//
// The scope decision belongs to no chapter (it is about whether this is the
// right change at all), so it sits above the chapters.
//
// From here ⏎ jumps: a decision opens on the Decide tab, a file opens its own
// diff. Answering stays on Decide — one place where answers happen.

// walkRowKind is what a Walkthrough list row is.
type walkRowKind int

const (
	walkScope   walkRowKind = iota // the scope decision, above the chapters
	walkChapter                    // one chapter
)

// walkRow is one row of the Walkthrough list.
type walkRow struct {
	kind    walkRowKind
	n       int // the chapter's 1-based number, or the scope decision's
	chapter *selfreview.Chapter
	stop    *selfreview.Stop // the scope decision
}

// walkRows is the list: the scope decision when there is one, then the
// chapters in reading order.
func (v *ReviewView) walkRows() []walkRow {
	run := v.d.Run
	if run == nil {
		return nil
	}
	var out []walkRow
	for i := range run.Stops {
		if run.Stops[i].IsScope() {
			out = append(out, walkRow{kind: walkScope, n: i + 1, stop: &run.Stops[i]})
			break
		}
	}
	for i := range run.Chapters {
		out = append(out, walkRow{kind: walkChapter, n: i + 1, chapter: &run.Chapters[i]})
	}
	return out
}

func (v *ReviewView) selectedWalkRow() (walkRow, bool) {
	rows := v.walkRows()
	if v.walkCursor >= 0 && v.walkCursor < len(rows) {
		return rows[v.walkCursor], true
	}
	return walkRow{}, false
}

// walkEntryKind is what a row inside a chapter card is.
type walkEntryKind int

const (
	walkEntryDecision walkEntryKind = iota // ⏎ opens it on the Decide tab
	walkEntryFile                          // ⏎ opens the file's diff
)

// walkEntry is one row inside the card the keyboard can land on.
type walkEntry struct {
	kind  walkEntryKind
	id    string // stop id
	n     int    // the decision's number
	title string
	path  string
}

// reviewStopNumbers maps each stop id to its 1-based number on Decide.
func reviewStopNumbers(run *selfreview.Run) map[string]int {
	out := map[string]int{}
	if run == nil {
		return out
	}
	for i, s := range run.Stops {
		out[s.ID] = i + 1
	}
	return out
}

// walkEntries is the card's selectable rows: its decisions, then its files.
func (v *ReviewView) walkEntries(r walkRow) []walkEntry {
	run := v.d.Run
	var out []walkEntry
	switch r.kind {
	case walkScope:
		out = append(out, walkEntry{kind: walkEntryDecision, id: r.stop.ID, n: r.n, title: r.stop.Title})
	case walkChapter:
		nums := reviewStopNumbers(run)
		for _, id := range r.chapter.Decisions {
			if n, ok := nums[id]; ok {
				out = append(out, walkEntry{kind: walkEntryDecision, id: id, n: n, title: run.Stops[n-1].Title})
			}
		}
		for _, p := range r.chapter.Files {
			out = append(out, walkEntry{kind: walkEntryFile, path: p})
		}
	}
	return out
}

// updateWalk handles the Walkthrough tab's keys.
func (v *ReviewView) updateWalk(k string) (*ReviewView, tea.Cmd) {
	rows := v.walkRows()
	if v.cardFocus {
		r, ok := v.selectedWalkRow()
		if !ok {
			v.cardFocus = false
			return v, nil
		}
		entries := v.walkEntries(r)
		switch k {
		case "esc", "tab":
			v.cardFocus = false
		case "j", "down":
			v.walkItem = clampInt(v.walkItem+1, 0, max(len(entries)-1, 0))
		case "k", "up":
			v.walkItem = clampInt(v.walkItem-1, 0, max(len(entries)-1, 0))
		case "pgdown", "ctrl+d", "space":
			v.walkCardScroll += max(v.cardViewHeight()-2, 1)
		case "pgup", "ctrl+u":
			v.walkCardScroll -= max(v.cardViewHeight()-2, 1)
		case "enter":
			if v.walkItem < len(entries) {
				v.openWalkEntry(entries[v.walkItem])
			}
		}
		return v, nil
	}
	switch k {
	case "esc":
		// One level per press: the Walkthrough's top steps back to Decide,
		// the review's home tab, and only esc there leaves the review.
		if !v.popBack() {
			v.back = nil
			v.switchTab(reviewTabDecide)
		}
		return v, nil
	case "j", "down":
		v.moveWalk(1, len(rows))
	case "k", "up":
		v.moveWalk(-1, len(rows))
	case "enter", "tab":
		r, ok := v.selectedWalkRow()
		switch {
		case !ok:
		case r.kind == walkScope && k == "enter":
			// One thing to do with it: decide it.
			v.openWalkEntry(walkEntry{kind: walkEntryDecision, id: r.stop.ID, n: r.n})
		default:
			v.cardFocus = true
		}
	}
	return v, nil
}

func (v *ReviewView) moveWalk(d, n int) {
	if n == 0 {
		return
	}
	c := clampInt(v.walkCursor+d, 0, n-1)
	if c != v.walkCursor {
		v.walkCursor, v.walkItem, v.walkCardScroll = c, 0, 0
	}
}

// openWalkEntry is ⏎ on a card row: a decision opens on the Decide tab with
// the highlight on it; a file opens its own diff (esc comes back here).
func (v *ReviewView) openWalkEntry(e walkEntry) {
	v.pushBack()
	switch e.kind {
	case walkEntryDecision:
		v.tab, v.screen, v.cardFocus = reviewTabDecide, reviewScreenDecisions, false
		for i, it := range v.items() {
			if it.kind == rowDecision && it.id == e.id {
				v.cursor = i
			}
		}
		v.resetCard()
		v.clamp()
	case walkEntryFile:
		if v.d.Doc != nil {
			v.openDiff(e.path)
		}
	}
}

// ---------------------------------------------------------------------------
// What happened to a file, in the person's words.

// reviewFileFate is a changed file's status on the Walkthrough: in a
// decision, safe to skip (and why), not reviewed (and why), or none of those.
type reviewFileFate struct {
	glyph string
	text  string
	style lipgloss.Style // the glyph's
	warn  bool
}

func reviewFateOf(run *selfreview.Run, answers map[string]selfreview.Answer, p string) reviewFileFate {
	var nums []string
	for i, s := range run.Stops {
		for _, h := range s.Hunks {
			if h.Path == p {
				nums = append(nums, strconv.Itoa(i+1))
				break
			}
		}
	}
	raised := answers[selfreview.FileStopID(p)].Kind != ""
	switch {
	case len(nums) == 1:
		return reviewFileFate{glyph: "◆", text: "in decision " + nums[0], style: reviewTextStyle()}
	case len(nums) > 1:
		return reviewFileFate{glyph: "◆", text: "in decisions " + strings.Join(nums[:len(nums)-1], ", ") + " and " + nums[len(nums)-1],
			style: reviewTextStyle()}
	case raised:
		return reviewFileFate{glyph: "✎", text: "you raised it", style: reviewTextStyle()}
	}
	for _, nc := range run.NotCovered {
		if nc == p {
			t := "not reviewed"
			if why := run.NotCoveredWhy[p]; why != "" {
				t += " — " + why
			}
			return reviewFileFate{glyph: "⚠", text: t, style: reviewWarnStyle(), warn: true}
		}
	}
	for _, s := range run.Skipped {
		if s.Path != p {
			continue
		}
		t := "safe to skip — " + lowerFirst(s.Reason)
		if s.By == "rule" {
			t = "safe to skip — " + s.Reason + " (auto-skipped)"
		}
		if s.Note != "" {
			// Fleet's own flag outranks the skip: say it first.
			return reviewFileFate{glyph: "⚠", text: s.Note + " (auto-skipped " + s.Reason + ")", style: reviewWarnStyle(), warn: true}
		}
		return reviewFileFate{glyph: "✓", text: t, style: reviewGoodStyle()}
	}
	return reviewFileFate{glyph: "·", text: "reviewed, no decision", style: DimStyle}
}

// chapterSafeToSkip reports a chapter whose every file is skipped whole and
// in no decision: nothing in it needs a person.
func chapterSafeToSkip(run *selfreview.Run, c *selfreview.Chapter) bool {
	if len(c.Decisions) > 0 || len(c.Files) == 0 {
		return false
	}
	whole := map[string]bool{}
	for _, s := range run.Skipped {
		if s.Scope != "rest" {
			whole[s.Path] = true
		}
	}
	for _, p := range c.Files {
		if !whole[p] {
			return false
		}
	}
	return true
}

// walkRoleWords names a chapter's role in the walkthrough's order.
func walkRoleWords(role string) string {
	switch role {
	case selfreview.RoleHeart:
		return "the heart of it"
	case selfreview.RoleConsequence:
		return "what follows"
	case selfreview.RoleGlue:
		return "the glue"
	case selfreview.RoleDocs:
		return "docs"
	}
	return role
}

// walkMarkers is a chapter's decisions as list markers: "◆2 ◆5".
func walkMarkers(run *selfreview.Run, c *selfreview.Chapter) string {
	nums := reviewStopNumbers(run)
	var out []string
	for _, id := range c.Decisions {
		if n, ok := nums[id]; ok {
			out = append(out, "◆"+strconv.Itoa(n))
		}
	}
	return strings.Join(out, " ")
}

// ---------------------------------------------------------------------------
// Render.

// renderWalk is the Walkthrough body: the list beside the chapter card when
// wide; the list, or the card full-width, when narrow.
func (v *ReviewView) renderWalk(w, bh int) string {
	listFocus := !v.cardFocus
	lines, cur := renderWalkList(v.d.Run, v.walkRows(), v.d.Answers, v.walkCursor, listFocus, v.listInner())
	left := v.listPanel(lines, cur, &v.walkScroll, "Walkthrough", listFocus)
	if v.narrow() {
		if v.cardFocus {
			return v.renderWalkCard(w, bh)
		}
		return left
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, v.renderWalkCard(v.cardWidth(), bh))
}

// renderWalkList draws the scope decision (when there is one), a rule, then
// each chapter: number and title (wrapped, never cut), and under it the
// decisions inside as ◆N markers — or a dim "safe to skip" when nothing in it
// needs you.
func renderWalkList(run *selfreview.Run, rows []walkRow, answers map[string]selfreview.Answer, cursor int, focused bool, w int) (lines []string, cursorLine int) {
	cursorLine = -1
	if run == nil {
		return nil, -1
	}
	if len(run.Chapters) == 0 {
		defer func() {
			lines = append(lines, "")
			for _, l := range rvWrap("No walkthrough: this review was made before fleet asked for one. r reviews again.", max(w-3, 10)) {
				lines = append(lines, "  "+DimStyle.Render(l))
			}
		}()
	}
	numW := len(strconv.Itoa(max(len(run.Chapters), 1)))
	// row draws one list row: lines (pre-split, raw) each with its style,
	// the first led by lead and the rest by an indent of the same width; a
	// band across the whole row when selected, the answer glyph at the right.
	row := func(sel bool, lead string, texts []string, styles []lipgloss.Style, glyph string) {
		bodyW := max(w-1-lipgloss.Width(lead)-2, 4)
		for j, t := range texts {
			ld := lead
			g := ""
			if j > 0 {
				ld = strings.Repeat(" ", lipgloss.Width(lead))
			} else {
				g = glyph
			}
			if sel {
				mark := " "
				if j == 0 {
					mark = SelectionMarker(focused).Render("▸")
				}
				lines = append(lines, mark+SelectionBand().Render(ld+rvFit(t, bodyW))+SelectionBandSecondary().Render(rvFit(g, 2)))
				continue
			}
			gs := "  "
			if g != "" {
				gs = ModeOn().Render(g) + " "
			}
			lines = append(lines, " "+DimStyle.Render(ld)+styles[j].Render(rvFit(t, bodyW))+gs)
		}
	}
	for i, r := range rows {
		sel := i == cursor
		if sel {
			cursorLine = len(lines)
		}
		switch r.kind {
		case walkScope:
			// Above the chapters: it is about whether this is the right
			// change at all, so it belongs to none of them.
			lead := " Scope  "
			ts := rvWrap("◆"+strconv.Itoa(r.n)+" "+r.stop.Title, max(w-1-lipgloss.Width(lead)-2, 4))
			st := make([]lipgloss.Style, len(ts))
			for j := range st {
				st[j] = reviewBoldStyle()
			}
			row(sel, lead, ts, st, reviewAnswerGlyph(answers[r.stop.ID].Kind))
			lines = append(lines, " "+DimStyle.Render(strings.Repeat("─", max(w-2, 1))))
		case walkChapter:
			// The title on its own line(s), never squeezed by a marker
			// column; the decisions inside — or "safe to skip" — under it.
			lead := fmt.Sprintf(" %*d  ", numW, r.n)
			titleStyle := reviewTextStyle()
			if r.chapter.Role == selfreview.RoleHeart {
				titleStyle = reviewBoldStyle()
			}
			ts := rvWrap(r.chapter.Title, max(w-1-lipgloss.Width(lead)-2, 4))
			st := make([]lipgloss.Style, len(ts))
			for j := range st {
				st[j] = titleStyle
			}
			marks := walkMarkers(run, r.chapter)
			switch {
			case marks != "":
				ts, st = append(ts, marks), append(st, reviewTextStyle())
			case chapterSafeToSkip(run, r.chapter):
				ts, st = append(ts, "safe to skip"), append(st, DimStyle)
			default:
				ts, st = append(ts, "no decisions"), append(st, DimStyle)
			}
			row(sel, lead, ts, st, "")
		}
	}
	return lines, cursorLine
}

// walkCardLayout is the chapter card's full content and the line each
// selectable entry sits on.
type walkCardLayout struct {
	lines      []string
	entryLines []int
}

// layoutWalkCard lays out the card for one Walkthrough row: a chapter's title,
// how it works, the decisions inside it and every file with what happened to
// it — or, for the scope row, the scope decision and what is missing.
func layoutWalkCard(run *selfreview.Run, answers map[string]selfreview.Answer, r walkRow, entries []walkEntry, cur int, focused bool, innerW int) walkCardLayout {
	var lay walkCardLayout
	tw := max(innerW-2, 1)
	add := func(s string) { lay.lines = append(lay.lines, " "+s) }
	blank := func() { lay.lines = append(lay.lines, "") }
	para := func(text string, style lipgloss.Style) {
		for _, l := range rvWrap(text, tw) {
			add(style.Render(l))
		}
	}
	// entry draws one selectable row; the highlight is a band like the
	// list's, and only while the card has the keyboard.
	entry := func(k int, lead, text, right string, textStyle, rightStyle lipgloss.Style) {
		lay.entryLines = append(lay.entryLines, len(lay.lines))
		sel := k == cur && focused
		rw := lipgloss.Width(right)
		textW := max(tw-3-lipgloss.Width(lead)-rw-1, 6)
		for j, l := range rvWrap(text, textW) {
			ld, rt := lead, right
			if j > 0 {
				ld, rt = strings.Repeat(" ", lipgloss.Width(lead)), ""
			}
			if sel {
				mark := " "
				if j == 0 {
					mark = SelectionMarker(true).Render("▸")
				}
				lay.lines = append(lay.lines, " "+mark+" "+SelectionBand().Render(ld+rvFit(l, textW)+" ")+SelectionBandSecondary().Render(rvFit(rt, rw)))
				continue
			}
			lay.lines = append(lay.lines, "   "+textStyle.Render(ld+rvFit(l, textW)+" ")+rightStyle.Render(rvFit(rt, rw)))
		}
	}

	blank()
	switch r.kind {
	case walkScope:
		s := r.stop
		para("Whether this is the right change at all: what was asked and isn't done here.", DimStyle)
		blank()
		entry(0, "◆"+strconv.Itoa(r.n)+" ", s.Title, reviewAnswerGlyph(answers[s.ID].Kind), reviewBoldStyle(), ModeOn())
		blank()
		para(s.Decide, reviewTextStyle())
		if len(s.Missing) > 0 {
			blank()
			add(DimStyle.Render("Missing"))
			for _, m := range s.Missing {
				for j, l := range rvWrap(m, max(tw-4, 4)) {
					lead := "  · "
					if j > 0 {
						lead = "    "
					}
					add(DimStyle.Render(lead) + reviewTextStyle().Render(l))
				}
			}
		} else if s.Why != "" {
			blank()
			para(s.Why, reviewTextStyle())
		}
		blank()
		para("⏎ opens it on Decide.", DimStyle)
		return lay
	}

	c := r.chapter
	para(c.Title, reviewBoldStyle())
	blank()
	switch {
	case c.Text != "":
		para(c.Text, reviewTextStyle())
	case c.ByFleet:
		para("The reviewer didn't place these files in a chapter; fleet lists them here so every changed file is somewhere.", DimStyle)
	}
	k := 0
	blank()
	var decisions, files []walkEntry
	for _, e := range entries {
		if e.kind == walkEntryDecision {
			decisions = append(decisions, e)
		} else {
			files = append(files, e)
		}
	}
	if len(decisions) == 0 {
		add(DimStyle.Render("No decisions in this chapter."))
	} else {
		add(DimStyle.Render("Decisions here"))
		for _, e := range decisions {
			entry(k, "◆"+strconv.Itoa(e.n)+" ", e.title, reviewAnswerGlyph(answers[e.id].Kind), reviewTextStyle(), ModeOn())
			k++
		}
	}
	blank()
	add(DimStyle.Render("Files · " + strconv.Itoa(len(files))))
	seen := map[string]int{}
	for _, e := range files {
		seen[path.Base(e.path)]++
	}
	fileName := func(p string) string {
		if name := path.Base(p); seen[name] <= 1 {
			return name
		}
		return path.Join(path.Base(path.Dir(p)), path.Base(p))
	}
	// One name column for every file, so the fates line up.
	nameW := 0
	for _, e := range files {
		nameW = max(nameW, lipgloss.Width(fileName(e.path)))
	}
	nameW = min(nameW, max(tw*2/5, 12))
	for _, e := range files {
		name := fileName(e.path)
		fate := reviewFateOf(run, answers, e.path)
		lay.entryLines = append(lay.entryLines, len(lay.lines))
		sel := k == cur && focused
		lead := fate.glyph + " "
		head := rvPath(name, nameW)
		statusW := max(tw-3-lipgloss.Width(lead)-nameW-2, 10)
		status := rvWrap(fate.text, statusW)
		fateStyle := DimStyle
		if fate.warn {
			fateStyle = reviewWarnStyle()
		}
		for j, st := range status {
			left := rvFit(head, nameW)
			g := lead
			if j > 0 {
				left, g = strings.Repeat(" ", nameW), "  "
			}
			if sel {
				mark := " "
				if j == 0 {
					mark = SelectionMarker(true).Render("▸")
				}
				lay.lines = append(lay.lines, " "+mark+" "+SelectionBand().Render(g+left+"  ")+SelectionBandSecondary().Render(rvFit(st, statusW)))
				continue
			}
			gs := strings.Repeat(" ", lipgloss.Width(g))
			if j == 0 {
				gs = fate.style.Render(fate.glyph) + " "
			}
			lay.lines = append(lay.lines, "   "+gs+reviewTextStyle().Render(left+"  ")+fateStyle.Render(rvFit(st, statusW)))
		}
		k++
	}
	return lay
}

// renderWalkCard frames the card for the selected Walkthrough row, scrolled
// to keep the entry the keyboard is on in view.
func (v *ReviewView) renderWalkCard(w, h int) string {
	r, ok := v.selectedWalkRow()
	if !ok {
		return RenderBorderedPanelInsets("", " ", "", "", "", w, h, false)
	}
	entries := v.walkEntries(r)
	v.walkItem = clampInt(v.walkItem, 0, max(len(entries)-1, 0))
	lay := layoutWalkCard(v.d.Run, v.d.Answers, r, entries, v.walkItem, v.cardFocus, max(w-2, 1))
	viewH := max(h-2, 1)
	if v.cardFocus && v.walkItem < len(lay.entryLines) {
		at := lay.entryLines[v.walkItem]
		if at < v.walkCardScroll {
			v.walkCardScroll = at
		} else if at >= v.walkCardScroll+viewH {
			v.walkCardScroll = at - viewH + 1
		}
	}
	v.walkCardScroll = clampInt(v.walkCardScroll, 0, max(len(lay.lines)-viewH, 0))
	vis := lay.lines[v.walkCardScroll:min(v.walkCardScroll+viewH, len(lay.lines))]

	accent := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	var title string
	switch r.kind {
	case walkScope:
		title = accent.Render("Scope") + DimStyle.Render(" · decision "+strconv.Itoa(r.n))
	default:
		title = accent.Render(fmt.Sprintf("Chapter %d of %d", r.n, len(v.d.Run.Chapters))) +
			DimStyle.Render(" · "+walkRoleWords(r.chapter.Role))
	}
	return RenderBorderedPanelInsets(strings.Join(vis, "\n"), title, "", "", "", w, h, v.cardFocus)
}
