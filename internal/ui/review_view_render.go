package ui

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/selfreview"
)

// The Review view's renderers. Everything below a method is a pure function of
// its arguments, so each piece of the screen has a golden of its own.
//
// Hierarchy, top to bottom: the verdict and one status line (exceptions only:
// high risk, CI failed, code changed, a gap against what was asked); the list
// — number, title, answer glyph — then, under a rule, the files nobody
// reviewed and the files safe to skip; and the card, which leads with the one
// question the person has to answer.

// reviewLabelW is the card's label column: "Why it matters " is the widest.
const reviewLabelW = 15

func reviewTextStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorText) }
func reviewBoldStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorText).Bold(true) }
func reviewWarnStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorYellow) }
func reviewGoodStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorGreen) }

// rvFit truncates raw text to w cells and pads it to exactly w. Raw first,
// then style: padding a styled string counts its escape bytes.
func rvFit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if sw := lipgloss.Width(s); sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// rvClip fits raw text into w cells without cutting a word: it ends at the
// last whole word that fits, marked "…". A single word wider than w is cut.
func rvClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	words := strings.Fields(s)
	out := ""
	for _, word := range words {
		next := word
		if out != "" {
			next = out + " " + word
		}
		if lipgloss.Width(next)+1 > w {
			break
		}
		out = next
	}
	if out == "" {
		return ansi.Truncate(s, w, "…")
	}
	return strings.TrimRight(out, " ,;:—-") + "…"
}

// rvPath fits a path into w cells keeping its tail — the file name is the part
// that identifies it.
func rvPath(p string, w int) string {
	if lipgloss.Width(p) <= w {
		return p
	}
	if w <= 1 {
		return ansi.Truncate(p, w, "")
	}
	return ansi.TruncateLeft(p, lipgloss.Width(p)-w+1, "…")
}

func reviewBase(p string) string { return path.Base(p) }

func rvThousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func rvPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// rvElapsed is a reviewer's run time: "61s", "2m05s". Under a second reads as
// nothing, not "0s" — that is a cached or stubbed run, not a fast review.
func rvElapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	if d < 2*time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// rvWrap word-wraps raw text to w cells (hard-wrapping words longer than w).
func rvWrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	s = ansi.Hardwrap(ansi.Wordwrap(s, w, ""), w, false)
	return strings.Split(s, "\n")
}

// rvWrapMax wraps to at most n lines; a longer text ends its last line at a
// whole word, marked "…".
func rvWrapMax(s string, w, n int) []string {
	lines := rvWrap(s, w)
	if len(lines) <= n {
		return lines
	}
	rest := strings.Join(lines[n-1:], " ")
	return append(lines[:n-1], rvClip(rest, w))
}

// ---------------------------------------------------------------------------
// Answers: the words and glyphs a person sees for fine / change / ask / later.

// reviewAnswerWord is an answer kind in the person's words ("ok as is").
func reviewAnswerWord(kind string) string { return selfreview.AnswerWord(kind) }

// reviewAnswerGlyph is an answer kind as one width-1 glyph, for the list.
func reviewAnswerGlyph(kind string) string {
	switch kind {
	case selfreview.AnswerFine:
		return "✓"
	case selfreview.AnswerChange:
		return "✎"
	case selfreview.AnswerAsk:
		return "?"
	case selfreview.AnswerLater:
		return "·"
	}
	return ""
}

// reviewAnswerLabel is the answer in effect, as a mode: "✓ ok as is".
func reviewAnswerLabel(a selfreview.Answer) string {
	if a.Kind == "" {
		return ""
	}
	return ModeOn().Render(reviewAnswerGlyph(a.Kind) + " " + reviewAnswerWord(a.Kind))
}

// ---------------------------------------------------------------------------
// Layout.

// headerLines is the header box's content. On a terminal too short for all
// of it, the summary gives way whole (the Walkthrough explains the change too)
// and then the verdict is held to what fits: a body of at least a few rows
// matters more than the header's last words.
func (v *ReviewView) headerLines() []string {
	// Narrow, an open card has the screen to itself: the summary steps
	// aside for the details (it is back on the list one esc away).
	summary := !(v.narrow() && v.cardFocus)
	full := reviewHeaderContent(v.d, v.counts(), max(v.width-2, 1), summary)
	room := max(v.height-2-1-reviewMinBody, 1)
	if len(full) <= room {
		return full
	}
	short := reviewHeaderContent(v.d, v.counts(), max(v.width-2, 1), false)
	if len(short) <= room {
		return short
	}
	return append(short[:max(room-1, 0)], short[len(short)-1])
}

// reviewMinBody is the fewest body rows the header leaves.
const reviewMinBody = 6

func (v *ReviewView) bodyHeight() int { return max(v.height-len(v.headerLines())-2-1, 3) }

func (v *ReviewView) listWidth() int { return clampInt(v.width*21/50, 46, 60) }

func (v *ReviewView) cardWidth() int {
	if v.narrow() {
		return v.width
	}
	return v.width - v.listWidth()
}

func (v *ReviewView) cardInnerWidth() int { return max(v.cardWidth()-2, 1) }
func (v *ReviewView) cardViewHeight() int { return max(v.bodyHeight()-2, 1) }
func (v *ReviewView) diffViewHeight() int { return max(v.bodyHeight()-2, 1) }

// ---------------------------------------------------------------------------
// View.

// View renders the whole screen at exactly width × height.
func (v *ReviewView) View() string {
	w, h := v.width, v.height
	if w < 30 || h < 10 {
		return DimStyle.Render(ansi.Truncate("review: terminal too small", max(w, 0), ""))
	}
	if v.helpOpen {
		// Every key the view has: the whole screen, not what the header
		// leaves of it.
		return v.renderHelpPanel(w, h-1) + "\n" + v.renderFooterLine()
	}
	head := v.headerLines()
	header := RenderBorderedPanelTopRight(strings.Join(head, "\n"),
		v.headerTitle(), v.headerRight(), w, len(head)+2, false)
	return header + "\n" + v.renderBody() + "\n" + v.renderFooterLine()
}

func (v *ReviewView) renderBody() string {
	w, bh := v.width, v.bodyHeight()
	switch {
	case v.helpOpen:
		return v.renderHelpPanel(w, bh)
	case v.screen == reviewScreenDiff:
		return v.renderDiffPanel(w, bh)
	case v.d.Run == nil:
		return strings.Join(padLines(reviewEmptyBody(v.d, w), bh, w), "\n")
	case v.tab == reviewTabWalk:
		return v.renderWalk(w, bh)
	case v.screen == reviewScreenDecisions && v.finished():
		return strings.Join(padLines(v.closingBody(w), bh, w), "\n")
	}
	listFocus := !v.cardFocus && !v.inputOpen
	var left string
	if v.screen == reviewScreenSkipped {
		lines, cur := renderReviewFileList(v.d.Run, v.d.Answers, v.skipCursor, listFocus, v.listInner())
		title := "Safe to skip · " + rvPlural(v.counts().SafeToSkip, "file", "files")
		if c := v.counts(); c.NotReviewed > 0 {
			title = "Files with no decision · " + strconv.Itoa(c.NotReviewed+c.SafeToSkip)
		}
		left = v.listPanel(lines, cur, &v.skipScroll, title, listFocus)
	} else {
		lines, cur := renderReviewList(v.listInput(), v.cursor, listFocus, v.listInner(), v.narrow())
		left = v.listPanel(lines, cur, &v.listScroll, "Decide", listFocus)
	}
	if v.narrow() {
		if v.cardFocus {
			return v.renderCardPanel(w, bh)
		}
		return left
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, v.renderCardPanel(v.cardWidth(), bh))
}

