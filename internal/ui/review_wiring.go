package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/deliver"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/reviewjob"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// The self-review wiring: the glue between the Review view (a pure component,
// review_view*.go), internal/reviewjob (prepare / answer / send / approve), and
// the main screen's ✎ badge and preview line.
//
// Everything that touches git, SQLite or a pane runs in a tea.Cmd; the Update
// goroutine only flips maps and hands data to the view. The one ordering
// hazard — an answer saved in one Cmd and a send loading answers in another —
// is closed by reviewWrites: every answer write is counted on the Update
// goroutine *before* its Cmd is returned, and send/approve wait for the count
// to drain before reading.

// reviewSweepInterval throttles the worker's summary sweep. A summary is one
// query plus one JSON decode per ready run; a few seconds is fine latency for
// a run another process (`fleet review`) finished.
const reviewSweepInterval = 5 * time.Second

// reviewPrepareTimeout bounds a prepare from the TUI: the reviewer's own
// timeout and its one repair retry, plus the git work around them.
const reviewPrepareTimeout = 2*selfreview.DefaultTimeout + 2*time.Minute

// reviewSendTimeout bounds a send: deliver blocks up to ~12s (startup grace
// plus the delivery check), a wake adds a relaunch.
const reviewSendTimeout = 45 * time.Second

type (
	// reviewSummariesMsg carries every checkout's latest-run summary.
	reviewSummariesMsg struct{ m map[string]reviewjob.Summary }

	// reviewPreparedMsg is a prepare's outcome.
	reviewPreparedMsg struct {
		checkout string
		rec      *reviewjob.Record
		err      error
		base     string // the base the diff was measured against, for "nothing to review"
	}

	// reviewLoadedMsg carries a run loaded for the view, its diff built.
	reviewLoadedMsg struct {
		checkout string
		rec      *reviewjob.Record
		doc      *review.Doc
		err      error
		open     bool // open the view on arrival (else refresh it if it shows this checkout)
	}

	// reviewStaleMsg is CheckStale's verdict on a run.
	reviewStaleMsg struct {
		runID string
		stale bool
	}

	// reviewWriteDoneMsg reports a SaveAnswer / MarkSeen / SetTarget result.
	reviewWriteDoneMsg struct {
		what string // "seen", "answer", "target"
		err  error
	}

	// reviewSentMsg is a send's outcome.
	reviewSentMsg struct {
		checkout, runID, title string
		answers                int
		err                    error
	}

	// reviewApprovedMsg is an approve's outcome.
	reviewApprovedMsg struct {
		checkout, runID string
		err             error
	}

	// reviewDiscardMsg is the confirmed `d` on a review row.
	reviewDiscardMsg struct{ checkout string }

	// reviewDiscardedMsg is a discard's outcome.
	reviewDiscardedMsg struct {
		checkout string
		err      error
	}

	// reviewStartReadyMsg carries the composed answers for a new session:
	// the round file is written, the run is still unsent until it starts.
	reviewStartReadyMsg struct {
		checkout, runID, prompt string
		round                   int
		err                     error
	}

	// reviewMarkedSentMsg reports recording a new session as a run's target
	// once it started with the answers.
	reviewMarkedSentMsg struct {
		runID string
		err   error
	}
)

// reviewCheckoutOf is the checkout a sidebar row's review belongs to: a
// session row's checkout, a checkout header, or the review row itself. why is
// set when the row has none (an origin header stands for several checkouts).
func reviewCheckoutOf(item SidebarItem) (root, why string) {
	switch {
	case item.IsReview, item.IsCheckoutHeader:
		return item.RepoPath, ""
	case item.IsOriginHeader:
		return "", "pick a checkout"
	case item.Session != nil:
		return session.GetRepoRoot(item.Session.ProjectPath), ""
	}
	return "", "nothing to review here"
}

// reviewCheckoutAtCursor is reviewCheckoutOf for the row under the cursor.
func (h *Home) reviewCheckoutAtCursor() (root, why string) {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return "", "nothing to review here"
	}
	return reviewCheckoutOf(h.flatItems[h.cursor])
}

// reviewBlocker says why a checkout can't be reviewed, or "". The `v` key, the
// context-menu rows and the palette command all ask this one function, so a lit
// row never dead-clicks and a dimmed one names the same reason the key would.
// Any checkout with a branch qualifies — a main clone as much as a worktree.
func (h *Home) reviewBlocker(root string) string {
	if root == "" {
		return "no checkout"
	}
	info := h.gitInfo()[root]
	switch {
	case info == nil:
		// The git cache fills a few seconds after launch; "no git repo"
		// there would be a false claim about the user's checkout.
		return "git status loading"
	case !info.IsWorktreeRepo && info.Branch == "":
		return "no git repo"
	}
	return ""
}

// reviewCheckout is the checkout at root with the sessions working it.
func (h *Home) reviewCheckout(root string) reviewjob.Checkout {
	return reviewjob.CheckoutOf(root, h.sessions)
}

