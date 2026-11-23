package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/selfreview"
)

// The Review view's footer and its `?` overlay.
//
// The footer is one line of at most a handful of keys — what the row under the
// highlight can do, Enter's meaning first among them — plus the way to the
// rest: `? keys`. Everything else lives in the overlay, so the footer never
// has to choose between being complete and being readable.

// reviewKey is one entry of the footer. why is set when the key cannot act
// right now: the entry renders dim with the reason, and the handler refuses on
// exactly the same clause (both read the same blocker).
type reviewKey struct {
	key, label, why string
	// short replaces label when the full row does not fit.
	short string
	// prio decides what goes first when the row does not fit: the highest
	// number drops first. 0 never drops.
	prio int
	// right puts the key in the right-aligned group (the way out: S, ? keys).
	right bool
}

// footerKeys is the footer for the view's current state. It always names what
// Enter does now, and changes as the highlight moves.
func (v *ReviewView) footerKeys() []reviewKey {
	help := reviewKey{key: "?", label: "keys", right: true}
	esc := reviewKey{key: "esc", label: "back", prio: 3}
	switch {
	case v.helpOpen:
		return []reviewKey{{key: "esc", label: "close"}}
	case v.d.Run == nil && v.d.Freshness == ReviewFailed:
		return []reviewKey{{key: "r", label: "try again"}, {key: "esc", label: "back"}}
	case v.d.Run == nil:
		return []reviewKey{{key: "esc", label: "back"}}
	case v.screen == reviewScreenDiff:
		back := reviewKey{key: "esc", label: "back"}
		if len(v.back) > 0 {
			back.label, back.short = "back to the chapter", "back"
		}
		return []reviewKey{{key: "j/k", label: "scroll", prio: 2}, {key: "J/K", label: "next/prev change", short: "change", prio: 1},
			back, help}
	case v.tab == reviewTabWalk:
		return v.walkFooterKeys(help, esc)
	case v.screen == reviewScreenSkipped:
		_, ok := v.selectedFile()
		raise := reviewKey{key: "x", label: "review it myself", short: "raise", why: reviewWhy(!ok || v.sent() || v.approved(), "not now"), prio: 1}
		if v.cardFocus {
			return []reviewKey{{key: "j/k", label: "scroll", prio: 3}, {key: "J/K", label: "next/prev change", short: "change", prio: 2},
				{key: "e", label: "more context", short: "more", why: v.expandBlocker(), prio: 4}, raise,
				{key: "esc", label: "list"}, help}
		}
		return []reviewKey{{key: "⏎", label: "read code", short: "code"}, raise, {key: "s", label: "back"}, help}
	case v.finished():
		switch {
		case v.sent():
			return []reviewKey{{key: "v", label: "round 2", why: v.roundBlocker()}, {key: "d", label: "full diff", short: "diff", prio: 2},
				{key: "esc", label: "back"}, help}
		case v.approved():
			return []reviewKey{{key: "r", label: "review again", why: v.regenerateBlocker()}, {key: "esc", label: "back"}, help}
		}
		ks := []reviewKey{{key: "A", label: "approve", why: v.approveBlocker()}}
		if v.hasSkipped() {
			ks = append(ks, reviewKey{key: "s", label: "why skipped", prio: 2})
		}
		if v.d.Doc != nil {
			ks = append(ks, reviewKey{key: "d", label: "read anyway", prio: 2})
		}
		return append(ks, esc, help)
	}

	_, ansWhy := v.answerTarget()
	// y keeps the code, n changes it: the two answers the card's
	// recommendation names, worded the same way.
	answers := []reviewKey{
		{key: "y", label: "keep", why: ansWhy, prio: 1},
		{key: "n", label: "change…", why: ansWhy, prio: 1},
		{key: "a", label: "ask agent…", short: "ask…", why: ansWhy, prio: 1},
		{key: "l", label: "later", why: ansWhy, prio: 2},
	}
	if v.cardFocus {
		return append(answers,
			reviewKey{key: "J/K", label: "next/prev change", short: "change", prio: 4},
			reviewKey{key: "e", label: "more context", short: "more", why: v.expandBlocker(), prio: 5},
			reviewKey{key: "esc", label: "list", prio: 3}, help)
	}
	it, _ := v.selectedItem()
	var keys []reviewKey
	switch it.kind {
	case rowFinish:
		if v.counts().Acting() == 0 {
			return []reviewKey{{key: "⏎", label: "approve", why: v.approveBlocker()}, esc, help}
		}
		send := reviewKey{key: "⏎", label: "send", why: v.sendBlocker()}
		if t, ok := v.target(); ok && send.why == "" && !v.startsSession() {
			send.label, send.short = "send to "+t.name(), "send"
		}
		return []reviewKey{send,
			{key: "A", label: "approve instead", short: "approve", why: v.approveBlocker(), prio: 2}, esc, help}
	case rowNotReviewed:
		keys = []reviewKey{{key: "⏎", label: "see the files", short: "files"},
			{key: "r", label: "review again", why: v.regenerateBlocker(), prio: 2}}
	case rowSafeToSkip:
		keys = []reviewKey{{key: "⏎", label: "see the files", short: "files"}}
	default:
		enter := reviewKey{key: "⏎", label: "read code", short: "code"}
		if it.stop != nil && len(it.stop.Hunks) == 0 {
			// The scope decision has no code: the card already says it all.
			if v.narrow() {
				enter.label, enter.short = "details", ""
			} else {
				enter.why = "no code"
			}
		}
		keys = append(answers, enter)
	}
	if len(v.back) > 0 {
		// Arrived here from a chapter: say where esc goes, since it no
		// longer closes the review.
		keys = append(keys, reviewKey{key: "esc", label: "back to the chapter", short: "back", prio: 2})
	}
	return append(append(keys, v.sendKey()), help)
}

