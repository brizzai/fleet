package reviewjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/deliver"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// scanboxFiles mirror the paths the golden scanbox reply talks about, so the
// fake reviewer's stops bind to real diff rows.
var scanboxModified = []string{
	"services/vulns/closures/store.py",
	"libs/schemas/python/src/schemas/pipelines.py",
	"pipelines/collectors/internal/collectors/scanboxSC/collector.go",
	"pipelines/collectors/internal/collectors/scanboxSC/client.go",
	"pipelines/collectors/internal/collectors/scanboxSC/collector_test.go",
	"go.mod",
	"go.sum",
	"docs/collectors/scanbox-sc.md",
}

const migrationPath = "db/migrations/0042_keep_closed_findings.sql"

type repo struct {
	dir string
	git func(args ...string) string
}

func (r repo) write(t *testing.T, name, content string) {
	t.Helper()
	p := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lines(n int, mark func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d%s\n", i, mark(i))
	}
	return b.String()
}

// isolate points HOME (and so config, accounts, transcripts) at a temp dir.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// scanboxRepo: master holds the files; branch feat changes them (committed),
// adds the migration (untracked) and a REVIEW.md tweak (uncommitted).
func scanboxRepo(t *testing.T) repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolate(t)
	r := repo{dir: t.TempDir()}
	r.git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = r.dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	r.git("init", "-q", "-b", "master")
	r.git("config", "user.email", "t@example.com")
	r.git("config", "user.name", "T")
	r.git("config", "commit.gpgsign", "false")
	for _, p := range scanboxModified {
		r.write(t, p, lines(500, func(int) string { return "" }))
	}
	r.write(t, "REVIEW.md", "# Rules\n\nCursors are saved after upload.\n\nalways-human:\n- `libs/schemas/**`\n")
	r.write(t, "services/REVIEW.md", "always-human: *.py\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "base")
	r.git("checkout", "-q", "-b", "feat")
	for _, p := range scanboxModified {
		r.write(t, p, lines(500, func(i int) string {
			if i%7 == 0 {
				return " changed"
			}
			return ""
		}))
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "scanboxSC: join 12h pipeline")
	r.write(t, migrationPath, lines(10, func(int) string { return " sql" }))
	// The agent loosening its own rules in the worktree must not take effect.
	r.write(t, "REVIEW.md", "# Rules\n\nalways-human:\n- nothing/\n")
	return r
}

func goldenRun(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "selfreview", "testdata", "scanbox", "model_output.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeRunner returns the golden scanbox reply, enforced against the real
// input, exactly as ClaudeRunner does after its call.
type fakeRunner struct {
	reply   []byte
	err     error
	calls   atomic.Int32
	gate    chan struct{} // when set, Review blocks until it is closed
	inputs  []selfreview.Input
	mu      sync.Mutex
	model   string
	account string
}

func (f *fakeRunner) Review(ctx context.Context, dir string, in selfreview.Input) (*selfreview.Run, selfreview.RunMeta, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, selfreview.RunMeta{}, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, selfreview.RunMeta{}, f.err
	}
	run, _, err := selfreview.Parse(f.reply)
	if err != nil {
		return nil, selfreview.RunMeta{}, err
	}
	warn := selfreview.Enforce(run, in)
	return run, selfreview.RunMeta{Model: f.model, Warnings: warn}, nil
}

type harness struct {
	r      repo
	db     *session.StateDB
	runner *fakeRunner
	sent   []string
	deps   Deps
	s      *session.Session
	c      Checkout
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	r := scanboxRepo(t)
	db, err := session.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{r: r, db: db, runner: &fakeRunner{reply: goldenRun(t), model: "sonnet"}}
	h.s = &session.Session{ID: "sess-1", Title: "vulns cursor", ProjectPath: r.dir, Agent: agent.Claude,
		BaseRef: "master", FirstPrompt: "first prompt"}
	h.c = Checkout{Root: r.dir, Sessions: []*session.Session{h.s}}
	h.deps = Deps{
		Store: db,
		NewRunner: func(model, configDir string) selfreview.Runner {
			h.runner.mu.Lock()
			h.runner.model, h.runner.account = model, configDir
			h.runner.mu.Unlock()
			return h.runner
		},
		Model:   func() string { return "sonnet" },
		Account: func(context.Context, Checkout) (string, string) { return "me@x", "/acct/me" },
		PR: func(string, string) *github.PR {
			return &github.PR{CIStatus: "FAILURE", FailedChecks: []string{"lint"}, BaseRefName: "master"}
		},
		Turns: func(*session.Session) []session.Turn {
			return []session.Turn{
				{Prompt: "Add Scanbox SC to the 12h pipeline", Reply: "working"},
				{Prompt: "[fleet review · round 1 · tree abc]\n1 to change.", Reply: "fixed"},
				{Prompt: "also fix 403s", Reply: "Done. CI is green."},
			}
		},
		Deliver: func(_ context.Context, _ *session.Session, msg string) (deliver.Result, error) {
			h.sent = append(h.sent, msg)
			return deliver.Result{Verified: true}, nil
		},
	}
	return h
}

