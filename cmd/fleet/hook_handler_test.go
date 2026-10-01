package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/hooks"
)

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

func TestNormalizeCopilotEvent(t *testing.T) {
	cases := []struct {
		event, notifType string
		wantEvent        string
		wantStatus       string
	}{
		{"userPromptSubmitted", "", "UserPromptSubmit", "running"},
		{"postToolUse", "", "PostToolUse", "running"},
		{"postToolUseFailure", "", "PostToolUse", "running"},
		{"notification", "permission_prompt", "Notification", "waiting"},
		{"notification", "elicitation_dialog", "Notification", "waiting"},
		{"notification", "something_new", "Notification", ""},
		{"agentStop", "", "Stop", "finished"},
		// /clear and /exit both send this, so it is never "dead" here.
		{"sessionEnd", "", "SessionEnd", "finished"},
		{"subagentStop", "", "subagentStop", ""},
		{"errorOccurred", "", "errorOccurred", ""},
		{"permissionRequest", "", "permissionRequest", ""},
	}
	// Only SessionEnd may carry a reason: a reason is what releases the owner.
	p := hookPayload{Reason: "timeout"}
	normalizeCopilotEvent("postToolUseFailure", &p)
	if p.Reason != "" {
		t.Errorf("postToolUseFailure kept reason %q", p.Reason)
	}
	for _, c := range cases {
		p := hookPayload{CopilotSessionID: "sid", NotificationType: c.notifType}
		ev, st := normalizeCopilotEvent(c.event, &p)
		if ev != c.wantEvent || st != c.wantStatus {
			t.Errorf("%s/%s: got (%q,%q), want (%q,%q)", c.event, c.notifType, ev, st, c.wantEvent, c.wantStatus)
		}
		if p.SessionID != "sid" {
			t.Errorf("%s: session id not carried over from sessionId", c.event)
		}
	}
}

// The reason is what marks a Copilot SessionEnd (it releases the owner), and it
// reaches analytics as exit_reason, so it must be a non-empty enum.
func TestCopilotSessionEndReasonIsAnEnum(t *testing.T) {
	for in, want := range map[string]string{
		"user_exit":             "user_exit",
		"":                      "other",
		"Exit: /Users/me/x":     "other",
		strings.Repeat("a", 40): "other",
	} {
		p := hookPayload{Reason: in}
		normalizeCopilotEvent("sessionEnd", &p)
		if p.Reason != want {
			t.Errorf("reason %q → %q, want %q", in, p.Reason, want)
		}
	}
}

func TestCopilotHookArgs(t *testing.T) {
	ev, ok := copilotHookArgs([]string{"--fleet-hook", "--agent", "copilot", "--event", "agentStop"})
	if !ok || ev != "agentStop" {
		t.Fatalf("got (%q,%v)", ev, ok)
	}
	if _, ok := copilotHookArgs([]string{"--fleet-hook"}); ok {
		t.Fatal("a Claude invocation read as Copilot")
	}
}

