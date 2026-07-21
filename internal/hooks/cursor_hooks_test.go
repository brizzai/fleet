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
	// Pre-existing user hook on an event we don't manage + on one we do.
	seed := `{"version":1,"hooks":{"afterFileEdit":[{"command":".cursor/hooks/format.sh"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectCursorHooks(dir); err != nil {
		t.Fatalf("InjectCursorHooks: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if !strings.Contains(string(data), ".cursor/hooks/format.sh") {
		t.Errorf("user hook was clobbered:\n%s", data)
	}
	if !strings.Contains(string(data), "afterFileEdit") {
		t.Errorf("user event removed:\n%s", data)
	}
	if !strings.Contains(string(data), "sessionStart") {
		t.Errorf("fleet event not added:\n%s", data)
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
	data, _ := os.ReadFile(path)
	if string(data) != seed {
		t.Errorf("hooks.json was modified despite the refusal:\nwant %s\ngot  %s", seed, data)
	}
}
