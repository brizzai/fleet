package ui

import (
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