// listInput gathers what the decision list draws.
func (v *ReviewView) listInput() reviewListInput {
	in := reviewListInput{items: v.items(), answers: v.d.Answers, run: v.d.Run, counts: v.counts(),
		pending: reviewPendingNow(v.d.Run, v.d.CI)}
	in.finish = v.finishLabel()
	return in
}

func (v *ReviewView) listInner() int {
	if v.narrow() {
		return v.width
	}
	return v.listWidth() - 2
}

// listPanel scrolls a list to keep its cursor line visible and frames it: a
// bordered panel when wide, bare rows when narrow (the list is the only thing
// on screen, so a border would only cost columns).
func (v *ReviewView) listPanel(lines []string, cur int, scroll *int, title string, focused bool) string {
	bh := v.bodyHeight()
	viewH := bh
	if !v.narrow() {
		viewH = bh - 2
	}
	if cur >= 0 {
		if cur < *scroll {
			*scroll = cur
		} else if cur >= *scroll+viewH {
			*scroll = cur - viewH + 1
		}
	}
	*scroll = clampInt(*scroll, 0, max(len(lines)-viewH, 0))
	vis := lines[*scroll:min(*scroll+viewH, len(lines))]
	if v.narrow() {
		return strings.Join(padLines(vis, bh, v.width), "\n")
	}
	return RenderBorderedPanelInsets(strings.Join(vis, "\n"), title, "", "", "", v.listWidth(), bh, focused)
}

func (v *ReviewView) renderCardPanel(w, h int) string {
	lay := v.cardLayout()
	v.cardScroll = clampInt(v.cardScroll, 0, max(len(lay.lines)-v.cardViewHeight(), 0))
	end := min(v.cardScroll+v.cardViewHeight(), len(lay.lines))
	vis := lay.lines[v.cardScroll:end]

	title, right := "", ""
	if c, ok := v.cardSubject(); ok {
		title = v.cardTitle(c, w)
		if c.answerable {
			right = reviewAnswerLabel(v.d.Answers[c.id])
		}
	}
	if title == "" {
		title = " "
	}
	return RenderBorderedPanelInsets(strings.Join(vis, "\n"), title, right, "", "", w, h, v.cardFocus && !v.inputOpen)
}

// cardTitle is the card's top inset: "2 of 4 · product call" (the kind dim),
// or what the row is. A narrow card has no list beside it, so it carries the
// decision's own title as well.
func (v *ReviewView) cardTitle(c reviewCard, w int) string {
	accent := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	switch c.kind {
	case rowDecision:
		head := fmt.Sprintf("%d of %d", c.n, len(v.d.Run.Stops))
		if c.kindPhrase != "" {
			return accent.Render(head) + DimStyle.Render(" · "+c.kindPhrase)
		}
		return accent.Render(head)
	case rowAddedByYou:
		return accent.Render("added by you")
	case rowNotReviewed, rowNotReviewedFile:
		return reviewWarnStyle().Bold(true).Render("not reviewed")
	case rowFinish:
		if v.counts().Acting() == 0 {
			return accent.Render("ready to approve")
		}
		return accent.Render("ready to send")
	case rowSafeToSkip:
		return accent.Render("safe to skip")
	case rowSkippedFile:
		return accent.Render(c.kindPhrase)
	}
	return ""
}

func padLines(lines []string, h, w int) []string {
	out := make([]string, h)
	for i := range h {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		if lw := lipgloss.Width(l); lw > w {
			l = ansi.Truncate(l, w, "")
		} else if lw < w {
			l += strings.Repeat(" ", w-lw)
		}
		out[i] = l
	}
	return out
}

// ---------------------------------------------------------------------------
// Header.

// reviewHeaderTitle is "Review · <branch>", trimmed to leave the right inset
// its room.
func reviewHeaderTitle(d ReviewData, w int) string {
	title := "Review"
	if d.CheckoutLabel != "" {
		title += " · " + rvClip(d.CheckoutLabel, max(w-40, 12))
	}
	return title
}

// reviewTabNames are the tabs as the title bar shows them, key first.
var reviewTabNames = []string{"1 Decide", "2 Walkthrough"}

// headerTitle is the title bar's left: "Review · <branch>", then the two tabs.
// Switching tab moves the keyboard to that tab's list, so the active one is a
// selection (a pill), not a mode (docs/design-system.md). The branch gives way
// first: the tabs are how you get around.
func (v *ReviewView) headerTitle() string {
	if v.d.Run == nil {
		return reviewHeaderTitle(v.d, v.width)
	}
	var tabs []string
	for i, name := range reviewTabNames {
		if reviewTab(i) == v.tab {
			tabs = append(tabs, SelectionPill(true).Render(" "+name+" "))
		} else {
			tabs = append(tabs, HelpKeyStyle.Render(name[:1])+DimStyle.Render(name[1:]))
		}
	}
	tabStr := strings.Join(tabs, "  ")
	accent := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	right := lipgloss.Width(v.headerRight())
	room := v.width - 8 - right - lipgloss.Width(tabStr) - 3
	title := "Review"
	if v.d.CheckoutLabel != "" && room >= 18 {
		title += " · " + rvClip(v.d.CheckoutLabel, room-len("Review · "))
	}
	return accent.Render(title) + "   " + tabStr
}

// headerRight is the progress — "0 of 4 answered" — or where the run stands
// once it is past answering. Pre-styled; the panel passes it through.
func (v *ReviewView) headerRight() string {
	switch {
	case v.d.Run == nil:
		return ""
	case v.approved():
		return reviewGoodStyle().Render("✓ approved")
	case v.sent():
		return DimStyle.Render("sent")
	}
	if p := v.counts().Progress(); p != "" {
		return DimStyle.Render(p)
	}
	return ""
}

