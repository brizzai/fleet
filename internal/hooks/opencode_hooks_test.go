package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectOpenCodePlugin(t *testing.T) {
	dir := t.TempDir()

	// First install: writes the plugin and reports changed.
	changed, err := InjectOpenCodePlugin(dir)
	if err != nil {
		t.Fatalf("InjectOpenCodePlugin: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on first install")
	}

	pluginPath := filepath.Join(dir, "plugin", "fleet-status.ts")
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	content := string(data)

	// The plugin must export a hook, forward via the (synchronous) hook-handler,
	// map the OpenCode-native events fleet understands, skip non-fleet sessions,
	// and latch the root session so sub-agents don't drive status.
	for _, want := range []string{
		"export const FleetStatus",
		"spawnSync",
		"hook-handler",
		"--fleet-hook",
		"session.busy",
		"session.idle",
		"session.error",
		// Both families are live and distinct: question.* is the AskUserQuestion
		// prompt, permission.* is a tool-permission prompt. Dropping either is a
		// stuck-at-running bug.
		"question.asked",
		"question.replied",
		"question.rejected",
		"permission.asked",
		"permission.replied",
		"FLEET_INSTANCE_ID", // non-fleet sessions skip the spawn
		"rootId",            // root-session latch (sub-agents filtered out)
	} {
		if !strings.Contains(content, want) {
			t.Errorf("plugin missing %q:\n%s", want, content)
		}
	}

	// The resolved fleet binary path is baked in so the plugin invokes this build.
	if bin := FleetBinaryPath(); !strings.Contains(content, bin) {
		t.Errorf("plugin does not bake in fleet binary path %q:\n%s", bin, content)
	}

	// Idempotent: a second install produces identical content → no write.
	changed, err = InjectOpenCodePlugin(dir)
	if err != nil {
		t.Fatalf("InjectOpenCodePlugin (2nd): %v", err)
	}
	if changed {
		t.Errorf("expected changed=false on idempotent re-install")
	}
}

func TestInjectOpenCodePluginRewritesOnPathChange(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "plugin", "fleet-status.ts")

	// Seed a stale plugin (different baked binary path) — injector should rewrite.
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginPath, []byte("const FLEET_BIN = \"/old/stale/fleet\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := InjectOpenCodePlugin(dir)
	if err != nil {
		t.Fatalf("InjectOpenCodePlugin: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true when refreshing a stale plugin")
	}
	data, _ := os.ReadFile(pluginPath)
	if strings.Contains(string(data), "/old/stale/fleet") {
		t.Errorf("stale binary path not replaced:\n%s", data)
	}
}
