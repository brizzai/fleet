package diagnostics

import (
	"strings"
	"testing"
)

// Every dynamic field in the environment block has to reach the scrubber.
//
// Only ClaudeVersion and CodexVersion did, which is the actual defect: someone
// had already decided a version string can carry a path, and everything around
// them was interpolated raw. This is the block that reaches a public issue
// without the reporter being shown it, so a field that skips the scrubber
// publishes whatever it happens to hold.
//
// Table-driven per field rather than one blob, so a failure names which one
// regressed instead of just "something leaked".
func TestEveryEnvironmentFieldIsScrubbed(t *testing.T) {
	const secret = "/Users/someone/clients/acme"

	fields := map[string]func(*Report){
		"Version":         func(r *Report) { r.Version = secret },
		"Arch":            func(r *Report) { r.Arch = secret },
		"KernelVersion":   func(r *Report) { r.KernelVersion = secret },
		"TmuxVersion":     func(r *Report) { r.TmuxVersion = secret },
		"ClaudeVersion":   func(r *Report) { r.ClaudeVersion = secret },
		"CodexVersion":    func(r *Report) { r.CodexVersion = secret },
		"GhVersion":       func(r *Report) { r.GhVersion = secret },
		"TERM":            func(r *Report) { r.TerminalEnv.TERM = secret },
		"TermProgram":     func(r *Report) { r.TerminalEnv.TermProgram = secret },
		"ColorTerm":       func(r *Report) { r.TerminalEnv.ColorTerm = secret },
		"SttySize":        func(r *Report) { r.TerminalEnv.SttySize = secret },
		"Lang":            func(r *Report) { r.TerminalEnv.Lang = secret },
		"LCAll":           func(r *Report) { r.TerminalEnv.LCAll = secret },
		"TmuxDefaultTerm": func(r *Report) { r.TerminalEnv.TmuxDefaultTerm = secret },
		"TmuxMouse":       func(r *Report) { r.TerminalEnv.TmuxMouse = secret },
	}

	// Stands in for the UI's sanitizeForIssue, which this package cannot import.
	scrub := func(s string) string { return strings.ReplaceAll(s, secret, "<scrubbed>") }

	for name, set := range fields {
		t.Run(name, func(t *testing.T) {
			r := &Report{}
			set(r)
			got := r.FormatEnvironmentMarkdown(false, scrub)
			if strings.Contains(got, secret) {
				t.Errorf("%s bypassed the scrubber:\n%s", name, got)
			}
			if !strings.Contains(got, "<scrubbed>") {
				t.Errorf("%s never reached the output at all — the test is asserting nothing:\n%s", name, got)
			}
		})
	}
}

// A nil scrubber still rewrites the home directory, which is what a report going
// somewhere private (a snapshot on disk) wants. Guards against the parameter
// being read as "sanitizing is now optional".
func TestNilScrubberStillRewritesHome(t *testing.T) {
	t.Setenv("HOME", "/Users/testuser")
	r := &Report{TerminalEnv: TerminalEnv{TermProgram: "/Users/testuser/bin/term"}}
	got := r.FormatEnvironmentMarkdown(false, nil)
	if strings.Contains(got, "/Users/testuser") {
		t.Errorf("home path survived a nil scrubber:\n%s", got)
	}
	if !strings.Contains(got, "~/bin/term") {
		t.Errorf("want the home rewritten to ~, got:\n%s", got)
	}
}
