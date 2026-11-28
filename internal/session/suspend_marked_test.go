package session

import "testing"

// TestMarkSuspendedIfAtRest: the park path decides and marks in one step, so a
// status change landing between "is it at rest?" and "mark it" can't get a
// running session killed.
func TestMarkSuspendedIfAtRest(t *testing.T) {
	cases := []struct {
		name       string
		status     Status
		id         string
		relaunch   bool
		wantMarked bool
	}{
		{"idle with id", StatusIdle, "conv", false, true},
		{"finished with id", StatusFinished, "conv", false, true},
		{"running", StatusRunning, "conv", false, false},
		{"waiting", StatusWaiting, "conv", false, false},
		{"no conversation id", StatusIdle, "", false, false},
		{"mid relaunch", StatusIdle, "conv", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{ID: "x", Status: tc.status, ClaudeSessionID: tc.id}
			if tc.relaunch {
				s.relaunching = 1
				s.Status = tc.status // beginRelaunch would say starting; keep the case about the counter
			}
			prior, ok := s.MarkSuspendedIfAtRest()
			if ok != tc.wantMarked {
				t.Fatalf("marked = %v, want %v", ok, tc.wantMarked)
			}
			if prior != tc.status {
				t.Errorf("prior = %s, want %s", prior, tc.status)
			}
			want := tc.status
			if tc.wantMarked {
				want = StatusSuspended
			}
			if got := s.GetStatus(); got != want {
				t.Errorf("status = %s, want %s", got, want)
			}
		})
	}
}

// TestSuspendMarkedSkipsAPickedUpSession: the kill runs after the mark, off the
// UI thread. A session resumed in between has left StatusSuspended, and the
// queued kill must not take its new agent down.
func TestSuspendMarkedSkipsAPickedUpSession(t *testing.T) {
	s := &Session{ID: "x", Status: StatusIdle, ClaudeSessionID: "conv"}
	prior, ok := s.MarkSuspendedIfAtRest()
	if !ok {
		t.Fatal("not marked")
	}
	s.SetStatus(StatusStarting) // resumeSelected, on the UI thread
	did, err := s.SuspendMarked(prior)
	if did || err != nil {
		t.Fatalf("SuspendMarked = %v, %v; want skipped", did, err)
	}
	if got := s.GetStatus(); got != StatusStarting {
		t.Errorf("status = %s, want starting left alone", got)
	}
}