func TestGatherAssemblesTheInput(t *testing.T) {
	h := newHarness(t)
	in, snap, err := Gather(context.Background(), h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	if snap.BaseRef != "master" || snap.BaseSHA != h.r.git("rev-parse", "master") ||
		snap.HeadSHA != h.r.git("rev-parse", "HEAD") || !snap.Dirty || snap.TreeSHA == "" {
		t.Errorf("snapshot = %+v", snap)
	}
	byPath := map[string]selfreview.FileInput{}
	for _, f := range in.Files {
		byPath[f.Path] = f
	}
	if len(in.Files) != len(scanboxModified)+2 { // + the untracked migration + REVIEW.md
		t.Errorf("files = %d: %v", len(in.Files), byPath)
	}
	if f := byPath["go.sum"]; f.RuleSkip != "lockfile" || f.Patch != "" {
		t.Errorf("go.sum = %+v", f)
	}
	if f := byPath[migrationPath]; f.Status != "A" || !f.HighRisk {
		t.Errorf("untracked migration = %+v", f)
	}
	// always-human from REVIEW.md at the base, not the worktree's loosened copy.
	if !byPath["libs/schemas/python/src/schemas/pipelines.py"].HighRisk {
		t.Error("libs/schemas/** from the base REVIEW.md was not applied")
	}
	// services/REVIEW.md's bare glob is re-rooted under services/.
	if !byPath["services/vulns/closures/store.py"].HighRisk {
		t.Error("nested REVIEW.md glob not applied")
	}
	if byPath["docs/collectors/scanbox-sc.md"].HighRisk {
		t.Error("docs file should not be high-risk")
	}
	if !strings.Contains(in.ReviewRules, "Cursors are saved after upload.") || strings.Contains(in.ReviewRules, "nothing/") {
		t.Errorf("rules = %q", in.ReviewRules)
	}
	if strings.Join(in.Prompts, "|") != "Add Scanbox SC to the 12h pipeline|also fix 403s" {
		t.Errorf("prompts = %q (fleet's own review message must be filtered)", in.Prompts)
	}
	if in.Claims != "Done. CI is green." || in.Round != 1 || in.Title != "feat (sessions: vulns cursor)" {
		t.Errorf("claims=%q round=%d title=%q", in.Claims, in.Round, in.Title)
	}
	if !strings.Contains(in.CommitLog, "scanboxSC: join 12h pipeline") || strings.Contains(in.CommitLog, "base") {
		t.Errorf("commit log = %q", in.CommitLog)
	}
	if in.CI.State != selfreview.CIFailure || len(in.CI.Failed) != 1 {
		t.Errorf("ci = %+v", in.CI)
	}
}

func TestGatherFallsBackToFirstPromptAndReadsTicket(t *testing.T) {
	h := newHarness(t)
	h.deps.Turns = func(*session.Session) []session.Turn { return nil }
	h.r.write(t, ".fleet/ticket/BRZ-1/ticket.md", "# BRZ-1 fix it\n")
	if err := os.WriteFile(filepath.Join(h.r.dir, ".git", "info", "exclude"), []byte(".fleet/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _, err := Gather(context.Background(), h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Prompts) != 1 || in.Prompts[0] != "first prompt" || in.Ticket != "# BRZ-1 fix it\n" {
		t.Errorf("prompts=%q ticket=%q", in.Prompts, in.Ticket)
	}
}

func TestGatherNothingToReview(t *testing.T) {
	h := newHarness(t)
	h.r.git("checkout", "-q", "-f", "master")
	h.r.git("clean", "-qfd")
	_, _, err := Gather(context.Background(), h.deps, h.c)
	if !errors.Is(err, ErrNothingToReview) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Prepare(context.Background(), h.deps, h.c); !errors.Is(err, ErrNothingToReview) {
		t.Fatalf("prepare err = %v", err)
	}
	if r, _ := h.db.LatestReviewRun(h.c.Root); r != nil {
		t.Error("an empty diff must not leave a row")
	}
}

func TestPrepareStoresReusesAndGoesStale(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec, err := Prepare(ctx, h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != session.ReviewReady || rec.Run == nil || len(rec.Run.Stops) == 0 || rec.Row.Account != "me@x" ||
		rec.Row.Round != 1 || rec.Row.CacheKey == "" || h.runner.account != "/acct/me" {
		t.Fatalf("rec = %+v row = %+v", rec, rec.Row)
	}
	if rec.Run.Verdict == "" || rec.Run.TotalFiles != len(scanboxModified)+2 {
		t.Errorf("run = %+v", rec.Run)
	}
	if !rec.NeedsYou() || rec.Unanswered() != len(rec.Run.Stops) {
		t.Errorf("unanswered = %d", rec.Unanswered())
	}

	again, err := Prepare(ctx, h.deps, h.c)
	if err != nil || again.Row.ID != rec.Row.ID || h.runner.calls.Load() != 1 {
		t.Fatalf("same snapshot should reuse: id %s vs %s, calls %d, err %v", again.Row.ID, rec.Row.ID, h.runner.calls.Load(), err)
	}

	loaded, err := Load(h.db, h.c.Root)
	if err != nil || loaded.Row.ID != rec.Row.ID || loaded.Run == nil || !loaded.Snapshot.Dirty {
		t.Fatalf("load = %+v, %v", loaded, err)
	}
	if stale, err := CheckStale(ctx, h.deps, loaded); err != nil || stale {
		t.Fatalf("fresh run read stale: %v %v", stale, err)
	}

	h.r.write(t, "docs/collectors/scanbox-sc.md", "rewritten\n")
	if stale, _ := CheckStale(ctx, h.deps, loaded); !stale || !loaded.Stale {
		t.Error("a changed worktree must read stale")
	}
	next, err := Prepare(ctx, h.deps, h.c)
	if err != nil || next.Row.ID == rec.Row.ID || h.runner.calls.Load() != 2 || next.Row.Round != 1 {
		t.Fatalf("changed snapshot: id %s round %d calls %d err %v", next.Row.ID, next.Row.Round, h.runner.calls.Load(), err)
	}

	forced, err := PrepareWith(ctx, h.deps, h.c, PrepareOptions{Force: true})
	if err != nil || forced.Row.ID == next.Row.ID || h.runner.calls.Load() != 3 {
		t.Fatalf("force must rerun: %v calls %d", err, h.runner.calls.Load())
	}

	sums, err := Summaries(h.db, []string{h.c.Root, "other"})
	if err != nil || len(sums) != 1 {
		t.Fatalf("summaries = %v, %v", sums, err)
	}
	if s := sums[h.c.Root]; s.RunID != forced.Row.ID || s.Status != session.ReviewReady || s.Counts.Decisions == 0 ||
		s.Counts.Unanswered != s.Counts.Decisions || s.Seen || s.Verdict == "" || !s.SentAt.IsZero() {
		t.Errorf("summary = %+v", s)
	}
	if err := MarkSeen(h.db, forced.Row.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := Summaries(h.db, nil); !s[h.c.Root].Seen {
		t.Error("seen not recorded")
	}
}

func TestPrepareFailureIsStoredAndRetried(t *testing.T) {
	h := newHarness(t)
	h.runner.err = claudeaccount.ErrClaudeMissing
	rec, err := Prepare(context.Background(), h.deps, h.c)
	if !errors.Is(err, claudeaccount.ErrClaudeMissing) || rec == nil || rec.Status != session.ReviewFailed ||
		rec.Row.Error != "claude is not installed" {
		t.Fatalf("rec = %+v, err = %v", rec, err)
	}
	h.runner.err = nil
	rec, err = Prepare(context.Background(), h.deps, h.c)
	if err != nil || rec.Status != session.ReviewReady || h.runner.calls.Load() != 2 {
		t.Fatalf("a failed run must not be reused: %+v %v", rec, err)
	}
}

func TestPrepareDedupesConcurrentCalls(t *testing.T) {
	h := newHarness(t)
	h.runner.gate = make(chan struct{})
	var wg sync.WaitGroup
	recs := make([]*Record, 3)
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := Prepare(context.Background(), h.deps, h.c)
			if err != nil {
				t.Error(err)
			}
			recs[i] = rec
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for !InFlight(h.c.Root) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !InFlight(h.c.Root) {
		t.Fatal("never in flight")
	}
	time.Sleep(200 * time.Millisecond) // let the others reach the flight
	close(h.runner.gate)
	wg.Wait()
	if n := h.runner.calls.Load(); n != 1 {
		t.Errorf("reviewer ran %d times for one snapshot", n)
	}
	for _, r := range recs {
		if r == nil || r.Row.ID != recs[0].Row.ID {
			t.Errorf("callers got different runs")
		}
	}
	if InFlight(h.c.Root) {
		t.Error("flight not cleared")
	}
}

// A preparing row from a process that died reads failed, and does not block a
// new run.
func TestOrphanedPreparingRowReadsFailed(t *testing.T) {
	h := newHarness(t)
	old := &session.ReviewRunRow{ID: "orphan", Checkout: h.c.Root, Status: session.ReviewPreparing,
		CreatedAt: time.Now().Add(-time.Hour)}
	if err := h.db.InsertReviewRun(old); err != nil {
		t.Fatal(err)
	}
	rec, err := Load(h.db, h.c.Root)
	if err != nil || rec.Status != session.ReviewFailed || rec.Row.Error == "" {
		t.Fatalf("orphan = %+v, %v", rec, err)
	}
	if s, _ := Summaries(h.db, nil); s[h.c.Root].Status != session.ReviewFailed {
		t.Errorf("summary status = %s", s[h.c.Root].Status)
	}
}

func TestSendRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec, err := Prepare(ctx, h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckSendable(rec); !errors.Is(err, ErrNoAnswers) {
		t.Fatalf("no answers: %v", err)
	}
	stops := rec.Run.Stops
	if err := SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: stops[0].ID, Kind: "maybe"}); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("bad kind: %v", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: stops[0].ID, Kind: selfreview.AnswerLater}))
	rec, _ = Load(h.db, h.c.Root)
	// ok and later give the agent nothing to do: approve instead.
	if err := CheckSendable(rec); !errors.Is(err, ErrNothingToChange) {
		t.Fatalf("later only: %v", err)
	}
	must(SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: stops[2].ID, Kind: selfreview.AnswerFine}))
	rec, _ = Load(h.db, h.c.Root)
	if err := CheckSendable(rec); !errors.Is(err, ErrNothingToChange) {
		t.Fatalf("ok + later: %v", err)
	}
	must(SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: stops[1].ID, Kind: selfreview.AnswerChange}))
	rec, _ = Load(h.db, h.c.Root)
	var ne *NoteError
	if err := CheckSendable(rec); !errors.As(err, &ne) || ne.StopID != stops[1].ID || !errors.Is(err, ErrNoteRequired) {
		t.Fatalf("change without note: %v", err)
	}
	if _, err := Send(ctx, h.deps, h.s, rec); !errors.Is(err, ErrNoteRequired) || len(h.sent) != 0 {
		t.Fatalf("send must refuse first: %v", err)
	}
	must(SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: stops[1].ID, Kind: selfreview.AnswerChange, Note: "start at 0 */24"}))
	must(SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: "file:docs/collectors/scanbox-sc.md", Kind: selfreview.AnswerAsk, Note: "why?"}))
	rec, _ = Load(h.db, h.c.Root)

	if _, err := Send(ctx, h.deps, h.s, rec); err != nil {
		t.Fatal(err)
	}
	if len(h.sent) != 1 || !strings.HasPrefix(h.sent[0], "[fleet review · round 1 · tree ") ||
		!strings.Contains(h.sent[0], "start at 0 */24") || !strings.Contains(h.sent[0], "Details: .fleet/review/round-1.md") {
		t.Fatalf("message = %q", h.sent)
	}
	md, err := os.ReadFile(filepath.Join(h.r.dir, ".fleet", "review", "round-1.md"))
	if err != nil || !strings.Contains(string(md), "# fleet review · round 1") {
		t.Fatalf("round file: %v\n%s", err, md)
	}
	if out := h.r.git("check-ignore", ".fleet/review/round-1.md"); out != ".fleet/review/round-1.md" {
		t.Errorf("round file is not git-excluded: %q", out)
	}
	rec, _ = Load(h.db, h.c.Root)
	if rec.Status != session.ReviewSent || len(rec.Sent) != 4 || rec.NeedsYou() || rec.Row.SessionID != h.s.ID {
		t.Errorf("after send: status %s sent %v", rec.Status, rec.Sent)
	}
	if s, _ := Summaries(h.db, nil); s[h.c.Root].SentAt.IsZero() || s[h.c.Root].Counts.Acting() != 2 {
		t.Errorf("summary after send = %+v", s[h.c.Root])
	}
	if _, err := Send(ctx, h.deps, h.s, rec); !errors.Is(err, ErrAlreadySent) {
		t.Errorf("resend: %v", err)
	}

	// Same snapshot after sending: v opens the sent run, no new review.
	again, err := Prepare(ctx, h.deps, h.c)
	if err != nil || again.Row.ID != rec.Row.ID {
		t.Fatalf("post-send same snapshot: %v %v", again, err)
	}

	// The agent changes things: round 2, built on round 1.
	h.r.write(t, "docs/collectors/scanbox-sc.md", "fixed\n")
	r2, err := Prepare(ctx, h.deps, h.c)
	if err != nil || r2.Row.Round != 2 || h.runner.calls.Load() != 2 {
		t.Fatalf("round 2: %+v %v", r2, err)
	}
	in := h.runner.inputs[1]
	if in.Round != 2 || in.Previous == nil || len(in.PreviousAnswers) != 4 {
		t.Errorf("round-2 input: round %d prev %v answers %d", in.Round, in.Previous != nil, len(in.PreviousAnswers))
	}

	if err := Approve(h.db, r2.Row.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := Load(h.db, h.c.Root); rec.Status != session.ReviewApproved {
		t.Errorf("approve: %s", rec.Status)
	}
}

