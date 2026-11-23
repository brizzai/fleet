package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/reviewjob"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

const reviewUsage = "Usage: fleet review [flags] <session | path | branch>"

// Exit codes, the same family as fleet wait's: 0 nothing needs you, 2 not
// reached (timeout, or --show of a run still preparing), 3 decisions need
// you, 4 the review failed. 1 stays "the command itself failed".
const (
	reviewExitOK      = 0
	reviewExitPending = 2
	reviewExitDecide  = 3
	reviewExitFailed  = 4
)

type reviewOpts struct {
	selector string
	json     bool
	walk     bool
	wait     bool
	show     bool
	force    bool
	round    int
	timeout  time.Duration
}

func reviewFlagSet(o *reviewOpts) *flag.FlagSet {
	fs := newFlagSet("fleet review")
	fs.BoolVar(&o.json, "json", false, "print the run as compact JSON (everything: summary, decisions, chapters)")
	fs.BoolVar(&o.walk, "walkthrough", false, "also print how the change works, chapter by chapter")
	fs.BoolVar(&o.show, "show", false, "print the latest run without preparing one")
	fs.BoolVar(&o.wait, "wait", false, "with --show: wait for a run that is still preparing")
	fs.BoolVar(&o.force, "regenerate", false, "review again even if this snapshot already has a review")
	fs.IntVar(&o.round, "round", 0, "with --show: print this round's run instead of the latest")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "give up after this long (exit 2)")
	return fs
}

