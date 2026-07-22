package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

// TestFastPassSessionsSelectsActiveStates pins the core contract of the
// split-cadence worker: only Running/Waiting/Starting sessions ride the ~500ms
// fast pass (they transition via pane content — no hook fires when a permission
// is approved), Idle/Finished/Error stay on the 2s round-robin, and a session
// already handled by the priority queue this cycle is not processed twice.
func TestFastPassSessionsSelectsActiveStates(t *testing.T) {
	mk := func(title string, st session.Status) *session.Session {
		s := session.NewSession(title, "/tmp/"+title)
		s.SetStatus(st)
		return s
	}

	running := mk("run", session.StatusRunning)
	waiting := mk("wait", session.StatusWaiting)
	starting := mk("start", session.StatusStarting)
	idle := mk("idle", session.StatusIdle)
	finished := mk("fin", session.StatusFinished)
	errored := mk("err", session.StatusError)
	alreadyDone := mk("already", session.StatusWaiting) // active, but priority-processed

	sessions := []*session.Session{running, waiting, starting, idle, finished, errored, alreadyDone}
	processed := map[string]bool{alreadyDone.ID: true}

	got := fastPassSessions(sessions, processed)

	gotIDs := make(map[string]bool, len(got))
	for _, s := range got {
		gotIDs[s.ID] = true
	}

	for _, want := range []*session.Session{running, waiting, starting} {
		if !gotIDs[want.ID] {
			t.Errorf("expected active session %q (%s) in fast pass", want.Title, want.GetStatus())
		}
	}
	for _, dont := range []*session.Session{idle, finished, errored, alreadyDone} {
		if gotIDs[dont.ID] {
			t.Errorf("did not expect session %q (%s, processed=%v) in fast pass",
				dont.Title, dont.GetStatus(), processed[dont.ID])
		}
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 fast-pass sessions, got %d", len(got))
	}
}

// TestRoundRobinBatchReachesIdleSessions pins the anti-starvation contract: the
// fast pass marks all active sessions processed, so the round-robin must still
// reach the idle/finished sessions instead of letting a fixed-width window land
// entirely on already-processed sessions and step the cursor past the idle ones.
func TestRoundRobinBatchReachesIdleSessions(t *testing.T) {
	mk := func(title string, st session.Status) *session.Session {
		s := session.NewSession(title, "/tmp/"+title)
		s.SetStatus(st)
		return s
	}

	// 8 sessions: indices 0-4 active (processed by the fast pass), 5-7 idle.
	// With statusRoundRobin=5 and the old fixed-window logic, the window 0..4
	// would be entirely processed → zero idle sessions updated, cursor jumps to
	// 5, and the idle sessions only get checked the cycle after.
	var sessions []*session.Session
	processed := map[string]bool{}
	for i := 0; i < 5; i++ {
		s := mk("active", session.StatusRunning)
		sessions = append(sessions, s)
		processed[s.ID] = true
	}
	idleIDs := map[string]bool{}
	for i := 0; i < 3; i++ {
		s := mk("idle", session.StatusIdle)
		sessions = append(sessions, s)
		idleIDs[s.ID] = true
	}

	batch, next := roundRobinBatch(sessions, processed, 0, statusRoundRobin)

	if len(batch) != 3 {
		t.Fatalf("expected all 3 idle sessions picked in one batch, got %d", len(batch))
	}
	for _, s := range batch {
		if !idleIDs[s.ID] {
			t.Errorf("round-robin picked a non-idle/processed session %q (%s)", s.Title, s.GetStatus())
		}
	}
	// Cursor advanced past every examined session (all 8), wrapping to 0.
	if next != 0 {
		t.Errorf("expected cursor to wrap to 0 after examining all 8 sessions, got %d", next)
	}
}