func TestSendRefusalLeavesTheRunReady(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec, err := Prepare(ctx, h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveAnswer(h.db, rec.Row.ID, selfreview.Answer{StopID: rec.Run.Stops[0].ID, Kind: selfreview.AnswerChange, Note: "x"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = Load(h.db, h.c.Root)
	refusal := &deliver.Refusal{Reason: deliver.ErrAgentRunning, Title: "t"}
	h.deps.Deliver = func(context.Context, *session.Session, string) (deliver.Result, error) {
		return deliver.Result{}, refusal
	}
	if _, err := Send(ctx, h.deps, h.s, rec); !errors.Is(err, deliver.ErrAgentRunning) {
		t.Fatalf("err = %v", err)
	}
	if rec, _ := Load(h.db, h.c.Root); rec.Status != session.ReviewReady {
		t.Errorf("a refused send must leave the run ready: %s", rec.Status)
	}
}

func TestPickAccount(t *testing.T) {
	isolate(t)
	now := time.Now()
	store := &claudeaccount.Store{}
	store.Upsert(claudeaccount.Account{Email: "own@x", OrgUUID: "o1"})
	store.Upsert(claudeaccount.Account{Email: "other@x", OrgUUID: "o2"})
	all := store.List()
	cfg := &config.Config{}
	s := &session.Session{Account: "own@x", Agent: agent.Claude, ProjectPath: t.TempDir()}
	c := Checkout{Root: s.ProjectPath, Sessions: []*session.Session{s}}

	if email, _ := pickAccount(store, all, nil, c, cfg, now); email != "own@x" {
		t.Errorf("usable own account: got %q", email)
	}
	usage := map[string]claudeaccount.Usage{"own@x": {LoggedOut: true}}
	if email, _ := pickAccount(store, all, usage, c, cfg, now); email != "other@x" {
		t.Errorf("logged-out own account: got %q", email)
	}
	codex := &session.Session{Account: "", Agent: agent.Codex, ProjectPath: t.TempDir()}
	cc := Checkout{Root: codex.ProjectPath, Sessions: []*session.Session{codex}}
	if email, _ := pickAccount(store, all, nil, cc, cfg, now); email != "own@x" {
		t.Errorf("non-claude session: got %q (first configured under the default strategy's tie)", email)
	}
}

// A review belongs to the checkout: every session working it contributes its
// prompts, in time order and labelled, and the newest reply is the claim.
func TestGatherReadsEverySessionInTheCheckout(t *testing.T) {
	h := newHarness(t)
	t0 := time.Now().Add(-time.Hour)
	other := &session.Session{ID: "sess-2", Title: "tests", ProjectPath: h.r.dir, Agent: agent.Claude,
		BaseRef: "origin/nope", CreatedAt: t0.Add(-time.Hour)}
	codex := &session.Session{ID: "sess-3", Title: "docs", ProjectPath: h.r.dir, Agent: agent.Codex,
		FirstPrompt: "write the docs", CreatedAt: t0.Add(5 * time.Minute)}
	h.s.CreatedAt = t0
	h.c.Sessions = append(h.c.Sessions, other, codex)
	h.deps.Turns = func(s *session.Session) []session.Turn {
		switch s.ID {
		case "sess-1":
			return []session.Turn{
				{Prompt: "add scanbox", PromptAt: t0, Reply: "added", ReplyAt: t0.Add(time.Minute)},
				{Prompt: "[fleet review · round 1]", PromptAt: t0.Add(20 * time.Minute)},
			}
		case "sess-2":
			return []session.Turn{{Prompt: "cover it with tests", PromptAt: t0.Add(10 * time.Minute),
				Reply: "Tests pass.", ReplyAt: t0.Add(11 * time.Minute)}}
		}
		return nil
	}
	in, snap, err := Gather(context.Background(), h.deps, h.c)
	if err != nil {
		t.Fatal(err)
	}
	want := "[vulns cursor] add scanbox|[docs] write the docs|[tests] cover it with tests"
	if got := strings.Join(in.Prompts, "|"); got != want {
		t.Errorf("prompts = %q\nwant      %q", got, want)
	}
	if in.Claims != "[tests] Tests pass." {
		t.Errorf("claims = %q (the newest reply across sessions)", in.Claims)
	}
	// The newest session that recorded a base wins: sess-1 (master), not
	// sess-2's older origin/nope.
	if snap.BaseRef != "master" {
		t.Errorf("base = %q", snap.BaseRef)
	}
	if !strings.Contains(in.Title, "vulns cursor, tests, docs") {
		t.Errorf("title = %q", in.Title)
	}
}

// The default send target is the session that most recently finished work, and
// a stored target wins while it is still in the checkout.
func TestDefaultTargetPrefersTheLastFinished(t *testing.T) {
	now := time.Now()
	a := &session.Session{ID: "a", LastAccessedAt: now.Add(-time.Minute)}
	b := &session.Session{ID: "b", LastAccessedAt: now.Add(-time.Hour)}
	c := Checkout{Root: "/r", Sessions: []*session.Session{b, a}}
	if got := DefaultTarget(c, ""); got != a {
		t.Errorf("most recently opened: got %v", got)
	}
	b.NoteFinished(now)
	if got := DefaultTarget(c, ""); got != b {
		t.Errorf("most recently finished: got %v", got)
	}
	if got := DefaultTarget(c, "a"); got != a {
		t.Errorf("stored target: got %v", got)
	}
	if got := DefaultTarget(c, "gone"); got != b {
		t.Errorf("a stored target no longer here falls back: got %v", got)
	}
	if ids := []string{Targets(c)[0].ID, Targets(c)[1].ID}; ids[0] != "b" || ids[1] != "a" {
		t.Errorf("targets = %v", ids)
	}
	if DefaultTarget(Checkout{Root: "/r"}, "") != nil {
		t.Error("no sessions → no target")
	}
}

func TestDiscardDeletesTheCheckoutsRuns(t *testing.T) {
	h := newHarness(t)
	if _, err := Prepare(context.Background(), h.deps, h.c); err != nil {
		t.Fatal(err)
	}
	if err := Discard(h.db, h.c.Root); err != nil {
		t.Fatal(err)
	}
	if rec, _ := Load(h.db, h.c.Root); rec != nil {
		t.Errorf("discard left %+v", rec.Row)
	}
}

func TestRerootGlob(t *testing.T) {
	for _, c := range []struct{ d, g, want string }{
		{"", "*.py", "*.py"},
		{"svc", "*.py", "svc/**/*.py"},
		{"svc", "db/", "svc/db/"},
		{"svc", "/api/**", "svc/api/**"},
	} {
		if got := rerootGlob(c.d, c.g); got != c.want {
			t.Errorf("rerootGlob(%q, %q) = %q, want %q", c.d, c.g, got, c.want)
		}
	}
}

func TestFailureTextForUnreadableReply(t *testing.T) {
	if got := failureText(fmt.Errorf("repair: %w", selfreview.ErrNoJSON)); got != "the reviewer's answer couldn't be read" {
		t.Errorf("failureText = %q", got)
	}
}
