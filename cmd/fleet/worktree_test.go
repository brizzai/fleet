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
