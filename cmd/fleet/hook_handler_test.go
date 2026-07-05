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
