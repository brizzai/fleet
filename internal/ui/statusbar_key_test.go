package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

func TestStatusBarApplicable(t *testing.T) {
	tests := []struct {
		name          string
		st            session.Status
		exists, known bool
		want          bool
	}{
		{"live session", session.StatusRunning, true, true, true},
		{"suspended: tmux killed on purpose", session.StatusSuspended, false, true, false},
		{"suspended even if cache says alive", session.StatusSuspended, true, true, false},
		{"errored with tmux gone", session.StatusError, false, true, false},
		{"errored with a dead pane still has chrome", session.StatusError, true, true, true},
		{"cache unknown: try", session.StatusFinished, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusBarApplicable(tt.st, tt.exists, tt.known); got != tt.want {
				t.Errorf("statusBarApplicable = %v, want %v", got, tt.want)
			}
		})
	}
}

// The key must not move when only the session's status moves (no option
// renders it, and each re-apply redraws the attached client's chrome), but it
// must move for a new pane process (a restart paints a plain bar first) and
// for any option that is rendered.
func TestStatusBarKey(t *testing.T) {
	opts := tmux.StatusBarOpts{DisplayName: "fix", Branch: "main", PRSummary: "#1"}
	base := statusBarKey("theme", 100, false, opts)

	if statusBarKey("theme", 100, false, opts) != base {
		t.Fatal("statusBarKey is not deterministic")
	}
	if statusBarKey("theme", 100, true, opts) == base {
		t.Error("leaving starting must change the key, or a themed apply that lost the race to Start's plain bar is never repaired")
	}
	if statusBarKey("theme", 101, false, opts) == base {
		t.Error("a new pane process (restart) must change the key, or the restarted session keeps an unthemed bar")
	}
	if statusBarKey("other", 100, false, opts) == base {
		t.Error("a theme change must change the key")
	}
	changed := opts
	changed.PRSummary = "#2"
	if statusBarKey("theme", 100, false, changed) == base {
		t.Error("a rendered option change must change the key")
	}
}