func printReviewUsage(w io.Writer) {
	fmt.Fprintln(w, reviewUsage)
	var o reviewOpts
	fs := reviewFlagSet(&o)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
Reviews a checkout — the worktree (or repo) a session works in, or a path, or
a branch name — as it stands (committed, uncommitted and untracked) against the
branch it was cut from, with a fresh read-only Claude call; or prints the
review this snapshot already has. The review belongs to the checkout, not to
one session: every session working it contributes its prompts. Shows the
verdict, what the change does, and the decisions only a human can make — each
with the reviewer's own recommendation; --walkthrough adds how the change
works, chapter by chapter. Answering and sending happen in the TUI (the Review
row under the checkout).

Exit: 0 nothing needs you, 3 decisions need you, 4 the review failed,
2 timed out (or --show of a run still preparing).`)
}

func parseReviewArgs(args []string) (reviewOpts, error) {
	var o reviewOpts
	pos, err := parseInterspersed(reviewFlagSet(&o), args)
	if err != nil {
		return o, err
	}
	switch len(pos) {
	case 0:
		return o, fmt.Errorf("missing session, path or branch")
	case 1:
		o.selector = pos[0]
	default:
		return o, fmt.Errorf("unexpected argument %q — fleet review takes one session, path or branch", pos[1])
	}
	if o.round < 0 || o.round > selfreview.MaxRounds {
		return o, fmt.Errorf("--round must be 1-%d", selfreview.MaxRounds)
	}
	if o.round > 0 && !o.show {
		return o, fmt.Errorf("--round needs --show")
	}
	if o.wait && !o.show {
		return o, fmt.Errorf("--wait needs --show (preparing always waits)")
	}
	if o.force && o.show {
		return o, fmt.Errorf("--regenerate conflicts with --show")
	}
	if o.timeout <= 0 {
		return o, fmt.Errorf("--timeout must be positive")
	}
	if o.walk && o.json {
		return o, fmt.Errorf("--walkthrough conflicts with --json (the JSON carries the chapters)")
	}
	return o, nil
}

func runReview(args []string) {
	opts, err := parseReviewArgs(args)
	handleParseErr(err, printReviewUsage)

	// Migrate before anything creates ~/.config/fleet, and init debuglog so
	// slog never falls back to stderr — the `fleet add` preamble. No tmux
	// check: a review reads git and the database, never a pane.
	migration.Run()
	debuglog.Init()
	defer debuglog.Close()

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fatalf("Failed to open database: %v", err)
	}
	defer storage.Close()
	rows, err := storage.LoadSessions()
	if err != nil {
		fatalf("Failed to load sessions: %v", err)
	}
	slots, err := storage.LoadSlotBindings()
	if err != nil {
		debuglog.Logger.Error("failed to load slot bindings", "err", err)
	}
	pinned, err := storage.LoadPinnedRepos()
	if err != nil {
		debuglog.Logger.Error("failed to load pinned repos", "err", err)
	}
	root, err := resolveReviewCheckout(rows, slots, pinned, opts.selector, reviewResolvers{
		branchOf: branchOfHead, rootOf: session.GetRepoRoot, isDir: isDir,
	})
	if err != nil {
		fatalf("%v", err)
	}
	all := make([]*session.Session, 0, len(rows))
	for _, r := range rows {
		all = append(all, session.FromRow(r))
	}
	c := reviewjob.CheckoutOf(root, all)
	label := checkoutLabel(root)

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	// Ctrl+C cancels the reviewer, which records the run as failed
	// ("cancelled") instead of leaving it preparing.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := reviewjob.Deps{Store: storage}
	var rec *reviewjob.Record
	switch {
	case opts.show:
		rec, err = showReview(ctx, storage, root, opts)
		if err == nil && rec == nil {
			if opts.round > 0 {
				fatalf("%s has no round-%d review — run fleet review %s", label, opts.round, root)
			}
			fatalf("%s has no review yet — run fleet review %s", label, root)
		}
	default:
		if !opts.json {
			fmt.Fprintf(os.Stderr, "Reviewing %s (a fresh Claude reads the change; usually 1-2 minutes)…\n", label)
		}
		rec, err = reviewjob.PrepareWith(ctx, deps, c, reviewjob.PrepareOptions{Force: opts.force})
	}

	switch {
	case errors.Is(err, reviewjob.ErrNothingToReview):
		if opts.json {
			printJSON(map[string]any{"status": "nothing", "checkout": root})
		} else {
			fmt.Printf("Nothing to review: %s has no changes against its base.\n", label)
		}
		os.Exit(reviewExitOK)
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(os.Stderr, "timed out after %s\n", opts.timeout)
		os.Exit(reviewExitPending)
	case err != nil && rec == nil:
		fatalf("review failed: %v", err)
	}

	if opts.json {
		printJSON(reviewJSONOf(rec))
	} else {
		printReview(os.Stdout, label, rec)
		if opts.walk {
			printWalkthrough(os.Stdout, rec)
		}
	}
	os.Exit(reviewExitCode(rec))
}

// reviewResolvers are resolveReviewCheckout's I/O, injected so the resolver
// stays pure.
type reviewResolvers struct {
	branchOf func(path string) string // checked-out branch ("" = detached / not git)
	rootOf   func(path string) string // checkout root of a path
	isDir    func(path string) bool
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// resolveReviewCheckout turns `fleet review`'s argument into a checkout root.
// A review belongs to a checkout, so the argument may name one three ways:
//
//   - a path ("." / "./wt" / "../wt" / "/abs" / "~/code/x"): its root;
//   - a branch checked out in a known checkout (one with a session, or a
//     pinned repo) — matched at the checkout level first, so two sessions in
//     one worktree don't make their own branch ambiguous;
//   - a session, by every `fleet send` tier: its checkout;
//
// and, last, a bare name that is a directory under the cwd.
func resolveReviewCheckout(rows []*session.SessionRow, slots map[int]string, pinned []string,
	selector string, io reviewResolvers) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("missing session, path or branch")
	}
	// Only an unmistakable path short-circuits: branch names carry slashes
	// ("feat/auth"), so a bare "a/b" is tried as a branch and a session first
	// and as a directory last.
	pathLike := selector == "." || selector == ".." || strings.HasPrefix(selector, "/") ||
		strings.HasPrefix(selector, "./") || strings.HasPrefix(selector, "../") || strings.HasPrefix(selector, "~")
	if pathLike {
		return reviewPathRoot(selector, io)
	}

	if !strings.HasPrefix(selector, "@") && io.branchOf != nil {
		seen := map[string]bool{}
		var roots []string
		add := func(p string) {
			if p == "" {
				return
			}
			r := io.rootOf(p)
			if !seen[r] {
				seen[r] = true
				roots = append(roots, r)
			}
		}
		for _, r := range rows {
			add(r.ProjectPath)
		}
		for _, p := range pinned {
			add(p)
		}
		var hits []string
		for _, r := range roots {
			if io.branchOf(r) == selector {
				hits = append(hits, r)
			}
		}
		sort.Strings(hits)
		switch len(hits) {
		case 1:
			return hits[0], nil
		case 0:
		default:
			return "", fmt.Errorf("branch %q is checked out in %d checkouts — pass a path:\n  %s",
				selector, len(hits), strings.Join(hits, "\n  "))
		}
	}

	if len(rows) > 0 {
		row, err := resolveSession(rows, slots, selector, nil)
		if err == nil {
			return io.rootOf(row.ProjectPath), nil
		}
		if !io.isDir(selector) {
			return "", err
		}
	}
	if io.isDir(selector) {
		return reviewPathRoot(selector, io)
	}
	return "", fmt.Errorf("no session, branch or directory matches %q — `fleet list` shows the sessions", selector)
}

func reviewPathRoot(p string, io reviewResolvers) (string, error) {
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, rest)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if !io.isDir(abs) {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return io.rootOf(abs), nil
}

// checkoutLabel names a checkout for the CLI's lines: its branch, with the
// directory when that says something the branch doesn't.
func checkoutLabel(root string) string {
	base := filepath.Base(root)
	if b := branchOfHead(root); b != "" && b != base {
		return b + " (" + base + ")"
	}
	return base
}

// showReview loads the latest (or the asked round's) run, waiting out a run
// still preparing when --wait asks.
func showReview(ctx context.Context, st *session.StateDB, checkout string, o reviewOpts) (*reviewjob.Record, error) {
	load := func() (*reviewjob.Record, error) {
		if o.round > 0 {
			return reviewjob.LoadRound(st, checkout, o.round)
		}
		return reviewjob.Load(st, checkout)
	}
	for {
		rec, err := load()
		if err != nil || rec == nil || !o.wait || rec.Status != session.ReviewPreparing {
			return rec, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func reviewExitCode(rec *reviewjob.Record) int {
	switch {
	case rec == nil:
		return reviewExitFailed
	case rec.Status == session.ReviewFailed:
		return reviewExitFailed
	case rec.Status == session.ReviewPreparing:
		return reviewExitPending
	case rec.NeedsYou():
		return reviewExitDecide
	}
	return reviewExitOK
}

// --- rendering ---

func printReview(w io.Writer, label string, rec *reviewjob.Record) {
	row := rec.Row
	header := fmt.Sprintf("── %s · round %d · tree %s", label, row.Round, short(row.TreeSHA))
	if rec.Snapshot.Dirty {
		header += " (uncommitted)"
	}
	if row.BaseRef != "" {
		header += " vs " + row.BaseRef
	}
	if row.Model != "" {
		header += " · " + row.Model
	}
	if row.ElapsedMS > 0 {
		header += " · " + elapsed(time.Duration(row.ElapsedMS)*time.Millisecond)
	}
	if rec.Run != nil && rec.Run.Reviewer != nil {
		header += " · " + reviewerLine(rec.Run.Reviewer)
	}
	header += " · " + rec.Status
	fmt.Fprintln(w, header)

	switch rec.Status {
	case session.ReviewPreparing:
		fmt.Fprintf(w, "Preparing since %s.\n", ago(row.CreatedAt))
		return
	case session.ReviewFailed:
		fmt.Fprintf(w, "!! %s\n", row.Error)
		return
	}
	run := rec.Run
	if run == nil {
		return
	}
	fmt.Fprintln(w, run.Verdict)
	if run.Summary != "" {
		fmt.Fprintf(w, "What it does: %s\n", run.Summary)
	}
	// An intent gap is the scope decision (listed first below), not an alert.
	// The same counts and phrases the sidebar row and the Review view print.
	c := rec.Counts()
	summary := []string{c.ToDecide(), c.LinesNeedYou(), c.SafeToSkipLabel()}
	if c.NotReviewed > 0 {
		summary = append(summary, c.NotReviewedLabel())
	}
	fmt.Fprintln(w, strings.Join(summary, " · "))

	for i, st := range run.Stops {
		mark := ""
		if a, ok := rec.Answer(st.ID); ok {
			mark = "  [" + selfreview.AnswerWord(a.Kind) + "]"
		}
		fmt.Fprintf(w, "%2d. %s  (%s)%s\n", i+1, st.Title, selfreview.KindPhrase(st.Kind), mark)
		if r := selfreview.RecommendLine(st.Recommend); r != "" {
			fmt.Fprintf(w, "    Recommend: %s\n", r)
		}
		fmt.Fprintf(w, "    Decide: %s\n", st.Decide)
		if st.How != "" {
			fmt.Fprintf(w, "    How it works: %s\n", st.How)
		}
		if st.Elsewhere != "" {
			fmt.Fprintf(w, "    Elsewhere: %s\n", st.Elsewhere)
		}
		if st.IsScope() {
			if len(st.Missing) > 0 {
				fmt.Fprintf(w, "    Missing: %s\n", strings.Join(st.Missing, "; "))
			} else if st.Why != "" {
				fmt.Fprintf(w, "    Missing: %s\n", st.Why)
			}
		}
		if anchor := firstHunk(st); anchor != "" {
			fmt.Fprintf(w, "    %s\n", anchor)
		}
	}
	for _, p := range run.Pending {
		fmt.Fprintf(w, "Not verified yet: %s\n", p)
	}
	for _, p := range run.NotCovered {
		if why := run.NotCoveredWhy[p]; why != "" {
			fmt.Fprintf(w, "Not reviewed: %s — %s\n", p, why)
			continue
		}
		fmt.Fprintf(w, "Not reviewed: %s\n", p)
	}
	switch rec.Status {
	case session.ReviewSent:
		fmt.Fprintln(w, "Answers sent to the session.")
	case session.ReviewApproved:
		fmt.Fprintln(w, "Approved.")
	default:
		if c.Unanswered > 0 {
			fmt.Fprintln(w, "Answer and send in the TUI: the Review row under the checkout (or v on any of its sessions).")
		}
	}
}

// printWalkthrough is --walkthrough: how the change works, chapter by
// chapter, each file with what happened to it.
func printWalkthrough(w io.Writer, rec *reviewjob.Record) {
	run := rec.Run
	if run == nil {
		return
	}
	fmt.Fprintln(w)
	if len(run.Chapters) == 0 {
		fmt.Fprintln(w, "No walkthrough: this review was made before fleet asked for one (--regenerate).")
		return
	}
	fmt.Fprintln(w, "── Walkthrough")
	num := map[string]int{}
	for i, s := range run.Stops {
		num[s.ID] = i + 1
	}
	for i, c := range run.Chapters {
		fmt.Fprintf(w, "%d. %s  (%s)\n", i+1, c.Title, c.Role)
		if c.Text != "" {
			fmt.Fprintf(w, "   %s\n", c.Text)
		}
		var ds []string
		for _, id := range c.Decisions {
			if n, ok := num[id]; ok {
				ds = append(ds, fmt.Sprintf("%d. %s", n, run.Stops[n-1].Title))
			}
		}
		if len(ds) > 0 {
			fmt.Fprintf(w, "   Decisions here: %s\n", strings.Join(ds, "; "))
		}
		for _, p := range c.Files {
			fmt.Fprintf(w, "   - %s — %s\n", p, fileFate(run, num, p))
		}
	}
}

// fileFate is what happened to a changed file, as the Walkthrough says it.
func fileFate(run *selfreview.Run, num map[string]int, p string) string {
	var ns []string
	for _, s := range run.Stops {
		for _, h := range s.Hunks {
			if h.Path == p {
				ns = append(ns, strconv.Itoa(num[s.ID]))
				break
			}
		}
	}
	if len(ns) > 0 {
		return "in decision " + strings.Join(ns, ", ")
	}
	for _, nc := range run.NotCovered {
		if nc == p {
			if why := run.NotCoveredWhy[p]; why != "" {
				return "not reviewed: " + why
			}
			return "not reviewed"
		}
	}
	for _, s := range run.Skipped {
		if s.Path == p {
			if s.By == "rule" {
				return "safe to skip (auto-skipped: " + s.Reason + ")"
			}
			return "safe to skip: " + s.Reason
		}
	}
	return "reviewed"
}

// reviewerLine is what the reviewer call did, for the header:
// "9 files read, 3 searches, 6 turns, $0.42".
func reviewerLine(m *selfreview.ReviewerMeta) string {
	parts := []string{m.ReadLabel()}
	if n := m.Searches(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralWord(n, "search", "searches")))
	}
	if m.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", m.Turns, pluralWord(m.Turns, "turn", "turns")))
	}
	if m.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", m.CostUSD))
	}
	return strings.Join(parts, ", ")
}

// elapsed is a reviewer's run time: whole seconds up to ten minutes ("74s"
// reads better than humanDur's "1m" for something that takes one or two).
func elapsed(d time.Duration) string {
	if d < 10*time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	return humanDur(d)
}

func firstHunk(st selfreview.Stop) string {
	if len(st.Hunks) == 0 {
		return ""
	}
	h := st.Hunks[0]
	switch {
	case h.Start == 0 && h.End == 0:
		return h.Path
	case h.Start == h.End || h.End == 0:
		return h.Path + ":" + strconv.Itoa(h.Start)
	}
	return fmt.Sprintf("%s:%d-%d", h.Path, h.Start, h.End)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// reviewJSON is `fleet review --json`: the stored run plus the enforced review.
type reviewJSON struct {
	ID       string `json:"id"`
	Checkout string `json:"checkout"`
	// Target is the session the answers were (or will be) sent to; "" = none
	// chosen yet.
	Target    string `json:"target,omitempty"`
	Status    string `json:"status"`
	Round     int    `json:"round"`
	BaseRef   string `json:"base_ref,omitempty"`
	BaseSHA   string `json:"base_sha,omitempty"`
	TreeSHA   string `json:"tree_sha,omitempty"`
	HeadSHA   string `json:"head_sha,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	Model     string `json:"model,omitempty"`
	Account   string `json:"account,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	// Reviewer repeats review.reviewer at the top level, where a script
	// checking "did it read anything?" looks first.
	Reviewer   *selfreview.ReviewerMeta `json:"reviewer,omitempty"`
	Error      string                   `json:"error,omitempty"`
	CreatedAt  time.Time                `json:"created_at"`
	Seen       bool                     `json:"seen,omitempty"`
	NeedsYou   bool                     `json:"needs_you"`
	Unanswered int                      `json:"unanswered"`
	Review     *selfreview.Run          `json:"review,omitempty"`
	Answers    []selfreview.Answer      `json:"answers,omitempty"`
	Sent       map[string]time.Time     `json:"sent,omitempty"`
}

func reviewerOf(rec *reviewjob.Record) *selfreview.ReviewerMeta {
	if rec.Run == nil {
		return nil
	}
	return rec.Run.Reviewer
}

func reviewJSONOf(rec *reviewjob.Record) reviewJSON {
	r := rec.Row
	return reviewJSON{
		ID: r.ID, Checkout: r.Checkout, Target: r.SessionID, Status: rec.Status, Round: r.Round,
		BaseRef: r.BaseRef, BaseSHA: r.BaseSHA, TreeSHA: r.TreeSHA, HeadSHA: r.HeadSHA, Dirty: rec.Snapshot.Dirty,
		Model: r.Model, Account: r.Account, ElapsedMS: r.ElapsedMS, Error: r.Error, CreatedAt: r.CreatedAt,
		Reviewer: reviewerOf(rec),
		Seen:     !r.SeenAt.IsZero(), NeedsYou: rec.NeedsYou(), Unanswered: rec.Unanswered(),
		Review: rec.Run, Answers: rec.Answers, Sent: rec.Sent,
	}
}