// reviewSelected is `v` (and the palette / menu "review" command): open the
// cursor's checkout's review when one exists, else start preparing one.
func (h *Home) reviewSelected() (tea.Model, tea.Cmd) {
	root, why := h.reviewCheckoutAtCursor()
	if why == "" {
		why = h.reviewBlocker(root)
	}
	if why != "" {
		h.setInfo("Can't review: " + why)
		return h, nil
	}
	// The row reads "press v for round 2" once the agent has finished the
	// sent answers: v is that next round. ⏎ on the row still opens the run.
	if b := h.reviewBadgeFor(root); b.Status == ReviewBadgeSent && !b.Working {
		return h, h.startReviewPrepare(root, false)
	}
	return h, h.reviewOpenOrPrepare(root)
}

// reviewOpenOrPrepare opens root's review — on the preparing screen while one
// is being generated — or starts preparing one when there is none (or the last
// one failed: its reason is on the row, and this is the retry).
func (h *Home) reviewOpenOrPrepare(root string) tea.Cmd {
	label := h.reviewLabel(root)
	sum, has := h.reviewSummaries[root]
	switch {
	case h.reviewPreparing[root] || (has && sum.Status == session.ReviewPreparing):
		h.logAction("review open", label, true)
		h.reviewView.SetSize(h.width, h.height)
		h.reviewView.Show(ReviewData{
			Checkout: root, CheckoutLabel: label, Freshness: ReviewPreparing,
			NewSessionGlyph: h.reviewNewSessionGlyph(),
		})
		h.syncReviewTargets(sum.Target)
		return nil
	case has && (sum.Status == session.ReviewReady || sum.Status == session.ReviewSent || sum.Status == session.ReviewApproved):
		h.logAction("review open", label, true)
		return h.reviewLoadCmd(root, true)
	}
	return h.startReviewPrepare(root, false)
}

// reviewLabel names a checkout: a worktree by its branch; a main clone by
// its directory and branch, since "main" alone names every repo's main.
func (h *Home) reviewLabel(root string) string {
	info := h.gitInfo()[root]
	switch {
	case info == nil || info.Branch == "":
		return filepath.Base(root)
	case info.IsWorktreeRepo:
		return info.Branch
	}
	return filepath.Base(root) + " · " + info.Branch
}

// startReviewPrepare kicks a prepare off the Update goroutine and shows the
// review row as preparing at once rather than on the next summary sweep.
func (h *Home) startReviewPrepare(root string, force bool) tea.Cmd {
	if h.reviewPreparing[root] {
		return nil
	}
	h.reviewPreparing[root] = true
	delete(h.reviewLocalFail, root)
	h.applyReviewBadges()
	label := h.reviewLabel(root)
	if force {
		h.logAction("review regenerate", label, true)
	} else {
		h.logAction("review prepare", label, true)
	}
	if !h.reviewView.IsVisible() {
		h.setInfo("Preparing a review. It takes a minute or two.")
	}
	c := h.reviewCheckout(root)
	deps := h.reviewDeps(c)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewPrepareTimeout)
		defer cancel()
		rec, err := reviewjob.PrepareWith(ctx, deps, c, reviewjob.PrepareOptions{Force: force})
		msg := reviewPreparedMsg{checkout: root, rec: rec, err: err}
		if errors.Is(err, reviewjob.ErrNothingToReview) {
			msg.base = reviewBaseFor(c, deps)
		}
		return msg
	}
}

// reviewBaseFor names the base a review would measure against — only for the
// "nothing to review" toast, so a best-effort read.
func reviewBaseFor(c reviewjob.Checkout, d reviewjob.Deps) string {
	prBase := ""
	if d.PR != nil {
		_, branch := git.ReadHead(c.Root)
		if pr := d.PR(c.Root, branch); pr != nil {
			prBase = pr.BaseRefName
		}
	}
	return reviewjob.ResolveBase(c, prBase)
}

// reviewDeps builds reviewjob's seams from the TUI's live state. Everything is
// resolved here, on the Update goroutine, and captured by value: the Cmd must
// not read h.cfg or the account store while Settings or the quota poll writes
// them.
func (h *Home) reviewDeps(c reviewjob.Checkout) reviewjob.Deps {
	model := reviewjob.ModelOrDefault(h.cfg.GetReviewModel())
	email, dir := h.reviewAccountFor(c)
	snap := h.gitInfo() // immutable snapshot
	return reviewjob.Deps{
		Store: h.storage,
		Model: func() string { return model },
		Account: func(context.Context, reviewjob.Checkout) (string, string) {
			if dir != "" {
				// Refresh the account dir the way every launch does; off the
				// Update goroutine, since it copies settings and links dirs.
				if err := claudeaccount.Provision(dir); err != nil {
					debuglog.Logger.Warn("review: provisioning account dir failed", "dir", dir, "err", err)
				}
			}
			return email, dir
		},
		PR: func(root, branch string) *github.PR {
			if info := snap[root]; info != nil && info.PR != nil && (branch == "" || info.Branch == branch) {
				return info.PR
			}
			return nil
		},
	}
}

// reviewAccountFor picks the account the reviewer runs as, from the TUI's live
// quota: the checkout's default send target's account unless it is logged out
// or spent, else what account_strategy would give a new session here
// (allowlist included, via selectAccountFor — the one place the TUI builds
// SelectOpts). Nothing selectable → the ambient login, as reviewjob's own
// default does.
func (h *Home) reviewAccountFor(c reviewjob.Checkout) (email, configDir string) {
	if h.accounts == nil || h.accounts.Len() == 0 {
		return "", ""
	}
	usage := h.accountUsageSnapshot()
	now := time.Now()
	if s := reviewjob.DefaultTarget(c, h.reviewSummaries[c.Root].Target); s != nil && s.Account != "" && s.Agent == agent.Claude {
		if acct, ok := h.accounts.Get(s.Account); ok {
			if u := usage[s.Account]; !u.LoggedOut && !u.Exhausted(now) {
				return acct.Email, acct.ConfigDir
			}
		}
	}
	if acct, ok := h.selectAccountFor(c.Root); ok {
		return acct.Email, acct.ConfigDir
	}
	return "", ""
}

