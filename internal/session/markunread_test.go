package session

import "testing"

// MarkUnread is the inverse of Acknowledge: an idle (acknowledged-finished)
// session flips back to Finished + unacknowledged so it re-flags for attention.
func TestMarkUnread_IdleToFinished(t *testing.T) {
	s := &Session{ID: "00000001-1710000000", Status: StatusIdle, Acknowledged: true}
	s.MarkUnread()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("status: got %q, want %q", got, StatusFinished)
	}
	if s.Acknowledged {
		t.Fatalf("Acknowledged should be cleared")
	}
}

// MarkUnread only moves an idle session; other states keep their status (it just
// clears the acknowledged flag).
func TestMarkUnread_NonIdleKeepsStatus(t *testing.T) {
	for _, st := range []Status{StatusRunning, StatusWaiting, StatusFinished} {
		s := &Session{ID: "00000002-1710000000", Status: st, Acknowledged: true}
		s.MarkUnread()
		if got := s.GetStatus(); got != st {
			t.Fatalf("status for %q: got %q, want unchanged", st, got)
		}
	}
}