// reviewHeaderContent is the header box's inner lines: the verdict, one status
// line, and — only when they apply — the gap against what was asked and how
// the previous round's changes went. The line count depends on the run alone,
// so the box never grows mid-keystroke.
func reviewHeaderContent(d ReviewData, c selfreview.Counts, innerW int, summary bool) []string {
	tw := max(innerW-2, 1)
	line := func(s string) string { return " " + s }
	run := d.Run
	if run == nil {
		// One line: the body below says the rest.
		if d.Freshness == ReviewFailed {
			return []string{line(ErrorStyle.Bold(true).Render("Review failed"))}
		}
		return []string{line(reviewTextStyle().Render("Preparing a review…"))}
	}
	var out []string
	verdict := run.Verdict
	if verdict == "" {
		verdict = "The reviewer gave no verdict."
	}
	// Prose wraps; it is never cut. A verdict or summary that ends in "…"
	// hides exactly the words a person needed.
	for _, l := range rvWrap(verdict, tw) {
		out = append(out, line(reviewBoldStyle().Render(l)))
	}
	if sum := run.Summary; sum != "" && summary {
		const label = "What it does: "
		for i, l := range rvWrap(label+sum, tw) {
			if i == 0 {
				l = DimStyle.Render(label) + reviewTextStyle().Render(strings.TrimPrefix(l, label))
			} else {
				l = reviewTextStyle().Render(l)
			}
			out = append(out, line(l))
		}
	}
	// What was asked and isn't done is a decision (the scope decision, listed
	// first), never a banner here: nothing the person can't act on is shown
	// as an alert. The status line points at it.
	out = append(out, line(reviewStatusLine(d, c, tw)))
	if p := reviewPrevLine(d.Round, run.Prev); p != "" {
		style := DimStyle
		if strings.Contains(p, "not done") {
			style = reviewWarnStyle()
		}
		out = append(out, line(style.Render(rvClip(p, tw))))
	}
	return out
}