// End to end through the real handler: argv carries the event, the camelCase
// payload carries the id and prompt, and the status file comes out Claude-shaped.
// copilotConversation makes session-state/<id>/, which Copilot creates for a
// real conversation (and never for a subagent) before its first hook.
func copilotConversation(t *testing.T, copilotHome, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(copilotHome, "session-state", id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runCopilotHook(t *testing.T, event, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(payload)
	_ = w.Close()
	oldStdin, oldArgs := os.Stdin, os.Args
	os.Stdin, os.Args = r, []string{"fleet", "hook-handler", "--fleet-hook", "--agent", "copilot", "--event", event}
	defer func() { os.Stdin, os.Args = oldStdin, oldArgs }()
	handleHookHandler()
}

func TestHookHandlerCopilotWritesStatusFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FLEET_INSTANCE_ID", "inst1")
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	copilotConversation(t, filepath.Join(home, ".copilot"), "abc")

	run := func(event, payload string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(payload)
		_ = w.Close()
		oldStdin, oldArgs := os.Stdin, os.Args
		os.Stdin, os.Args = r, []string{"fleet", "hook-handler", "--fleet-hook", "--agent", "copilot", "--event", event}
		defer func() { os.Stdin, os.Args = oldStdin, oldArgs }()
		handleHookHandler()
	}

	run("userPromptSubmitted", `{"sessionId":"abc","timestamp":1,"cwd":"/x","prompt":"fix the bug"}`)
	sf, err := hooks.ReadStatusFile(filepath.Join(hooks.GetHooksDir(), "inst1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if sf.Status != "running" || sf.SessionID != "abc" || sf.UserPrompt != "fix the bug" || sf.PromptCount != 1 {
		t.Fatalf("status file = %+v", sf)
	}

	// A tool call while already running must not rewrite the file: Copilot
	// fires one per tool call and blocks on each.
	statusPath := filepath.Join(hooks.GetHooksDir(), "inst1.json")
	before, _ := os.Stat(statusPath)
	run("postToolUse", `{"sessionId":"abc","timestamp":2,"cwd":"/x","reason":"stray"}`)
	if after, _ := os.Stat(statusPath); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a repeated postToolUse rewrote the status file")
	}

	// Approving a permission fires no hook; the tool finishing is the first
	// sign, so postToolUse after waiting must write running.
	run("notification", `{"sessionId":"abc","timestamp":2,"cwd":"/x","notification_type":"permission_prompt"}`)
	if sf, _ = hooks.ReadStatusFile(statusPath); sf.Status != "waiting" {
		t.Fatalf("after permission notification: status = %q, want waiting", sf.Status)
	}
	run("postToolUse", `{"sessionId":"abc","timestamp":3,"cwd":"/x"}`)
	if sf, _ = hooks.ReadStatusFile(statusPath); sf.Status != "running" {
		t.Fatalf("after postToolUse from waiting: status = %q, want running", sf.Status)
	}

	run("sessionEnd", `{"sessionId":"abc","timestamp":2,"cwd":"/x","reason":"user_exit"}`)
	sf, _ = hooks.ReadStatusFile(filepath.Join(hooks.GetHooksDir(), "inst1.json"))
	if sf.Status != "finished" || sf.Reason != "user_exit" || sf.PromptCount != 1 {
		t.Fatalf("after sessionEnd: %+v", sf)
	}
}

// Copilot's hooks fire for every Copilot session on the machine; one that isn't
// fleet's must leave no trace. Asserted on stdin, not on files: debuglog.Init
// is once-per-process, so an earlier test has already created its log and a
// file check would pass with the guard removed.
func TestHookHandlerCopilotWithoutInstanceIsSilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FLEET_INSTANCE_ID", "")
	r, w, _ := os.Pipe()
	_, _ = w.WriteString(`{"sessionId":"abc","prompt":"x"}`)
	_ = w.Close()
	oldStdin, oldArgs := os.Stdin, os.Args
	os.Stdin, os.Args = r, []string{"fleet", "hook-handler", "--fleet-hook", "--agent", "copilot", "--event", "userPromptSubmitted"}
	defer func() { os.Stdin, os.Args = oldStdin, oldArgs }()
	handleHookHandler()
	if rest, _ := io.ReadAll(r); len(rest) == 0 {
		t.Fatal("handler read the payload of a session fleet doesn't own")
	}
}

// A subagent fires userPromptSubmitted, tool and stop hooks under its own id.
// They must not reach the status file: its prompt would replace the user's
// (auto-naming) and its id would read as the conversation being typed in.
func TestHookHandlerDropsCopilotSubagentHooks(t *testing.T) {
	home := t.TempDir()
	copilotHome := filepath.Join(home, ".copilot")
	t.Setenv("HOME", home)
	t.Setenv("FLEET_INSTANCE_ID", "inst1")
	t.Setenv("COPILOT_HOME", copilotHome)
	copilotConversation(t, copilotHome, "main")

	runCopilotHook(t, "userPromptSubmitted", `{"sessionId":"main","prompt":"fix the bug"}`)
	runCopilotHook(t, "userPromptSubmitted", `{"sessionId":"sub","prompt":"In the current working directory only"}`)
	runCopilotHook(t, "agentStop", `{"sessionId":"sub","stopReason":"end_turn"}`)

	sf, err := hooks.ReadStatusFile(filepath.Join(hooks.GetHooksDir(), "inst1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if sf.SessionID != "main" || sf.UserPrompt != "fix the bug" || sf.PromptCount != 1 || sf.PromptSessionID != "main" {
		t.Fatalf("a subagent hook reached the status file: %+v", sf)
	}
}

// The conversation the user last typed into is kept across later events, so a
// session can follow /new or /clear even when fleet reads the file after the
// turn finished.
func TestHookHandlerKeepsPromptSessionAcrossEvents(t *testing.T) {
	home := t.TempDir()
	copilotHome := filepath.Join(home, ".copilot")
	t.Setenv("HOME", home)
	t.Setenv("FLEET_INSTANCE_ID", "inst1")
	t.Setenv("COPILOT_HOME", copilotHome)
	copilotConversation(t, copilotHome, "new")

	runCopilotHook(t, "userPromptSubmitted", `{"sessionId":"new","prompt":"start over"}`)
	runCopilotHook(t, "agentStop", `{"sessionId":"new","stopReason":"end_turn"}`)

	sf, _ := hooks.ReadStatusFile(filepath.Join(hooks.GetHooksDir(), "inst1.json"))
	if sf.Status != "finished" || sf.PromptSessionID != "new" {
		t.Fatalf("status file = %+v, want finished with prompt_session_id kept", sf)
	}
}
