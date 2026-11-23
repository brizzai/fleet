// Package reviewjob is self-review's one API: the TUI and `fleet review` both
// prepare, load, answer, send and approve review runs through it, so neither
// grows a second copy of the policy.
//
// It glues the pure pieces together — internal/git for the snapshot and diff,
// internal/selfreview for the prompt, reply and rules, internal/deliver for
// typing the answers into the session — and owns the review_runs rows.
//
// No Bubble Tea here. Every call may block (git, the reviewer, the pane), so a
// TUI caller runs them from a tea.Cmd, never from Update.
package reviewjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/brizzai/fleet/internal/deliver"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// Typed errors. Callers branch with errors.Is.
var (
	// ErrNothingToReview: the worktree has no changes against its base.
	ErrNothingToReview = errors.New("nothing to review: no changes against the base")
	// ErrNoRun: the checkout has never been reviewed.
	ErrNoRun = errors.New("no review for this checkout yet")
	// ErrNotReady: the run is preparing or failed, so there is nothing to
	// answer or send.
	ErrNotReady = errors.New("the review is not ready")
	// ErrAlreadySent: the run's answers were already delivered (or the run
	// was approved). Review again for the next round.
	ErrAlreadySent = errors.New("this review was already sent")
	// ErrNoAnswers: Send needs at least one answer.
	ErrNoAnswers = errors.New("answer at least one decision before sending")
	// ErrNothingToChange: every answer is ok or later, so a send would wake
	// the agent to say "nothing to change". Approve instead.
	ErrNothingToChange = errors.New("nothing to change: every answer is ok or later — approve instead")
	// ErrNoteRequired: a change or ask answer has no note. See *NoteError.
	ErrNoteRequired = errors.New("a change or ask answer needs a note")
	// ErrBadAnswer: SaveAnswer got a kind that is not fine/change/ask/later.
	ErrBadAnswer = errors.New("unknown answer")
	// ErrNoCheckout: the checkout has no root path.
	ErrNoCheckout = errors.New("no checkout to review")
)

// Checkout is what a review covers: one checkout — the repo or worktree root
// the sidebar groups sessions under — and the sessions working in it. A review
// belongs to the checkout, not to any one session: several sessions can work
// one worktree, and the change under review is the tree's, whoever typed it.
type Checkout struct {
	// Root is the checkout root (session.GetRepoRoot of its sessions'
	// ProjectPath) — the key runs are stored under.
	Root string
	// Sessions are the sessions whose ProjectPath is in Root, any order. Their
	// prompts are the intent, their stored base_ref the base, and one of them
	// receives the answers on Send. May be empty.
	Sessions []*session.Session
}

// CheckoutOf collects the sessions in all whose checkout is root.
func CheckoutOf(root string, all []*session.Session) Checkout {
	c := Checkout{Root: root}
	for _, s := range all {
		if s != nil && session.GetRepoRoot(s.ProjectPath) == root {
			c.Sessions = append(c.Sessions, s)
		}
	}
	return c
}

// lastWork is when s last finished work, as well as this process can tell:
// the running → finished transition it saw, else when the user last opened
// it, else when it was created.
func lastWork(s *session.Session) time.Time {
	if t := s.FinishedAt(); !t.IsZero() {
		return t
	}
	if !s.LastAccessedAt.IsZero() {
		return s.LastAccessedAt
	}
	return s.CreatedAt
}

// Targets orders the checkout's sessions as send targets: the one that most
// recently finished work first (else most recently opened). It is the order
// the Review view's target key cycles through.
func Targets(c Checkout) []*session.Session {
	out := slices.Clone(c.Sessions)
	sort.SliceStable(out, func(i, j int) bool { return lastWork(out[i]).After(lastWork(out[j])) })
	return out
}

// DefaultTarget is the session Send goes to unless the human picks another:
// preferID when it is still one of the checkout's sessions (the run's stored
// target), else the first of Targets. nil when the checkout has no session.
func DefaultTarget(c Checkout, preferID string) *session.Session {
	ts := Targets(c)
	if preferID != "" {
		for _, s := range ts {
			if s.ID == preferID {
				return s
			}
		}
	}
	if len(ts) == 0 {
		return nil
	}
	return ts[0]
}

// NoteError names the stop whose change/ask answer has no note.
type NoteError struct {
	StopID string
	Kind   string
}

func (e *NoteError) Error() string {
	return fmt.Sprintf("%s answer on %s needs a note", e.Kind, e.StopID)
}