// walkFooterKeys is the Walkthrough tab's footer: what ⏎ does on the row the
// keyboard is on — read a chapter, open a decision on Decide, open a file's
// diff — and the way back.
func (v *ReviewView) walkFooterKeys(help, esc reviewKey) []reviewKey {
	r, ok := v.selectedWalkRow()
	if !v.cardFocus {
		enter := reviewKey{key: "⏎", label: "read chapter", short: "read"}
		switch {
		case !ok:
			enter.why = "no chapters"
		case r.kind == walkScope:
			enter.label, enter.short = "decide it", "decide"
		}
		return []reviewKey{enter, {key: "j/k", label: "move", prio: 2}, {key: "esc", label: "Decide", prio: 1}, help}
	}
	enter := reviewKey{key: "⏎", label: "open"}
	if es := v.walkEntries(r); v.walkItem < len(es) {
		if es[v.walkItem].kind == walkEntryDecision {
			enter.label, enter.short = "open on Decide", "open"
		} else {
			enter.label, enter.short = "read its diff", "diff"
			if v.d.Doc == nil {
				enter.why = "no code"
			}
		}
	}
	return []reviewKey{enter, {key: "j/k", label: "move", prio: 2}, {key: "esc", label: "chapters", prio: 1}, help}
}

// sendKey is the footer's way out: S with where it goes, or A when nothing
// answered asks the agent for anything.
func (v *ReviewView) sendKey() reviewKey {
	c := v.counts()
	if c.Answered+c.AddedByYou > 0 && c.Acting() == 0 {
		return reviewKey{key: "A", label: "approve", why: v.approveBlocker(), right: true}
	}
	k := reviewKey{key: "S", label: "send", why: v.sendBlocker(), right: true}
	if k.why == "" {
		switch {
		case v.startsSession():
			g := v.d.NewSessionGlyph
			if g != "" {
				g += " "
			}
			k.label, k.short = "send to a new "+g+"session", "send"
		default:
			if t, ok := v.target(); ok {
				k.label, k.short = "send to "+t.name(), "send"
			}
		}
	}
	return k
}

func reviewWhy(cond bool, why string) string {
	if cond {
		return why
	}
	return ""
}

