package control

import (
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
)

func TestExitNote(t *testing.T) {
	failed := "\x1b[31mNo conversation found with session ID: e2d500ca\x1b[0m\n❯ "
	if got := exitNote(PaneShell, failed); got == "" || !ResumeFailed(failed) {
		t.Errorf("a failed resume should be named, got %q", got)
	}
	// A respawned agent is the pane's own process, so its failure leaves a dead
	// pane rather than a shell — same diagnosis.
	if got := exitNote(PaneDead, failed); got == "" {
		t.Error("a failed resume in a dead pane should be named too")
	}
	if got := exitNote(PaneShell, "❯ "); got != "agent exited; pane is at a shell prompt" {
		t.Errorf("plain exit note = %q", got)
	}
	if got := exitNote(PaneDead, "bye"); got != "" {
		t.Errorf("a dead pane with nothing to say gets no note, got %q", got)
	}
}

func TestSinceFor(t *testing.T) {
	prompt := time.Now().Add(-5 * time.Minute)
	hook := time.Now().Add(-time.Minute)
	reply := time.Now().Add(-30 * time.Second)

	running := []session.Turn{{PromptAt: prompt}}
	done := []session.Turn{{PromptAt: prompt, ReplyAt: reply, Done: true}}

	for _, tt := range []struct {
		name   string
		status session.Status
		hook   string
		turns  []session.Turn
		want   time.Time
	}{
		{"a running turn started at its prompt", session.StatusRunning, "running", running, prompt},
		{"running without a transcript falls back to the hook", session.StatusRunning, "running", nil, hook},
		{"waiting since the hook that reported it", session.StatusWaiting, "waiting", done, hook},
		{"finished since the Stop hook", session.StatusFinished, "finished", done, hook},
		{"finished with a stale hook uses the reply", session.StatusIdle, "running", done, reply},
		{"nothing says", session.StatusError, "", nil, time.Time{}},
	} {
		ob := &Observation{Status: tt.status, HookAt: hook, Turns: tt.turns}
		if got := sinceFor(ob, tt.hook); !got.Equal(tt.want) {
			t.Errorf("%s: since = %v, want %v", tt.name, got, tt.want)
		}
	}
}