// Is matches ErrNoteRequired.
func (e *NoteError) Is(target error) bool { return target == ErrNoteRequired }

// Snapshot is the exact state a run reviewed.
type Snapshot struct {
	BaseRef string // the ref measured against ("origin/master")
	BaseSHA string // merge-base(BaseRef, HEAD): the diff's left side
	TreeSHA string // the worktree as written by git.SnapshotTree: the right side
	HeadSHA string
	Dirty   bool // TreeSHA differs from HEAD's tree: uncommitted or untracked work
}

// Record is one run as the UI and CLI show it.
type Record struct {
	Row *session.ReviewRunRow
	// Run is the enforced review; nil while preparing and when it failed.
	Run *selfreview.Run
	// Answers are the human's answers to Run, in the order first given.
	Answers []selfreview.Answer
	// Sent maps a stop id to when its answer was delivered (absent = unsent).
	Sent     map[string]time.Time
	Snapshot Snapshot
	// Stale: the worktree has changed since the run's snapshot. Only set by
	// CheckStale (it costs a SnapshotTree).
	Stale bool
	// Status is Row.Status, except that a preparing row nobody in this process
	// is running and that is older than the reviewer's timeout reads failed
	// (the process that started it died).
	Status string
	// Warnings are Enforce's notes; only on a run prepared by this call.
	Warnings []string
}

// Answer returns the answer to stopID, if any.
func (r *Record) Answer(stopID string) (selfreview.Answer, bool) {
	for _, a := range r.Answers {
		if a.StopID == stopID {
			return a, true
		}
	}
	return selfreview.Answer{}, false
}

// Counts is the run's counts against its answers — the one definition the
// CLI, the sidebar row and the Review view all print (selfreview.Counts).
// Zero for a run that is not ready.
func (r *Record) Counts() selfreview.Counts {
	if r == nil {
		return selfreview.Counts{}
	}
	return selfreview.CountsOf(r.Run, r.Answers)
}

// Unanswered counts the run's decisions with no answer yet — "N to decide".
// Zero for a run that is not ready.
func (r *Record) Unanswered() int { return r.Counts().Unanswered }

// NeedsYou reports whether the human has decisions to make on this run: it is
// ready (not yet sent or approved) and has at least one unanswered stop.
func (r *Record) NeedsYou() bool {
	return r != nil && r.Status == session.ReviewReady && r.Unanswered() > 0
}

// Store is the slice of *session.StateDB this package uses.
type Store interface {
	InsertReviewRun(r *session.ReviewRunRow) error
	UpdateReviewRun(r *session.ReviewRunRow) error
	LoadReviewRun(id string) (*session.ReviewRunRow, error)
	LatestReviewRun(checkout string) (*session.ReviewRunRow, error)
	ListReviewRuns(checkout string) ([]*session.ReviewRunRow, error)
	ReviewRunsForCheckouts(checkouts []string) (map[string]*session.ReviewRunRow, error)
	SetReviewTarget(runID, sessionID string) error
	DeleteReviewRunsForCheckout(checkout string) error
	SaveReviewAnswer(a session.ReviewAnswerRow) error
	LoadReviewAnswers(runID string) ([]session.ReviewAnswerRow, error)
	MarkReviewSeen(runID string, at time.Time) error
	MarkReviewSent(runID string, at time.Time) error
	MarkReviewApproved(runID string) error
	LoadPRCache() (map[string]*session.PRCacheRow, error)
	deliver.Store
}

var _ Store = (*session.StateDB)(nil)

// Deps are the package's seams. Store is required; every nil func takes the
// production default, so the CLI passes Deps{Store: db} and tests fake the
// rest.
type Deps struct {
	Store Store

	// NewRunner builds the reviewer for one run. Default: a
	// selfreview.ClaudeRunner on that model and account config dir.
	NewRunner func(model, configDir string) selfreview.Runner
	// Model is the reviewer model. Default: config.json's review_model, else
	// selfreview.DefaultModel.
	Model func() string
	// Account picks the Claude account the reviewer runs as: email ("" =
	// ambient login) and its CLAUDE_CONFIG_DIR. Default: the account of the
	// checkout's default send target when it is a usable Claude account, else
	// claudeaccount.Select under the origin's allowlist (see resolveAccount).
	// The TUI, which holds live quota, may replace it.
	Account func(ctx context.Context, c Checkout) (email, configDir string)
	// PR is the cached PR for the checkout (nil = none). Default: the TUI's
	// persisted PR cache, matched on the checkout root and the checked-out
	// branch. The TUI may pass its in-memory RepoInfo instead.
	PR func(root, branch string) *github.PR
	// Turns returns the session's transcript turns, oldest first. Default:
	// the Claude transcript; nil for other agents.
	Turns func(s *session.Session) []session.Turn
	// Deliver types the round message into the session. Default:
	// deliver.Deliver with RefuseRunning.
	Deliver func(ctx context.Context, s *session.Session, msg string) (deliver.Result, error)
	// Now is the clock. Default time.Now.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) model() string {
	if d.Model != nil {
		if m := d.Model(); m != "" {
			return m
		}
		return selfreview.DefaultModel
	}
	return configuredModel()
}

