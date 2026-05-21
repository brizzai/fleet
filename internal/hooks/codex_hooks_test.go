package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectCodexHooks(t *testing.T) {
	dir := t.TempDir()

	// First install: should write and report changed.
	changed, err := InjectCodexHooks(dir)
	if err != nil {
		t.Fatalf("InjectCodexHooks: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on first install")
	}

	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse hooks.json: %v\n%s", err, data)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Stop"} {
		matchers, ok := root.Hooks[event]
		if !ok || len(matchers) == 0 || len(matchers[0].Hooks) == 0 {
			t.Fatalf("event %q missing fleet hook", event)
		}
		h := matchers[0].Hooks[0]
		if h.Type != "command" || !strings.Contains(h.Command, "hook-handler") {
			t.Errorf("event %q: unexpected hook entry %+v", event, h)
		}
	}
	// Note: idempotency on re-install relies on the fleet-hook marker
	// ("fleet hook-handler") matching the launch command, which requires the
	// binary to be named "fleet" — true in production but not under `go test`
	// (binary is *.test), so we don't assert no-op re-install here.
}

func TestInjectCodexHooksPreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing user hook on an event we don't manage + on one we do.
	seed := `{"hooks":{"PreToolUse":[{"matcher":"^Bash$","hooks":[{"type":"command","command":"/usr/bin/mine"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectCodexHooks(dir); err != nil {
		t.Fatalf("InjectCodexHooks: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if !strings.Contains(string(data), "/usr/bin/mine") {
		t.Errorf("user hook was clobbered:\n%s", data)
	}
	if !strings.Contains(string(data), "PreToolUse") {
		t.Errorf("user event removed:\n%s", data)
	}
	if !strings.Contains(string(data), "SessionStart") {
		t.Errorf("fleet event not added:\n%s", data)
	}
}

func TestEnsureCodexDirTrust(t *testing.T) {
	dir := t.TempDir()
	proj := "/Users/me/code/proj"

	if err := EnsureCodexDirTrust(dir, proj); err != nil {
		t.Fatalf("EnsureCodexDirTrust: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	want := `[projects."/Users/me/code/proj"]`
	if !strings.Contains(string(data), want) {
		t.Errorf("missing trust table %q:\n%s", want, data)
	}
	if !strings.Contains(string(data), `trust_level = "trusted"`) {
		t.Errorf("missing trust_level:\n%s", data)
	}

	// Idempotent: a second call must not duplicate the table.
	if err := EnsureCodexDirTrust(dir, proj); err != nil {
		t.Fatalf("EnsureCodexDirTrust (2nd): %v", err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Count(string(data2), want) != 1 {
		t.Errorf("trust table duplicated:\n%s", data2)
	}
}

func TestEnsureCodexDirTrustRespectsExisting(t *testing.T) {
	dir := t.TempDir()
	proj := "/Users/me/code/proj"
	// User already has an entry (even a non-trusted one) — must not be touched.
	existing := "model = \"gpt-5.5\"\n\n[projects.\"/Users/me/code/proj\"]\ntrust_level = \"untrusted\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCodexDirTrust(dir, proj); err != nil {
		t.Fatalf("EnsureCodexDirTrust: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(data), `trust_level = "trusted"`) {
		t.Errorf("overwrote user's existing trust setting:\n%s", data)
	}
}