// reviewTargetLive reports whether S can deliver to s: a pane to type into, or
// a suspended session deliver wakes. A session in error has neither.
func reviewTargetLive(s *session.Session) bool {
	st := s.GetStatus()
	if st == session.StatusSuspended {
		return true
	}
	return st != session.StatusError && s.IsAlive()
}

// reviewTargets is the view's send-target list for root: the checkout's
// sessions in reviewjob.Targets order with the live ones first, and the
// chosen one — the pick made in this TUI, else the run's stored target, else
// the first — carried in Target by the caller via reviewTargetIndex.
func (h *Home) reviewTargets(root string) []reviewTarget {
	ordered := reviewjob.Targets(h.reviewCheckout(root))
	sort.SliceStable(ordered, func(i, j int) bool {
		return reviewTargetLive(ordered[i]) && !reviewTargetLive(ordered[j])
	})
	out := make([]reviewTarget, 0, len(ordered))
	for _, s := range ordered {
		st := s.GetStatus()
		out = append(out, reviewTarget{
			ID: s.ID, Title: s.Title, Glyph: agentGlyph(s.Agent), Live: reviewTargetLive(s),
			Running: st == session.StatusRunning || st == session.StatusStarting,
		})
	}
	return out
}

// reviewTargetIndex picks the default target in ts: this TUI's pick, else the
// run's stored target, else the first (the live session that most recently
// finished work).
func (h *Home) reviewTargetIndex(root, stored string, ts []reviewTarget) int {
	for _, want := range []string{h.reviewTargetPick[root], stored} {
		if want == "" {
			continue
		}
		for i, t := range ts {
			if t.ID == want {
				return i
			}
		}
	}
	return 0
}

// reviewNewSessionGlyph is the default agent's sigil, for "a new ✻ session".
func (h *Home) reviewNewSessionGlyph() string {
	return agentGlyph(agent.Parse(h.cfg.GetDefaultAgent()))
}

// reviewLoadCmd loads the checkout's latest run and builds its diff.
func (h *Home) reviewLoadCmd(root string, open bool) tea.Cmd {
	st := h.storage
	return func() tea.Msg {
		rec, err := reviewjob.Load(st, root)
		msg := reviewLoadedMsg{checkout: root, rec: rec, err: err, open: open}
		if err == nil && rec != nil {
			msg.doc = reviewDocFor(rec)
		}
		return msg
	}
}

// reviewDocFor rebuilds the run's diff (base → snapshot tree) for the card and
// the full-diff screen. nil when it can't — the view then shows no code.
func reviewDocFor(rec *reviewjob.Record) *review.Doc {
	row := rec.Row
	if row == nil || rec.Run == nil || row.BaseSHA == "" || row.TreeSHA == "" || row.Worktree == "" {
		return nil
	}
	diffs, err := git.DiffFiles(row.Worktree, row.BaseSHA, row.TreeSHA)
	if err != nil {
		debuglog.Logger.Warn("review: rebuilding the diff failed", "run", row.ID, "err", err)
		return nil
	}
	files := make([]review.File, 0, len(diffs))
	for _, f := range diffs {
		files = append(files, review.File{
			Path: f.Path, Previous: f.OldPath, Status: reviewFileStatus(f.Status),
			Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch, Binary: f.Binary,
		})
	}
	return review.NewDoc(files, row.Worktree)
}

// reviewFileStatus maps git's status letter onto the engine's vocabulary
// (GitHub's words, which is what internal/review was written against).
func reviewFileStatus(letter string) string {
	switch letter {
	case "A":
		return "added"
	case "D":
		return "removed"
	case "R":
		return "renamed"
	case "C":
		return "copied"
	case "M":
		return "modified"
	}
	return "changed"
}

// reviewDataFor is the view's data for a loaded run. Targets are filled by the
// caller (syncReviewTargets), which knows the live sessions.
func reviewDataFor(root, label string, rec *reviewjob.Record, doc *review.Doc, stale bool) ReviewData {
	d := ReviewData{Checkout: root, CheckoutLabel: label, Doc: doc}

	if rec == nil || rec.Row == nil {
		return d
	}
	row := rec.Row
	d.RunID, d.Round, d.Model = row.ID, row.Round, row.Model
	d.Elapsed = time.Duration(row.ElapsedMS) * time.Millisecond
	d.Snapshot = reviewShortSHA(row.HeadSHA)
	if rec.Snapshot.Dirty {
		d.Snapshot += " + uncommitted"
	}
	d.Run = rec.Run
	d.Answers = make(map[string]selfreview.Answer, len(rec.Answers))
	for _, a := range rec.Answers {
		d.Answers[a.StopID] = a
	}
	switch rec.Status {
	case session.ReviewPreparing:
		d.Freshness = ReviewPreparing
	case session.ReviewFailed:
		d.Freshness, d.FailReason = ReviewFailed, row.Error
	case session.ReviewSent, session.ReviewApproved:
		d.Status = rec.Status
	}
	if stale && d.Freshness == "" {
		d.Freshness = ReviewStale
	}
	if d.Freshness == "" {
		d.Freshness = ReviewFresh
	}
	return d
}

func reviewShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// reviewViewCheckout is the checkout the open view shows, or "".
func (h *Home) reviewViewCheckout() string {
	if !h.reviewView.IsVisible() {
		return ""
	}
	return h.reviewView.Data().Checkout
}

// reviewViewing reports whether the open view shows root.
func (h *Home) reviewViewing(root string) bool {
	return root != "" && h.reviewViewCheckout() == root
}

// syncReviewTargets refreshes the open view's send targets — which sessions
// exist, are live, are running — keeping its chosen one, or picking the default
// (stored is the run's recorded target) when it has none. Called on open and on
// every tick.
func (h *Home) syncReviewTargets(stored string) {
	root := h.reviewViewCheckout()
	if root == "" {
		return
	}
	ts := h.reviewTargets(root)
	if len(h.reviewView.Data().Targets) == 0 {
		// First fill: the default, not "whatever index 0 happens to be".
		d := h.reviewView.Data()
		d.Targets, d.Target = ts, h.reviewTargetIndex(root, stored, ts)
		d.NewSessionGlyph = h.reviewNewSessionGlyph()
		d.AgentWorking = h.reviewBadgeFor(root).Working
		h.reviewView.SetData(d)
		return
	}
	h.reviewView.SetTargets(ts)
	h.reviewView.SetAgentWorking(h.reviewBadgeFor(root).Working)
}

// reviewSummariesCmd refreshes the review rows now (after an action), rather
// than on the next sweep.
func (h *Home) reviewSummariesCmd() tea.Cmd {
	st := h.storage
	return func() tea.Msg {
		m, err := reviewjob.Summaries(st, nil)
		if err != nil {
			debuglog.Logger.Warn("review summaries failed", "err", err)
			return nil
		}
		return reviewSummariesMsg{m: m}
	}
}

// maybeSyncReviewSummaries is the worker's sweep: it re-reads every checkout's
// latest-run summary so a run finished elsewhere (`fleet review`) shows in the
// sidebar. Runs on the worker goroutine (SQLite); the rows update on Update via
// reviewSummariesMsg. Every checkout's: the table holds a row per run, and the
// sidebar decides which checkouts are on screen.
func (h *Home) maybeSyncReviewSummaries() {
	if !h.lastReviewSweepAt.IsZero() && time.Since(h.lastReviewSweepAt) < reviewSweepInterval {
		return
	}
	h.lastReviewSweepAt = time.Now()
	m, err := reviewjob.Summaries(h.storage, nil)
	if err != nil {
		debuglog.Logger.Warn("review summaries sweep failed", "err", err)
		return
	}
	// program.Send is a rendezvous an attach suspends; the next sweep resends.
	if h.isAttaching.Load() {
		return
	}
	h.send(reviewSummariesMsg{m: m})
}

// reviewBadgeFor is the checkout's review-row state. Local knowledge (a
// prepare this TUI started, a failure that left no row, a stale verdict) beats
// the stored summary, which only the next sweep would correct.
func (h *Home) reviewBadgeFor(root string) reviewBadge {
	if h.reviewPreparing[root] {
		return reviewBadge{Status: ReviewBadgePreparing}
	}
	if reason, ok := h.reviewLocalFail[root]; ok {
		return reviewBadge{Status: ReviewBadgeFailed, Reason: reason}
	}
	sum, ok := h.reviewSummaries[root]
	if !ok {
		return reviewBadge{}
	}
	b := reviewBadge{Counts: sum.Counts, Seen: sum.Seen, Model: sum.Model, Elapsed: sum.Elapsed,
		Verdict: sum.Verdict, Reason: sum.Error, Round: sum.Round}
	switch sum.Status {
	case session.ReviewPreparing:
		b.Status = ReviewBadgePreparing
	case session.ReviewFailed:
		b.Status = ReviewBadgeFailed
	case session.ReviewReady:
		b.Status = ReviewBadgeReady
		if h.reviewStale[sum.RunID] {
			b.Status = ReviewBadgeStale
		}
	case session.ReviewSent:
		b.Status = ReviewBadgeSent
		b.Working = h.reviewAgentWorking(sum)
	case session.ReviewApproved:
		b.Status = ReviewBadgeApproved
	}
	return b
}

// reviewSentGrace is how long after a send the agent counts as working even
// before its status says so: the hook that flips it to running lands a moment
// after the paste, and "press v for round 2" in that gap would invite a review
// of work not yet started.
const reviewSentGrace = 20 * time.Second

// reviewAgentWorking reports whether the session a sent run went to is still
// working on the answers.
func (h *Home) reviewAgentWorking(sum reviewjob.Summary) bool {
	if !sum.SentAt.IsZero() && time.Since(sum.SentAt) < reviewSentGrace {
		return true
	}
	s := h.sessionByID[sum.Target]
	if s == nil {
		return false
	}
	switch s.GetStatus() {
	case session.StatusRunning, session.StatusStarting, session.StatusWaiting:
		return true
	}
	return false
}

