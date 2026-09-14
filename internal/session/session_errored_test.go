package session

import (
	"slices"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
)

// recordErrored swaps trackEvent for a recorder of the reasons session_errored
// is sent with.
func recordErrored(t *testing.T) *[]string {
	t.Helper()
	var reasons []string
	orig := trackEvent
	trackEvent = func(event string, props map[string]interface{}) {
		if event == analytics.EventSessionErrored {
			reasons = append(reasons, props["reason"].(string))
		}
	}
	t.Cleanup(func() { trackEvent = orig })
	return &reasons
}

// One failure is one event: a status pass that finds the session still in error
// sends nothing, while failing again after recovering is a new failure.
func TestSessionErroredReportsEachFailureOnce(t *testing.T) {
	debuglog.Init()
	reasons := recordErrored(t)
	s := &Session{
		ID:            "errored",
		Agent:         agent.OpenCode,
		Status:        StatusRunning,
		paneCapturer:  &mockPane{},
		hookStatus:    "error",
		hookUpdatedAt: time.Now(),
	}

	s.UpdateStatus()
	s.UpdateStatus()
	s.hookStatus = "running"
	s.UpdateStatus()
	s.hookStatus = "error"
	s.UpdateStatus()

	if want := []string{"agent_error", "agent_error"}; !slices.Equal(*reasons, want) {
		t.Errorf("session_errored reasons = %v, want %v", *reasons, want)
	}
}

// A liveness check that lands mid-launch sees a tmux session that doesn't exist
// yet. The launch reports its own failure, so the status pass stays quiet — as it
// does for a session that was already in error.
func TestSessionErroredOnlyFromALiveStatus(t *testing.T) {
	debuglog.Init()
	t.Setenv("HOME", t.TempDir()) // a death also writes a crash dump
	for _, tc := range []struct {
		old  Status
		want []string
	}{
		{StatusRunning, []string{"tmux_gone"}},
		{StatusStarting, nil},
		{StatusError, nil},
	} {
		reasons := recordErrored(t)
		s := &Session{ID: "dead", Status: tc.old, paneCapturer: &mockPane{dead: true}}
		s.UpdateStatus()
		if !slices.Equal(*reasons, tc.want) {
			t.Errorf("from %s: session_errored reasons = %v, want %v", tc.old, *reasons, tc.want)
		}
	}
}
