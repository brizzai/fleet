package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

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
	if got["gitInfoUpdateMsg"] {
		t.Error("refreshAllGitAndPR sends gitInfoUpdateMsg — that carries the payload " +
			"through the Update loop, reintroducing the blocking rendezvous. Write the " +
			"cache directly and send gitRepaintMsg as a hint instead.")
	}
}

// TestWorkerWatchdogIgnoresAttach: an attach suspends the Tea loop for as long
// as the user is in the session, so worker latency measured across one says
// nothing about a wedge. Without this gate the watchdog fired on every attach
// longer than workerStallThreshold — 18 stall dumps in a single day of normal
// use, which is enough noise to hide a real one.
func TestWorkerWatchdogIgnoresAttach(t *testing.T) {
	if !mentions(t, "workerWatchdog", "isAttaching") {
		t.Error("workerWatchdog no longer consults isAttaching — every attach over " +
			"workerStallThreshold will dump a false stall")
	}
	// The gitWorker skips its cycle during an attach for the same reason: the
	// sidebar isn't on screen, and the detach path rebuilds it wholesale.
	if !mentions(t, "gitWorkerCycle", "isAttaching") {
		t.Error("gitWorkerCycle no longer consults isAttaching — the fan-out will run " +
			"against a Tea loop that tea.Exec has suspended")
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