// reviewCI is the checkout's live CI rollup in selfreview's words, from the
// git cache's PR (nothing when there is no PR).
func (h *Home) reviewCI(root string) (string, []string) {
	info := h.gitInfo()[root]
	if info == nil || info.PR == nil {
		return selfreview.CIUnknown, nil
	}
	switch info.PR.CIStatus {
	case "SUCCESS":
		return selfreview.CISuccess, nil
	case "PENDING":
		return selfreview.CIPending, nil
	case "FAILURE":
		return selfreview.CIFailure, info.PR.FailedChecks
	}
	return selfreview.CIUnknown, nil
}

// reviewPreviewFor is the preview's ✎ line for a session: its checkout's review.
func (h *Home) reviewPreviewFor(s *session.Session) reviewPreview {
	if s == nil {
		return reviewPreview{}
	}
	return h.reviewPreviewForCheckout(session.GetRepoRoot(s.ProjectPath))
}

// reviewPreviewForCheckout is the ✎ line for a checkout's review.
func (h *Home) reviewPreviewForCheckout(root string) reviewPreview {
	b := h.reviewBadgeFor(root)
	p := reviewPreview{Status: b.Status, Reason: b.Reason}
	if b.Status != ReviewBadgePreparing {
		p.Verdict = b.Verdict
	}
	return p
}

// reviewRowBadge is what rebuildFlatItems asks to place a checkout's review
// row: shown while a run exists or is preparing, gone once approved.
func (h *Home) reviewRowBadge(root string) (reviewBadge, bool) {
	b := h.reviewBadgeFor(root)
	return b, reviewRowShown(b)
}

// applyReviewBadges re-lays the sidebar after review state changed: a review
// row may appear, disappear or change its text. Called whenever review state
// changes; rebuildFlatItems keeps the cursor on its row by identity when rows
// come or go.
func (h *Home) applyReviewBadges() {
	h.rebuildFlatItems()
}

// syncReviewTick keeps the open view's send targets in step with the sessions;
// called on every tick.
func (h *Home) syncReviewTick() {
	if root := h.reviewViewCheckout(); root != "" {
		h.syncReviewTargets(h.reviewSummaries[root].Target)
	}
}

// handleReviewMsg handles every review message. ok=false: not one of ours.
func (h *Home) handleReviewMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reviewSummariesMsg:
		h.reviewSummaries = msg.m
		h.applyReviewBadges()
		// A view left on the preparing screen for a run another process (or
		// an earlier TUI) is preparing fills in once the sweep sees it land.
		if root := h.reviewViewCheckout(); root != "" && !h.reviewPreparing[root] {
			d := h.reviewView.Data()
			if sum, ok := msg.m[root]; ok && d.Run == nil && d.Freshness == ReviewPreparing && sum.Status != session.ReviewPreparing {
				return h.reviewLoadCmd(root, false), true
			}
		}
		return nil, true

	case reviewPreparedMsg:
		return h.handleReviewPrepared(msg), true

	case reviewLoadedMsg:
		return h.handleReviewLoaded(msg), true

	case reviewStaleMsg:
		if msg.stale {
			h.reviewStale[msg.runID] = true
		} else {
			delete(h.reviewStale, msg.runID)
		}
		h.applyReviewBadges()
		if h.reviewView.IsVisible() {
			d := h.reviewView.Data()
			if d.RunID == msg.runID && (d.Freshness == ReviewFresh || d.Freshness == ReviewStale) {
				d.Freshness = ReviewFresh
				if msg.stale {
					d.Freshness = ReviewStale
				}
				h.reviewView.SetData(d)
			}
		}
		return nil, true

	case reviewWriteDoneMsg:
		if msg.err != nil {
			switch msg.what {
			case "seen":
				h.setError("review_seen", msg.err)
			case "target":
				h.setError("review_target", msg.err)
			default:
				h.setError("review_answer", msg.err)
			}
		}
		return h.reviewSummariesCmd(), true

	case reviewAnswerMsg:
		// The view already shows the answer; persist it. Counted here, before
		// the Cmd exists, so a send dispatched after it waits for it.
		h.reviewWrites.Add(1)
		st, runID, ans := h.storage, msg.RunID, msg.Answer
		wg := &h.reviewWrites
		return func() tea.Msg {
			defer wg.Done()
			return reviewWriteDoneMsg{what: "answer", err: reviewjob.SaveAnswer(st, runID, ans)}
		}, true

	case reviewTargetMsg:
		// Remembered here at once (the next open defaults to it) and on the
		// run, so `fleet review --json` and a restarted TUI agree.
		h.reviewTargetPick[msg.Checkout] = msg.SessionID
		if msg.RunID == "" {
			return nil, true
		}
		st, runID, id := h.storage, msg.RunID, msg.SessionID
		return func() tea.Msg {
			return reviewWriteDoneMsg{what: "target", err: reviewjob.SetTarget(st, runID, id)}
		}, true

	case reviewSendMsg:
		return h.reviewSend(msg.RunID), true

	case reviewStartSessionMsg:
		return h.reviewStartSession(msg.RunID, msg.Checkout), true

	case reviewStartReadyMsg:
		return h.handleReviewStartReady(msg), true

	case reviewMarkedSentMsg:
		if msg.err != nil {
			h.setError("review_send", msg.err)
			return nil, true
		}
		if h.reviewView.IsVisible() && h.reviewView.Data().RunID == msg.runID {
			d := h.reviewView.Data()
			d.Status = reviewStatusSent
			h.reviewView.SetData(d)
		}
		return h.reviewSummariesCmd(), true

	case reviewApproveMsg:
		return h.reviewApprove(msg.RunID), true

	case reviewRoundMsg:
		if msg.Checkout == "" {
			return nil, true
		}
		return h.startReviewPrepare(msg.Checkout, false), true

	case reviewRegenerateMsg:
		if msg.Checkout == "" {
			return nil, true
		}
		// A toast from before ("Review approved") must not sit over the
		// new review being prepared.
		h.toasts.Clear()
		if h.reviewViewing(msg.Checkout) {
			d := h.reviewView.Data()
			d.Freshness = ReviewPreparing
			h.reviewView.SetData(d)
		}
		return h.startReviewPrepare(msg.Checkout, true), true

	case reviewCloseMsg:
		return h.reviewSummariesCmd(), true

	case reviewSentMsg:
		return h.handleReviewSent(msg), true

	case reviewApprovedMsg:
		if msg.err != nil {
			h.setError("review_approve", msg.err)
			return nil, true
		}
		// Approving is how a review is cleared: its row goes away, so the
		// view goes with it rather than lingering on counts that no longer
		// apply.
		if h.reviewView.IsVisible() && h.reviewView.Data().RunID == msg.runID {
			h.reviewView.Hide()
		}
		h.setInfo("Approved. " + h.reviewLabel(msg.checkout) + " has no review waiting.")
		return h.reviewSummariesCmd(), true

	case reviewDiscardMsg:
		st, root := h.storage, msg.checkout
		h.logAction("review discard", h.reviewLabel(root), true)
		return func() tea.Msg {
			return reviewDiscardedMsg{checkout: root, err: reviewjob.Discard(st, root)}
		}, true

	case reviewDiscardedMsg:
		if msg.err != nil {
			h.setError("review_discard", msg.err)
			return nil, true
		}
		delete(h.reviewSummaries, msg.checkout)
		delete(h.reviewLocalFail, msg.checkout)
		delete(h.reviewTargetPick, msg.checkout)
		h.applyReviewBadges()
		h.clampCursor()
		h.setInfo("Review discarded.")
		return h.reviewSummariesCmd(), true
	}
	return nil, false
}