// TestRoundRobinBatchCapsAndResumes verifies the budget cap and that the cursor
// resumes mid-list so successive cycles cover every session.
func TestRoundRobinBatchCapsAndResumes(t *testing.T) {
	mk := func() *session.Session {
		s := session.NewSession("s", "/tmp/s")
		s.SetStatus(session.StatusIdle)
		return s
	}
	var sessions []*session.Session
	for i := 0; i < 12; i++ {
		sessions = append(sessions, mk())
	}
	processed := map[string]bool{}

	batch, next := roundRobinBatch(sessions, processed, 0, statusRoundRobin)
	if len(batch) != statusRoundRobin {
		t.Fatalf("expected budget-capped batch of %d, got %d", statusRoundRobin, len(batch))
	}
	if next != statusRoundRobin {
		t.Errorf("expected cursor at %d, got %d", statusRoundRobin, next)
	}
	// Next cycle resumes where the last left off.
	batch2, _ := roundRobinBatch(sessions, processed, next, statusRoundRobin)
	if batch2[0].ID != sessions[statusRoundRobin].ID {
		t.Errorf("expected second batch to resume at index %d", statusRoundRobin)
	}
}

// TestHeavyCycleDue verifies the wall-clock gate that throttles the worker's
// heavy ~2s work while the fast pass runs every ~500ms tick.
func TestHeavyCycleDue(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	// Fresh worker (zero lastHeavy) is always due, so the first cycle runs full init.
	if !heavyCycleDue(time.Time{}, base) {
		t.Error("zero lastHeavy should be due (first cycle must run heavy)")
	}
	// Within tickInterval → not due: a fast-only cycle, heavy work throttled.
	if heavyCycleDue(base, base.Add(tickInterval-time.Millisecond)) {
		t.Error("sub-tickInterval gap should not be due")
	}
	// >= tickInterval later → due.
	if !heavyCycleDue(base, base.Add(tickInterval)) {
		t.Error(">= tickInterval gap should be due")
	}
}

// callees returns the names of every function/method called in the body of the
// named top-level func in app.go. Used by the guards below, which pin *which
// goroutine* a call runs on — a property no runtime assertion can reach,
// because the damage only shows up when a real attach suspends the Tea loop.
func callees(t *testing.T, fnName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch c := call.Fun.(type) {
			case *ast.Ident:
				out[c.Name] = true
			case *ast.SelectorExpr:
				out[c.Sel.Name] = true
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no calls found in %s — did it get renamed?", fnName)
	}
	return out
}

// TestStatusWorkerCycleDoesNotRunGitFanout pins the fix for the status freeze:
// the git+PR fan-out must not share a goroutine with the ~500ms fast pass.
//
// The fan-out is unbounded (N repos at 4-wide, each a serial git refresh plus
// up to two 15s `gh` calls) and every result has to reach the Update loop —
// which tea.Exec suspends for the duration of an attach. While it lived inline
// in statusWorkerCycle, one 16-minute cycle froze every session's status for
// its whole duration. That matters most for waiting→running: approving a
// permission fires no hook, so the flip is pane-detection-only and rides the
// fast pass exclusively.
func TestStatusWorkerCycleDoesNotRunGitFanout(t *testing.T) {
	if callees(t, "statusWorkerCycle")["refreshAllGitAndPR"] {
		t.Error("statusWorkerCycle calls refreshAllGitAndPR — the git+PR fan-out is back " +
			"on the fast pass's goroutine and will starve pane detection during slow " +
			"cycles. It belongs in gitWorkerCycle.")
	}
	if !callees(t, "gitWorkerCycle")["refreshAllGitAndPR"] {
		t.Error("gitWorkerCycle no longer calls refreshAllGitAndPR — git/PR info will never refresh")
	}
}