func (d Deps) runner(model, configDir string) selfreview.Runner {
	if d.NewRunner != nil {
		return d.NewRunner(model, configDir)
	}
	return selfreview.ClaudeRunner{Model: model, ConfigDir: configDir, Timeout: selfreview.DefaultTimeout}
}

func (d Deps) account(ctx context.Context, c Checkout) (string, string) {
	if d.Account != nil {
		return d.Account(ctx, c)
	}
	return resolveAccount(ctx, c)
}

func (d Deps) deliver(ctx context.Context, s *session.Session, msg string) (deliver.Result, error) {
	if d.Deliver != nil {
		return d.Deliver(ctx, s, msg)
	}
	return deliver.Deliver(ctx, deliver.Deps{}, s, msg, deliver.Options{RefuseRunning: true})
}

// staleAfter is how old a preparing row may get before it reads as failed:
// the reviewer's own timeout plus room for the git work around it.
const staleAfter = selfreview.DefaultTimeout + 2*time.Minute

// interruptedMsg is the failure text of a preparing row whose process died.
const interruptedMsg = "interrupted: the process preparing this review exited"

// effectiveStatus is row.Status, with an orphaned preparing row read as failed.
func effectiveStatus(row *session.ReviewRunRow, now time.Time) string {
	if row.Status == session.ReviewPreparing && !inFlightRun(row.ID) && now.Sub(row.CreatedAt) > staleAfter {
		return session.ReviewFailed
	}
	return row.Status
}

// record builds a Record from a stored row and its answers.
func record(st Store, row *session.ReviewRunRow, now time.Time) (*Record, error) {
	rec := &Record{
		Row:    row,
		Status: effectiveStatus(row, now),
		Snapshot: Snapshot{
			BaseRef: row.BaseRef, BaseSHA: row.BaseSHA, TreeSHA: row.TreeSHA, HeadSHA: row.HeadSHA,
		},
	}
	if rec.Status == session.ReviewFailed && row.Status == session.ReviewPreparing && row.Error == "" {
		row.Error = interruptedMsg
	}
	if row.JSON != "" {
		var run selfreview.Run
		if err := json.Unmarshal([]byte(row.JSON), &run); err != nil {
			return nil, fmt.Errorf("review run %s: stored JSON unreadable: %w", row.ID, err)
		}
		// A run stored before the scope decision existed gets one from its
		// intent gap, so no screen shows the gap as a banner.
		selfreview.Upgrade(&run)
		rec.Run = &run
	}
	ans, err := st.LoadReviewAnswers(row.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range ans {
		rec.Answers = append(rec.Answers, selfreview.Answer{StopID: a.StopID, Kind: a.Answer, Note: a.Note})
		if !a.SentAt.IsZero() {
			if rec.Sent == nil {
				rec.Sent = map[string]time.Time{}
			}
			rec.Sent[a.StopID] = a.SentAt
		}
	}
	return rec, nil
}

// Load returns the checkout's latest run, or (nil, nil) when it has none. It
// does no git work beyond one rev-parse for Snapshot.Dirty; staleness is
// CheckStale's job.
func Load(st Store, checkout string) (*Record, error) {
	row, err := st.LatestReviewRun(checkout)
	if err != nil || row == nil {
		return nil, err
	}
	rec, err := record(st, row, time.Now())
	if err != nil {
		return nil, err
	}
	if row.HeadSHA != "" && row.TreeSHA != "" && row.Worktree != "" {
		if headTree, err := gitOut(context.Background(), row.Worktree, "rev-parse", "--verify", "--quiet",
			"--end-of-options", row.HeadSHA+"^{tree}"); err == nil {
			rec.Snapshot.Dirty = headTree != row.TreeSHA
		}
	}
	return rec, nil
}

// LoadRound returns the checkout's newest run of the given round, or
// (nil, nil) when it has none.
func LoadRound(st Store, checkout string, round int) (*Record, error) {
	runs, err := st.ListReviewRuns(checkout)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.Round == round {
			return record(st, r, time.Now())
		}
	}
	return nil, nil
}