func (h *Home) handleReviewPrepared(msg reviewPreparedMsg) tea.Cmd {
	delete(h.reviewPreparing, msg.checkout)
	viewing := h.reviewViewing(msg.checkout)
	refresh := h.reviewSummariesCmd()

	switch {
	case errors.Is(msg.err, reviewjob.ErrNothingToReview):
		base := msg.base
		if base == "" {
			base = "its base"
		}
		h.setInfo("Nothing to review: no changes against " + base + ".")
		if viewing {
			d := h.reviewView.Data()
			d.Freshness, d.FailReason = ReviewFailed, "nothing to review: no changes against "+base
			h.reviewView.SetData(d)
		}
		h.applyReviewBadges()
		return refresh

	case msg.err != nil:
		reason := msg.err.Error()
		if msg.rec == nil || msg.rec.Row == nil {
			// Failed before a run was stored: only this map remembers why.
			h.reviewLocalFail[msg.checkout] = reason
		} else if msg.rec.Row.Error != "" {
			reason = msg.rec.Row.Error
		}
		// A toast is a sentence for a person, not an error chain.
		toast := "Review failed. " + reviewFailText(reason) + " Press v to retry."
		h.setError("review_prepare", errors.New(toast))
		if viewing {
			d := h.reviewView.Data()
			d.Freshness, d.FailReason = ReviewFailed, reason
			h.reviewView.SetData(d)
		}
		h.applyReviewBadges()
		return refresh
	}

	delete(h.reviewLocalFail, msg.checkout)
	h.applyReviewBadges()
	if msg.rec != nil && msg.rec.Status == session.ReviewSent {
		// v asked for the next round, but the checkout hasn't moved since the
		// answers went out: there is nothing new to review yet.
		h.setInfo("No new changes since you sent your answers.")
		return tea.Batch(refresh, h.reviewLoadCmd(msg.checkout, !viewing))
	}
	if viewing {
		// Regenerated (or first-prepared) under the open view: load the new
		// run and its diff into it.
		return tea.Batch(refresh, h.reviewLoadCmd(msg.checkout, false))
	}
	// Never yank focus: the review row says where it is.
	h.setInfo("Review ready for " + h.reviewLabel(msg.checkout) + ". Open it from its Review row.")
	return refresh
}

func (h *Home) handleReviewLoaded(msg reviewLoadedMsg) tea.Cmd {
	if msg.err != nil {
		h.setError("review_load", msg.err)
		return nil
	}
	if msg.rec == nil || msg.rec.Row == nil {
		return nil
	}
	root := msg.checkout
	label := h.reviewLabel(root)
	stored := msg.rec.Row.SessionID
	switch {
	case h.reviewViewing(root):
		// Keep the run's freshness only if the new data is still that run,
		// and the chosen send target across the reload.
		prev := h.reviewView.Data()
		d := reviewDataFor(root, label, msg.rec, msg.doc, h.reviewStale[msg.rec.Row.ID])
		d.CI, d.CIFailed = h.reviewCI(root)
		d.Targets, d.Target, d.NewSessionGlyph = prev.Targets, prev.Target, prev.NewSessionGlyph
		h.reviewView.SetData(d)
		h.syncReviewTargets(stored)
	case msg.open && !h.modalOpen():
		h.reviewView.SetSize(h.width, h.height)
		d := reviewDataFor(root, label, msg.rec, msg.doc, h.reviewStale[msg.rec.Row.ID])
		d.CI, d.CIFailed = h.reviewCI(root)
		h.reviewView.Show(d)
		h.syncReviewTargets(stored)
	default:
		return nil
	}
	return tea.Batch(h.reviewSeenCmd(msg.rec), h.reviewStaleCmd(msg.rec))
}

