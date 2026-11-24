package session

import (
	"errors"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

func TestPaneHasDraft(t *testing.T) {
	debuglog.Init()
	for _, tc := range []struct {
		name string
		pane string
		want bool
	}{
		{"typed text", inputPane("\x1b[39m❯ hello draft text"), true},
		{"multi-line typed text", inputPane("\x1b[39m❯ first line", "  second line"), true},
		{"empty box (just sent / nothing typed)", inputPane("\x1b[39m❯ \x1b[7m \x1b[0m"), false},
		{"Claude's own suggestion is not a draft", inputPane("\x1b[39m❯ \x1b[2mcheck on it when the plan is ready\x1b[0m"), false},
		{"suggestion under the block cursor", inputPane("\x1b[39m❯ \x1b[7mc\x1b[27;2mommit this\x1b[0m"), false},
		{"no input box on screen", "⏺ working...\n  some output", false},
	} {
		if got := paneHasDraft(tc.pane); got != tc.want {
			t.Errorf("%s: paneHasDraft = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The flag follows the pane through UpdateStatus: set while text sits in the box,
// cleared once it is sent (the box is empty again), untouched by a failed capture.
func TestUpdateStatusTracksInputDraft(t *testing.T) {
	debuglog.Init()
	mock := &mockPane{content: inputPane("\x1b[39m❯ half a thought")}
	s := &Session{ID: "d", Agent: agent.Claude, Status: StatusIdle, paneCapturer: mock}

	s.UpdateStatus()
	if !s.HasInputDraft() {
		t.Fatal("typed text in the box: want a draft")
	}

	mock.captureErr = errors.New("tmux busy")
	s.UpdateStatus()
	if !s.HasInputDraft() {
		t.Fatal("a capture we could not read must not clear the marker")
	}

	mock.captureErr = nil
	mock.content = inputPane("\x1b[39m❯ \x1b[7m \x1b[0m") // sent: box is empty again
	s.UpdateStatus()
	if s.HasInputDraft() {
		t.Fatal("prompt sent: the draft marker must go")
	}
}

// Only Claude's box is readable; another agent must never claim a draft.
func TestNonClaudeNeverHasDraft(t *testing.T) {
	debuglog.Init()
	s := &Session{ID: "c", Agent: agent.Codex, Status: StatusIdle,
		paneCapturer: &mockPane{content: inputPane("\x1b[39m❯ hello draft text")}}
	s.refreshInputDraft()
	if s.HasInputDraft() {
		t.Fatal("codex session reported a draft")
	}
}
