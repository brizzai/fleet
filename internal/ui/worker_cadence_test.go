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