// reviewSeenCmd marks a run opened (the review row goes dim).
func (h *Home) reviewSeenCmd(rec *reviewjob.Record) tea.Cmd {
	if rec == nil || rec.Row == nil || !rec.Row.SeenAt.IsZero() || rec.Run == nil {
		return nil
	}
	st, runID := h.storage, rec.Row.ID
	return func() tea.Msg {
		return reviewWriteDoneMsg{what: "seen", err: reviewjob.MarkSeen(st, runID)}
	}
}

// reviewStaleCmd is the lazy staleness check: a SnapshotTree hashes changed
// files, so it never runs on the Update goroutine and never blocks opening.
func (h *Home) reviewStaleCmd(rec *reviewjob.Record) tea.Cmd {
	if rec == nil || rec.Row == nil || rec.Run == nil {
		return nil
	}
	st := h.storage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		stale, err := reviewjob.CheckStale(ctx, reviewjob.Deps{Store: st}, rec)
		if err != nil {
			debuglog.Logger.Debug("review: stale check failed", "run", rec.Row.ID, "err", err)
			return nil
		}
		return reviewStaleMsg{runID: rec.Row.ID, stale: stale}
	}
}

// reviewSend composes and delivers the open run's answers to the view's chosen
// target.
func (h *Home) reviewSend(runID string) tea.Cmd {
	root := h.reviewViewCheckout()
	t, ok := h.reviewView.target()
	if root == "" || !ok {
		return nil
	}
	s := h.sessionByID[t.ID]
	if s == nil {
		h.setError("review_send", errors.New("the send target is gone — t picks another"))
		return nil
	}
	h.logAction("review send", s.Title, true)
	deps := h.reviewDeps(h.reviewCheckout(root))
	wg := &h.reviewWrites
	title := s.Title
	return func() tea.Msg {
		wg.Wait() // every answer given before S is on disk
		out := reviewSentMsg{checkout: root, runID: runID, title: title}
		rec, err := reviewjob.Load(deps.Store, root)
		switch {
		case err != nil:
			out.err = err
			return out
		case rec == nil || rec.Row == nil || rec.Row.ID != runID:
			out.err = errors.New("a newer review replaced this one — reopen it")
			return out
		}
		out.answers = reviewActingAnswers(rec)
		ctx, cancel := context.WithTimeout(context.Background(), reviewSendTimeout)
		defer cancel()
		_, out.err = reviewjob.Send(ctx, deps, s, rec)
		return out
	}
}

// reviewRefusalText is a refused send in plain sentences. Refusal.Short names
// main-screen keys ("press r"), which mean something else inside the view, so
// the review speaks for itself: what happened, then what to do.
func reviewRefusalText(title string, ref *deliver.Refusal) string {
	switch {
	case errors.Is(ref.Reason, deliver.ErrNoTmux), errors.Is(ref.Reason, deliver.ErrPaneDead),
		errors.Is(ref.Reason, deliver.ErrAtShellPrompt):
		return "Not sent: " + title + " has stopped. Restart it from the sidebar."
	case errors.Is(ref.Reason, deliver.ErrAgentRunning):
		return "Not sent: " + title + " is still working. Send once it finishes."
	case errors.Is(ref.Reason, deliver.ErrWaitingPrompt):
		return "Not sent: " + title + " is waiting on a prompt. Answer it first."
	case errors.Is(ref.Reason, deliver.ErrNotConfirmed):
		return "Not sent: " + title + " wasn't ready for input. Try again."
	}
	return "Not sent: " + ref.Short() + "."
}

// reviewActingAnswers counts the answers a send carries (later is not one).
func reviewActingAnswers(rec *reviewjob.Record) int {
	n := 0
	for _, a := range rec.Answers {
		if a.Kind == selfreview.AnswerChange || a.Kind == selfreview.AnswerAsk {
			n++
		}
	}
	return n
}

func (h *Home) handleReviewSent(msg reviewSentMsg) tea.Cmd {
	if msg.err != nil {
		var ref *deliver.Refusal
		if errors.As(msg.err, &ref) {
			// A refusal is the guard doing its job, not a failure: say why and
			// what to do, and leave the run ready to send again.
			h.toasts.Add(ToastWarn, reviewRefusalText(msg.title, ref))
			return nil
		}
		h.setError("review_send", msg.err)
		return nil
	}
	if h.reviewView.IsVisible() && h.reviewView.Data().RunID == msg.runID {
		d := h.reviewView.Data()
		d.Status, d.AgentWorking = reviewStatusSent, true
		h.reviewView.SetData(d)
	}
	h.setInfo(fmt.Sprintf("Sent %s to %s.", rvPlural(msg.answers, "answer", "answers"), msg.title))
	return h.reviewSummariesCmd()
}

