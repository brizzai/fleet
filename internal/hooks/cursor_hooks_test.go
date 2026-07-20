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
	// Note: idempotency on re-install relies on the fleet-hook marker
	// ("fleet hook-handler") matching the launch command, which requires the
	// binary to be named "fleet" — true in production but not under `go test`
	// (binary is *.test), so we don't assert no-op re-install here.
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
