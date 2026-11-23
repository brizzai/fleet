package ui

import (
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/selfreview"
)

// The Review view: one full-screen surface where a person answers the few
// decisions a self-review run put in front of them (docs/self-review.md,
// "Screens" and "User flow").
//
// It is a pure component, driven by plain data and emitting messages: it
// never reads the store, never runs git, never delivers anything. The wiring
// layer builds a ReviewData, hands it over, and acts on the review*Msg values
// the keys produce. That keeps every screen testable from a fixture and keeps
// blocking I/O off the Update goroutine by construction — there is none here
// to move.
//
// Every number it shows comes from selfreview.CountsOf, the one definition the
// CLI and the sidebar's review row read too.

// reviewAnswerMsg records one answer. The view has already applied it to its
// own copy (the row repaints on the keypress); the wiring persists it.
type reviewAnswerMsg struct {
	RunID  string
	Answer selfreview.Answer
}

// reviewSendMsg asks the wiring to compose and deliver the run's answers.
type reviewSendMsg struct{ RunID string }

// reviewApproveMsg approves the run's snapshot.
type reviewApproveMsg struct{ RunID string }

// reviewRegenerateMsg asks for a fresh run over the checkout's current tree.
type reviewRegenerateMsg struct{ Checkout string }

// reviewRoundMsg asks for the next round once the agent has worked through
// the sent answers: a review of the checkout as it stands now, which is a new
// round when the tree moved and the sent run again when it did not.
type reviewRoundMsg struct{ Checkout string }

// reviewTargetMsg records the session S will send to (the `t` key).
type reviewTargetMsg struct {
	RunID, Checkout, SessionID string
}

// reviewStartSessionMsg asks the wiring to deliver the run's answers by
// starting a new session in the checkout with them as its first prompt — what
// S does, after a y, when no session there can take them.
type reviewStartSessionMsg struct{ RunID, Checkout string }

// reviewCloseMsg says the view closed itself (esc at the top level).
type reviewCloseMsg struct{}

// ReviewFreshness says how the run on screen relates to the worktree now.
type ReviewFreshness string

const (
	ReviewFresh     ReviewFreshness = "fresh"
	ReviewStale     ReviewFreshness = "stale"     // the tree moved since this run
	ReviewPreparing ReviewFreshness = "preparing" // a run is being generated
	ReviewFailed    ReviewFreshness = "failed"    // the last attempt failed (FailReason)
)

// Run statuses the view distinguishes (review_runs.status). Empty reads as ready.
const (
	reviewStatusSent     = "sent"
	reviewStatusApproved = "approved"
)

// reviewTarget is one session S can deliver to.
type reviewTarget struct {
	ID, Title string
	Glyph     string // the agent sigil (✻ ◇ △ ⚇)
	// Live: a pane to type into (alive), or a suspended session deliver can
	// wake. A session in error has neither.
	Live bool
	// Running refuses S: delivery never queues behind a working agent.
	Running bool
}

// name is how the footer and the sent screen name the target: "✻ api".
func (t reviewTarget) name() string {
	if t.Glyph == "" {
		return t.Title
	}
	return t.Glyph + " " + t.Title
}

// ReviewData is everything the view renders. The caller owns all of it; the
// view keeps its own copy of Answers so an answer shows on the keypress.
type ReviewData struct {
	RunID string
	// Checkout is the root of the reviewed checkout (the run's key);
	// CheckoutLabel names it in the header (its branch).
	Checkout      string
	CheckoutLabel string
	Round         int
	Model         string
	Elapsed       time.Duration
	// Snapshot is the short label of what was reviewed, e.g.
	// "62055a2 + uncommitted". Shown in the keys overlay, not the header.
	Snapshot   string
	Freshness  ReviewFreshness
	FailReason string
	// Status is the run's stored status: "", "ready", "sent" or "approved".
	Status string
	// CI is the checkout's live PR check rollup (selfreview.CI*), "" when
	// fleet knows nothing; CIFailed names the failing checks.
	CI       string
	CIFailed []string
	// Targets are the checkout's sessions S can send to, in `t`'s cycle order;
	// Target indexes the chosen one. Empty: the checkout has no session.
	Targets []reviewTarget
	Target  int
	// NewSessionGlyph is the default agent's sigil, for S's "start a new
	// session" offer when no target is live.
	NewSessionGlyph string
	// AgentWorking: the answers were sent and the agent they went to is
	// working on them (or was sent them moments ago). The next round waits.
	AgentWorking bool

	Run     *selfreview.Run // nil while a first run prepares, or after it failed
	Answers map[string]selfreview.Answer
	Doc     *review.Doc // built from the run's files; nil renders no code
}

// reviewScreen is which body the Decide tab shows (the diff screen overlays
// either tab).
type reviewScreen int

const (
	reviewScreenDecisions reviewScreen = iota
	reviewScreenSkipped                // the files with no decision: not reviewed, safe to skip
	reviewScreenDiff
)

// reviewTab is which of the view's two tabs has the keyboard. Switching moves
// the keyboard to the other tab's list, so a tab is a selection, not a mode
// (docs/design-system.md, "Tabs are not automatically modes").
type reviewTab int

const (
	reviewTabDecide reviewTab = iota // answer the decisions (the default)
	reviewTabWalk                    // how the change works, chapter by chapter
)

