package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectCursorHooks(t *testing.T) {
	dir := t.TempDir()

	// First install: should write and report changed.
	changed, err := InjectCursorHooks(dir)
	if err != nil {
		t.Fatalf("InjectCursorHooks: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on first install")
	}

	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var root struct {
		Version int                          `json:"version"`
		Hooks   map[string][]cursorHookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse hooks.json: %v\n%s", err, data)
	}
	if root.Version != 1 {
		t.Errorf("expected version 1, got %d", root.Version)
	}
	for _, event := range cursorHookEvents {
		entries, ok := root.Hooks[event]
		if !ok || len(entries) == 0 {
			t.Fatalf("event %q missing fleet hook", event)
		}
		e := entries[0]
		if e.Type != "command" || !strings.Contains(e.Command, "hook-handler") {
			t.Errorf("event %q: unexpected hook entry %+v", event, e)
		}
	}
	// Re-install is a no-op: isFleetHook also matches the binary-name-independent
	// "--fleet-hook" marker arg, which GetHookCommand always appends regardless
	// of what the running binary is named — so this holds under `go test` too,
	// not just a binary literally named "fleet".
	changed, err = InjectCursorHooks(dir)
	if err != nil {
		t.Fatalf("InjectCursorHooks (2nd): %v", err)
	}
	if changed {
		t.Errorf("expected changed=false on re-install, got true")
	}
}

func TestInjectCursorHooksPreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing user hook on an event we don't manage (afterFileEdit); a
	// managed event with a pre-existing user hook is covered separately by
	// TestInjectCursorHooksPreservesUnknownFieldsAndPromptHooks.
	seed := `{"version":1,"hooks":{"afterFileEdit":[{"command":".cursor/hooks/format.sh"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectCursorHooks(dir); err != nil {
		t.Fatalf("InjectCursorHooks: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".cursor/hooks/format.sh") {
		t.Errorf("user hook was clobbered:\n%s", data)
	}
	if !strings.Contains(string(data), "afterFileEdit") {
		t.Errorf("user event removed:\n%s", data)
	}
	if !strings.Contains(string(data), "stop") {
		t.Errorf("fleet event not added:\n%s", data)
	}
}

func TestInjectCursorHooksPreservesUnknownFieldsAndPromptHooks(t *testing.T) {
	dir := t.TempDir()
	// A managed event with: (1) a user's command-hook entry carrying fields
	// []cursorHookEntry doesn't model (matcher, timeout), and (2) a prompt-hook
	// entry with no "command" field at all. Both must survive with their fields
	// intact (re-marshaling via json.MarshalIndent means whitespace/key order
	// can change, but no data is lost); only fleet's own entry gets appended
	// alongside them.
	seed := `{"version":1,"hooks":{"stop":[` +
		`{"command":"./hooks/notify.sh","matcher":"^Bash$","timeout":30},` +
		`{"type":"prompt","prompt":"Summarize what changed"}` +
		`]}}`
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectCursorHooks(dir); err != nil {
		t.Fatalf("InjectCursorHooks: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var root struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse hooks.json: %v\n%s", err, data)
	}
	entries := root.Hooks["stop"]
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries on stop (2 preserved + fleet's), got %d:\n%s", len(entries), data)
	}

	var userCmdHook, promptHook map[string]any
	if err := json.Unmarshal(entries[0], &userCmdHook); err != nil {
		t.Fatal(err)
	}
	if userCmdHook["matcher"] != "^Bash$" || userCmdHook["timeout"] != float64(30) {
		t.Errorf("user command-hook entry lost unrelated fields: %+v", userCmdHook)
	}
	if err := json.Unmarshal(entries[1], &promptHook); err != nil {
		t.Fatal(err)
	}
	if promptHook["prompt"] != "Summarize what changed" || promptHook["type"] != "prompt" {
		t.Errorf("prompt-hook entry (no \"command\" field) was corrupted: %+v", promptHook)
	}
	if !strings.Contains(string(entries[2]), "hook-handler") {
		t.Errorf("fleet's own entry missing or malformed: %s", entries[2])
	}
}

func TestInjectCursorHooksRefusesMalformedEventEntries(t *testing.T) {
	dir := t.TempDir()
	// One of our managed events holds something that isn't a []cursorHookEntry
	// array (e.g. hand-edited or written by another tool in a different shape).
	// The whole write must be refused rather than silently dropping it.
	seed := `{"version":1,"hooks":{"stop":{"command":"not-an-array"}}}`
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectCursorHooks(dir); err == nil {
		t.Fatal("expected InjectCursorHooks to error on malformed event entries, got nil")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != seed {
		t.Errorf("hooks.json was modified despite the refusal:\nwant %s\ngot  %s", seed, data)
	}
}

func TestInjectCursorHooksHandlesNullHooksSection(t *testing.T) {
	dir := t.TempDir()
	// json.Unmarshal leaves a map nil (not an error) when the JSON value is
	// null, so a hand-edited "hooks": null must not panic on the later
	// events[event] = ... assignment.
	seed := `{"version":1,"hooks":null}`
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := InjectCursorHooks(dir)
	if err != nil {
		t.Fatalf("InjectCursorHooks: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true when populating a null hooks section")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stop") {
		t.Errorf("fleet event not added:\n%s", data)
	}
}
