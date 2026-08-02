package main

import "testing"

func TestMapEventToStatus(t *testing.T) {
	cases := []struct {
		event  string
		status string
		want   string
	}{
		{"UserPromptSubmit", "", "running"},
		{"Stop", "", "finished"},
		{"PreCompact", "", "running"}, // /compact & auto-compaction: a multi-minute busy phase
		{"PermissionRequest", "", "waiting"},
		{"SessionStart", "", "finished"},
		{"SessionEnd", "", "dead"},
		{"Notification", "", ""}, // resolved separately by matcher
		{"session.busy", "", "running"},
		{"session.idle", "", "finished"},
		{"session.error", "", "error"},
		{"permission.asked", "", "waiting"},
		{"permission.replied", "", "running"},
		// Cursor CLI events (see internal/hooks/cursor_hooks.go).
		{"beforeSubmitPrompt", "", "running"},
		// Both shell hooks are running, never waiting: afterShellExecution fires
		// on command completion (its payload carries output/duration), so waiting
		// here would pin the row for the whole command. Cursor exposes no
		// approval hook, so fleet has no waiting signal for it at all.
		{"beforeShellExecution", "", "running"},
		{"afterShellExecution", "", "running"},
		// Cursor's stop carries how the turn ended.
		{"stop", "completed", "finished"},
		{"stop", "aborted", "finished"}, // user-initiated cancel: over, not broken
		{"stop", "error", "error"},
		{"stop", "", "finished"},       // absent status: treat as a normal finish
		{"sessionEnd", "", "finished"}, // conversation end, not process death — see mapEventToStatus
		// Cursor's lowerCamelCase "sessionStart" is deliberately unmapped —
		// distinct from Claude/Codex's PascalCase "SessionStart" above, which
		// does map to "finished". See the no-sessionStart-case comment in
		// mapEventToStatus.
		{"sessionStart", "", ""},
		{"UnknownEvent", "", ""},
		// status is only consulted for Cursor's stop; it must not leak into
		// other agents' events that happen to carry one.
		{"Stop", "error", "finished"},
	}
	for _, c := range cases {
		if got := mapEventToStatus(c.event, c.status); got != c.want {
			t.Errorf("mapEventToStatus(%q, %q) = %q, want %q", c.event, c.status, got, c.want)
		}
	}
}

func TestIsPromptSubmit(t *testing.T) {
	cases := []struct {
		event string
		want  bool
	}{
		{"UserPromptSubmit", true},
		{"beforeSubmitPrompt", true},
		{"Stop", false},
		{"stop", false},
		{"sessionStart", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPromptSubmit(c.event); got != c.want {
			t.Errorf("isPromptSubmit(%q) = %v, want %v", c.event, got, c.want)
		}
	}
}

func TestIsCompactSessionStart(t *testing.T) {
	cases := []struct {
		event, source string
		want          bool
	}{
		{"SessionStart", "compact", true},  // the closing bracket we must skip
		{"SessionStart", "startup", false}, // normal boot → finished
		{"SessionStart", "resume", false},
		{"SessionStart", "clear", false}, // /clear → fresh idle → finished
		{"SessionStart", "", false},
		{"Stop", "compact", false},       // only SessionStart is special-cased
		{"PreCompact", "compact", false}, // PreCompact still maps to running
	}
	for _, c := range cases {
		if got := isCompactSessionStart(c.event, c.source); got != c.want {
			t.Errorf("isCompactSessionStart(%q, %q) = %v, want %v", c.event, c.source, got, c.want)
		}
	}
}