// reviewFailText is a failure in plain words, ending in a full stop.
func reviewFailText(reason string) string {
	reason = strings.TrimSpace(strings.SplitN(reason, "\n", 2)[0])
	switch {
	case reason == "":
		return "No reason was given."
	case strings.Contains(reason, "holds no review JSON"), strings.Contains(reason, "couldn't be read"):
		return "The reviewer's answer couldn't be read."
	}
	r := []rune(reason)
	s := strings.ToUpper(string(r[:1])) + string(r[1:])
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// reviewPrevLine is round ≥2's account of the last round's changes:
// "Your 2 changes from round 1: 1 done, 1 not done yet".
func reviewPrevLine(round int, prev []selfreview.Followup) string {
	if len(prev) == 0 {
		return ""
	}
	done := 0
	for _, f := range prev {
		if f.Status == "addressed" {
			done++
		}
	}
	head := fmt.Sprintf("Your %s from round %d: ", rvPlural(len(prev), "change", "changes"), max(round-1, 1))
	switch {
	case done == len(prev):
		return head + "done"
	case done == 0:
		return head + "not done yet"
	}
	return head + fmt.Sprintf("%d done, %d not done yet", done, len(prev)-done)
}

// reviewStatusPart is one segment of the status line.
type reviewStatusPart struct {
	text  string
	style lipgloss.Style
	prio  int // 0 never drops; higher drops first
}

// reviewStatusLine is "106 of 1,489 changed lines need you · 14 files safe to
// skip · CI ✓", led by the exceptions that need a person's eye: high risk, CI
// failed, the code changed since the review. Lower-priority segments drop
// whole rather than wrap or cut mid-word.
func reviewStatusLine(d ReviewData, c selfreview.Counts, w int) string {
	var parts []reviewStatusPart
	if d.Run != nil && d.Run.Risk == "high" {
		// Information, not an alert: the decisions are what to act on.
		parts = append(parts, reviewStatusPart{"high risk", reviewBoldStyle(), 0})
	}
	if d.CI == selfreview.CIFailure {
		t := "✕ CI failed"
		if len(d.CIFailed) > 0 {
			t += ": " + strings.Join(d.CIFailed, ", ")
		}
		parts = append(parts, reviewStatusPart{t, ErrorStyle, 0})
	}
	stale := d.Freshness == ReviewStale
	if stale {
		parts = append(parts, reviewStatusPart{"⚠ code changed since this review: r to refresh", reviewWarnStyle(), 0})
	}
	if d.Status != reviewStatusSent && d.Status != reviewStatusApproved && c.NeedLines > 0 {
		parts = append(parts, reviewStatusPart{c.LinesNeedYou(), reviewTextStyle(), 1})
	}
	if c.NotReviewed > 0 {
		parts = append(parts, reviewStatusPart{c.NotReviewedLabel(), reviewWarnStyle(), 2})
	}
	if c.SafeToSkip > 0 {
		parts = append(parts, reviewStatusPart{c.SafeToSkipLabel(), DimStyle, 3})
	}
	if c.Scope > 0 && d.Status != reviewStatusSent && d.Status != reviewStatusApproved {
		parts = append(parts, reviewStatusPart{"scope: decision " + strconv.Itoa(c.Scope), DimStyle, 2})
	}
	switch d.CI {
	case selfreview.CISuccess:
		parts = append(parts, reviewStatusPart{"CI ✓", reviewGoodStyle(), 4})
	case selfreview.CIPending:
		parts = append(parts, reviewStatusPart{"CI running", DimStyle, 4})
	}
	render := func(ps []reviewStatusPart) (string, int) {
		var raw []string
		var styled []string
		for _, p := range ps {
			raw = append(raw, p.text)
			styled = append(styled, p.style.Render(p.text))
		}
		return strings.Join(styled, DimStyle.Render(" · ")), lipgloss.Width(strings.Join(raw, " · "))
	}
	for {
		s, sw := render(parts)
		if sw <= w {
			return s
		}
		worst := -1
		for i, p := range parts {
			if p.prio > 0 && (worst < 0 || p.prio >= parts[worst].prio) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		parts = append(parts[:worst], parts[worst+1:]...)
	}
	if stale {
		// The exceptions alone overflow: say the change in fewer words.
		for i := range parts {
			if strings.HasPrefix(parts[i].text, "⚠") {
				parts[i].text = "⚠ code changed: r to refresh"
			}
		}
	}
	s, _ := render(parts)
	return ansi.Truncate(s, w, "…")
}

// reviewEmptyBody is the body while there is no run to show: preparing, or
// the last attempt failed.
func reviewEmptyBody(d ReviewData, w int) []string {
	tw := max(w-4, 1)
	out := []string{""}
	add := func(text string, style lipgloss.Style) {
		for _, l := range rvWrap(text, tw) {
			out = append(out, "  "+style.Render(l))
		}
	}
	if d.Freshness == ReviewFailed {
		add(reviewFailText(d.FailReason), reviewTextStyle())
		out = append(out, "")
		add("Press r to try again, or esc to go back.", DimStyle)
		return out
	}
	add(ReviewGlyph+" A reviewer is reading the changes in this checkout.", reviewTextStyle())
	out = append(out, "")
	add("It usually takes a minute or two. You can close this; the Review row in the sidebar says when it's ready.", DimStyle)
	return out
}

// closingBody is the body once nothing is left to answer: the answers were
// sent, the run was approved, or there was nothing for a person in it.
func (v *ReviewView) closingBody(w int) []string {
	tw := max(w-6, 1)
	c := v.counts()
	out := []string{""}
	add := func(text string, style lipgloss.Style) {
		for _, l := range rvWrap(text, tw) {
			out = append(out, "   "+style.Render(l))
		}
	}
	blank := func() { out = append(out, "") }

	switch {
	case v.approved():
		add("✓ You approved this review.", reviewGoodStyle().Bold(true))
		blank()
		add("Nothing is left to do here. Press r to review the checkout again after more changes.", DimStyle)
	case v.sent():
		t, _ := v.target()
		who := t.name()
		if who == "" {
			who = "the session"
		}
		add("✓ Sent to "+who+": "+c.AnswerSummary()+".", reviewGoodStyle().Bold(true))
		blank()
		next := "round " + strconv.Itoa(max(v.d.Round, 1)+1)
		if v.d.AgentWorking {
			out = append(out, "   "+lipgloss.NewStyle().Foreground(ColorGreen).Render("●")+" "+
				reviewTextStyle().Render("The agent is working on your answers."))
			add("When it finishes, press v to review what it changed ("+next+").", DimStyle)
		} else {
			add("The agent has stopped. Press v to review what it changed ("+next+").", reviewTextStyle())
		}
		if items := v.sentItems(); len(items) > 0 {
			blank()
			add("What you sent:", DimStyle)
			out = append(out, items...)
		}
	default:
		add("✓ Nothing needs you.", reviewGoodStyle().Bold(true))
		blank()
		if c.SafeToSkip > 0 {
			add(fmt.Sprintf("The reviewer found no decision for you. %s are safe to skip; s shows why.",
				rvPlural(c.SafeToSkip, "file", "files")), reviewTextStyle())
		} else {
			add("The reviewer found no decision for you.", reviewTextStyle())
		}
		blank()
		add("A approves it. d shows the full diff if you want to read it anyway.", DimStyle)
	}
	return out
}

// sentItems lists the changes and questions a send carried: the decision,
// then the person's own words under it.
func (v *ReviewView) sentItems() []string {
	var out []string
	tw := max(v.width-12, 10)
	for _, it := range v.items() {
		if !it.answerable() {
			continue
		}
		a := v.d.Answers[it.id]
		if a.Kind != selfreview.AnswerChange && a.Kind != selfreview.AnswerAsk {
			continue
		}
		title := it.title
		if it.kind == rowAddedByYou {
			title = reviewBase(it.path)
		}
		out = append(out, "     "+DimStyle.Render(reviewAnswerGlyph(a.Kind)+" "+rvClip(title, tw)))
		for _, l := range rvWrap(a.Note, tw) {
			out = append(out, "       "+reviewTextStyle().Render(l))
		}
	}
	return out
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

// ---------------------------------------------------------------------------
// The list.

// reviewListInput is what the decision list draws.
type reviewListInput struct {
	items   []reviewItem
	answers map[string]selfreview.Answer
	run     *selfreview.Run
	counts  selfreview.Counts
	finish  string // the finish row's label
	pending []string
}

// reviewPendingNow is the run's "not verified yet" list as it stands now: a
// line about CI is dropped once fleet's live rollup has finished, since "CI
// running" under a header that says CI failed (or passed) contradicts it.
func reviewPendingNow(run *selfreview.Run, ci string) []string {
	if run == nil {
		return nil
	}
	if ci != selfreview.CISuccess && ci != selfreview.CIFailure {
		return run.Pending
	}
	var out []string
	for _, p := range run.Pending {
		if !reviewMentionsCI(p) {
			out = append(out, p)
		}
	}
	return out
}

func reviewMentionsCI(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	}) {
		if w == "ci" || w == "checks" {
			return true
		}
	}
	return false
}

// renderReviewList draws the rows: the decisions (number, title, answer
// glyph; narrow also the question under each title), the files you raised,
// the finish row, a rule, then the files nobody reviewed and the safe-to-skip
// row. cursorLine is the line the highlight sits on, for scrolling.
func renderReviewList(in reviewListInput, cursor int, focused bool, w int, narrow bool) (lines []string, cursorLine int) {
	cursorLine = -1
	numW := len(strconv.Itoa(max(len(in.run.Stops), 1)))
	glyphW := 2 // the answer glyph and a space
	if len(in.run.Stops) == 0 {
		lines = append(lines, "  "+DimStyle.Render("No decisions for you."))
	}
	ruled := false
	rule := func() {
		if !ruled {
			lines = append(lines, " "+DimStyle.Render(strings.Repeat("─", max(w-2, 1))))
			ruled = true
		}
	}
	// Narrow, the card is a keypress away, so each row also says what the
	// reviewer recommends: "rec: keep" / "rec: change".
	recW := 0
	if narrow {
		for _, it := range in.items {
			if it.stop != nil && it.stop.Recommend != nil {
				recW = len("rec: change")
			}
		}
	}
	for i, it := range in.items {
		if i == cursor {
			cursorLine = len(lines)
		}
		sel := i == cursor
		a := in.answers[it.id]
		switch it.kind {
		case rowDecision, rowAddedByYou:
			num := fmt.Sprintf(" %*d  ", numW, it.n)
			title := it.title
			if it.kind == rowAddedByYou {
				num = " " + strings.Repeat(" ", numW-1) + "+  "
				title = rvPath(it.path, max(w-1-len(num)-glyphW-15, 8)) + " (added by you)"
			}
			rec := ""
			if recW > 0 && it.stop != nil && it.stop.Recommend != nil {
				rec = "rec: " + it.stop.Recommend.Answer
			}
			titleW := max(w-1-lipgloss.Width(num)-glyphW-1-recW, 4)
			if recW > 0 {
				titleW-- // a space between the title and the marker
			}
			// A title wraps rather than loses its end: it is the consequence,
			// and the last words are usually the point.
			for j, tl := range rvWrap(title, titleW) {
				lead := num
				if j > 0 {
					lead = strings.Repeat(" ", lipgloss.Width(num))
				}
				cell := rvFit(tl, titleW) + " "
				g, r := "", ""
				if j == 0 {
					g, r = reviewAnswerGlyph(a.Kind), rec
				}
				if recW > 0 {
					cell += rvFit(r, recW+1)
				}
				if sel {
					mark := " "
					if j == 0 {
						mark = SelectionMarker(focused).Render("▸")
					}
					lines = append(lines, mark+SelectionBand().Render(lead+cell)+SelectionBandSecondary().Render(rvFit(g, glyphW)))
					continue
				}
				gs := rvFit("", glyphW)
				if g != "" {
					gs = ModeOn().Render(g) + " "
				}
				titleStyle := reviewTextStyle()
				if a.Kind == "" {
					titleStyle = reviewBoldStyle()
				}
				if recW > 0 {
					body := rvFit(tl, titleW) + " "
					lines = append(lines, " "+DimStyle.Render(lead)+titleStyle.Render(body)+DimStyle.Render(rvFit(r, recW+1))+gs)
					continue
				}
				lines = append(lines, " "+DimStyle.Render(lead)+titleStyle.Render(cell)+gs)
			}
			if narrow && it.kind == rowDecision && it.stop != nil && it.stop.Decide != "" {
				indent := 1 + lipgloss.Width(num)
				for _, l := range rvWrap(it.stop.Decide, max(w-indent-2, 10)) {
					lines = append(lines, strings.Repeat(" ", indent)+DimStyle.Render(l))
				}
			}
		case rowFinish:
			// Two lines: what is done, then the one button that finishes it.
			// The button is the primary action — the thing Enter presses
			// while the highlight is on it — so it fills; elsewhere it is a
			// bold label, and the selection is the only fill on screen.
			lines = append(lines, "", "  "+reviewGoodStyle().Render("✓")+" "+
				reviewTextStyle().Render(fmt.Sprintf("All %s answered", rvPlural(in.counts.Decisions, "decision", "decisions"))))
			if i == cursor {
				cursorLine = len(lines)
			}
			label := rvClip(in.finish, max(w-6, 8))
			if sel {
				lines = append(lines, SelectionMarker(focused).Render("▸")+"   "+PrimaryAction().Render(" "+label+" "))
			} else {
				lines = append(lines, "     "+reviewBoldStyle().Render(label))
			}
		case rowNotReviewed:
			// One folded row: coverage at a glance. ⏎ lists the files (x
			// raises one); the Walkthrough shows each in its chapter.
			if !ruled {
				lines = append(lines, "")
				rule()
				if i == cursor {
					cursorLine = len(lines)
				}
			}
			label := "⚠ Not reviewed · " + rvPlural(in.counts.NotReviewed, "file", "files")
			if in.counts.NotReviewed >= 4 {
				label += ": r to retry"
			}
			if sel {
				lines = append(lines, SelectionMarker(focused).Render("▸")+
					SelectionBand().Render(rvFit(" "+label, max(w-1-2, 1)))+SelectionBandSecondary().Render(rvFit("⏎", 2)))
			} else {
				lines = append(lines, "  "+reviewWarnStyle().Render(rvFit(label, max(w-2-2, 1)))+DimStyle.Render(rvFit("⏎", 2)))
			}
		case rowSafeToSkip:
			if !ruled {
				lines = append(lines, "")
				rule()
				lines = append(lines, "  "+DimStyle.Render("Not reviewed · none"))
				if i == cursor {
					cursorLine = len(lines)
				}
			}
			label := "Safe to skip · " + rvPlural(in.counts.SafeToSkip, "file", "files")
			hint := "s"
			if sel {
				cursorLine = len(lines)
				lines = append(lines, SelectionMarker(focused).Render("▸")+
					SelectionBand().Render(rvFit(" "+label, max(w-1-2, 1)))+SelectionBandSecondary().Render(rvFit(hint, 2)))
			} else {
				lines = append(lines, "  "+reviewTextStyle().Render(rvFit(label, max(w-2-2, 1)))+DimStyle.Render(rvFit(hint, 2)))
			}
		}
	}
	if !ruled {
		lines = append(lines, "")
		rule()
		lines = append(lines, "  "+DimStyle.Render("Not reviewed · none"))
		lines = append(lines, "  "+DimStyle.Render("Safe to skip · none"))
	}
	if len(in.pending) > 0 {
		lines = append(lines, "")
		for _, l := range rvWrap("Not verified yet: "+strings.Join(in.pending, "; "), max(w-3, 10)) {
			lines = append(lines, "  "+DimStyle.Render(l))
		}
	}
	return lines, cursorLine
}

// finishLabel is the finish row's button: what S does once every decision
// has an answer — "S send to ✻ api", or "A approve" when no answer asks the
// agent for anything. The card beside it lists what goes.
func (v *ReviewView) finishLabel() string {
	c := v.counts()
	if c.Acting() == 0 {
		return "A approve: nothing to change"
	}
	if v.startsSession() {
		g := v.d.NewSessionGlyph
		if g != "" {
			g += " "
		}
		return "S send to a new " + g + "session"
	}
	t, _ := v.target()
	return "S send to " + t.name()
}

// reviewTargetOf is d's chosen send target.
func reviewTargetOf(d ReviewData) (reviewTarget, bool) {
	if d.Target >= 0 && d.Target < len(d.Targets) {
		return d.Targets[d.Target], true
	}
	return reviewTarget{}, false
}

// reviewAnyLive reports whether some session in d's checkout can take a send.
func reviewAnyLive(d ReviewData) bool {
	for _, t := range d.Targets {
		if t.Live {
			return true
		}
	}
	return false
}

// reviewActingSummary is the part of a send the agent acts on: "2 changes and
// 1 question".
func reviewActingSummary(c selfreview.Counts) string {
	var parts []string
	if c.Changes > 0 {
		parts = append(parts, rvPlural(c.Changes, "change", "changes"))
	}
	if c.Asks > 0 {
		parts = append(parts, rvPlural(c.Asks, "question", "questions"))
	}
	return strings.Join(parts, " and ")
}

// ---------------------------------------------------------------------------
// The skip screen.

// reviewSkipFrom names who skipped a file, in the person's words.
func reviewSkipFrom(s selfreview.Skip) string {
	if s.By == "rule" {
		return "Auto-skipped (tests, lockfiles, generated)"
	}
	return "Reviewer: safe to skip"
}

// reviewFileGroup is the section a file-list row sits under.
func reviewFileGroup(f reviewFile) string {
	if f.skip == nil {
		return "⚠ Not reviewed"
	}
	return reviewSkipFrom(*f.skip)
}

// renderReviewFileList draws the files with no decision: the ones nobody
// reviewed first, then the skip list grouped by who decided it — fleet's rules
// or the reviewer — each file with its reason, and fleet's own flag ("tests
// changed — assertions removed") where it raised one.
func renderReviewFileList(run *selfreview.Run, answers map[string]selfreview.Answer, cursor int, focused bool, w int) (lines []string, cursorLine int) {
	cursorLine = -1
	files := reviewFiles(run)
	seen := map[string]int{}
	for _, f := range files {
		seen[reviewBase(f.path)]++
	}
	group := ""
	for i, f := range files {
		from := reviewFileGroup(f)
		if from != group {
			if group != "" {
				lines = append(lines, "")
			}
			count := 0
			for _, o := range files {
				if reviewFileGroup(o) == from {
					count++
				}
			}
			style := PaletteSectionStyle
			if f.skip == nil {
				style = reviewWarnStyle().Bold(true)
			}
			lines = append(lines, " "+style.Render(rvClip(fmt.Sprintf("%s · %d", from, count), w-2)))
			group = from
		}
		p := reviewBase(f.path)
		if seen[p] > 1 {
			// Two README.md: the directory tells them apart.
			p = path.Join(reviewBase(path.Dir(f.path)), p)
		}
		if f.skip != nil && f.skip.Scope == "rest" {
			p += " (rest of file)"
		}
		// The group header already says why a rule skipped a file; what is
		// left to say per row is the reviewer's reason, fleet's reason for not
		// accepting a skip, or a flag of fleet's.
		note, noteStyle := "", DimStyle
		switch {
		case answers[selfreview.FileStopID(f.path)].Kind != "":
			note = "→ you raised it"
		case f.skip == nil:
			note = run.NotCoveredWhy[f.path]
		case f.skip.Note != "":
			note, noteStyle = "⚠ "+f.skip.Note, reviewWarnStyle()
		case f.skip.By != "rule":
			note = f.skip.Reason
		}
		pathCell := rvFit(rvPath(p, w-3), w-3) + " "
		if i == cursor {
			cursorLine = len(lines)
			lines = append(lines, SelectionMarker(focused).Render("▸")+SelectionBand().Render(" "+pathCell))
		} else {
			lines = append(lines, "  "+reviewTextStyle().Render(pathCell))
		}
		if note != "" {
			for _, l := range rvWrap(note, w-5) {
				lines = append(lines, "    "+noteStyle.Render(l))
			}
		}
	}
	return lines, cursorLine
}

// ---------------------------------------------------------------------------
// Card.

// reviewCard is what the card explains: one decision, one file, or the finish
// and safe-to-skip rows.
type reviewCard struct {
	id         string
	kind       reviewRowKind
	n          int
	title      string
	kindPhrase string
	decide     string // bold, right under the recommendation
	recommend  *selfreview.Recommend
	how        string
	elsewhere  string
	why        string
	missing    []string // the scope decision's
	scope      bool
	showTitle  bool // narrow: the title leads the card (no list beside it)
	checked    []selfreview.Check
	notes      []string // plain paragraphs, for the cards that are not a decision
	hunks      []selfreview.Hunk
	answerable bool
}

func itemCard(it reviewItem, d ReviewData, c selfreview.Counts) reviewCard {
	run := d.Run
	whole := []selfreview.Hunk{{Path: it.path}}
	switch it.kind {
	case rowDecision:
		s := it.stop
		return reviewCard{id: it.id, kind: it.kind, n: it.n, title: s.Title, kindPhrase: reviewKindPhrase(s.Kind),
			decide: s.Decide, recommend: s.Recommend, how: s.How, elsewhere: s.Elsewhere, why: s.Why,
			missing: s.Missing, scope: s.IsScope(), checked: s.Checked, hunks: s.Hunks, answerable: true}
	case rowNotReviewed:
		n := 0
		if run != nil {
			n = len(run.NotCovered)
		}
		return reviewCard{id: it.id, kind: it.kind, notes: []string{
			rvPlural(n, "changed file", "changed files") + " the reviewer covered nowhere: no decision and no reason to skip. Fleet never skips a file silently.",
			"", "⏎ lists them, with fleet's reason where it refused a skip. x on one raises it as a change of your own; r reviews the checkout again.",
			"", "The Walkthrough (2) shows each in its chapter."}}
	case rowAddedByYou:
		note := "You raised this file yourself."
		if run != nil {
			for _, s := range run.Skipped {
				if s.Path == it.path {
					note = "It was skipped (" + strings.ToLower(s.Reason) + "), and you raised it yourself."
				}
			}
		}
		return reviewCard{id: it.id, kind: it.kind, title: it.path, decide: it.path, notes: []string{note},
			hunks: whole, answerable: true}
	case rowFinish:
		return reviewCard{id: it.id, kind: it.kind, notes: finishNotes(d, c)}
	case rowSafeToSkip:
		return reviewCard{id: it.id, kind: it.kind, notes: safeToSkipNotes(run)}
	}
	return reviewCard{}
}

// finishNotes is the finish card: what S will send, and what it leaves out.
func finishNotes(d ReviewData, c selfreview.Counts) []string {
	var out []string
	if c.Acting() == 0 {
		out = append(out, "Every answer is ok or later, so there is nothing for the agent to change. A approves the review.")
		return out
	}
	to := "the agent"
	if t, ok := reviewTargetOf(d); ok && reviewAnyLive(d) {
		to = t.name()
	}
	out = append(out, "S sends "+reviewActingSummary(c)+" to "+to+":", "")
	items := reviewItems(d.Run, d.Answers, d.Status)
	for _, it := range items {
		if !it.answerable() {
			continue
		}
		a := d.Answers[it.id]
		if a.Kind != selfreview.AnswerChange && a.Kind != selfreview.AnswerAsk {
			continue
		}
		title := it.title
		if it.kind == rowAddedByYou {
			title = reviewBase(it.path)
		}
		out = append(out, "  "+reviewAnswerGlyph(a.Kind)+" "+title, "    "+a.Note)
	}
	if left := c.OK + c.Later; left > 0 {
		out = append(out, "", rvPlural(left, "answer", "answers")+" marked ok or later stay here: the agent has nothing to do for them.")
	}
	return out
}

// safeToSkipNotes is the safe-to-skip row's card: the two groups and their
// sizes, and any file fleet flagged anyway.
func safeToSkipNotes(run *selfreview.Run) []string {
	if run == nil {
		return nil
	}
	rule, guide := 0, 0
	var flagged []string
	for _, s := range run.Skipped {
		if s.By == "rule" {
			rule++
		} else {
			guide++
		}
		if s.Note != "" {
			flagged = append(flagged, reviewBase(s.Path)+": "+s.Note)
		}
	}
	out := []string{fmt.Sprintf("%s changed, and none of them needs a decision.",
		rvPlural(len(run.Skipped), "file", "files"))}
	if rule > 0 {
		out = append(out, fmt.Sprintf("  %d auto-skipped (tests, lockfiles, generated)", rule))
	}
	if guide > 0 {
		out = append(out, fmt.Sprintf("  %d the reviewer read and called safe to skip", guide))
	}
	if len(flagged) > 0 {
		out = append(out, "", "Worth a look:")
		for _, f := range flagged {
			out = append(out, "  ⚠ "+f)
		}
	}
	return append(out, "", "⏎ lists them with the reason for each.")
}

// notReviewedCard is the file list's card for a file nobody reviewed.
func notReviewedCard(run *selfreview.Run, p string) reviewCard {
	why := "The reviewer didn't look at this file, and fleet never skips a file silently."
	if run != nil && run.NotCoveredWhy[p] != "" {
		why = "The reviewer " + run.NotCoveredWhy[p] + ". Fleet doesn't accept that skip, so it's yours to read."
	}
	return reviewCard{id: selfreview.FileStopID(p), kind: rowNotReviewedFile, title: p, decide: p,
		notes: []string{why, "", "x raises it as a change of your own; r reviews the checkout again."},
		hunks: []selfreview.Hunk{{Path: p}}}
}

func skipCard(s selfreview.Skip) reviewCard {
	note := "Skipped by fleet's rules: " + s.Reason + "."
	phrase := "auto-skipped"
	if s.By != "rule" {
		note, phrase = "The reviewer read it: "+lowerFirst(s.Reason)+".", "safe to skip"
	}
	if s.Scope == "rest" {
		note += " Its other lines are in a decision."
	}
	notes := []string{note}
	if s.Note != "" {
		notes = append(notes, "", "⚠ "+capFirst(s.Note)+".")
	}
	notes = append(notes, "", "x raises it as a change of your own.")
	return reviewCard{id: selfreview.FileStopID(s.Path), kind: rowSkippedFile, title: s.Path, decide: s.Path,
		kindPhrase: phrase, notes: notes, hunks: []selfreview.Hunk{{Path: s.Path}}}
}

// reviewSplitCitation splits an elsewhere line into its prose and the path it
// cites ("EdgeWatcher retries once — collectors/edgewatcher/collector.go"). A
// line whose tail is not a path is all prose.
func reviewSplitCitation(s string) (text, path string) {
	for _, sep := range []string{" — ", " - ", ": "} {
		if i := strings.LastIndex(s, sep); i > 0 {
			tail := strings.TrimSpace(s[i+len(sep):])
			if !strings.Contains(tail, " ") && (strings.Contains(tail, "/") || strings.Contains(tail, ".")) {
				return strings.TrimSpace(s[:i]), strings.TrimRight(tail, ".,;")
			}
		}
	}
	return s, ""
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && r[1] >= 'A' && r[1] <= 'Z' {
		return s // an acronym: "CI", "README"
	}
	return strings.ToLower(string(r[:1])) + string(r[1:])
}

// reviewHunkSpan resolves a hunk to stream rows. A file-level hunk (no line
// range) spans the whole file.
func reviewHunkSpan(doc *review.Doc, h selfreview.Hunk) (review.RowSpan, bool) {
	if doc == nil {
		return review.RowSpan{}, false
	}
	if h.Start == 0 && h.End == 0 {
		s := doc.Stream
		for i, f := range s.Files {
			if f == h.Path && i < len(s.FileStarts) {
				to := len(s.Lines)
				if i+1 < len(s.FileStarts) {
					to = s.FileStarts[i+1]
				}
				// Drop the spacing row that separates this file from the next.
				for to > s.FileStarts[i]+1 && s.Lines[to-1].Kind == review.LineBlank {
					to--
				}
				// Start under the file's own header band: the card labels the
				// file itself, and the full diff marks code, not chrome.
				return review.RowSpan{From: min(s.FileStarts[i]+1, to), To: to, Exact: true}, true
			}
		}
		return review.RowSpan{}, false
	}
	side := h.Side
	if side != review.SideOld {
		side = review.SideNew
	}
	return review.BindRange(doc, h.Path, side, h.Start, h.End)
}

func reviewHunkRange(h selfreview.Hunk) string {
	switch {
	case h.Start == 0 && h.End == 0:
		return "whole file"
	case h.End <= h.Start:
		return "L" + strconv.Itoa(h.Start)
	}
	return fmt.Sprintf("L%d–%d", h.Start, h.End)
}

// reviewCardLayout is the card's full (unscrolled) content, plus the line each
// hunk's label sits on, for J/K.
type reviewCardLayout struct {
	lines     []string
	hunkLines []int
}

// layoutReviewCard lays out a card at innerW columns. A decision leads with
// the reviewer's recommendation and the question, both bold — what to answer
// and what the reviewer would answer — then how the mechanism works, how the
// rest of the repo does it, why it matters, what the reviewer checked, the
// answer in effect, and the code. cur is the change J/K is on; focused marks
// it.
func layoutReviewCard(c reviewCard, a selfreview.Answer, doc *review.Doc, innerW, cur, extra int, focused bool) reviewCardLayout {
	var lay reviewCardLayout
	tw := max(innerW-2, 1)
	add := func(s string) { lay.lines = append(lay.lines, " "+s) }
	blank := func() { lay.lines = append(lay.lines, "") }

	blank()
	switch c.kind {
	case rowDecision:
		if c.showTitle {
			// Narrow, no list beside the card: the decision's own title
			// leads, whole, rather than clipped into the border.
			for _, l := range rvWrap(c.title, tw) {
				add(reviewTextStyle().Render(l))
			}
			blank()
		}
		if r := c.recommend; r != nil {
			rec, key := "Recommend: keep as is", "y"
			if r.Answer == selfreview.RecommendChange {
				rec, key = "Recommend: change", "n"
			}
			if r.Text != "" {
				rec += " — " + lowerFirst(r.Text)
			}
			ls := rvWrap(rec, max(tw-3, 4))
			for i, l := range ls {
				if i == len(ls)-1 {
					l = reviewBoldStyle().Render(l) + "   " + HelpKeyStyle.Render(key)
				} else {
					l = reviewBoldStyle().Render(l)
				}
				add(l)
			}
			blank()
		}
		for _, l := range rvWrap(c.decide, tw) {
			add(reviewBoldStyle().Render(l))
		}
	case rowNotReviewedFile, rowAddedByYou, rowSkippedFile:
		add(reviewBoldStyle().Render(rvPath(c.decide, tw)))
	}
	if c.kind == rowDecision || c.kind == rowNotReviewedFile || c.kind == rowAddedByYou || c.kind == rowSkippedFile {
		blank()
	}

	field := func(label, text string, style lipgloss.Style) {
		if text == "" {
			return
		}
		for i, l := range rvWrap(text, max(tw-reviewLabelW, 1)) {
			lab := ""
			if i == 0 {
				lab = label
			}
			add(DimStyle.Render(rvFit(lab, reviewLabelW)) + style.Render(l))
		}
	}
	field("How it works", c.how, reviewTextStyle())
	if c.how != "" && (c.elsewhere != "" || c.why != "") {
		blank()
	}
	if c.elsewhere != "" {
		// The citation is a path: it gets a line of its own, kept by its tail
		// (the file name), rather than hard-wrapped mid-word into the prose.
		text, cite := reviewSplitCitation(c.elsewhere)
		field("Elsewhere", text, reviewTextStyle())
		if cite != "" {
			add(strings.Repeat(" ", reviewLabelW) + DimStyle.Render(rvPath(cite, max(tw-reviewLabelW, 8))))
		}
		blank()
	}
	if c.scope {
		// The scope decision's why is what is missing: listed whole.
		if len(c.missing) > 0 {
			for i, m := range c.missing {
				lab := ""
				if i == 0 {
					lab = "Missing"
				}
				for j, l := range rvWrap(m, max(tw-reviewLabelW-2, 4)) {
					lead, mk := "", "  "
					if j == 0 {
						lead, mk = lab, "· "
					}
					add(DimStyle.Render(rvFit(lead, reviewLabelW)) + DimStyle.Render(mk) + reviewTextStyle().Render(l))
				}
			}
		} else {
			field("Missing", c.why, reviewTextStyle())
		}
	} else {
		field("Why it matters", c.why, reviewTextStyle())
	}
	if len(c.checked) > 0 {
		blank()
	}
	for i, ch := range c.checked {
		lab := ""
		if i == 0 {
			lab = "Checked"
		}
		verified := ch.Src != "claim" && !ch.Demoted
		mark, textStyle := DimStyle.Render("·"), DimStyle
		text := ch.Text
		if verified {
			mark, textStyle = reviewGoodStyle().Render("✓"), reviewTextStyle()
		} else {
			text += " (agent's word, not checked)"
		}
		for j, l := range rvWrap(text, max(tw-reviewLabelW-2, 4)) {
			lead, m := "", " "
			if j == 0 {
				lead, m = lab, mark
			}
			add(DimStyle.Render(rvFit(lead, reviewLabelW)) + m + " " + textStyle.Render(l))
		}
	}
	for _, n := range c.notes {
		if n == "" {
			blank()
			continue
		}
		// Leading spaces are a hanging indent: an item keeps its column
		// when it wraps. Deeper-indented lines are the person's own words.
		body := strings.TrimLeft(n, " ")
		indent := strings.Repeat(" ", len(n)-len(body))
		style := reviewTextStyle()
		if len(indent) >= 4 {
			style = reviewBoldStyle()
		}
		for _, l := range rvWrap(body, max(tw-len(indent), 4)) {
			add(indent + style.Render(l))
		}
	}
	if a.Kind != "" && a.Note != "" {
		blank()
		field("Answer", reviewAnswerGlyph(a.Kind)+" "+reviewAnswerWord(a.Kind)+": "+a.Note, reviewTextStyle())
	}

	for k, h := range c.hunks {
		blank()
		lay.hunkLines = append(lay.hunkLines, len(lay.lines))
		span, ok := reviewHunkSpan(doc, h)
		rng := reviewHunkRange(h)
		switch {
		case doc == nil || !ok:
			rng = "not in this diff"
		case !span.Exact:
			rng += " · showing the whole file"
		}
		of := ""
		if len(c.hunks) > 1 {
			of = fmt.Sprintf("%d of %d", k+1, len(c.hunks))
		}
		markStyle, labelStyle := DimStyle, DimStyle
		if k == cur && focused {
			markStyle, labelStyle = SelectionMarker(true), reviewTextStyle()
		}
		room := max(tw-2-lipgloss.Width(rng)-3-lipgloss.Width(of)-2, 8)
		label := rvPath(reviewBase(h.Path), room) + " · " + rng
		gap := max(tw-2-lipgloss.Width(label)-lipgloss.Width(of), 1)
		add(markStyle.Render("◆") + " " + labelStyle.Render(label) + strings.Repeat(" ", gap) + DimStyle.Render(of))
		if doc != nil && ok {
			span = reviewTrimSpan(doc, span, h, reviewCardContext+extra)
			lay.lines = append(lay.lines, RenderHunk(doc, span.From, span.To, innerW, false)...)
		}
	}
	return lay
}

// reviewCardContext is how many lines around the cited ones the card shows
// before `e` asks for more.
const reviewCardContext = 3

// reviewTrimSpan narrows a bound span to the lines the decision cites plus
// ctx lines either side. A hunk binds whole, and a new file is one hunk: a
// citation of L229–268 would otherwise open the card on line 1. File-level
// hunks and spans with no numbered rows in range are left as they are.
func reviewTrimSpan(doc *review.Doc, span review.RowSpan, h selfreview.Hunk, ctx int) review.RowSpan {
	if doc == nil || !span.Exact || (h.Start == 0 && h.End == 0) {
		return span
	}
	lo, hi := h.Start-ctx, max(h.End, h.Start)+ctx
	from, to := -1, -1
	for i := span.From; i < span.To && i < len(doc.Stream.Lines); i++ {
		l := doc.Stream.Lines[i]
		n := l.New
		if h.Side == review.SideOld {
			n = l.Old
		}
		if n == 0 || !l.Kind.IsCode() {
			continue
		}
		if n >= lo && n <= hi {
			if from < 0 {
				from = i
			}
			to = i + 1
		}
	}
	if from < 0 {
		return span
	}
	return review.RowSpan{From: from, To: to, Exact: true}
}

// reviewTrimHides reports whether trimming span to ctx lines leaves code rows
// of the hunk off the card — whether `e` has more of the hunk to show before
// it has to reach outside it.
func reviewTrimHides(doc *review.Doc, span review.RowSpan, h selfreview.Hunk, ctx int) bool {
	t := reviewTrimSpan(doc, span, h, ctx)
	for i := span.From; i < span.To && i < len(doc.Stream.Lines); i++ {
		if (i < t.From || i >= t.To) && doc.Stream.Lines[i].Kind.IsCode() {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Full diff.

// reviewDecisionLabels maps the hunk header row above each decision's code to
// its label — "◆ decision 1", "◆ decisions 1, 3" — so the full diff names
// which decision a hunk belongs to once, on the hunk, not on every line.
func reviewDecisionLabels(doc *review.Doc, run *selfreview.Run) map[int]string {
	out := map[int]string{}
	if doc == nil || run == nil {
		return out
	}
	byRow := map[int][]int{}
	starts := doc.Stream.HunkStarts
	for i, s := range run.Stops {
		for _, h := range s.Hunks {
			span, ok := reviewHunkSpan(doc, h)
			if !ok || !span.Exact {
				continue
			}
			// The hunk the span sits in, or the first one it covers — in the
			// decision's own file: a span can start on a row above its hunk's
			// header, and the nearest header above that is another file's.
			row := -1
			for k, hs := range starts {
				if doc.Stream.Lines[hs].File != h.Path {
					continue
				}
				end := doc.Stream.HunkEnds[k]
				if (hs <= span.From && span.From < end) || (hs >= span.From && hs < span.To) {
					row = hs
					break
				}
			}
			if row < 0 {
				continue
			}
			if !containsInt(byRow[row], i+1) {
				byRow[row] = append(byRow[row], i+1)
			}
		}
	}
	for row, ns := range byRow {
		sort.Ints(ns)
		strs := make([]string, len(ns))
		for i, n := range ns {
			strs[i] = strconv.Itoa(n)
		}
		label := "◆ decision " + strs[0]
		if len(ns) > 1 {
			label = "◆ decisions " + strings.Join(strs, ", ")
		}
		out[row] = label
	}
	return out
}

func containsInt(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (v *ReviewView) renderDiffPanel(w, h int) string {
	rows := v.diffRows()
	viewH := v.diffViewHeight()
	top := clampInt(v.diffTop, 0, max(len(rows)-viewH, 0))
	vis := rows[top:min(top+viewH, len(rows))]
	inner := max(w-2, 3)
	labels := reviewDecisionLabels(v.d.Doc, v.d.Run)
	var out []string
	if len(vis) > 0 {
		body := renderDiffRows(v.d.Doc.Stream, vis, inner, false, diffRenderOpts{cursor: -1})
		for i, r := range vis {
			line := body[i]
			if label, ok := labels[r.primary()]; ok {
				lw := lipgloss.Width(label) + 3
				line = ansi.Truncate(line, max(inner-lw, 1), "") + "  " + reviewBoldStyle().Render(label) + " "
				if pad := inner - lipgloss.Width(line); pad > 0 {
					line += strings.Repeat(" ", pad)
				}
			}
			out = append(out, line)
		}
	}
	right := ""
	if len(rows) > 0 {
		right = fmt.Sprintf("%d–%d of %d", top+1, top+len(vis), len(rows))
	}
	title := "Full diff"
	if v.diffFile != "" {
		title = "Diff · " + rvPath(v.diffFile, max(w-20-lipgloss.Width(right), 10))
	}
	return RenderBorderedPanelInsets(strings.Join(out, "\n"), title, right, "", "", w, h, true)
}
