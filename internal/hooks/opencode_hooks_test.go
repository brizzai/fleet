package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

	// One default export must satisfy both plugin loaders: OpenCode 1.x calls
	// server() on a default export that has an id, and 2.x decodes { id, setup }.
	// 2.x refuses the old named export ("Plugin must export a default definition
	// with an id and an effect or setup function.") and 1.x refuses a default
	// export without server(), so dropping either key breaks one major version.
	for _, want := range []string{
		"export default {",
		`id: "fleet-status",`,
		"server: async (_input) => ({",
		"setup(ctx) {",
		"ctx.event.subscribe({ signal: controller.signal })",
		"return () => controller.abort()",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("plugin missing %q:\n%s", want, content)
		}
	}

	// The plugin must forward via the (synchronous) hook-handler, map the
	// OpenCode-native events fleet understands, skip non-fleet sessions, and latch
	// the root session so sub-agents don't drive status.
	for _, want := range []string{
		"spawnSync",
		"hook-handler",
		"--fleet-hook",
		"session.busy",
		"session.idle",
		"session.error",
		"FLEET_INSTANCE_ID", // non-fleet sessions skip the spawn
		// root-session latch (sub-agents filtered out); both handlers gate on it
		"if (!isRoot(sessionID)) return",
		// 2.x MCP elicitation forms are owned by "global", which is not a session
		// and must not take the latch from the root.
		`if (!sessionID || sessionID === "global") return false`,
		// 2.x re-imports the file when fleet rewrites it; a module-level latch
		// would start over and could take a sub-agent.
		`if (globalThis[ROOT_ID] === undefined) globalThis[ROOT_ID] = sessionID`,
		// 2.x: only a private server belongs to one TUI. The shared background
		// service inherits whichever client's FLEET_INSTANCE_ID spawned it. 1.19+
		// also calls setup(), and only this gate keeps it from doubling every
		// status there, so it must stay something a 1.x process can't satisfy.
		`if (!process.argv.includes("--stdio")) return`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("plugin missing %q:\n%s", want, content)
		}
	}

	// The prompt branches are asserted whole, not by event name. Both
	// `permission.asked` and `permission.replied` appear twice in the generated
	// plugin — in the condition AND as the notify() argument — so a bare-name
	// check passes even with the condition deleted. Deleting one family from a
	// condition is exactly the stuck-at-running regression this guards, so the
	// assertion has to pin the `type ===` chain itself.
	//
	// question.* is the AskUserQuestion prompt, permission.* is a tool-permission
	// prompt (the user's `permission: ask` config). Both are live in OpenCode
	// 1.14.x — these lists are additive, never a swap. 2.x asks questions through
	// forms, so its chain pairs form.* with permission.* the same way.
	flat := strings.Join(strings.Fields(content), " ")
	for _, want := range []string{
		`if (type === "question.asked" || type === "permission.asked") { promptAsked(sessionID)`,
		`type === "question.replied" || type === "question.rejected" || type === "permission.replied" ) { promptReplied(sessionID)`,
		`if (type === "form.created" || type === "permission.asked") { promptAsked(sessionID)`,
		`type === "form.replied" || type === "form.cancelled" || type === "permission.replied" ) { promptReplied(sessionID)`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("plugin missing event branch %q:\n%s", want, content)
		}
	}

	// Both handlers notify session.busy and session.error, so the bare names
	// above can't tell a deleted 1.x branch from a live one. Pin the 1.x branches
	// and each handler's wiring whole: swapping onEventV1/onEventV2, or reading
	// the wrong envelope (properties vs data), must fail here.
	for _, want := range []string{
		`if (type === "session.status") { const st = props.status && props.status.type if (st === "busy") busy(sessionID) else if (st === "idle") turnEnded("session.idle", sessionID) }`,
		`} else if (type === "session.error") { turnEnded("session.error", sessionID) } else if (type === "question.asked"`,
		`const props = (event && event.properties) || {}`,
		`const data = (event && event.data) || {}`,
		`server: async (_input) => ({ event: async ({ event }) => onEventV1(event), })`,
		`for await (const event of ctx.event.subscribe({ signal: controller.signal })) onEventV2(event)`,
	} {
		if n := strings.Count(flat, want); n != 1 {
			t.Errorf("plugin has %d copies of %q, want 1:\n%s", n, want, content)
		}
	}

	// 2.x publishes no session.status: a busy period is one
	// session.execution.started and one terminal event, so a plugin keyed on
	// session.status loads fine and reports nothing. form.created carries its
	// session at data.form.sessionID, so reading only data.sessionID would skip
	// every question prompt (the stuck-at-running bug again).
	for _, want := range []string{
		`const sessionID = reportAs(type, data.sessionID || (data.form && data.form.sessionID))`,
		`if (type === "session.execution.started") { busy(sessionID)`,
		`type === "session.execution.succeeded" || type === "session.execution.interrupted" ) { turnEnded("session.idle", sessionID)`,
		// failed is 2.x's only terminal event for a failed turn; without the
		// trailing idle the row stays at error until the next prompt.
		`if (type === "session.execution.failed") {`,
		`turnEnded("session.error", sessionID) turnEnded("session.idle", sessionID) }`,
		// A sub-agent's prompt, or a 2.x MCP form owned by "global", blocks the
		// root's turn: both handlers report prompts under the root, so the row
		// reads waiting instead of running.
		`const sessionID = reportAs(type, props.sessionID)`,
		`return globalThis[ROOT_ID] === undefined ? sessionID : globalThis[ROOT_ID]`,
		`"permission.asked", "permission.replied",`,
		`"form.created", "form.replied", "form.cancelled",`,
		`"question.asked", "question.replied", "question.rejected",`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("plugin missing event branch %q:\n%s", want, content)
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

// TestOpenCodePluginOverlappingPrompts runs the generated plugin under node and
// drives both handlers through two overlapping prompts. Rebinding sub-agent
// prompts to the root merges their lifetimes, so answering the first of two must
// not report running while the second is still open: hook-handler maps a reply
// to running and OpenCode has no pane fallback to restore waiting. The pinned
// strings above can't see an event sequence, and they can't see a syntax error
// either, which would ship as a plugin-load failure on every launch.
func TestOpenCodePluginOverlappingPrompts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "fleet")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >> '"+logPath+"'\necho >> '"+logPath+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binLiteral, _ := json.Marshal(stub)
	if err := os.WriteFile(filepath.Join(dir, "plugin.mjs"), []byte(fmt.Sprintf(openCodePluginTemplate, binLiteral)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Each handler gets a fresh node process, so the globalThis state starts clean.
	cases := []struct {
		name, harness string
	}{
		{"1.x", `
const h = await p.server({})
const ev = (type, sessionID, extra) => h.event({ event: { type, properties: { sessionID, ...extra } } })
await ev("session.status", "root", { status: { type: "busy" } })
await ev("permission.asked", "childA")
await ev("question.asked", "childB")
await ev("permission.replied", "childA")
await ev("session.status", "root", { status: { type: "busy" } }) // 1.x re-announces busy after a reply
await ev("question.replied", "childB")
await ev("session.idle", "root")
await ev("question.replied", "childB") // stale: nothing open
`},
		{"2.x", `
process.argv.push("--stdio")
const evs = [
  ["session.execution.started", { sessionID: "root" }],
  ["permission.asked", { sessionID: "childA" }],
  ["form.created", { form: { sessionID: "global" } }],
  ["permission.replied", { sessionID: "childA" }],
  ["form.replied", { sessionID: "global" }],
  ["session.execution.interrupted", { sessionID: "root" }],
  ["form.cancelled", { sessionID: "global" }], // stale: the turn already ended
]
await new Promise((done) => p.setup({ event: { subscribe: async function* () {
  for (const [type, data] of evs) yield { type, data }
  done()
} } }))
`},
	}
	want := strings.Join([]string{
		`{"hook_event_name":"session.busy","session_id":"root"}`,
		`{"hook_event_name":"permission.asked","session_id":"root"}`,
		`{"hook_event_name":"permission.asked","session_id":"root"}`,
		`{"hook_event_name":"permission.replied","session_id":"root"}`,
		`{"hook_event_name":"session.idle","session_id":"root"}`,
	}, "\n")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(logPath)
			script := filepath.Join(dir, "run.mjs")
			src := "const p = (await import(\"./plugin.mjs\")).default\n" + tc.harness
			if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(node, script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "FLEET_INSTANCE_ID=test")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("node: %v\n%s", err, out)
			}
			got, _ := os.ReadFile(logPath)
			if g := strings.TrimSpace(string(got)); g != want {
				t.Errorf("hook calls:\n%s\nwant:\n%s", g, want)
			}
		})
	}
}