// reviewNarrowWidth is the width below which the list and the card stop
// sharing the screen (spec: wide ≥ 100 columns).
const reviewNarrowWidth = 100

// reviewRowKind is what a list row is.
type reviewRowKind int

const (
	rowDecision        reviewRowKind = iota // one of the reviewer's decisions
	rowAddedByYou                           // a skipped file the human raised as their own
	rowFinish                               // "All 4 answered → S send …": the way out
	rowNotReviewed                          // "Not reviewed · 2 files": opens the file list there
	rowSafeToSkip                           // "Safe to skip · 14 files": opens the file list
	rowSkippedFile                          // one file on the file list (cards only)
	rowNotReviewedFile                      // one unreviewed file on the file list (cards only)
)

// Fixed ids for the rows that are not a stop or a file, so SetData can keep
// the cursor on them by identity too.
const (
	reviewFinishID      = "#finish"
	reviewSkipID        = "#skip"
	reviewNotReviewedID = "#notreviewed"
)

// reviewItem is one row of the list.
type reviewItem struct {
	kind  reviewRowKind
	id    string // stop id, selfreview.FileStopID(path), or a fixed id
	n     int    // the decision's 1-based number (decisions only)
	title string
	stop  *selfreview.Stop
	path  string
}

// answerable reports whether y/n/a/l act on the row.
func (it reviewItem) answerable() bool { return it.kind == rowDecision || it.kind == rowAddedByYou }

// reviewKindPhrase names a stop's kind the way a person would say it
// ("deploy order", "likely bug"). Shown dim in the card's title; the list
// carries only the title.
func reviewKindPhrase(kind string) string { return selfreview.KindPhrase(kind) }

// reviewConfirm is a y/n question the footer asks before a key acts — one at a
// time, owning the keyboard until answered.
type reviewConfirm struct {
	question string
	yes      string // what y does, e.g. "approve"
	msg      tea.Msg
}

// ReviewView is the Review screen.
type ReviewView struct {
	visible       bool
	width, height int
	d             ReviewData

	tab    reviewTab
	screen reviewScreen
	prev   reviewScreen // where esc returns from the full diff
	// back is the way out of a jump: ⏎ on a chapter's decision or file
	// leaves the chapter, and esc must return to it — one level per press,
	// never straight out of the review.
	back []reviewLoc
	// diffFile scopes the diff screen to one file (opened from a chapter's
	// file); "" is the full diff.
	diffFile string

	// The Walkthrough tab: its list (the scope row, then the chapters), the
	// row inside the chapter card the keyboard is on, and their scrolls.
	walkCursor     int
	walkScroll     int
	walkItem       int
	walkCardScroll int
	// cardFocus is true while the keyboard is in the card (wide) or the card
	// is open full-width (narrow).
	cardFocus bool
	// helpOpen shows every key the view has (the `?` overlay).
	helpOpen bool

	cursor     int // list rows: decisions, then finish / not reviewed / safe to skip
	listScroll int
	skipCursor int
	skipScroll int
	cardScroll int
	hunk       int // current hunk within the card
	extra      int // context lines `e` added around the card's code
	diffTop    int

	// The note input, open for n / a / x. Exactly one highlight: while it is
	// open the panels give up the accent and the caret lives here.
	input     textinput.Model
	inputOpen bool
	inputKind string // selfreview.AnswerChange | AnswerAsk
	inputFor  string // the stop id the note answers
	inputErr  string

	// docGen bumps whenever the Doc's stream may have changed (expand, new
	// data), invalidating anything that indexes it.
	docGen int

	// confirm is the y/n the footer is asking, or nil.
	confirm *reviewConfirm

	cardCache    reviewCardLayout
	cardCacheKey reviewCardKey
	cardCacheOK  bool
}

// NewReviewView opens the view on d at the given terminal size.
func NewReviewView(d ReviewData, width, height int) *ReviewView {
	v := &ReviewView{}
	v.SetSize(width, height)
	v.Show(d)
	return v
}

// Show (re)opens the view on d, at the first decision still unanswered.
func (v *ReviewView) Show(d ReviewData) {
	v.visible = true
	v.tab, v.screen, v.cardFocus, v.helpOpen, v.diffFile = reviewTabDecide, reviewScreenDecisions, false, false, ""
	v.cursor, v.listScroll, v.skipCursor, v.skipScroll = 0, 0, 0, 0
	v.cardScroll, v.hunk, v.diffTop = 0, 0, 0
	v.walkCursor, v.walkScroll, v.walkItem, v.walkCardScroll = 0, 0, 0, 0
	v.inputOpen, v.confirm, v.back = false, nil, nil
	v.SetData(d)
	if i, ok := v.nextUnanswered(-1); ok {
		v.cursor = i
		return
	}
	// Everything answered: open on the way out.
	for i, it := range v.items() {
		if it.kind == rowFinish {
			v.cursor = i
		}
	}
}

// Hide closes the view, dropping any open note, question or overlay.
func (v *ReviewView) Hide() {
	v.visible, v.inputOpen, v.confirm, v.helpOpen = false, false, nil, false
}

// IsVisible reports whether the view is on screen.
func (v *ReviewView) IsVisible() bool { return v != nil && v.visible }

