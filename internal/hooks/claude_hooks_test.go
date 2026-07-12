package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsFleetHook(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"marker arg", "/usr/local/bin/fleet hook-handler --fleet-hook", true},
		{"renamed binary with marker arg", "/tmp/go-build/exe/fleet-dev hook-handler --fleet-hook", true},
		{"legacy command without arg", "/usr/local/bin/fleet hook-handler", true},
		{"foreign tool", "/usr/local/bin/other-tool hook-handler", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isFleetHook(c.command); got != c.want {
				t.Errorf("isFleetHook(%q) = %v, want %v", c.command, got, c.want)
			}
		})
	}
}

func TestIsGoRunBinary(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"go run temp binary", "/private/var/folders/k3/T/go-build2026166274/b001/exe/fleet", true},
		{"installed binary", "/Users/dev/.local/bin/fleet", false},
		{"repo build output", "/Users/dev/code/fleet/build/fleet", false},
		{"exe dir outside a go-build tree", "/opt/tools/exe/fleet", false},
		// A distant go-build* ancestor is not a go run temp: only the exact
		// go-build<rand>/b<N>/exe layout is. Walking every ancestor got this wrong.
		{"go-build prefix on a far ancestor", "/Users/dev/go-build-tools/dist/exe/fleet", false},
		{"non-numeric build step dir", "/tmp/go-build123/bee/exe/fleet", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isGoRunBinary(c.path); got != c.want {
				t.Errorf("isGoRunBinary(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestGetHookCommandHasMarker(t *testing.T) {
	if cmd := GetHookCommand(); !strings.Contains(cmd, fleetHookArg) {
		t.Errorf("GetHookCommand() = %q, want it to contain the marker %q", cmd, fleetHookArg)
	}
}

// Claude runs the hook command through a shell, and a dev build's path is the
// repo checkout — which may contain spaces. Unquoted, the shell would split it
// and every hook would fail silently.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/usr/local/bin/fleet", `'/usr/local/bin/fleet'`},
		{"/Users/dev/My Projects/fleet/build/fleet", `'/Users/dev/My Projects/fleet/build/fleet'`},
		{"/tmp/it's/fleet", `'/tmp/it'\''s/fleet'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMergeHookEventMarkerMigration(t *testing.T) {
	current := GetHookCommand()

	t.Run("legacy command (no arg) is upgraded in place, not duplicated", func(t *testing.T) {
		existing := json.RawMessage(`[{"hooks":[{"type":"command","command":"/old/path/fleet hook-handler"}]}]`)
		result := mergeHookEvent(existing, "", true)
		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers[0].Hooks) != 1 {
			t.Fatalf("expected 1 hook (no duplicate), got %d", len(matchers[0].Hooks))
		}
		if matchers[0].Hooks[0].Command != current {
			t.Errorf("command = %q, want upgraded to %q", matchers[0].Hooks[0].Command, current)
		}
	})

	t.Run("renamed binary carrying the marker arg is recognized, not duplicated", func(t *testing.T) {
		existing := json.RawMessage(`[{"hooks":[{"type":"command","command":"/tmp/exe/fleet-dev hook-handler --fleet-hook"}]}]`)
		result := mergeHookEvent(existing, "", true)
		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers[0].Hooks) != 1 {
			t.Fatalf("expected 1 hook (no duplicate), got %d", len(matchers[0].Hooks))
		}
	})
}

func TestMergeHookEvent(t *testing.T) {
	t.Run("nil input creates new matcher", func(t *testing.T) {
		result := mergeHookEvent(nil, "", true)
		if result == nil {
			t.Fatal("expected non-nil result")
		}

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers) != 1 {
			t.Fatalf("expected 1 matcher, got %d", len(matchers))
		}
		if len(matchers[0].Hooks) != 1 {
			t.Fatalf("expected 1 hook, got %d", len(matchers[0].Hooks))
		}
		if matchers[0].Hooks[0].Type != "command" {
			t.Errorf("hook type = %q, want command", matchers[0].Hooks[0].Type)
		}
	})

	t.Run("appends to existing hooks", func(t *testing.T) {
		existing := json.RawMessage(`[{"hooks":[{"type":"command","command":"other-tool"}]}]`)
		result := mergeHookEvent(existing, "", true)

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers) != 1 {
			t.Fatalf("expected 1 matcher, got %d", len(matchers))
		}
		if len(matchers[0].Hooks) != 2 {
			t.Fatalf("expected 2 hooks, got %d", len(matchers[0].Hooks))
		}
	})

	t.Run("updates existing fleet hook", func(t *testing.T) {
		existing := json.RawMessage(`[{"hooks":[{"type":"command","command":"fleet hook-handler"}]}]`)
		result := mergeHookEvent(existing, "", true)

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// Should not duplicate.
		if len(matchers[0].Hooks) != 1 {
			t.Fatalf("expected 1 hook (no duplicate), got %d", len(matchers[0].Hooks))
		}
	})

	t.Run("respects matcher", func(t *testing.T) {
		existing := json.RawMessage(`[{"matcher":"other","hooks":[{"type":"command","command":"other"}]}]`)
		result := mergeHookEvent(existing, "permission_prompt", true)

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers) != 2 {
			t.Fatalf("expected 2 matchers, got %d", len(matchers))
		}
	})
}

func TestRemoveFleetFromEvent(t *testing.T) {
	t.Run("only fleet hooks", func(t *testing.T) {
		raw := json.RawMessage(`[{"hooks":[{"type":"command","command":"fleet hook-handler"}]}]`)
		result, removed := removeFleetFromEvent(raw)
		if !removed {
			t.Error("expected removed=true")
		}
		if result != nil {
			t.Error("expected nil result when all hooks removed")
		}
	})

	t.Run("mixed hooks", func(t *testing.T) {
		raw := json.RawMessage(`[{"hooks":[{"type":"command","command":"other-tool"},{"type":"command","command":"fleet hook-handler"}]}]`)
		result, removed := removeFleetFromEvent(raw)
		if !removed {
			t.Error("expected removed=true")
		}
		if result == nil {
			t.Fatal("expected non-nil result (other hooks remain)")
		}

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(result, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers[0].Hooks) != 1 {
			t.Fatalf("expected 1 remaining hook, got %d", len(matchers[0].Hooks))
		}
		if matchers[0].Hooks[0].Command != "other-tool" {
			t.Errorf("remaining hook = %q, want other-tool", matchers[0].Hooks[0].Command)
		}
	})

	t.Run("no fleet hooks", func(t *testing.T) {
		raw := json.RawMessage(`[{"hooks":[{"type":"command","command":"other-tool"}]}]`)
		_, removed := removeFleetFromEvent(raw)
		if removed {
			t.Error("expected removed=false")
		}
	})
}

func TestWriteAndReadStatusFile(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		dir := t.TempDir()
		sf := &StatusFile{
			Status:      "running",
			SessionID:   "claude-sess-123",
			Event:       "Stop",
			Timestamp:   1700000000,
			UserPrompt:  "fix the bug",
			PromptCount: 2,
		}

		if err := WriteStatusFile(dir, "instance-001", sf); err != nil {
			t.Fatalf("WriteStatusFile failed: %v", err)
		}

		filePath := filepath.Join(dir, "instance-001.json")
		got, err := ReadStatusFile(filePath)
		if err != nil {
			t.Fatalf("ReadStatusFile failed: %v", err)
		}

		if got.Status != sf.Status {
			t.Errorf("Status: got %q, want %q", got.Status, sf.Status)
		}
		if got.SessionID != sf.SessionID {
			t.Errorf("SessionID: got %q, want %q", got.SessionID, sf.SessionID)
		}
		if got.Event != sf.Event {
			t.Errorf("Event: got %q, want %q", got.Event, sf.Event)
		}
		if got.Timestamp != sf.Timestamp {
			t.Errorf("Timestamp: got %d, want %d", got.Timestamp, sf.Timestamp)
		}
		if got.UserPrompt != sf.UserPrompt {
			t.Errorf("UserPrompt: got %q, want %q", got.UserPrompt, sf.UserPrompt)
		}
		if got.PromptCount != sf.PromptCount {
			t.Errorf("PromptCount: got %d, want %d", got.PromptCount, sf.PromptCount)
		}
	})

	t.Run("creates directory if needed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "hooks")
		sf := &StatusFile{Status: "waiting", Event: "PermissionRequest", Timestamp: 1700000001}

		if err := WriteStatusFile(dir, "instance-002", sf); err != nil {
			t.Fatalf("WriteStatusFile failed: %v", err)
		}

		filePath := filepath.Join(dir, "instance-002.json")
		got, err := ReadStatusFile(filePath)
		if err != nil {
			t.Fatalf("ReadStatusFile failed: %v", err)
		}
		if got.Status != "waiting" {
			t.Errorf("Status: got %q, want %q", got.Status, "waiting")
		}
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		dir := t.TempDir()
		sf1 := &StatusFile{Status: "running", Event: "Stop", Timestamp: 1700000000}
		sf2 := &StatusFile{Status: "finished", Event: "SessionEnd", Timestamp: 1700000010}

		if err := WriteStatusFile(dir, "instance-003", sf1); err != nil {
			t.Fatalf("WriteStatusFile (first) failed: %v", err)
		}
		if err := WriteStatusFile(dir, "instance-003", sf2); err != nil {
			t.Fatalf("WriteStatusFile (second) failed: %v", err)
		}

		filePath := filepath.Join(dir, "instance-003.json")
		got, err := ReadStatusFile(filePath)
		if err != nil {
			t.Fatalf("ReadStatusFile failed: %v", err)
		}
		if got.Status != "finished" {
			t.Errorf("Status: got %q, want %q (expected overwrite)", got.Status, "finished")
		}
	})

	t.Run("read missing file returns error", func(t *testing.T) {
		_, err := ReadStatusFile("/nonexistent/path/missing.json")
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("read invalid JSON returns error", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(filePath, []byte("not valid json"), 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		_, err := ReadStatusFile(filePath)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

func TestEventHasFleetHook(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			"has fleet hook",
			`[{"hooks":[{"type":"command","command":"fleet hook-handler"}]}]`,
			true,
		},
		{
			"has fleet hook with path",
			`[{"hooks":[{"type":"command","command":"/usr/local/bin/fleet hook-handler"}]}]`,
			true,
		},
		{
			"no fleet hook",
			`[{"hooks":[{"type":"command","command":"other-tool"}]}]`,
			false,
		},
		{
			"empty hooks",
			`[{"hooks":[]}]`,
			false,
		},
		{
			"invalid JSON",
			`invalid`,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventHasFleetHook(json.RawMessage(tt.raw))
			if got != tt.want {
				t.Errorf("eventHasFleetHook = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEventHasFleetHookWithMatcher(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		matcher string
		want    bool
	}{
		{
			"matching matcher with fleet",
			`[{"matcher":"permission_prompt","hooks":[{"type":"command","command":"fleet hook-handler"}]}]`,
			"permission_prompt",
			true,
		},
		{
			"wrong matcher",
			`[{"matcher":"other","hooks":[{"type":"command","command":"fleet hook-handler"}]}]`,
			"permission_prompt",
			false,
		},
		{
			"correct matcher but no fleet hook",
			`[{"matcher":"permission_prompt","hooks":[{"type":"command","command":"other-tool"}]}]`,
			"permission_prompt",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventHasFleetHookWithMatcher(json.RawMessage(tt.raw), tt.matcher)
			if got != tt.want {
				t.Errorf("eventHasFleetHookWithMatcher = %v, want %v", got, tt.want)
			}
		})
	}
}

// buildHooksMapWithMarker builds a hooks map using literal JSON with the fleet marker.
// This is needed because mergeHookEvent uses GetHookCommand() which resolves to the test binary path.
func buildHooksMapWithMarker() map[string]json.RawMessage {
	fleetHookJSON := `[{"hooks":[{"type":"command","command":"fleet hook-handler","async":true}]}]`
	notificationHook := `[{"matcher":"permission_prompt|elicitation_dialog","hooks":[{"type":"command","command":"fleet hook-handler","async":true}]},{"matcher":"idle_prompt","hooks":[{"type":"command","command":"fleet hook-handler","async":true}]}]`

	hooks := map[string]json.RawMessage{
		"UserPromptSubmit":  json.RawMessage(fleetHookJSON),
		"Stop":              json.RawMessage(fleetHookJSON),
		"PreCompact":        json.RawMessage(fleetHookJSON),
		"PermissionRequest": json.RawMessage(fleetHookJSON),
		"Notification":      json.RawMessage(notificationHook),
		"SessionStart":      json.RawMessage(fleetHookJSON),
		"SessionEnd":        json.RawMessage(fleetHookJSON),
	}
	return hooks
}

func TestHooksAlreadyInstalled(t *testing.T) {
	t.Run("empty hooks map returns false", func(t *testing.T) {
		hooks := make(map[string]json.RawMessage)
		if hooksAlreadyInstalled(hooks) {
			t.Error("expected false for empty hooks")
		}
	})

	t.Run("all events present returns true", func(t *testing.T) {
		hooks := buildHooksMapWithMarker()
		if !hooksAlreadyInstalled(hooks) {
			t.Error("expected true when all hooks are installed")
		}
	})

	t.Run("missing one event returns false", func(t *testing.T) {
		hooks := buildHooksMapWithMarker()
		delete(hooks, "UserPromptSubmit")
		if hooksAlreadyInstalled(hooks) {
			t.Error("expected false when UserPromptSubmit is missing")
		}
	})
}

func TestHasStaleHookEvents(t *testing.T) {
	t.Run("no stale events", func(t *testing.T) {
		hooks := buildHooksMapWithMarker()
		if hasStaleHookEvents(hooks) {
			t.Error("expected false when no stale events exist")
		}
	})

	t.Run("stale event detected", func(t *testing.T) {
		hooks := make(map[string]json.RawMessage)
		// Add a hook for an event we don't subscribe to anymore.
		hooks["ObsoleteEvent"] = json.RawMessage(`[{"hooks":[{"type":"command","command":"fleet hook-handler"}]}]`)
		if !hasStaleHookEvents(hooks) {
			t.Error("expected true when stale event exists")
		}
	})

	t.Run("non-fleet event in unknown key is not stale", func(t *testing.T) {
		hooks := make(map[string]json.RawMessage)
		hooks["SomeOtherEvent"] = json.RawMessage(`[{"hooks":[{"type":"command","command":"other-tool"}]}]`)
		if hasStaleHookEvents(hooks) {
			t.Error("expected false when unknown event has no fleet hooks")
		}
	})
}

func TestCleanStaleHookEvents(t *testing.T) {
	t.Run("removes stale fleet hooks", func(t *testing.T) {
		hooks := make(map[string]json.RawMessage)
		hooks["ObsoleteEvent"] = json.RawMessage(`[{"hooks":[{"type":"command","command":"fleet hook-handler"}]}]`)

		cleanStaleHookEvents(hooks)

		if _, ok := hooks["ObsoleteEvent"]; ok {
			t.Error("expected ObsoleteEvent to be removed")
		}
	})

	t.Run("preserves non-fleet hooks in stale events", func(t *testing.T) {
		hooks := make(map[string]json.RawMessage)
		hooks["ObsoleteEvent"] = json.RawMessage(`[{"hooks":[{"type":"command","command":"other-tool"},{"type":"command","command":"fleet hook-handler"}]}]`)

		cleanStaleHookEvents(hooks)

		raw, ok := hooks["ObsoleteEvent"]
		if !ok {
			t.Fatal("expected ObsoleteEvent to remain (has non-fleet hooks)")
		}

		var matchers []claudeHookMatcher
		if err := json.Unmarshal(raw, &matchers); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Errorf("expected 1 remaining hook, got %d matchers", len(matchers))
		}
		if matchers[0].Hooks[0].Command != "other-tool" {
			t.Errorf("remaining hook: got %q, want %q", matchers[0].Hooks[0].Command, "other-tool")
		}
	})

	t.Run("does not touch active events", func(t *testing.T) {
		hooks := buildHooksMapWithMarker()
		before := len(hooks)
		cleanStaleHookEvents(hooks)
		after := len(hooks)
		if before != after {
			t.Errorf("expected active events to be preserved: before=%d, after=%d", before, after)
		}
	})
}

func TestInjectAndRemoveClaudeHooks(t *testing.T) {
	t.Run("inject into empty dir creates settings with hooks", func(t *testing.T) {
		dir := t.TempDir()
		installed, err := InjectClaudeHooks(dir)
		if err != nil {
			t.Fatalf("InjectClaudeHooks failed: %v", err)
		}
		if !installed {
			t.Error("expected installed=true for fresh install")
		}

		// Verify file was created with hooks section.
		settingsPath := filepath.Join(dir, "settings.json")
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("settings.json not created: %v", err)
		}

		var settings map[string]json.RawMessage
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("parse settings: %v", err)
		}
		if _, ok := settings["hooks"]; !ok {
			t.Error("expected hooks section in settings.json")
		}
	})

	t.Run("remove from nonexistent file", func(t *testing.T) {
		dir := t.TempDir()
		removed, err := RemoveClaudeHooks(dir)
		if err != nil {
			t.Fatalf("RemoveClaudeHooks failed: %v", err)
		}
		if removed {
			t.Error("expected removed=false for nonexistent file")
		}
	})

	t.Run("inject preserves existing settings", func(t *testing.T) {
		dir := t.TempDir()
		settingsPath := filepath.Join(dir, "settings.json")

		// Write existing settings with a custom key.
		existing := map[string]interface{}{
			"apiKey": "sk-test-123",
		}
		data, _ := json.MarshalIndent(existing, "", "  ")
		if err := os.WriteFile(settingsPath, data, 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		_, err := InjectClaudeHooks(dir)
		if err != nil {
			t.Fatalf("InjectClaudeHooks failed: %v", err)
		}

		// Verify existing key is preserved.
		settingsData, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("ReadFile failed: %v", err)
		}

		var settings map[string]json.RawMessage
		if err := json.Unmarshal(settingsData, &settings); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		if _, ok := settings["apiKey"]; !ok {
			t.Error("expected apiKey to be preserved after injection")
		}
		if _, ok := settings["hooks"]; !ok {
			t.Error("expected hooks to be present after injection")
		}
	})

	t.Run("AreHooksInstalled returns false for empty dir", func(t *testing.T) {
		dir := t.TempDir()
		if AreHooksInstalled(dir) {
			t.Error("expected false for empty dir")
		}
	})

	t.Run("AreHooksInstalled returns false for settings without hooks", func(t *testing.T) {
		dir := t.TempDir()
		settingsPath := filepath.Join(dir, "settings.json")
		data := []byte(`{"apiKey": "test"}`)
		if err := os.WriteFile(settingsPath, data, 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
		if AreHooksInstalled(dir) {
			t.Error("expected false for settings without hooks")
		}
	})

	t.Run("AreHooksInstalled returns true for manually installed hooks", func(t *testing.T) {
		dir := t.TempDir()
		settingsPath := filepath.Join(dir, "settings.json")

		hooks := buildHooksMapWithMarker()
		hooksData, _ := json.Marshal(hooks)
		settings := map[string]json.RawMessage{
			"hooks": hooksData,
		}
		data, _ := json.MarshalIndent(settings, "", "  ")
		if err := os.WriteFile(settingsPath, data, 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		if !AreHooksInstalled(dir) {
			t.Error("expected true for settings with all hooks")
		}
	})
}
