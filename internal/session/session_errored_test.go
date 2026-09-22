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

// recordErroredProps is recordErrored when the test is about what rode along
// with the reason rather than how many events were sent.
func recordErroredProps(t *testing.T) *[]map[string]interface{} {
	t.Helper()
	var sent []map[string]interface{}
	orig := trackEvent
	trackEvent = func(event string, props map[string]interface{}) {
		if event == analytics.EventSessionErrored {
			sent = append(sent, props)
		}
	}
	t.Cleanup(func() { trackEvent = orig })
	return &sent
}

// SessionEnd also fires on /clear and /logout, so "the hook said dead" on its own
// can't tell a conversation rotation from a session that died. The reason Claude
// Code sent is what separates them, and it has to survive the whole trip:
// status file → watcher → UpdateHookStatus → the event.
func TestSessionErroredCarriesTheHooksExitReason(t *testing.T) {
	debuglog.Init()
	for _, ag := range []agent.Type{agent.Claude, agent.OpenCode} {
		sent := recordErroredProps(t)
		s := &Session{ID: "ended", Agent: ag, Status: StatusRunning, paneCapturer: &mockPane{}}
		s.UpdateHookStatus(&HookStatus{Status: "dead", Reason: "clear", UpdatedAt: time.Now()}, false)
		s.UpdateStatus()

		if len(*sent) != 1 {
			t.Fatalf("%s: got %d session_errored events, want 1", ag, len(*sent))
		}
		if got := (*sent)[0]["reason"]; got != "hook_dead" {
			t.Errorf("%s: reason = %v, want hook_dead", ag, got)
		}
		if got := (*sent)[0]["exit_reason"]; got != "clear" {
			t.Errorf("%s: exit_reason = %v, want clear", ag, got)
		}
	}
}

// A hook with no reason (every event but SessionEnd, and a Claude Code too old to
// send one) must not send an empty exit_reason: an absent property is a fact the
// backend can filter on, an empty string is a value that has to be explained.
// The reason must not outlive its hook either — it describes the hook being held.
func TestExitReasonIsOmittedWhenTheHookHasNone(t *testing.T) {
	debuglog.Init()
	for _, tc := range []struct {
		name  string
		hooks []HookStatus
	}{
		{"never had one", []HookStatus{{Status: "dead", UpdatedAt: time.Now()}}},
		{"cleared by a later hook", []HookStatus{
			{Status: "dead", Reason: "logout", UpdatedAt: time.Now().Add(-time.Minute)},
			{Status: "running", UpdatedAt: time.Now().Add(-time.Second)},
			{Status: "dead", UpdatedAt: time.Now()},
		}},
	} {
		sent := recordErroredProps(t)
		s := &Session{ID: "ended", Agent: agent.OpenCode, Status: StatusRunning, paneCapturer: &mockPane{}}
		for i := range tc.hooks {
			s.UpdateHookStatus(&tc.hooks[i], false)
		}
		s.UpdateStatus()

		if len(*sent) == 0 {
			t.Fatalf("%s: no session_errored event", tc.name)
		}
		last := (*sent)[len(*sent)-1]
		if _, ok := last["exit_reason"]; ok {
			t.Errorf("%s: exit_reason = %v, want the property absent", tc.name, last["exit_reason"])
		}
	}
}

// A death tmux can still describe carries how the process went: exit_code tells a
// clean `/exit` from a crash, and exit_signal names the killer (9 = SIGKILL, the
// machine running out of memory). Both liveness branches read the same exitProps,
// and a tmux that can't answer adds nothing rather than a blank.
func TestSessionErroredCarriesThePanesExitCode(t *testing.T) {
	debuglog.Init()
	t.Setenv("HOME", t.TempDir()) // a death also writes a crash dump
	for _, tc := range []struct {
		name string
		pane *mockPane
		want map[string]string
	}{
		{"killed", &mockPane{dead: true, exitStatus: "137", exitSignal: "9"}, map[string]string{"exit_code": "137", "exit_signal": "9"}},
		{"tmux can't say", &mockPane{dead: true}, nil},
	} {
		sent := recordErroredProps(t)
		s := &Session{ID: "dead", Status: StatusRunning, paneCapturer: tc.pane}
		s.UpdateStatus()

		if len(*sent) != 1 {
			t.Fatalf("%s: got %d session_errored events, want 1", tc.name, len(*sent))
		}
		for _, k := range []string{"exit_code", "exit_signal"} {
			got, ok := (*sent)[0][k]
			if want, wanted := tc.want[k], tc.want[k] != ""; wanted != ok || (ok && got != want) {
				t.Errorf("%s: %s = %v (present %v), want %q (present %v)", tc.name, k, got, ok, want, wanted)
			}
		}
	}
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