// SetSize records the terminal size.
func (v *ReviewView) SetSize(w, h int) { v.width, v.height = w, h; v.clamp() }

// SetData replaces what the view shows — a finished run, a new status, the
// agent starting or stopping — keeping the cursor on the same row when it
// still exists. The highlight never moves on its own.
func (v *ReviewView) SetData(d ReviewData) {
	prevID := ""
	if it, ok := v.selectedItem(); ok {
		prevID = it.id
	}
	answers := make(map[string]selfreview.Answer, len(d.Answers))
	for k, a := range d.Answers {
		answers[k] = a
	}
	d.Answers = answers
	v.d = d
	v.docGen++
	if prevID != "" {
		for i, it := range v.items() {
			if it.id == prevID {
				v.cursor = i
			}
		}
	}
	v.clamp()
}

// SetTargets updates the facts S depends on that change under an open view —
// which sessions are live or running — keeping the chosen target by identity.
func (v *ReviewView) SetTargets(ts []reviewTarget) {
	cur := ""
	if t, ok := v.target(); ok {
		cur = t.ID
	}
	v.d.Targets, v.d.Target = ts, 0
	for i, t := range ts {
		if t.ID == cur {
			v.d.Target = i
		}
	}
}

// SetAgentWorking records whether the agent a sent run went to is still on it.
func (v *ReviewView) SetAgentWorking(w bool) { v.d.AgentWorking = w }

// target is the session S sends to, if the checkout has any.
func (v *ReviewView) target() (reviewTarget, bool) {
	if v.d.Target >= 0 && v.d.Target < len(v.d.Targets) {
		return v.d.Targets[v.d.Target], true
	}
	return reviewTarget{}, false
}

// anyLiveTarget reports whether some session in the checkout can take a send.
func (v *ReviewView) anyLiveTarget() bool {
	for _, t := range v.d.Targets {
		if t.Live {
			return true
		}
	}
	return false
}

// startsSession is true when S would start a new session rather than send to
// an existing one: no session in the checkout is live.
func (v *ReviewView) startsSession() bool { return !v.anyLiveTarget() }

// Data returns what the view currently holds, answers included.
func (v *ReviewView) Data() ReviewData { return v.d }

func (v *ReviewView) narrow() bool { return v.width < reviewNarrowWidth }

// counts is the run's counts against the view's own answers — the same
// function the CLI and the sidebar row call.
func (v *ReviewView) counts() selfreview.Counts {
	return reviewCountsOf(v.d.Run, v.d.Answers)
}

// reviewCountsOf is selfreview.CountsOf over the view's answer map.
func reviewCountsOf(run *selfreview.Run, answers map[string]selfreview.Answer) selfreview.Counts {
	ans := make([]selfreview.Answer, 0, len(answers))
	for _, a := range answers {
		ans = append(ans, a)
	}
	return selfreview.CountsOf(run, ans)
}

// sent / approved: the run is past answering.
func (v *ReviewView) sent() bool     { return v.d.Status == reviewStatusSent }
func (v *ReviewView) approved() bool { return v.d.Status == reviewStatusApproved }

// finished reports a run whose body is a closing screen rather than the list:
// sent, approved, or with nothing in it that needs a person.
func (v *ReviewView) finished() bool {
	return v.d.Run != nil && (v.sent() || v.approved() || v.counts().NothingNeedsYou())
}

// ---------------------------------------------------------------------------
// Model: the rows the keys walk.

// reviewItems flattens a run into list rows: the reviewer's decisions, the
// files the human raised themselves, the finish row once every decision has
// an answer, then one folded row each for the files nobody reviewed and the
// files safe to skip — coverage at a glance; the files themselves are one
// key away, and in context on the Walkthrough tab.
func reviewItems(run *selfreview.Run, answers map[string]selfreview.Answer, status string) []reviewItem {
	if run == nil {
		return nil
	}
	var out []reviewItem
	answered := 0
	for i := range run.Stops {
		s := &run.Stops[i]
		out = append(out, reviewItem{kind: rowDecision, id: s.ID, n: i + 1, title: s.Title, stop: s})
		if answers[s.ID].Kind != "" {
			answered++
		}
	}
	var promoted []string
	for id, a := range answers {
		if p, ok := strings.CutPrefix(id, "file:"); ok && a.Kind != "" {
			promoted = append(promoted, p)
		}
	}
	sort.Strings(promoted)
	for _, p := range promoted {
		out = append(out, reviewItem{kind: rowAddedByYou, id: selfreview.FileStopID(p), title: p, path: p})
	}
	if len(run.Stops) > 0 && answered == len(run.Stops) && status != reviewStatusSent && status != reviewStatusApproved {
		out = append(out, reviewItem{kind: rowFinish, id: reviewFinishID})
	}
	if len(run.NotCovered) > 0 {
		out = append(out, reviewItem{kind: rowNotReviewed, id: reviewNotReviewedID})
	}
	if len(run.Skipped) > 0 {
		out = append(out, reviewItem{kind: rowSafeToSkip, id: reviewSkipID})
	}
	return out
}

func (v *ReviewView) items() []reviewItem { return reviewItems(v.d.Run, v.d.Answers, v.d.Status) }

func (v *ReviewView) hasSkipped() bool { return v.d.Run != nil && len(v.d.Run.Skipped) > 0 }

