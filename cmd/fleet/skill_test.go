package main

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/skill"
)

func names(agents []skill.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	return out
}

func TestResolveAgents(t *testing.T) {
	fallback, _ := skill.Find("claude")

	tests := []struct {
		name    string
		sel     string
		want    []string
		wantErr string
	}{
		{name: "empty uses the verb's default", sel: "", want: []string{"claude"}},
		{name: "all expands to every agent", sel: "all", want: names(skill.Agents())},
		{name: "single name", sel: "codex", want: []string{"codex"}},
		{name: "comma list, spaces tolerated", sel: "codex, cursor", want: []string{"codex", "cursor"}},
		// A typo must fail loudly: silently resolving to nothing would install
		// for no agent and still report success.
		{name: "unknown name errors", sel: "claud", wantErr: `unknown agent "claud"`},
		{name: "empty list errors", sel: ",", wantErr: "no agent names"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgents(tt.sel, []skill.Agent{fallback})
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveAgents(%q) = %v, want error", tt.sel, names(got))
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAgents(%q): %v", tt.sel, err)
			}
			if strings.Join(names(got), ",") != strings.Join(tt.want, ",") {
				t.Errorf("resolveAgents(%q) = %v, want %v", tt.sel, names(got), tt.want)
			}
		})
	}
}

// Every outcome the skill package can return needs a readable line. The default
// branch marks itself with "?", so a new outcome that nobody wrote a line for
// shows up here instead of printing a bare enum value at the user.
func TestSkillOutcomeLineCoversEveryOutcome(t *testing.T) {
	all := []skill.Outcome{
		skill.Written, skill.Unchanged, skill.Installed, skill.Outdated,
		skill.Absent, skill.Removed, skill.Skipped, skill.Failed,
	}
	for _, o := range all {
		marker, label, _ := skillOutcomeLine(skill.Result{Outcome: o, Path: "/tmp/x"})
		if marker == "?" {
			t.Errorf("outcome %q fell through to the default case", o)
		}
		if marker == "" || label == "" {
			t.Errorf("outcome %q rendered marker=%q label=%q", o, marker, label)
		}
	}
}