// TestGitFanoutPublishesWithoutTheUpdateLoop pins the second half of the fix.
//
// Bubble Tea's msgs channel is UNBUFFERED (v2 tea.go: `msgs: make(chan Msg)`)
// and Send is a bare rendezvous with no deadline, so while tea.Exec holds the
// loop suspended nothing drains it. The fan-out used to push each repo's result
// through h.send, which parked one goroutine per repo for the entire attach,
// each holding a semaphore slot, wedging the fan-out until detach — 16 of 18
// captured stall dumps had all four slots stuck exactly there.
//
// So the fan-out must publish through writeGitInfo (a lock-free COW swap, safe
// from any goroutine) and treat the message as a repaint hint only.
func TestGitFanoutPublishesWithoutTheUpdateLoop(t *testing.T) {
	got := callees(t, "refreshAllGitAndPR")
	if !got["writeGitInfo"] {
		t.Error("refreshAllGitAndPR no longer calls writeGitInfo — results are " +
			"presumably routed through h.send again, which blocks for the whole " +
			"attach on Tea's unbuffered channel")
	}
	// Must be mentions, not callees: `gitInfoUpdateMsg{...}` is an
	// *ast.CompositeLit argument, never a *ast.CallExpr.Fun, so callees can
	// never see it and the assertion would silently always pass.
	if mentions(t, "refreshAllGitAndPR", "gitInfoUpdateMsg") {
		t.Error("refreshAllGitAndPR references gitInfoUpdateMsg — that carries the payload " +
			"through the Update loop, reintroducing the blocking rendezvous. Write the " +
			"cache directly and send gitRepaintMsg as a hint instead.")
	}
}

// TestGitWorkerCycleSkipsWhileAttaching is the behavioral counterpart to the AST
// guards: it proves the gate's *polarity*, which `mentions` cannot. An inverted
// condition (`if !h.isAttaching.Load()`) still mentions the identifier and would
// sail past a presence check while making the fan-out run only during attaches.
func TestGitWorkerCycleSkipsWhileAttaching(t *testing.T) {
	newHome := func(t *testing.T) *Home {
		t.Helper()
		dir := t.TempDir()
		storage, err := session.Open(filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { storage.Close() })

		h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
		s := session.NewSession("s", filepath.Join(dir, "repo"))
		h.sessions = []*session.Session{s}
		return h
	}

	// Attached: the fan-out must not run, so the cache stays empty.
	h := newHome(t)
	h.isAttaching.Store(true)
	h.gitWorkerCycle()
	if n := len(h.gitInfo()); n != 0 {
		t.Errorf("gitWorkerCycle populated %d repos while attached — the fan-out ran "+
			"against a suspended Tea loop", n)
	}

	// Not attached: same input must populate the cache. Without this arm the test
	// above would pass for a cycle that never does anything.
	h2 := newHome(t)
	h2.gitWorkerCycle()
	if len(h2.gitInfo()) == 0 {
		t.Error("gitWorkerCycle populated nothing while detached — the fan-out never ran")
	}
}

// TestWorkerWatchdogIgnoresAttach: an attach suspends the Tea loop for as long
// as the user is in the session, so worker latency measured across one says
// nothing about a wedge. Without this gate the watchdog fired on every attach
// longer than workerStallThreshold — 18 stall dumps in a single day of normal
// use, which is enough noise to hide a real one.
func TestWorkerWatchdogDiscountsAttachWindow(t *testing.T) {
	if !mentions(t, "workerWatchdog", "attachAdjustedStall") {
		t.Error("workerWatchdog no longer discounts the attach window — every attach " +
			"over workerStallThreshold will dump a false stall")
	}
	// Both workers must be watched. The git+PR fan-out — the blocking work the
	// watchdog exists for — lives on gitWorker now, so watching only the status
	// worker would read healthy straight through a permanent git wedge.
	if !mentions(t, "workerWatchdog", "lastGitCycleNano") {
		t.Error("workerWatchdog does not watch gitWorker's liveness stamps — a wedged " +
			"git fan-out would freeze branch/dirty/PR while the heartbeat reads healthy")
	}
	if !mentions(t, "gitWorkerCycle", "gitCycleStartNano") {
		t.Error("gitWorkerCycle sets no liveness stamps — nothing can observe it wedging")
	}
}