func (v *ReviewView) selectedItem() (reviewItem, bool) {
	items := v.items()
	if v.cursor >= 0 && v.cursor < len(items) {
		return items[v.cursor], true
	}
	return reviewItem{}, false
}

// nextUnanswered is the first unanswered decision after row from (wrapping;
// from < 0 searches from the top), for the auto-advance an answer makes and
// for where the view opens.
func (v *ReviewView) nextUnanswered(from int) (int, bool) {
	items := v.items()
	n := len(items)
	for k := 1; k <= n; k++ {
		i := (from + k + n) % n
		if from < 0 {
			i = k - 1
		}
		if it := items[i]; it.kind == rowDecision && v.d.Answers[it.id].Kind == "" {
			return i, true
		}
	}
	return 0, false
}

// advance moves the cursor after an answer on row from: to the next
// unanswered decision, else to the finish row, else nowhere.
func (v *ReviewView) advance(from int) {
	if i, ok := v.nextUnanswered(from); ok {
		v.cursor = i
		v.resetCard()
		return
	}
	for i, it := range v.items() {
		if it.kind == rowFinish {
			v.cursor, v.cardFocus = i, false
			v.resetCard()
			return
		}
	}
}

// reviewFile is one row of the file list: a file nobody reviewed (skip nil),
// or a skip-list entry.
type reviewFile struct {
	path string
	skip *selfreview.Skip
}

// reviewFiles orders the file list as its screen shows it: the files nobody
// reviewed first (they are the ones to look at), then fleet's own skips
// (tests, lockfiles, generated), then the reviewer's, each in run order.
func reviewFiles(run *selfreview.Run) []reviewFile {
	if run == nil {
		return nil
	}
	var out, guide []reviewFile
	for _, p := range run.NotCovered {
		out = append(out, reviewFile{path: p})
	}
	for i := range run.Skipped {
		s := &run.Skipped[i]
		if s.By == "rule" {
			out = append(out, reviewFile{path: s.Path, skip: s})
		} else {
			guide = append(guide, reviewFile{path: s.Path, skip: s})
		}
	}
	return append(out, guide...)
}

func (v *ReviewView) selectedFile() (reviewFile, bool) {
	fs := reviewFiles(v.d.Run)
	if v.skipCursor >= 0 && v.skipCursor < len(fs) {
		return fs[v.skipCursor], true
	}
	return reviewFile{}, false
}

// cardSubject is what the card shows: the selected row, or the selected
// skipped file on the skip screen.
func (v *ReviewView) cardSubject() (reviewCard, bool) {
	if v.screen == reviewScreenSkipped {
		f, ok := v.selectedFile()
		if !ok {
			return reviewCard{}, false
		}
		if f.skip == nil {
			return notReviewedCard(v.d.Run, f.path), true
		}
		return skipCard(*f.skip), true
	}
	it, ok := v.selectedItem()
	if !ok {
		return reviewCard{}, false
	}
	c := itemCard(it, v.d, v.counts())
	c.showTitle = v.narrow()
	return c, true
}

// answerTarget is the row the answer keys act on, and why not when none.
func (v *ReviewView) answerTarget() (string, string) {
	if v.d.Run == nil {
		return "", "no review yet"
	}
	if v.sent() || v.approved() {
		return "", "already " + v.d.Status
	}
	if v.tab == reviewTabWalk {
		return "", "answer on Decide"
	}
	if v.screen == reviewScreenSkipped {
		return "", "x raises a file"
	}
	it, ok := v.selectedItem()
	switch {
	case !ok:
		return "", "nothing to answer"
	case it.answerable():
		return it.id, ""
	}
	return "", "not a decision"
}

// ---------------------------------------------------------------------------
// Guards. Each is consulted by both the key handler and the footer, so a key
// the footer lights can never dead-click and a dimmed one names the clause
// that actually failed.

func (v *ReviewView) sendBlocker() string {
	c := v.counts()
	switch {
	case v.d.Run == nil:
		return "no review yet"
	case v.sent():
		return "already sent"
	case v.approved():
		return "approved"
	case c.Answered+c.AddedByYou == 0:
		return "answer one first"
	}
	if v.startsSession() || c.Acting() == 0 {
		return "" // S offers a new session, or approving instead
	}
	t, _ := v.target()
	switch {
	case !t.Live:
		return t.Title + " has stopped: t picks another"
	case t.Running:
		return t.Title + " is working"
	}
	return ""
}

// targetBlocker says why `t` can't cycle the send target, or "".
func (v *ReviewView) targetBlocker() string {
	switch {
	case len(v.d.Targets) < 2:
		return "one session"
	case v.sent() || v.approved():
		return "already " + v.d.Status
	}
	return ""
}

func (v *ReviewView) approveBlocker() string {
	switch {
	case v.d.Run == nil:
		return "no review yet"
	case v.approved():
		return "already approved"
	case v.d.Freshness == ReviewStale:
		return "code changed: r first"
	case v.d.Freshness == ReviewPreparing:
		return "new review preparing"
	}
	return ""
}

func (v *ReviewView) regenerateBlocker() string {
	if v.d.Freshness == ReviewPreparing {
		return "already preparing"
	}
	return ""
}

