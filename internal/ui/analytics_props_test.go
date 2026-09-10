package ui

import (
	"errors"
	"testing"
)

// The consent dialog promises file paths, repo/branch names and the rest are
// never sent, and Basic users send error_occurred too — so anything that isn't
// fleet's own wording has to come out as "other".
func TestErrorCategoryKeepsOnlyFleetProse(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"failed to start session: tmux: exit status 1", "failed to start session"},
		{"launchpad skipped a repo: secret-repo — no account", "launchpad skipped a repo"},
		{"account not allowed for this repo: Ada isn't in allowed_accounts", "account not allowed for this repo"},
		{"opencode CLI not found: install OpenCode to create sessions", "opencode CLI not found"},
		{"open /Users/ada/.config/fleet/state.db: permission denied", "other"},
		{"secret-repo: no allowed account is logged in", "other"},
		{"feature-login already exists", "other"}, // a branch name inside prose, e.g. shell-provider output
		{"ada@example.com is not allowed", "other"},
		{`couldn't remove worktree "feature-x": busy`, "other"},
		{"reloaded 3 sessions, 1 failed: a, b", "other"},
		{"fatal: invalid reference: feature-x", "other"},
		{"this is fleet prose but far too long to be a category", "other"},
	} {
		if got := errorCategory(errors.New(tc.msg)); got != tc.want {
			t.Errorf("errorCategory(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestEditorNameDropsPathAndFlags(t *testing.T) {
	for spec, want := range map[string]string{
		"code":                            "code",
		"code -n":                         "code",
		"/Users/ada/bin/nvim -u ~/.vimrc": "nvim",
		"":                                "",
	} {
		if got := editorName(spec); got != want {
			t.Errorf("editorName(%q) = %q, want %q", spec, got, want)
		}
	}
}