// renderReviewFooter lays the keys out on one row of exactly w cells: the
// left group, then the right group flush right. When the row does not fit it
// first takes the short labels, then drops the lowest-priority entries
// (highest prio). The order on screen never changes, so a key that survives
// stays where it was.
func renderReviewFooter(keys []reviewKey, w int) string {
	const sep = "  "
	short := false
	part := func(k reviewKey) string {
		label := k.label
		if short && k.short != "" {
			label = k.short
		}
		if k.why != "" {
			return DimStyle.Render(k.key + " " + label + " (" + k.why + ")")
		}
		return HelpKeyStyle.Render(k.key) + " " + HelpDescStyle.Render(label)
	}
	render := func(ks []reviewKey) (string, int) {
		var left, right []string
		for _, k := range ks {
			if k.right {
				right = append(right, part(k))
			} else {
				left = append(left, part(k))
			}
		}
		l := " " + strings.Join(left, sep)
		r := strings.Join(right, sep) + " "
		gap := w - lipgloss.Width(l) - lipgloss.Width(r)
		if len(right) == 0 {
			return l, lipgloss.Width(l)
		}
		need := lipgloss.Width(l) + 3 + lipgloss.Width(r)
		return l + strings.Repeat(" ", max(gap, 3)) + r, need
	}
	ks := append([]reviewKey(nil), keys...)
	if _, n := render(ks); n > w {
		short = true
	}
	for {
		s, n := render(ks)
		if n <= w {
			return padLines([]string{s}, 1, w)[0]
		}
		worst := -1
		for i, k := range ks {
			if k.prio > 0 && (worst < 0 || k.prio >= ks[worst].prio) {
				worst = i
			}
		}
		if worst < 0 {
			return padLines([]string{ansi.Truncate(s, w, "")}, 1, w)[0]
		}
		ks = append(ks[:worst], ks[worst+1:]...)
	}
}

// renderFooterLine is the bottom row: the key footer, a y/n question while one
// is open, or the note input.
func (v *ReviewView) renderFooterLine() string {
	w := v.width
	if v.confirm != nil {
		q := " " + reviewBoldStyle().Render(v.confirm.question)
		keys := HelpKeyStyle.Render("y") + " " + HelpDescStyle.Render(v.confirm.yes) + "  " +
			HelpKeyStyle.Render("n") + " " + HelpDescStyle.Render("cancel") + " "
		if lipgloss.Width(q)+3+lipgloss.Width(keys) > w {
			q = " " + reviewBoldStyle().Render(rvClip(v.confirm.question, max(w-lipgloss.Width(keys)-4, 8)))
		}
		gap := max(w-lipgloss.Width(q)-lipgloss.Width(keys), 1)
		return padLines([]string{q + strings.Repeat(" ", gap) + keys}, 1, w)[0]
	}
	if !v.inputOpen {
		return renderReviewFooter(v.footerKeys(), w)
	}
	hint := HelpKeyStyle.Render("⏎") + " " + HelpDescStyle.Render("save") + "  " +
		HelpKeyStyle.Render("esc") + " " + HelpDescStyle.Render("cancel") + " "
	if v.inputErr != "" {
		hint = ErrorStyle.Render(v.inputErr) + "  " + hint
	}
	field := ansi.Truncate(v.input.View(), max(w-lipgloss.Width(hint)-3, 1), "")
	gap := max(w-1-lipgloss.Width(field)-lipgloss.Width(hint), 1)
	return padLines([]string{" " + field + strings.Repeat(" ", gap) + hint}, 1, w)[0]
}

// ---------------------------------------------------------------------------
// The `?` overlay: every key the view has, and who reviewed it.

// reviewHelpSection is one group of the keys overlay.
type reviewHelpSection struct {
	title string
	rows  [][2]string
}

