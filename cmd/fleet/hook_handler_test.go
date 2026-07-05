package main

import "testing"

func TestMapEventToStatus(t *testing.T) {
	cases := []struct {
		event string
		want  string
	}{
		{"UserPromptSubmit", "running"},
		{"Stop", "finished"},
		{"PreCompact", "running"}, // /compact & auto-compaction: a multi-minute busy phase
		{"PermissionRequest", "waiting"},
		{"SessionStart", "finished"},
		{"SessionEnd", "dead"},
		{"Notification", ""}, // resolved separately by matcher
		{"session.busy", "running"},
		{"session.idle", "finished"},
		{"session.error", "error"},
		{"permission.asked", "waiting"},
		{"permission.replied", "running"},
		{"UnknownEvent", ""},
	}
	for _, c := range cases {
		if got := mapEventToStatus(c.event); got != c.want {
			t.Errorf("mapEventToStatus(%q) = %q, want %q", c.event, got, c.want)
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