// roundBlocker says why `v` can't start the next round from the sent screen.
func (v *ReviewView) roundBlocker() string {
	if !v.sent() {
		return "nothing sent yet"
	}
	if v.d.AgentWorking {
		return "when the agent finishes"
	}
	return ""
}

func (v *ReviewView) expandBlocker() string {
	if v.d.Doc == nil {
		return "no code"
	}
	return ""
}

// unsentAnswers counts answers given on a run not yet sent — what `r` would
// leave behind.
func (v *ReviewView) unsentAnswers() int {
	if v.d.Run == nil || v.sent() || v.approved() {
		return 0
	}
	c := v.counts()
	return c.Answered + c.AddedByYou
}

// ---------------------------------------------------------------------------
// Update.

// Update handles the view's keys. Non-key messages only reach the note input
// (its caret blink).
func (v *ReviewView) Update(msg tea.Msg) (*ReviewView, tea.Cmd) {
	// ctrl+q leaves the review from anywhere — the same key that detaches an
	// attached session, so "get me back to the sidebar" is one chord however
	// deep you are (note box, confirm, help, a card, the full diff). Answers
	// already saved stay saved; an unsaved note is dropped, as esc drops it.
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "ctrl+q" {
		v.inputOpen = false
		v.confirm = nil
		v.helpOpen = false
		return v.close()
	}
	if v.inputOpen {
		return v.updateInput(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}
	k := key.String()

	if v.confirm != nil {
		// The y/n owns the keyboard until answered: a stray key must not
		// both dismiss it and act on the list behind it.
		switch k {
		case "y", "Y":
			m := v.confirm.msg
			v.confirm = nil
			return v, func() tea.Msg { return m }
		case "n", "N", "esc":
			v.confirm = nil
		}
		return v, nil
	}
	if v.helpOpen {
		if k == "?" || k == "esc" || k == "q" {
			v.helpOpen = false
		}
		return v, nil
	}

	// Keys that mean the same thing on every screen.
	switch k {
	case "1", "2":
		if v.d.Run != nil {
			v.back = nil // a tab you picked yourself is not a jump to return from
			v.switchTab(map[string]reviewTab{"1": reviewTabDecide, "2": reviewTabWalk}[k])
		}
		return v, nil
	case "?":
		v.helpOpen = true
		return v, nil
	case "S":
		return v, v.send()
	case "t":
		if v.targetBlocker() != "" {
			return v, nil
		}
		v.d.Target = (v.d.Target + 1) % len(v.d.Targets)
		t, _ := v.target()
		runID, co := v.d.RunID, v.d.Checkout
		return v, func() tea.Msg { return reviewTargetMsg{RunID: runID, Checkout: co, SessionID: t.ID} }
	case "A":
		return v, v.approve()
	case "r":
		if v.regenerateBlocker() != "" {
			return v, nil
		}
		m := reviewRegenerateMsg{Checkout: v.d.Checkout}
		if n := v.unsentAnswers(); n > 0 {
			v.confirm = &reviewConfirm{
				question: "Review again? Your " + rvPlural(n, "answer", "answers") + " here won't be sent.",
				yes:      "review again", msg: m,
			}
			return v, nil
		}
		return v, func() tea.Msg { return m }
	case "d":
		if v.screen != reviewScreenDiff && v.d.Doc != nil {
			v.openDiff("")
			return v, nil
		}
	}

	switch {
	case v.screen == reviewScreenDiff:
		return v.updateDiff(k)
	case v.tab == reviewTabWalk:
		return v.updateWalk(k)
	case v.screen == reviewScreenSkipped:
		return v.updateSkipped(k)
	}
	if v.finished() {
		return v.updateFinished(k)
	}
	return v.updateDecisions(k)
}

// send is S: the answers to the target, a new session when none is live, or —
// when no answer asks the agent to do anything — an offer to approve instead.
func (v *ReviewView) send() tea.Cmd {
	if v.sendBlocker() != "" {
		return nil
	}
	if v.counts().Acting() == 0 {
		if v.approveBlocker() != "" {
			return nil
		}
		v.confirm = &reviewConfirm{question: "Nothing to change. Approve instead?", yes: "approve",
			msg: reviewApproveMsg{RunID: v.d.RunID}}
		return nil
	}
	if v.startsSession() {
		g := v.d.NewSessionGlyph
		if g != "" {
			g += " "
		}
		v.confirm = &reviewConfirm{
			question: "No session here is running. Start a new " + g + "session with your answers?",
			yes:      "start", msg: reviewStartSessionMsg{RunID: v.d.RunID, Checkout: v.d.Checkout},
		}
		return nil
	}
	id := v.d.RunID
	return func() tea.Msg { return reviewSendMsg{RunID: id} }
}

// approve is A, behind a y/n when decisions are still unanswered.
func (v *ReviewView) approve() tea.Cmd {
	if v.approveBlocker() != "" {
		return nil
	}
	m := reviewApproveMsg{RunID: v.d.RunID}
	if n := v.counts().Unanswered; n > 0 && !v.sent() {
		v.confirm = &reviewConfirm{question: "Approve with " + rvPlural(n, "decision", "decisions") + " unanswered?",
			yes: "approve", msg: m}
		return nil
	}
	return func() tea.Msg { return m }
}

func (v *ReviewView) close() (*ReviewView, tea.Cmd) {
	v.Hide()
	return v, func() tea.Msg { return reviewCloseMsg{} }
}

// updateFinished is the closing screens: sent, approved, nothing needs you.
func (v *ReviewView) updateFinished(k string) (*ReviewView, tea.Cmd) {
	switch k {
	case "esc", "q":
		return v.close()
	case "v":
		if v.roundBlocker() == "" {
			co := v.d.Checkout
			return v, func() tea.Msg { return reviewRoundMsg{Checkout: co} }
		}
	case "s":
		if v.hasFiles() {
			v.openFiles(false)
		}
	}
	return v, nil
}

func (v *ReviewView) updateDecisions(k string) (*ReviewView, tea.Cmd) {
	if v.cardFocus {
		if cmd, done := v.updateCard(k); done {
			return v, cmd
		}
	}
	switch k {
	case "esc":
		if v.cardFocus {
			v.cardFocus = false
			return v, nil
		}
		if v.popBack() {
			return v, nil
		}
		return v.close()
	case "j", "down":
		if !v.cardFocus {
			v.moveCursor(1)
		}
	case "k", "up":
		if !v.cardFocus {
			v.moveCursor(-1)
		}
	case "enter", "tab":
		if v.cardFocus {
			if k == "tab" {
				v.cardFocus = false
			}
			return v, nil
		}
		it, ok := v.selectedItem()
		switch {
		case !ok:
		case it.kind == rowSafeToSkip:
			v.openFiles(false)
		case it.kind == rowNotReviewed:
			v.openFiles(true)
		case it.kind == rowFinish:
			if k == "enter" {
				return v, v.send()
			}
		case it.stop != nil && len(it.stop.Hunks) == 0 && !v.narrow():
			// No code to read, and the card beside the list already shows
			// everything (the footer says so).
		default:
			v.cardFocus = true
		}
	case "s":
		if v.hasFiles() {
			v.openFiles(false)
		}
	case "y":
		return v, v.answer(selfreview.AnswerFine)
	case "l":
		return v, v.answer(selfreview.AnswerLater)
	case "n":
		v.openNote(selfreview.AnswerChange)
	case "a":
		v.openNote(selfreview.AnswerAsk)
	}
	return v, nil
}

// updateCard handles the keys that only mean something inside the card.
// done reports whether the key was consumed.
func (v *ReviewView) updateCard(k string) (tea.Cmd, bool) {
	switch k {
	case "j", "down":
		v.cardScroll++
	case "k", "up":
		v.cardScroll--
	case "pgdown", "ctrl+d", "space":
		v.cardScroll += max(v.cardViewHeight()-2, 1)
	case "pgup", "ctrl+u":
		v.cardScroll -= max(v.cardViewHeight()-2, 1)
	case "J", "enter":
		v.jumpHunk(1)
	case "K":
		v.jumpHunk(-1)
	case "e":
		v.expandHunk()
	default:
		return nil, false
	}
	v.clampCard()
	return nil, true
}

func (v *ReviewView) updateSkipped(k string) (*ReviewView, tea.Cmd) {
	if v.cardFocus {
		if cmd, done := v.updateCard(k); done {
			return v, cmd
		}
	}
	switch k {
	case "esc", "s":
		if v.cardFocus && k == "esc" {
			v.cardFocus = false
			return v, nil
		}
		v.screen, v.cardFocus = reviewScreenDecisions, false
		v.resetCard()
	case "tab":
		v.cardFocus = !v.cardFocus
	case "j", "down":
		if !v.cardFocus && v.skipCursor < len(reviewFiles(v.d.Run))-1 {
			v.skipCursor++
			v.resetCard()
		}
	case "k", "up":
		if !v.cardFocus && v.skipCursor > 0 {
			v.skipCursor--
			v.resetCard()
		}
	case "enter":
		if _, ok := v.selectedFile(); ok {
			v.cardFocus = true
		}
	case "x":
		if f, ok := v.selectedFile(); ok && !v.sent() && !v.approved() {
			v.openNoteFor(selfreview.AnswerChange, selfreview.FileStopID(f.path), "change "+reviewBase(f.path), "")
		}
	}
	v.clamp()
	return v, nil
}

func (v *ReviewView) updateDiff(k string) (*ReviewView, tea.Cmd) {
	rows := v.diffRows()
	page := max(v.diffViewHeight()-2, 1)
	switch k {
	case "esc", "d":
		if v.popBack() {
			return v, nil
		}
		v.screen, v.diffFile = v.prev, ""
		return v, nil
	case "j", "down":
		v.diffTop++
	case "k", "up":
		v.diffTop--
	case "pgdown", "ctrl+d", "space":
		v.diffTop += page
	case "pgup", "ctrl+u":
		v.diffTop -= page
	case "g", "home":
		v.diffTop = 0
	case "G", "end":
		v.diffTop = len(rows)
	case "J", "K":
		if len(rows) == 0 {
			return v, nil
		}
		s := v.d.Doc.Stream
		at := rows[clampInt(v.diffTop, 0, max(len(rows)-1, 0))].primary()
		next := review.NextIndex(s.HunkStarts, at)
		if k == "K" {
			next = review.PrevIndex(s.HunkStarts, at)
		}
		if next >= 0 {
			v.diffTop = rowOfStream(rows, next)
		}
	}
	v.diffTop = clampInt(v.diffTop, 0, max(len(rows)-v.diffViewHeight(), 0))
	return v, nil
}

// hasFiles reports whether the file list has anything in it.
func (v *ReviewView) hasFiles() bool { return len(reviewFiles(v.d.Run)) > 0 }

// openFiles opens the file list — on the files nobody reviewed, or on the
// first one safe to skip.
func (v *ReviewView) openFiles(notReviewed bool) {
	v.screen, v.cardFocus = reviewScreenSkipped, false
	v.skipCursor = 0
	if !notReviewed && v.d.Run != nil {
		v.skipCursor = len(v.d.Run.NotCovered)
	}
	v.resetCard()
	v.clamp()
}

// openDiff opens the diff screen over whatever is showing: the whole change,
// or one file of it.
func (v *ReviewView) openDiff(file string) {
	if v.screen != reviewScreenDiff {
		v.prev = v.screen
	}
	v.screen, v.diffFile, v.diffTop = reviewScreenDiff, file, 0
}

// reviewLoc is one place in the view: what esc returns to after a jump.
type reviewLoc struct {
	tab                                                   reviewTab
	screen, prev                                          reviewScreen
	diffFile                                              string
	diffTop, cursor, walkCursor, walkItem, walkCardScroll int
	cardFocus                                             bool
}

func (v *ReviewView) pushBack() {
	v.back = append(v.back, reviewLoc{
		tab: v.tab, screen: v.screen, prev: v.prev, diffFile: v.diffFile, diffTop: v.diffTop,
		cursor: v.cursor, walkCursor: v.walkCursor, walkItem: v.walkItem,
		walkCardScroll: v.walkCardScroll, cardFocus: v.cardFocus,
	})
}

// popBack returns to where the last jump left from; false when there is none.
func (v *ReviewView) popBack() bool {
	n := len(v.back)
	if n == 0 {
		return false
	}
	l := v.back[n-1]
	v.back = v.back[:n-1]
	v.tab, v.screen, v.prev, v.diffFile, v.diffTop = l.tab, l.screen, l.prev, l.diffFile, l.diffTop
	v.cursor, v.walkCursor, v.walkItem, v.walkCardScroll, v.cardFocus = l.cursor, l.walkCursor, l.walkItem, l.walkCardScroll, l.cardFocus
	v.clamp()
	return true
}

// switchTab moves the keyboard to the other tab, closing the diff screen it
// may have been reading.
func (v *ReviewView) switchTab(t reviewTab) {
	if v.screen == reviewScreenDiff {
		v.screen, v.diffFile = v.prev, ""
	}
	v.tab = t
	if t == reviewTabWalk {
		v.cardFocus = false
	}
	v.clamp()
}

func (v *ReviewView) moveCursor(d int) {
	n := len(v.items())
	if n == 0 {
		return
	}
	c := clampInt(v.cursor+d, 0, n-1)
	if c != v.cursor {
		v.cursor = c
		v.resetCard()
	}
	v.clamp()
}

func (v *ReviewView) resetCard() { v.cardScroll, v.hunk, v.extra = 0, 0, 0 }

// answer applies a note-less answer to the row under the highlight and moves
// on to the next decision still waiting.
func (v *ReviewView) answer(kind string) tea.Cmd {
	id, why := v.answerTarget()
	if why != "" {
		return nil
	}
	return v.commit(selfreview.Answer{StopID: id, Kind: kind})
}

func (v *ReviewView) commit(a selfreview.Answer) tea.Cmd {
	if v.d.Answers == nil {
		v.d.Answers = map[string]selfreview.Answer{}
	}
	v.d.Answers[a.StopID] = a
	// The rows can change under the answer (the finish row appears, a raised
	// file joins the list), so re-find the answered row by identity first.
	from := v.cursor
	for i, it := range v.items() {
		if it.id == a.StopID && it.answerable() {
			from = i
		}
	}
	v.cursor = from
	v.advance(from)
	v.clamp()
	runID := v.d.RunID
	return func() tea.Msg { return reviewAnswerMsg{RunID: runID, Answer: a} }
}

func (v *ReviewView) openNote(kind string) {
	id, why := v.answerTarget()
	if why != "" {
		return
	}
	prefill := ""
	if it, ok := v.selectedItem(); ok && it.stop != nil && kind == selfreview.AnswerChange {
		prefill = reviewChangePrefill(*it.stop)
	}
	v.openNoteFor(kind, id, reviewAnswerWord(kind), prefill)
}

// reviewChangePrefill is what `n` starts the change note with: the reviewer's
// recommended change when it recommends one, and for the scope decision what
// it would take to finish the job. The person edits it or sends it as it is
// — it is their answer once they press ⏎.
func reviewChangePrefill(s selfreview.Stop) string {
	if s.IsScope() {
		if len(s.Missing) > 0 {
			return "Also implement: " + strings.Join(s.Missing, ", ")
		}
		return "Also implement what was asked and is missing here"
	}
	if r := s.Recommend; r != nil && r.Answer == selfreview.RecommendChange && r.Text != "" {
		return r.Text
	}
	return ""
}

func (v *ReviewView) openNoteFor(kind, id, label, prefill string) {
	ti := NewTextInput()
	ti.Prompt = label + " › "
	if kind == selfreview.AnswerAsk {
		ti.Placeholder = "your question for the agent"
	} else {
		ti.Placeholder = "what the agent should change"
	}
	if a, ok := v.d.Answers[id]; ok && a.Kind == kind {
		ti.SetValue(a.Note)
		ti.CursorEnd()
	} else if prefill != "" {
		ti.SetValue(prefill)
		ti.CursorEnd()
	}
	ti.SetWidth(max(v.width-len([]rune(ti.Prompt))-24, 10))
	ti.Focus()
	v.input, v.inputOpen = ti, true
	v.inputKind, v.inputFor, v.inputErr = kind, id, ""
}

func (v *ReviewView) updateInput(msg tea.Msg) (*ReviewView, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			v.inputOpen = false
			return v, nil
		case "enter":
			note := strings.TrimSpace(v.input.Value())
			if note == "" {
				// Refuse, don't fall back: a change with no note tells the agent
				// nothing, and the message would ship a bare "CHANGE".
				v.inputErr = "type a note first"
				return v, nil
			}
			v.inputOpen = false
			return v, v.commit(selfreview.Answer{StopID: v.inputFor, Kind: v.inputKind, Note: note})
		}
		v.inputErr = ""
	}
	var cmd tea.Cmd
	v.input, cmd = v.input.Update(msg)
	return v, cmd
}