// reviewStartSession is S's confirmed "start a new session": compose the
// answers (writing the round file) off the Update goroutine, then launch a
// session in the checkout with them as its first prompt.
func (h *Home) reviewStartSession(runID, root string) tea.Cmd {
	if root == "" || runID == "" {
		return nil
	}
	h.logAction("review start session", h.reviewLabel(root), true)
	st := h.storage
	wg := &h.reviewWrites
	return func() tea.Msg {
		wg.Wait()
		out := reviewStartReadyMsg{checkout: root, runID: runID}
		rec, err := reviewjob.Load(st, root)
		switch {
		case err != nil:
			out.err = err
			return out
		case rec == nil || rec.Row == nil || rec.Row.ID != runID:
			out.err = errors.New("a newer review replaced this one — reopen it")
			return out
		}
		out.round = rec.Row.Round
		out.prompt, out.err = reviewjob.Compose(rec, root)
		return out
	}
}

// handleReviewStartReady launches the new session through the ordinary create
// path (default agent, the account strategy, the allowlist), carrying the run
// id so the run is marked sent only once the session has actually started.
func (h *Home) handleReviewStartReady(msg reviewStartReadyMsg) tea.Cmd {
	if msg.err != nil {
		h.setError("review_send", msg.err)
		return nil
	}
	_, cmd := h.handleSessionCreate(sessionCreateMsg{
		path:        msg.checkout,
		title:       fmt.Sprintf("Review round %d", max(msg.round, 1)),
		prompt:      msg.prompt,
		reviewRunID: msg.runID,
	})
	if cmd != nil {
		h.setInfo("Starting a session with your answers.")
	}
	return cmd
}

// reviewMarkSentCmd records a session started with a run's answers as the
// run's target and marks the run sent.
func (h *Home) reviewMarkSentCmd(runID, sessionID string) tea.Cmd {
	st := h.storage
	return func() tea.Msg {
		return reviewMarkedSentMsg{runID: runID, err: reviewjob.MarkSentTo(st, runID, sessionID, time.Now())}
	}
}

// reviewApprove records approval of the open run's snapshot.
func (h *Home) reviewApprove(runID string) tea.Cmd {
	root := h.reviewViewCheckout()
	if root == "" {
		return nil
	}
	h.logAction("review approve", h.reviewLabel(root), true)
	st := h.storage
	wg := &h.reviewWrites
	return func() tea.Msg {
		wg.Wait()
		return reviewApprovedMsg{checkout: root, runID: runID, err: reviewjob.Approve(st, runID)}
	}
}

// confirmDiscardReview is `d` on a review row: discard the checkout's review
// (every run and answer), behind a y/n.
func (h *Home) confirmDiscardReview(root string) tea.Cmd {
	if h.reviewPreparing[root] || h.reviewSummaries[root].Status == session.ReviewPreparing {
		h.setInfo("Can't discard while the review is preparing.")
		return nil
	}
	h.confirmDialog.ShowDanger("Discard Review?", h.reviewLabel(root),
		[]string{"Deletes the review and your answers.", "Press v later to review again."},
		func() tea.Msg { return reviewDiscardMsg{checkout: root} })
	return nil
}

// reviewMenuItem is the "Review Changes" row of a session's or checkout's
// context menu — the same guard the `v` key and the palette command run.
func (h *Home) reviewMenuItem(root string) ContextMenuItem {
	it := ContextMenuItem{ID: "review", Label: "Review Changes", Shortcut: "v", Key: "v"}
	if why := h.reviewBlocker(root); why != "" {
		it.Note = why
	} else {
		it.Enabled = true
	}
	return it
}

// reviewContextMenu is `.` on a review row: open, regenerate, discard. Each
// guard is the one its handler runs, and each note names the clause that
// failed.
func (h *Home) reviewContextMenu() (string, []ContextMenuItem) {
	item := h.flatItems[h.cursor]
	root := item.RepoPath
	preparing := h.reviewPreparing[root] || item.Review.Status == ReviewBadgePreparing

	regen := ContextMenuItem{ID: "review_regenerate", Label: "Review Again"}
	switch why := h.reviewBlocker(root); {
	case preparing:
		regen.Note = "already reviewing"
	case why != "":
		regen.Note = why
	default:
		regen.Enabled = true
	}
	discard := ContextMenuItem{ID: "review_discard", Label: "Discard Review", Shortcut: "d", Key: "d"}
	if preparing {
		discard.Note = "still preparing"
	} else {
		discard.Enabled = true
	}
	items := []ContextMenuItem{
		{ID: "review", Label: "Open Review", Shortcut: "⏎", Key: "v", Enabled: true},
		regen,
		discard,
	}
	return "review: " + h.reviewLabel(root), items
}

// reviewRowPreview is the preview pane while the cursor is on a review row:
// the run's verdict and what the keys do. ok=false on any other row.
func (h *Home) reviewRowPreview(width, height int) (inner, title string, ok bool) {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || !h.flatItems[h.cursor].IsReview {
		return "", "", false
	}
	item := h.flatItems[h.cursor]
	label := h.reviewLabel(item.RepoPath)
	n := len(h.reviewCheckout(item.RepoPath).Sessions)
	return renderReviewRowPreview(label, item.Review, n, width, height), ReviewGlyph + " Review · " + label, true
}
