package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseWorktreeArgs(t *testing.T) {
	t.Run("branch only", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"fix-login"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.branch != "fix-login" {
			t.Errorf("branch = %q, want fix-login", o.branch)
		}
		// Base and agent stay empty so the caller can fill in the repo's default
		// branch and the configured default agent — parsing can't see either.
		if o.base != "" || o.agentName != "" || o.repoPath != "" {
			t.Errorf("expected unset defaults, got base=%q agent=%q path=%q", o.base, o.agentName, o.repoPath)
		}
		if o.noSession {
			t.Error("noSession should default to false")
		}
	})

	t.Run("all flags", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"--base", "develop", "--path", "/tmp/repo", "--agent", "codex", "feature/x"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.branch != "feature/x" || o.base != "develop" || o.repoPath != "/tmp/repo" || o.agentName != "codex" {
			t.Errorf("got %+v", o)
		}
	})

	t.Run("account flag", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"--account", "work@example.com", "fix-login"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.account != "work@example.com" {
			t.Errorf("account = %q, want work@example.com", o.account)
		}
	})

	// --account is a claude.ai credential; the other agents never read it, so
	// accepting the combination would silently do nothing.
	t.Run("account with non-claude agent", func(t *testing.T) {
		_, err := parseWorktreeArgs([]string{"--account", "work@example.com", "--agent", "codex", "fix-login"})
		if err == nil || !strings.Contains(err.Error(), "only applies to claude") {
			t.Errorf("err = %v, want a claude-only rejection", err)
		}
	})

	t.Run("account with no-session", func(t *testing.T) {
		_, err := parseWorktreeArgs([]string{"--account", "work@example.com", "--no-session", "fix-login"})
		if err == nil || !strings.Contains(err.Error(), "no effect") {
			t.Errorf("err = %v, want a no-effect rejection", err)
		}
	})

	// `fleet worktree my-branch --no-session` is the order most people type, and
	// a plain flag.Parse would reject it — it stops at the first positional.
	t.Run("flags after branch", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"fix-login", "--base", "develop", "--no-session"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.branch != "fix-login" || o.base != "develop" || !o.noSession {
			t.Errorf("got %+v", o)
		}
	})

	t.Run("flags on both sides of branch", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"--base", "develop", "fix-login", "--agent", "codex"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.branch != "fix-login" || o.base != "develop" || o.agentName != "codex" {
			t.Errorf("got %+v", o)
		}
	})

	t.Run("two positionals rejected", func(t *testing.T) {
		_, err := parseWorktreeArgs([]string{"fix-login", "extra"})
		if err == nil {
			t.Fatal("expected an error for a second positional argument")
		}
	})

	t.Run("missing branch", func(t *testing.T) {
		_, err := parseWorktreeArgs(nil)
		if !errors.Is(err, errMissingBranch) {
			t.Fatalf("err = %v, want errMissingBranch", err)
		}
	})

	t.Run("invalid branch name", func(t *testing.T) {
		// A leading '-' is unreachable here — the flag package claims it before
		// ValidateBranchName ever sees it — so these are the cases that matter.
		for _, branch := range []string{"has..dots", "trailing/", "@", "with@{brace"} {
			if _, err := parseWorktreeArgs([]string{branch}); err == nil {
				t.Errorf("branch %q: expected a validation error", branch)
			}
		}
	})

	t.Run("unknown agent is rejected", func(t *testing.T) {
		// agent.Parse falls back to Claude for anything it doesn't recognize, so
		// without this check a typo would silently launch the wrong agent.
		_, err := parseWorktreeArgs([]string{"--agent", "codek", "fix-login"})
		if err == nil {
			t.Fatal("expected an error for an unknown agent")
		}
		if !strings.Contains(err.Error(), "codek") {
			t.Errorf("error should name the bad value, got %q", err)
		}
	})

	t.Run("known agents accepted", func(t *testing.T) {
		for _, name := range []string{"claude", "codex", "opencode"} {
			if _, err := parseWorktreeArgs([]string{"--agent", name, "fix-login"}); err != nil {
				t.Errorf("agent %q: unexpected error %v", name, err)
			}
		}
	})

	t.Run("agent conflicts with no-session", func(t *testing.T) {
		_, err := parseWorktreeArgs([]string{"--agent", "codex", "--no-session", "fix-login"})
		if err == nil {
			t.Fatal("expected --agent + --no-session to be rejected")
		}
	})

	t.Run("prompt long and short forms", func(t *testing.T) {
		for _, flag := range []string{"--prompt", "-p"} {
			o, err := parseWorktreeArgs([]string{"fix-login", flag, "fix the flaky test"})
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", flag, err)
			}
			if o.prompt != "fix the flaky test" {
				t.Errorf("%s: prompt = %q", flag, o.prompt)
			}
		}
	})

	t.Run("prompt keeps stdin marker for the caller", func(t *testing.T) {
		// Parsing stays pure — "-" is resolved by runWorktree, which owns stdin.
		o, err := parseWorktreeArgs([]string{"fix-login", "-p", "-"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.prompt != "-" {
			t.Errorf("prompt = %q, want -", o.prompt)
		}
	})

	t.Run("empty prompt is rejected", func(t *testing.T) {
		// `-p "$(gh issue view 999)"` on a missing issue expands to nothing.
		// Starting a promptless session there looks like the flag is broken.
		for _, empty := range []string{"", "   "} {
			if _, err := parseWorktreeArgs([]string{"fix-login", "-p", empty}); err == nil {
				t.Errorf("prompt %q: expected an error", empty)
			}
		}
	})

	t.Run("unset prompt is not an error", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"fix-login"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.prompt != "" {
			t.Errorf("prompt = %q, want empty", o.prompt)
		}
	})

	t.Run("prompt conflicts with no-session", func(t *testing.T) {
		_, err := parseWorktreeArgs([]string{"fix-login", "-p", "do it", "--no-session"})
		if err == nil {
			t.Fatal("expected -prompt + -no-session to be rejected")
		}
	})

	t.Run("no-session alone", func(t *testing.T) {
		o, err := parseWorktreeArgs([]string{"--no-session", "fix-login"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !o.noSession {
			t.Error("noSession should be true")
		}
	})
}

func TestParseWorktreeArgsTicket(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string // substring; "" means it must parse
		check   func(*testing.T, worktreeOpts)
	}{
		{
			name: "ticket alone names the branch later",
			args: []string{"-ticket", "BRZ-3182"},
			check: func(t *testing.T, o worktreeOpts) {
				if o.ticket != "BRZ-3182" || o.branch != "" {
					t.Errorf("ticket=%q branch=%q", o.ticket, o.branch)
				}
			},
		},
		{
			name:  "ticket is upper-cased",
			args:  []string{"-t", "brz-3182"},
			check: func(t *testing.T, o worktreeOpts) { mustEqual(t, o.ticket, "BRZ-3182") },
		},
		{
			name:  "explicit branch wins",
			args:  []string{"my-branch", "-ticket", "BRZ-1"},
			check: func(t *testing.T, o worktreeOpts) { mustEqual(t, o.branch, "my-branch") },
		},
		{
			// -ticket "$(lookup)" that produced nothing must not silently
			// degrade into an ordinary worktree — same rule as -p ''.
			name:    "explicitly empty ticket is rejected",
			args:    []string{"-ticket", ""},
			wantErr: "-ticket was empty",
		},
		{
			name:    "non-identifier is rejected before anything is created",
			args:    []string{"-ticket", "not-a-ticket"},
			wantErr: "not a ticket identifier",
		},
		{
			// They set the same field and say opposite things.
			name:    "ticket and prompt conflict",
			args:    []string{"-ticket", "BRZ-1", "-p", "do the thing"},
			wantErr: "pick one",
		},
		{
			// Unlike -prompt: a git-excluded ticket dir is useful without a session.
			name:  "ticket with no-session is allowed",
			args:  []string{"-ticket", "BRZ-1", "-no-session"},
			check: func(t *testing.T, o worktreeOpts) { mustEqual(t, o.ticket, "BRZ-1") },
		},
		{
			name:    "no-ticket-start alone is rejected",
			args:    []string{"branch", "-no-ticket-start"},
			wantErr: "has no effect without -ticket",
		},
		{
			// A positional BETWEEN two flags — the shape the peeling loop in
			// worktree.go exists for. Sharing args with the case above made this
			// one test nothing its neighbour did not.
			name: "flags parse on either side",
			args: []string{"-ticket", "BRZ-1", "my-branch", "-no-session"},
			check: func(t *testing.T, o worktreeOpts) {
				mustEqual(t, o.ticket, "BRZ-1")
				mustEqual(t, o.branch, "my-branch")
				if !o.noSession {
					t.Error("-no-session after the positional was dropped")
				}
			},
		},
		{
			name:    "no branch and no ticket still errors",
			args:    []string{},
			wantErr: "missing branch name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := parseWorktreeArgs(c.args)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.check != nil {
				c.check(t, o)
			}
		})
	}
}

func mustEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
