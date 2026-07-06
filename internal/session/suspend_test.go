package session

import (
	"testing"

	"github.com/brizzai/fleet/internal/debuglog"
)

// A suspended session must be left untouched by the status pipeline. Its tmux is
// intentionally gone (pane dead), which would otherwise trip the liveness gates at
// the top of UpdateStatus and flip it to error + write a spurious crash dump.
func TestUpdateStatus_SuspendedShortCircuits(t *testing.T) {
	debuglog.Init()
	mock := &mockPane{dead: true} // dead pane → IsAlive()=false, IsPaneDead()=true
	s := &Session{
		ID:           "suspend-test",
		Title:        "suspended",
		Status:       StatusSuspended,
		paneCapturer: mock,
	}
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusSuspended {
		t.Fatalf("suspended session was clobbered by UpdateStatus: got %q, want %q", got, StatusSuspended)
	}
	if s.deathRecorded {
		t.Fatalf("suspended session wrongly triggered a crash dump")
	}
}

// A suspended session must ignore incoming hooks (e.g. the killed agent's
// SessionEnd "dead" death-rattle) so resume starts from a clean slate.
func TestUpdateHookStatus_IgnoredWhileSuspended(t *testing.T) {
	debuglog.Init()
	s := &Session{ID: "suspend-hook", Status: StatusSuspended}
	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "abc"}, false); changed {
		t.Fatalf("hook was applied while suspended")
	}
	if got := s.GetHookStatus(); got != "" {
		t.Fatalf("suspended session stored a hook status: %q", got)
	}
}