// CheckStale snapshots the worktree again and sets rec.Stale when it no longer
// matches the run's tree. A SnapshotTree hashes changed files, so it is the
// one call here worth doing lazily.
func CheckStale(ctx context.Context, d Deps, rec *Record) (bool, error) {
	if rec == nil || rec.Row == nil {
		return false, ErrNoRun
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	tree, err := snapshotTree(rec.Row.Worktree)
	if err != nil {
		return false, err
	}
	rec.Stale = tree != rec.Row.TreeSHA
	return rec.Stale, nil
}

// SaveAnswer records the human's answer to one stop of a run. An empty Kind
// clears it. Notes are not required here — the UI saves as the human types —
// but Send refuses a change or ask without one.
func SaveAnswer(st Store, runID string, ans selfreview.Answer) error {
	switch ans.Kind {
	case "", selfreview.AnswerFine, selfreview.AnswerChange, selfreview.AnswerAsk, selfreview.AnswerLater:
	default:
		return fmt.Errorf("%w %q", ErrBadAnswer, ans.Kind)
	}
	if ans.StopID == "" {
		return fmt.Errorf("%w: no stop id", ErrBadAnswer)
	}
	return st.SaveReviewAnswer(session.ReviewAnswerRow{RunID: runID, StopID: ans.StopID, Answer: ans.Kind, Note: ans.Note})
}

// MarkSeen records that the human opened the run.
func MarkSeen(st Store, runID string) error { return st.MarkReviewSeen(runID, time.Now()) }

// Approve records the human's approval of the run's snapshot.
func Approve(st Store, runID string) error {
	row, err := st.LoadReviewRun(runID)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrNoRun
	}
	if row.Status != session.ReviewReady && row.Status != session.ReviewSent && row.Status != session.ReviewApproved {
		return ErrNotReady
	}
	return st.MarkReviewApproved(runID)
}

// SetTarget records the session a run's answers go to.
func SetTarget(st Store, runID, sessionID string) error { return st.SetReviewTarget(runID, sessionID) }

// Discard deletes every run of the checkout and their answers. A prepare in
// flight for it is refused: its row would be deleted under it.
func Discard(st Store, checkout string) error {
	if InFlight(checkout) {
		return errors.New("a review is being prepared for this checkout")
	}
	return st.DeleteReviewRunsForCheckout(checkout)
}

// Summary is what the sidebar and preview need about a checkout's latest run.
type Summary struct {
	RunID string
	Model string
	// Elapsed is how long the reviewer took.
	Elapsed time.Duration
	// Target is the run's stored send target ("" = none chosen yet).
	Target string
	Status string // effective status (an orphaned preparing row reads failed)
	Round  int
	// Counts is the run's counts against its answers (zero unless the run
	// has a review): the same numbers `fleet review` and the Review view
	// print, from the same function.
	Counts selfreview.Counts
	// SentAt is when the answers were last delivered (zero = unsent).
	SentAt    time.Time
	Seen      bool
	Verdict   string
	Error     string
	TreeSHA   string
	CreatedAt time.Time
}

// Summaries returns each listed checkout's latest-run summary in one query
// (plus one answers read per ready run). Checkouts with no run are absent.
func Summaries(st Store, checkouts []string) (map[string]Summary, error) {
	rows, err := st.ReviewRunsForCheckouts(checkouts)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make(map[string]Summary, len(rows))
	for key, row := range rows {
		sum := Summary{
			RunID: row.ID, Status: effectiveStatus(row, now), Round: row.Round, Seen: !row.SeenAt.IsZero(),
			Error: row.Error, TreeSHA: row.TreeSHA, CreatedAt: row.CreatedAt,
			Model: row.Model, Elapsed: time.Duration(row.ElapsedMS) * time.Millisecond, Target: row.SessionID,
		}
		if sum.Status == session.ReviewFailed && sum.Error == "" {
			sum.Error = interruptedMsg
		}
		if row.JSON != "" {
			rec, err := record(st, row, now)
			if err == nil && rec.Run != nil {
				sum.Counts, sum.Verdict = rec.Counts(), rec.Run.Verdict
				for _, at := range rec.Sent {
					if at.After(sum.SentAt) {
						sum.SentAt = at
					}
				}
			}
		}
		out[key] = sum
	}
	return out, nil
}