// ---------------------------------------------------------------------------
// Card scrolling and hunks.

// reviewCardKey is everything a card layout depends on. The layout renders
// every hunk row through the syntax highlighter, and a skipped generated file
// can be hundreds of rows, so it is built once per change rather than on every
// key and frame.
type reviewCardKey struct {
	id         string
	kind       reviewRowKind
	screen     reviewScreen
	w, hunk    int
	extra      int
	focused    bool
	gen        int
	answer     selfreview.Answer
	answers    int // the finish card lists every answer
	themeProbe string
}

func (v *ReviewView) cardLayout() reviewCardLayout {
	c, ok := v.cardSubject()
	if !ok {
		return reviewCardLayout{}
	}
	key := reviewCardKey{id: c.id, kind: c.kind, screen: v.screen, w: v.cardInnerWidth(), hunk: v.hunk, extra: v.extra,
		focused: v.cardFocus && !v.inputOpen, gen: v.docGen, answer: v.d.Answers[c.id],
		themeProbe: DimStyle.Render("x")}
	if c.kind == rowFinish {
		// The finish card lists every answer, and v.d.Answers is the view's
		// own copy: count edits so any answer rebuilds it.
		key.answers = len(v.d.Answers)
		key.gen = -1
	}
	if c.kind != rowFinish && v.cardCacheOK && v.cardCacheKey == key {
		return v.cardCache
	}
	v.cardCache = layoutReviewCard(c, key.answer, v.d.Doc, key.w, key.hunk, key.extra, key.focused)
	v.cardCacheKey, v.cardCacheOK = key, true
	return v.cardCache
}