func (v *ReviewView) helpSections() []reviewHelpSection {
	send := "send your answers"
	if t, ok := v.target(); ok && !v.startsSession() {
		send = "send to " + t.name()
	}
	finish := [][2]string{{"S", send}}
	if len(v.d.Targets) > 1 {
		finish = append(finish, [2]string{"t", fmt.Sprintf("pick another session (%d)", len(v.d.Targets))})
	}
	finish = append(finish,
		[2]string{"A", "approve: nothing left to do"},
		[2]string{"r", "review again"},
		[2]string{"v", "next round, after the agent"},
		[2]string{"esc", "back"},
		[2]string{"ctrl+q", "close the review"})
	return []reviewHelpSection{
		{"Answer (1 Decide)", [][2]string{
			{"y", "keep: the code stays as is"},
			{"n", "change… (prefilled advice)"},
			{"a", "ask agent… (a question)"},
			{"l", "later (not sent)"},
		}},
		{"Read", [][2]string{
			{"1 / 2", "Decide / Walkthrough tab"},
			{"j/k", "move"},
			{"⏎", "read the code"},
			{"J/K", "next / previous change"},
			{"e", "more context"},
			{"s", "files with no decision"},
			{"x", "review a file yourself"},
			{"d", "full diff"},
		}},
		{"Walkthrough (2)", [][2]string{
			{"⏎", "read a chapter"},
			{"⏎", "on a decision: answer it"},
			{"⏎", "on a file: its diff"},
			{"esc", "back to the chapters"},
		}},
		{"Finish", finish},
	}
}

// reviewerLine is what the reviewer did, for the overlay: "opus · 61s · read
// 1 file, 3 searches · $0.51 · a1c39f7 + uncommitted".
func (v *ReviewView) reviewerLine() string {
	var parts []string
	if v.d.Model != "" {
		parts = append(parts, v.d.Model)
	}
	if e := rvElapsed(v.d.Elapsed); e != "" {
		parts = append(parts, e)
	}
	if v.d.Run != nil && v.d.Run.Reviewer != nil {
		m := v.d.Run.Reviewer
		read := m.ReadLabel()
		if n := m.Searches(); n > 0 {
			read += fmt.Sprintf(", %s", rvPlural(n, "search", "searches"))
		}
		parts = append(parts, read)
		if m.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f", m.CostUSD))
		}
	}
	if v.d.Round > 1 {
		parts = append(parts, fmt.Sprintf("round %d of %d", v.d.Round, selfreview.MaxRounds))
	}
	if v.d.Snapshot != "" {
		parts = append(parts, v.d.Snapshot)
	}
	return strings.Join(parts, " · ")
}

func (v *ReviewView) renderHelpPanel(w, h int) string {
	inner := max(w-2, 10)
	two := inner >= 76
	colW := inner
	if two {
		colW = inner / 2
	}
	secs := v.helpSections()
	// The key column fits the widest key ("ctrl+q"), so no key is ever cut to
	// "ctrl…" — a truncated chord is a key nobody can press.
	kw := 5
	for _, s := range secs {
		for _, r := range s.rows {
			kw = max(kw, ansi.StringWidth(r[0]))
		}
	}
	section := func(s reviewHelpSection) []string {
		out := []string{" " + PaletteSectionStyle.Render(s.title)}
		for _, r := range s.rows {
			out = append(out, "   "+HelpKeyStyle.Render(rvFit(r[0], kw))+" "+HelpDescStyle.Render(rvClip(r[1], colW-kw-5)))
		}
		return out
	}
	var left, right []string
	add := func(col *[]string, lines []string) {
		*col = append(*col, "")
		*col = append(*col, lines...)
	}
	add(&left, section(secs[0]))
	add(&left, section(secs[1]))
	rcol := &left
	if two {
		rcol = &right
	}
	for _, s := range secs[2:] {
		add(rcol, section(s))
	}
	if meta := v.reviewerLine(); meta != "" {
		rev := []string{" " + PaletteSectionStyle.Render("Reviewer")}
		for _, l := range rvWrap(meta, colW-5) {
			rev = append(rev, "   "+DimStyle.Render(l))
		}
		add(rcol, rev)
	}
	lines := left
	if two {
		n := max(len(left), len(right))
		lines = make([]string, n)
		l, r := padLines(left, n, colW), padLines(right, n, inner-colW)
		for i := range n {
			lines[i] = l[i] + r[i]
		}
	}
	if len(lines) > h-2 {
		lines = lines[:max(h-2, 0)]
	}
	return RenderBorderedPanelInsets(strings.Join(lines, "\n"), "Review keys", "", "", "", w, h, true)
}
