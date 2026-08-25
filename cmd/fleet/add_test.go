package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestParseAddArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    addOpts
		wantErr string
	}{
		{
			name:    "bare add stays a usage error",
			args:    nil,
			wantErr: "nothing to do",
		},
		{
			name: "path only",
			args: []string{"~/code/api"},
			want: addOpts{path: "~/code/api"},
		},
		{
			name: "path omitted when a flag is given",
			args: []string{"-p", "fix the login bug"},
			want: addOpts{prompt: "fix the login bug"},
		},
		{
			// The flag package stops at the first positional, so without the
			// peel loop this is the order that would break.
			name: "flags after the path",
			args: []string{".", "--agent", "codex", "-p", "do it"},
			want: addOpts{path: ".", agentName: "codex", prompt: "do it"},
		},
		{
			name: "flags before the path",
			args: []string{"--agent", "codex", ".", "-p", "do it"},
			want: addOpts{path: ".", agentName: "codex", prompt: "do it"},
		},
		{
			name: "prompt is trimmed",
			args: []string{".", "-p", "  do it\n"},
			want: addOpts{path: ".", prompt: "do it"},
		},
		{
			// `-p -` must reach runAdd unresolved: reading stdin is I/O and
			// parsing stays pure.
			name: "stdin sentinel survives parsing",
			args: []string{".", "-p", "-"},
			want: addOpts{path: ".", prompt: "-"},
		},
		{
			// `-p "$(gh issue view 999)"` on a missing issue expands to nothing;
			// silently starting a promptless session reads as a broken flag.
			name:    "explicitly empty prompt is rejected",
			args:    []string{".", "-p", ""},
			wantErr: "-prompt was empty",
		},
		{
			name:    "whitespace-only prompt is rejected",
			args:    []string{".", "-p", "   "},
			wantErr: "-prompt was empty",
		},
		{
			// agent.Parse falls back to Claude, so a typo would silently launch
			// the wrong agent.
			name:    "unknown agent is rejected",
			args:    []string{".", "--agent", "cluade"},
			wantErr: `unknown agent "cluade"`,
		},
		{
			name: "known agents are accepted",
			args: []string{".", "--agent", "opencode"},
			want: addOpts{path: ".", agentName: "opencode"},
		},
		{
			// The account is a claude.ai credential the other agents never read.
			name:    "account with a non-claude agent is rejected",
			args:    []string{".", "--agent", "codex", "--account", "a@b.com"},
			wantErr: "--account only applies to claude sessions",
		},
		{
			name: "account with claude is accepted",
			args: []string{".", "--agent", "claude", "--account", "a@b.com"},
			want: addOpts{path: ".", agentName: "claude", account: "a@b.com"},
		},
		{
			name: "model and effort",
			args: []string{".", "--model", "opus", "--effort", "xhigh"},
			want: addOpts{path: ".", model: "opus", effort: "xhigh"},
		},
		{
			name: "provider-qualified model",
			args: []string{".", "--model", "anthropic/claude-sonnet-5"},
			want: addOpts{path: ".", model: "anthropic/claude-sonnet-5"},
		},
		{
			// The value is embedded in a command string that gets typed into
			// the pane's shell — see agent.ValidateLaunchValue.
			name:    "model carrying a shell metacharacter is rejected",
			args:    []string{".", "--model", "opus; rm -rf ~"},
			wantErr: `invalid --model "opus; rm -rf ~"`,
		},
		{
			name:    "effort carrying a command substitution is rejected",
			args:    []string{".", "--effort", "$(whoami)"},
			wantErr: `invalid --effort "$(whoami)"`,
		},
		{
			// `fleet add "$REPO"` with REPO unset must not fall through to cwd —
			// that is the failed-substitution case, not an omitted path.
			name:    "explicitly empty path is rejected",
			args:    []string{""},
			wantErr: "path was empty",
		},
		{
			name:    "whitespace-only path is rejected",
			args:    []string{"   ", "-p", "do it"},
			wantErr: "path was empty",
		},
		{
			// OpenCode's --variant lives on `run`, not the default command fleet
			// launches, and its root parser is strict — so the flag is refused
			// rather than silently dropped.
			name:    "effort with opencode is rejected",
			args:    []string{".", "--agent", "opencode", "--effort", "high"},
			wantErr: "--effort has no effect on opencode sessions",
		},
		{
			name: "model with opencode is fine",
			args: []string{".", "--agent", "opencode", "--model", "anthropic/claude-sonnet-5"},
			want: addOpts{path: ".", agentName: "opencode", model: "anthropic/claude-sonnet-5"},
		},
		{
			name: "effort with codex is fine",
			args: []string{".", "--agent", "codex", "--effort", "high"},
			want: addOpts{path: ".", agentName: "codex", effort: "high"},
		},
		{
			name:    "two positionals",
			args:    []string{"a", "b"},
			wantErr: `unexpected argument "b"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAddArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseAddArgs(%q) = %+v, want error %q", tt.args, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseAddArgs(%q) error = %q, want it to contain %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAddArgs(%q) unexpected error: %v", tt.args, err)
			}
			if got != tt.want {
				t.Errorf("parseAddArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// `-h` is a request, not a failure: runAdd prints usage to stdout and exits 0.
func TestParseAddArgsHelp(t *testing.T) {
	if _, err := parseAddArgs([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseAddArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

// runAdd's error branch calls err.Error() while deciding whether to print the
// flag list, so the sentinel has to carry a message rather than being compared
// only by identity.
func TestMissingAddArgsSentinelHasAMessage(t *testing.T) {
	if errMissingAddArgs.Error() == "" {
		t.Fatal("errMissingAddArgs must carry a message")
	}
}