func (v *ReviewView) jumpHunk(d int) {
	lay := v.cardLayout()
	if len(lay.hunkLines) == 0 {
		return
	}
	v.hunk = clampInt(v.hunk+d, 0, len(lay.hunkLines)-1)
	v.cardScroll = lay.hunkLines[v.hunk]
}

// expandHunk is `e`: more lines around the card's code — first more of the
// hunk the citation sits in, then, once the whole hunk shows, unchanged lines
// from the worktree around it.
func (v *ReviewView) expandHunk() {
	if v.expandBlocker() != "" {
		return
	}
	c, ok := v.cardSubject()
	if !ok || v.hunk >= len(c.hunks) {
		return
	}
	h := c.hunks[v.hunk]
	span, ok := reviewHunkSpan(v.d.Doc, h)
	if !ok {
		return
	}
	if reviewTrimHides(v.d.Doc, span, h, reviewCardContext+v.extra) {
		v.extra += 8
		return
	}
	if v.d.Doc.CanExpand() && v.d.Doc.ExpandRows(span.From, span.To, false) {
		v.docGen++
	}
}

func (v *ReviewView) clampCard() {
	lay := v.cardLayout()
	v.cardScroll = clampInt(v.cardScroll, 0, max(len(lay.lines)-v.cardViewHeight(), 0))
}

func (v *ReviewView) clamp() {
	v.cursor = clampInt(v.cursor, 0, max(len(v.items())-1, 0))
	v.skipCursor = clampInt(v.skipCursor, 0, max(len(reviewFiles(v.d.Run))-1, 0))
	v.walkCursor = clampInt(v.walkCursor, 0, max(len(v.walkRows())-1, 0))
}

// diffRows flattens the stream for the diff screen: the whole change, or the
// one file it was opened on.
func (v *ReviewView) diffRows() []dispRow {
	if v.d.Doc == nil {
		return nil
	}
	s := v.d.Doc.Stream
	if v.diffFile != "" {
		for i, f := range s.Files {
			if f == v.diffFile && i < len(s.FileStarts) {
				to := len(s.Lines)
				if i+1 < len(s.FileStarts) {
					to = s.FileStarts[i+1]
				}
				// The spacing row before the next file is not this file's.
				for to > s.FileStarts[i]+1 && s.Lines[to-1].File != f {
					to--
				}
				return buildRowsRange(s, s.FileStarts[i], to, false)
			}
		}
	}
	return buildRows(s, false)
}