// TestAttachAdjustedStall pins the arithmetic the watchdog's accuracy rests on:
// an attach must never manufacture a stall, and must never mask one either.
func TestAttachAdjustedStall(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	attachedFor := func(d time.Duration) int64 { return now.Add(-d).UnixNano() }

	cases := []struct {
		name        string
		stalledFor  time.Duration
		attachStart int64
		want        time.Duration
	}{
		{"not attached — unchanged", 100 * time.Second, 0, 100 * time.Second},
		{"whole stall inside the attach — fully discounted", 100 * time.Second, attachedFor(100 * time.Second), 0},
		{"attach longer than the stall — floors at 0", 30 * time.Second, attachedFor(100 * time.Second), 0},
		// The case the plain skip got wrong: a 95s stall of which only 20s is
		// attach is still a 75s real stall, and must stay measurable.
		{"partial overlap — remainder survives", 95 * time.Second, attachedFor(20 * time.Second), 75 * time.Second},
	}
	for _, c := range cases {
		if got := attachAdjustedStall(c.stalledFor, c.attachStart, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// mentions reports whether the named top-level func in app.go references the
// given identifier anywhere in its body.
func mentions(t *testing.T, fnName, ident string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == ident {
				found = true
			}
			return true
		})
		return found
	}
	t.Fatalf("func %s not found in app.go — renamed?", fnName)
	return false
}

// calleeName returns the called function's bare name (`h.foo(...)` → "foo").
func calleeName(call *ast.CallExpr) string {
	switch c := call.Fun.(type) {
	case *ast.Ident:
		return c.Name
	case *ast.SelectorExpr:
		return c.Sel.Name
	}
	return ""
}

// TestStatusWorkerFeedsHookChangesIntoPriority pins the fix for a 24s status lag:
// statusWorkerCycle's own syncHookStatuses call must not drop its return value.
//
// syncHookStatuses diffs the hook against the session's in-memory hook state and then
// overwrites it, so a transition is consume-once — whichever caller syncs first eats
// it. During an attach tea.Exec suspends the Update loop, so hookChangedMsg never runs
// and this worker call is always the first to observe. Discarding the result dropped
// the transition entirely: the session fell back to the round-robin (≈26s at 65
// sessions) and statusUpdateMsg's post-detach catch-up sync found nothing left to
// enqueue, which is precisely the case it exists to cover.
//
// AST-based for the same reason the guards above are: the damage only appears when a
// real attach suspends the Tea loop while the worker keeps cycling, and UpdateStatus
// needs a live tmux pane to reach Running at all — neither is reachable from a unit
// test, and Session.paneCapturer is package-private to internal/session.
func TestStatusWorkerFeedsHookChangesIntoPriority(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "statusWorkerCycle" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("statusWorkerCycle not found in app.go — renamed?")
	}

	// 1. The result must be bound. A bare `h.syncHookStatuses(...)` expression
	// statement is the original bug verbatim.
	var bound string
	ast.Inspect(fn, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && calleeName(call) == "syncHookStatuses" {
				t.Error("statusWorkerCycle discards syncHookStatuses' return value — the hook " +
					"transitions this call consumed are lost, so sessions whose status changed " +
					"during an attach wait for the round-robin instead of the priority pass")
			}
		case *ast.AssignStmt:
			for i, rhs := range s.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok || calleeName(call) != "syncHookStatuses" || i >= len(s.Lhs) {
					continue
				}
				if id, ok := s.Lhs[i].(*ast.Ident); ok {
					bound = id.Name
				}
			}
		}
		return true
	})
	if bound == "" {
		t.Fatal("no `x := h.syncHookStatuses(...)` in statusWorkerCycle — either the worker " +
			"stopped syncing hooks, or the result is bound in a form this guard cannot see")
	}

	// 2. Binding it is not enough: it has to reach the priority set. Without this arm
	// a log-only use (or `_ = hookChanged`) would satisfy the check above while the
	// changed sessions still starved on the round-robin.
	fed := false
	ast.Inspect(fn, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if id, ok := rng.X.(*ast.Ident); !ok || id.Name != bound {
			return true
		}
		ast.Inspect(rng.Body, func(b ast.Node) bool {
			if id, ok := b.(*ast.Ident); ok && id.Name == "priorityIDs" {
				fed = true
			}
			return true
		})
		return true
	})
	if !fed {
		t.Errorf("statusWorkerCycle binds syncHookStatuses' result to %q but never merges it "+
			"into priorityIDs — the sessions it consumed still wait for the round-robin", bound)
	}
}
