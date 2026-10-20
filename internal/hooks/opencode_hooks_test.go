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
		// A stream error re-enters the subscription instead of ending
		// reporting for the server (the adapter fails the stream on its first
		// error), under an import-generation counter so a re-imported module's
		// loop never runs beside the stale one.
		"while (!controller.signal.aborted && current()) {",
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
		// 2.x: only a private server belongs to one TUI. The shared background
		// service inherits whichever client's FLEET_INSTANCE_ID spawned it. 1.19+
		// also calls setup(), and only this gate keeps it from doubling every
		// status there, so it must stay something a 1.x process can't satisfy.
		`if (!process.argv.includes("--stdio")) return`,
		// Reports carry the conversation fleet launched the pane for, latched
		// once: a switched-to session's own id would run through fleet's
		// Claude rotation checks, which have no OpenCode transcripts to
		// consult and would freeze the row on the old id.
		`if (globalThis[FLEET_ID] === undefined) globalThis[FLEET_ID] = sessionID`,
		// The re-entry pause is abort-aware — teardown must not wait the
		// second out — and removes its listener when the timer wins, so a
		// persistently failing stream cannot pile listeners onto the signal.
		`const onAbort = () => { clearTimeout(timer); resolve() }`,
		`let timer`, // declared before onAbort reads it
		// The open-ask ledger is bounded like the settled one.
		`if (asks.size > 256) asks.delete(asks.keys().next().value)`,
		// The import generation, kept per location: a re-import bumps its
		// location's, and a stale loop exits instead of resubscribing beside
		// the new module, while another directory's setup() leaves it alone.
		`const where = (loc.directory || "") + "\u0000" + (loc.workspaceID || "")`,
		`generations.set(where, generation)`,
		`const current = () => globalThis[GENERATION] instanceof Map && globalThis[GENERATION].get(where) === generation`,
		// The generation is re-checked per dispatch, not just per
		// subscription: one stale dispatch would corrupt the prompt state a
		// replacement module shares.
		`if (controller.signal.aborted || !current()) break`,
		// A hot upgrade inherits the old module's root latch but had no
		// reporting id: seed one from the inherited root, before a switch or
		// prompt can move the latch, and count the root as known.
		`if (globalThis[FLEET_ID] === undefined) globalThis[FLEET_ID] = globalThis[ROOT_ID]`,
		`knownRoot(globalThis[ROOT_ID])`,
		// A prompt is owned by the conversation its ancestry names: a
		// session's creation event records its parent, and the chain walks
		// to the conversation it belongs to, so a child of a departed
		// conversation is never reported however many switches ago and live
		// again when its conversation returns. A known root (the departing
		// one included) owns itself, and a "global" MCP form is keyed by form
		// id at creation.
		`function rootAncestor(sessionID) {`,
		`if (type === "session.created") {`,
		`parents.set(data.sessionID, data.parentID)`,
		`owners.set(id, ancestor)`,
		`if (owner !== undefined && owner !== globalThis[ROOT_ID]) {`,
		// Another conversation's prompts are not reported but still counted
		// against it, so a return finds its open prompts as they stand; a
		// known root owns its own prompts.
		`countAway(type, owner)`,
		// A reply settles the count its ask went to (noted by prompt id),
		// without recording the prompter as owned; a turn end proven lost by
		// a later acknowledgement closes only the asks made before it.
		`if (ask.conv === globalThis[ROOT_ID]) return ask.conv`,
		`if (settled !== undefined && settled.delete(pid)) return null`,
		// A lost turn end found on the next start closes only the asks
		// delivered no later than the subscription that delivered the turn's
		// start; every subscription is numbered.
		`if (lostTerminal) globalThis[OPEN_PROMPTS] = forgetAsks(sessionID, (ask) => ask.epoch > lostAfter)`,
		`starts.set(data.sessionID, delivering)`,
		// Before anything latched, the first event naming a session latches
		// ahead of reportAs; a session with recorded ancestry latches its
		// root conversation, never itself, and a root latched from anything
		// but its own start or end is taken as live.
		`const root = parents !== undefined && parents.has(sid) ? rootAncestor(sid) : sid`,
		`if (!starts.has(root)) starts.set(root, delivering)`,
		// Per-session bookkeeping is bounded, oldest first.
		`const BOUNDED = [ROOTS_SEEN, PARENTS, PROMPT_OWNERS, TURNS, START_EPOCHS, LIVE, AWAY_PROMPTS]`,
		`while (entries !== undefined && entries.size > 4096) entries.delete(entries.keys().next().value)`,
		`const subscription = (globalThis[EPOCH] = (globalThis[EPOCH] || 0) + 1)`,
		`if (type === "permission.replied") return data.requestID`,
		`if (seen !== undefined && seen.has(sessionID)) return sessionID`,
		// Only a chain terminating at a known root is an answer; one that
		// runs out of edges is incomplete and reads as unknown, so a lost
		// intermediate edge cannot cache a wrong owner.
		`if (seen !== undefined && seen.has(id)) return id`,
		// A fresh viewed for the latched conversation applies a terminal lost
		// with the stream only while that conversation is still believed
		// live; after a turn that did end it reports nothing, so a background
		// sub-agent's prompt survives.
		`globalThis[OPEN_PROMPTS] = forgetAsks(data.sessionID, (ask) => ask.at !== undefined && ask.at > data.idle)`,
		// A creation naming no parent is a conversation root.
		`rootsSeen().add(data.sessionID)`,
		// A viewed acknowledgement only proves idleness when newer than the
		// target's last turn start — the TUI retries failed posts, and a
		// retried ack for the previous watermark can land mid-turn.
		`function viewedAssertsIdle(data) {`,
		`return started === undefined || data.idle > started`,
		`turns.set(data.sessionID, event.created)`,
		`function switchRoot(sessionID) {`,
		`if (type === "form.created") return data.form && data.form.sessionID === "global" ? "form:" + data.form.id : null`,
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
		`for await (const event of ctx.event.subscribe({ signal: controller.signal })) { if (controller.signal.aborted || !current()) break delivering = subscription onEventV2(event) }`,
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
		`const sessionID = reportAs(type, data.sessionID || (data.form && data.form.sessionID), promptKey(type, data), promptID(type, data), event.created)`,
		// session.viewed arrives before the root-only gate, so a switch moves
		// the latch instead of being rejected as a foreign session.
		`if (type === "session.viewed") { if (!viewedAssertsIdle(data) || !data.sessionID || data.sessionID === "global") return`,
		`if (globalThis[OPEN_PROMPTS]) notify("permission.asked", data.sessionID) else notify("session.idle", data.sessionID) }`,
		// The first user message typed into a known root conversation moves
		// the latch home — the recovery for returning to an already-read
		// conversation, which session.viewed cannot signal. Typed mid-turn,
		// it joins the running busy period and no start follows, so the
		// switch reports busy itself.
		`data.item.type === "user" && data.sessionID !== undefined && data.sessionID !== globalThis[ROOT_ID] && rootsSeen().has(data.sessionID)`,
		`switchRoot(data.sessionID) globalThis[OPEN_PROMPTS] = 0 forgetAsks(data.sessionID) if (live().has(data.sessionID)) notify("session.busy", data.sessionID) }`,
		// A fork is a conversation root too; it publishes session.forked,
		// never session.created.
		`if (type === "session.forked") { if (data.sessionID) rootsSeen().add(data.sessionID) return }`,
		// A hot upgrade from a template without turn state treats the
		// inherited root as live, so a lost terminal can still be closed out.
		`if (globalThis[LIVE] === undefined) { live() if (globalThis[ROOT_ID] !== undefined) live().add(globalThis[ROOT_ID]) }`,
		// 2.x re-imports the file when fleet rewrites it; a module-level latch
		// would start over and could take a sub-agent. Latching also marks the
		// session as a known root, which ancestry walks require.
		`if (globalThis[ROOT_ID] === undefined) { globalThis[ROOT_ID] = sessionID knownRoot(sessionID) }`,
		// A switch moves the gate and the prompt count with it: the departing
		// conversation's count is kept away, and the target's comes back.
		`if (from !== undefined && globalThis[OPEN_PROMPTS]) awayPrompts().set(from, globalThis[OPEN_PROMPTS])`,
		`knownRoot(sessionID) globalThis[ROOT_ID] = sessionID globalThis[OPEN_PROMPTS] = awayPrompts().get(sessionID) || 0 awayPrompts().delete(sessionID) }`,
		// The bound evicts the least recently written entry: an update re-inserts.
		`turns.delete(data.sessionID) // re-set as newest: the bound evicts the stalest`,
		// Only a start for a conversation still believed live proves a lost
		// terminal and closes the prompts from before the gap; otherwise an
		// open background prompt holds busy back.
		`lostTerminal = live().delete(data.sessionID)`,
		`if (type === "session.execution.started") { if (lostTerminal) globalThis[OPEN_PROMPTS] = forgetAsks(sessionID, (ask) => ask.epoch > lostAfter) busy(sessionID)`,
		`type === "session.execution.succeeded" || type === "session.execution.interrupted" ) { turnEnded("session.idle", sessionID)`,
		// failed is 2.x's only terminal event for a failed turn; without the
		// trailing idle the row stays at error until the next prompt.
		`if (type === "session.execution.failed") {`,
		`turnEnded("session.error", sessionID) turnEnded("session.idle", sessionID) }`,
		// A sub-agent's prompt, or a 2.x MCP form owned by "global", blocks the
		// root's turn: both handlers report prompts under the root, so the row
		// reads waiting instead of running.
		`const sessionID = reportAs(type, props.sessionID)`,
		`if (key || (sessionID !== globalThis[ROOT_ID] && sessionID !== "global")) {`,
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
  ["session.created", { sessionID: "childA", parentID: "root" }],
  ["session.execution.started", { sessionID: "root" }],
  ["permission.asked", { sessionID: "childA" }],
  ["form.created", { form: { sessionID: "global" } }],
  ["permission.replied", { sessionID: "childA" }],
  ["form.replied", { sessionID: "global" }],
  ["session.execution.interrupted", { sessionID: "root" }],
  ["form.cancelled", { sessionID: "global" }], // stale: the turn already ended
]
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  for (const [type, data] of evs) yield { type, data }
  finished()
} } })
await done
stop() // without it the re-entry loop would resubscribe and replay the events
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

// runOpenCodePlugin renders the generated plugin with a stub fleet binary that
// logs every hook payload, runs harness under node with p bound to the
// plugin's default export (a fresh process, so the globalThis state starts
// clean), and returns the logged hook calls one per line.
func runOpenCodePlugin(t *testing.T, harness string) string {
	t.Helper()
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
	script := filepath.Join(dir, "run.mjs")
	src := "const p = (await import(\"./plugin.mjs\")).default\n" + harness
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
	return strings.TrimSpace(string(got))
}

// assertHookCalls compares the logged hook calls with want, one call per line.
func assertHookCalls(t *testing.T, got string, want ...string) {
	t.Helper()
	if w := strings.Join(want, "\n"); got != w {
		t.Errorf("hook calls:\n%s\nwant:\n%s", got, w)
	}
}

// TestOpenCodePluginStreamRecoveryAndSwitch runs the generated plugin under
// node through the two failures of #348 in one flow. The subscription ends for
// good on its first error, so a stream that dies mid-turn must be re-entered
// or the server reports nothing further. And a switch to another session in
// the same TUI (session.viewed) must move the gate so the active session's
// events drive the row again — while every report keeps carrying the
// conversation fleet launched the pane for, so fleet's rotation machinery
// never sees a foreign session id it cannot verify for OpenCode.
func TestOpenCodePluginStreamRecoveryAndSwitch(t *testing.T) {
	// Stream one dies mid-turn; the plugin must re-enter and keep reporting.
	// The stale started(root) at the end of stream two must be rejected: the
	// pane switched away from root.
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
const streams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "root" } }
    yield { type: "session.execution.started", created: 1, data: { sessionID: "root" } }
    yield { type: "permission.asked", data: { sessionID: "childA" } }
    yield { type: "session.viewed", data: { sessionID: "other", idle: Date.now() + 1000 } }
    yield { type: "session.execution.started", created: 1, data: { sessionID: "other" } }
    throw new Error("stream failed")
  },
  async function* () {
    yield { type: "session.execution.succeeded", data: { sessionID: "other" } }
    yield { type: "session.viewed", data: { sessionID: "other", idle: Date.now() + 1000 } } // duplicate: must not re-report idle
    yield { type: "session.execution.started", created: 1, data: { sessionID: "root" } } // stale: the pane left root
    finished()
  },
]
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"root"}`,     // root's turn
		`{"hook_event_name":"permission.asked","session_id":"root"}`, // childA's prompt, gated under root
		`{"hook_event_name":"session.idle","session_id":"root"}`,     // the switch: viewed implies idle
		`{"hook_event_name":"session.busy","session_id":"root"}`,     // other's turn — root's prompt count stayed with root
		`{"hook_event_name":"session.idle","session_id":"root"}`,     // after the stream died and was re-entered
	)
}

// TestOpenCodePluginLostTurnBoundaryAndStalePrompts covers the two gap
// aftermaths the live-only feed leaves. A stream that dies between a prompt
// and its turn's end loses both the reply and the terminal event, so the
// open-prompt count survives as stale: a start for a conversation still
// believed live proves the terminal was lost, so it must reset the count and
// report busy, not hold the row at waiting for a whole turn. And a prompt
// from the conversation the pane switched away from (its sub-agent still
// asking while nobody looks) must be dropped, while a prompt from the new
// conversation's tree is reported.
func TestOpenCodePluginLostTurnBoundaryAndStalePrompts(t *testing.T) {
	// Stream one dies with a prompt open and the turn unfinished; the reply
	// and the terminal event happen during the gap and are lost. Stream two
	// carries the next turn's start (count must reset), a switch, stale
	// prompts from the abandoned tree (a child's and the departed root's own
	// ask and reply), and a live prompt from the new one.
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
const streams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "root" } }
    yield { type: "session.execution.started", created: 1, data: { sessionID: "root" } }
    yield { type: "permission.asked", data: { sessionID: "childA" } }
    throw new Error("stream failed")
  },
  async function* () {
    yield { type: "session.execution.started", created: 1, data: { sessionID: "root" } } // root still live: its terminal was lost, the stale count must not hold busy back
    yield { type: "session.viewed", data: { sessionID: "other", idle: Date.now() + 1000 } }
    yield { type: "permission.asked", data: { sessionID: "childA" } } // stale: childA belongs to the tree the pane left
    yield { type: "permission.asked", data: { sessionID: "root" } } // stale: the departed root asks under its own id
    yield { type: "session.created", data: { sessionID: "childB", parentID: "other" } }
    yield { type: "permission.asked", data: { sessionID: "childB" } } // live: the new conversation's tree
    yield { type: "permission.replied", data: { sessionID: "root" } } // stale: must not clear childB's open prompt
    yield { type: "session.execution.succeeded", data: { sessionID: "other" } }
    finished()
  },
]
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"root"}`,     // root's turn
		`{"hook_event_name":"permission.asked","session_id":"root"}`, // childA's prompt, gated under root
		`{"hook_event_name":"session.busy","session_id":"root"}`,     // next turn: the lost reply+terminal left the count stale, reset wins
		`{"hook_event_name":"session.idle","session_id":"root"}`,     // the switch: viewed implies idle
		`{"hook_event_name":"permission.asked","session_id":"root"}`, // childB's prompt — childA's and the departed root's were dropped
		`{"hook_event_name":"session.idle","session_id":"root"}`,     // the new conversation's turn end
	)
}

// TestOpenCodePluginPromptOwnershipAcrossSwitches covers prompt ownership
// across multiple switches and a revisit: a conversation departed two
// switches ago must not have its asks attributed to the conversation now
// being looked at (a one-deep stash forgets it), and its prompts must go
// live again the moment the pane returns to it.
func TestOpenCodePluginPromptOwnershipAcrossSwitches(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "childA" } } // A's tree
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // A departs, owning itself
  yield { type: "session.viewed", data: { sessionID: "C", idle: Date.now() + 1000 } } // B departs too
  yield { type: "permission.asked", data: { sessionID: "A" } } // two switches away: counted against A, not reported
  yield { type: "permission.asked", data: { sessionID: "childA" } } // two switches away: counted against A, not reported
  yield { type: "session.created", data: { sessionID: "childC", parentID: "C" } }
  yield { type: "permission.asked", data: { sessionID: "childC" } } // live: C's tree
  yield { type: "session.viewed", data: { sessionID: "A", idle: Date.now() + 1000 } } // revisit A
  yield { type: "session.execution.started", created: 2, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "childA" } } // live again: A's tree
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childA's prompt, owned by A
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // switch to B
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // switch to C
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childC's prompt — A's and childA's were dropped
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // switch back to A
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's next turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childA is live again
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // and the turn's end
	)
}

// TestOpenCodePluginStaleViewedDoesNotStealLatch covers the cross-root half
// of the delayed-acknowledgement race: a retried acknowledgement for a
// departed conversation's old watermark must not move the latch off the
// conversation now running — its reply and terminal event would be rejected
// and the row would sit at waiting long after the turn ended.
func TestOpenCodePluginStaleViewedDoesNotStealLatch(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1850, data: { sessionID: "A" } }
  yield { type: "session.viewed", data: { sessionID: "B", idle: 1900 } } // the pane moves to B
  yield { type: "session.execution.started", created: 1950, data: { sessionID: "B" } }
  yield { type: "permission.asked", data: { sessionID: "B" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A's turn ends: the retained start watermark must keep the guard armed
  yield { type: "session.viewed", data: { sessionID: "A", idle: 1700 } } // stale retry artifact: must not steal the latch
  yield { type: "permission.replied", data: { sessionID: "B" } } // B's live prompt closes
  yield { type: "session.execution.succeeded", data: { sessionID: "B" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
		`{"hook_event_name":"session.busy","session_id":"A"}`,       // B's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`,   // B's own prompt
		`{"hook_event_name":"permission.replied","session_id":"A"}`, // the stale ack stole nothing
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginDelayedViewedAcknowledgement covers the retried-viewed
// race: the TUI reposts failed view acknowledgements with backoff, and a
// retried acknowledgement for the previous turn's watermark can be published
// while the next turn runs (the server's idle watermark only advances at
// terminals). Such an acknowledgement must not repair anything — no idle
// report, no prompt-count erase — while the turn and its prompt are live.
func TestOpenCodePluginDelayedViewedAcknowledgement(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
  yield { type: "session.execution.started", created: 1850, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "childA" } }
  yield { type: "session.viewed", data: { sessionID: "A", idle: 1700 } } // stale watermark: the ack predates this turn
  yield { type: "permission.replied", data: { sessionID: "childA" } } // still open: the reply must report
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`,   // childA's prompt
		`{"hook_event_name":"permission.replied","session_id":"A"}`, // the stale ack repaired nothing
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginEnqueueMovesLatchBack covers the return to an
// already-read conversation: session.viewed cannot fire on the way back
// (nothing is unread), so the first user message typed into a known root
// conversation moves the latch home. A sub-agent's task is a user item too,
// and a conversation never seen as a root is not a switch target — neither
// may move the latch.
func TestOpenCodePluginEnqueueMovesLatchBack(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A finishes and is read
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane moves to B
  yield { type: "session.created", data: { sessionID: "childB", parentID: "B" } }
  yield { type: "session.inbox.enqueued", data: { sessionID: "childB", item: { type: "user" } } } // a sub-agent task: never moves the latch
  yield { type: "session.inbox.enqueued", data: { sessionID: "C", item: { type: "user" } } } // never seen as a root: ignored
  yield { type: "session.inbox.enqueued", data: { sessionID: "A", item: { type: "user" } } } // the user types into idle A: latch home, no report
  yield { type: "session.execution.started", created: 2, data: { sessionID: "A" } } // gated again: the turn reports
  yield { type: "permission.asked", data: { sessionID: "childA" } } // and A's prompts report
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's first turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // its end
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch to B
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn after the latch came home
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childA's prompt reports again
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginEnqueueIntoRunningConversation covers a message typed
// into a conversation that is still mid-turn when the pane returns to it: it
// joins the running busy period, so no start follows and the switch itself
// must report busy. The TUI takes no message while a prompt of that
// conversation's tree is pending, so none is open then — even one that was
// open when the pane left and was answered while the latch was elsewhere.
func TestOpenCodePluginEnqueueIntoRunningConversation(t *testing.T) {
	t.Run("busy", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane looks at finished B
  yield { type: "session.inbox.enqueued", data: { sessionID: "A", item: { type: "user" } } } // a steer typed into running A
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`, // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`, // the switch to B
			`{"hook_event_name":"session.busy","session_id":"A"}`, // back in A mid-turn: no start follows, the switch says busy
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		)
	})
	t.Run("prompt answered while away", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "childA" } } // P1
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane looks at finished B
  yield { type: "permission.replied", data: { sessionID: "childA" } } // P1 answered back in A, before the latch follows: dropped
  yield { type: "session.inbox.enqueued", data: { sessionID: "A", item: { type: "user" } } } // a steer typed into running A
  yield { type: "permission.asked", data: { sessionID: "childA" } } // P2
  yield { type: "permission.replied", data: { sessionID: "childA" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // P1
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // back in A: nothing open, so busy — not a stale waiting
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // P2
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // answered: running
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		)
	})
}

// TestOpenCodePluginNewConversationIsKnownRoot covers a conversation started
// in the TUI after the pane started: a new one (/new, a new tab) names no
// parent in its creation event, and a fork publishes session.forked instead,
// whose parentID is its source conversation rather than sub-agent descent.
// Either is a known root — typing into it moves the latch, and its
// sub-agents' prompts are owned by it rather than by whichever conversation
// was latched before.
func TestOpenCodePluginNewConversationIsKnownRoot(t *testing.T) {
	for _, tc := range []struct{ name, birth string }{
		{"created", `{ type: "session.created", data: { sessionID: "N" } }`},
		{"forked", `{ type: "session.forked", data: { sessionID: "N", parentID: "A" } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  yield `+tc.birth+` // a new conversation
  yield { type: "session.created", data: { sessionID: "childN", parentID: "N" } }
  yield { type: "permission.asked", data: { sessionID: "childN" } } // owned by N, not A: counted against N, not reported
  yield { type: "session.inbox.enqueued", data: { sessionID: "N", item: { type: "user" } } } // the user types into N: the TUI takes no message while a prompt is pending, so it settled
  yield { type: "session.execution.started", created: 2, data: { sessionID: "N" } }
  yield { type: "permission.asked", data: { sessionID: "childN" } } // N's tree, now live
  yield { type: "permission.replied", data: { sessionID: "childN" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "N" } }
  finished()
} } })
await done
stop()
`)
			assertHookCalls(t, got,
				`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
				`{"hook_event_name":"session.idle","session_id":"A"}`,     // its end
				`{"hook_event_name":"session.busy","session_id":"A"}`,     // N's turn, after the latch followed the message
				`{"hook_event_name":"permission.asked","session_id":"A"}`, // childN's prompt under N
				`{"hook_event_name":"permission.replied","session_id":"A"}`,
				`{"hook_event_name":"session.idle","session_id":"A"}`,
			)
		})
	}
}

// TestOpenCodePluginBackgroundPromptSurvives covers a background sub-agent
// that keeps running after its root's turn ended and asks for permission:
// neither a viewed acknowledgement of the finished turn nor the root's next
// start may clear that open prompt — only a start or acknowledgement for a
// conversation still believed live proves a lost terminal. Asked while the
// pane looks at another conversation, the prompt is not reported but still
// counted, so returning to its conversation reports waiting, not idle.
func TestOpenCodePluginBackgroundPromptSurvives(t *testing.T) {
	t.Run("same conversation", func(t *testing.T) {
		testOpenCodePluginBackgroundPromptSameConversation(t)
	})
	t.Run("answered while away", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A's turn ends; bg keeps running
  yield { type: "session.viewed", data: { sessionID: "B", idle: 150 } } // the pane looks at finished B
  yield { type: "permission.asked", data: { sessionID: "bg" } } // counted against A
  yield { type: "permission.replied", data: { sessionID: "bg" } } // and settled against A
  yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // back to A: nothing open
  finished()
} } })
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`, // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`, // its end
			`{"hook_event_name":"session.idle","session_id":"A"}`, // the switch to B
			`{"hook_event_name":"session.idle","session_id":"A"}`, // back in A: bg's prompt was answered meanwhile
		)
	})
	t.Run("while away", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.viewed", data: { sessionID: "B", idle: 150 } } // the pane looks at finished B
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A's turn ends; bg keeps running
  yield { type: "permission.asked", data: { sessionID: "bg" } } // bg asks while the pane is away: counted, not reported
  yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // back to A
  yield { type: "permission.replied", data: { sessionID: "bg" } }
  finished()
} } })
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // back in A: bg is still waiting, not idle
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // answered
		)
	})
}

func testOpenCodePluginBackgroundPromptSameConversation(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A's turn ends; bg keeps running
  yield { type: "permission.asked", data: { sessionID: "bg" } } // bg asks between A's turns
  yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // the TUI acknowledges A's finished turn
  yield { type: "session.execution.started", created: 300, data: { sessionID: "A" } } // the user types into A
  yield { type: "permission.replied", data: { sessionID: "bg" } } // the user answers bg
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,       // its end
		`{"hook_event_name":"permission.asked","session_id":"A"}`,   // bg's prompt — kept through the ack and the next start
		`{"hook_event_name":"permission.replied","session_id":"A"}`, // answered: A's turn is running
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginKnownRootOwnsItsPrompts covers a known root asking
// under its own id while another conversation holds the latch (its user
// message lost with the stream, say): a root belongs to itself, so the
// prompt is counted against it rather than reported under the latched
// conversation, which would sit at waiting on a prompt that is not its own.
func TestOpenCodePluginKnownRootOwnsItsPrompts(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.created", data: { sessionID: "B" } } // a known root, never latched
  yield { type: "permission.asked", data: { sessionID: "B" } } // B's own prompt: not A's
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane moves to B
  yield { type: "permission.replied", data: { sessionID: "B" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,       // A's end: B's prompt did not hold it at waiting
		`{"hook_event_name":"permission.asked","session_id":"A"}`,   // on B: its prompt is still open
		`{"hook_event_name":"permission.replied","session_id":"A"}`, // answered
	)
}

// TestOpenCodePluginUnknownPrompterReplyFollowsItsAsk covers prompts asked
// by a prompter with no verified ancestry (a conversation resumed from an
// earlier server run never publishes its creation): each ask is counted
// under the conversation latched when it lands, and its reply must settle
// that same count — after a switch, after the prompter's ancestry resolves
// elsewhere, and with another of its asks still open — or the count goes
// stale and a return to that conversation reports waiting with nothing
// pending. A late reply to a prompt its turn end already closed is dropped,
// not taken for a reply to the conversation latched now.
func TestOpenCodePluginUnknownPrompterReplyFollowsItsAsk(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		want         []string
	}{
		{"reply after a switch", `
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "C", id: "p1" } } // unknown prompter: counted under A
  yield { type: "session.viewed", data: { sessionID: "B", idle: 150 } } // the pane moves to B; A's count goes with A
  yield { type: "permission.replied", data: { sessionID: "C", requestID: "p1" } } // settles A's count, not B's
  yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // back to A: nothing open
  yield { type: "session.execution.started", created: 300, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // its end
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // C's prompt, accepted under A
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch to B
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // back in A: idle, not a stale waiting
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's next turn is not held back
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		}},
		{"ancestry resolves later", `
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  yield { type: "session.created", data: { sessionID: "c", parentID: "R" } } // R is a resumed conversation: not a known root yet
  yield { type: "session.execution.started", created: 200, data: { sessionID: "R" } }
  yield { type: "permission.asked", data: { sessionID: "c", id: "p1" } } // unknown: counted under A
  yield { type: "session.execution.succeeded", data: { sessionID: "R" } }
  yield { type: "session.viewed", data: { sessionID: "R", idle: 300 } } // R latched: c now resolves to R
  yield { type: "permission.replied", data: { sessionID: "c", requestID: "p1" } } // still settles A's count
  yield { type: "session.viewed", data: { sessionID: "A", idle: 150 } } // back to A: nothing open`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // its end
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // c's prompt, accepted under A
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch to R
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // back in A: idle
		}},
		{"overlapping asks", `
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "C", id: "p1" } } // counted under A
  yield { type: "session.viewed", data: { sessionID: "B", idle: 150 } }
  yield { type: "permission.asked", data: { sessionID: "C", id: "p2" } } // counted under B
  yield { type: "permission.replied", data: { sessionID: "C", requestID: "p1" } } // settles A's: B's p2 is still open
  yield { type: "permission.replied", data: { sessionID: "C", requestID: "p2" } } // settles B's
  yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // back to A: nothing open`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // its end
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p1 under A
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p2 under B
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // only p2's reply opens B
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // back in A: idle
		}},
		{"late cancellation after its turn ended", `
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "form.created", data: { form: { id: "F1", sessionID: "C" } } } // unknown prompter: counted under A
  yield { type: "session.execution.interrupted", data: { sessionID: "A" } } // A's turn end closes F1
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } }
  yield { type: "session.execution.started", created: 2, data: { sessionID: "B" } }
  yield { type: "form.created", data: { form: { id: "F2", sessionID: "B" } } } // B's own form
  yield { type: "form.cancelled", data: { id: "F1", sessionID: "C" } } // late: F1 was settled, dropped
  yield { type: "form.created", data: { form: { id: "F3", sessionID: "B" } } } // B's second form
  yield { type: "form.replied", data: { id: "F3", sessionID: "B" } } // F2 still open
  yield { type: "form.replied", data: { id: "F2", sessionID: "B" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "B" } }`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // F1, accepted under A
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // A interrupted
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // B's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // F2
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // F3: F1's late cancellation cleared nothing
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // only once F3 and F2 are both answered
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {`+tc.events+`
  finished()
} } })
await done
stop()
`)
			assertHookCalls(t, got, tc.want...)
		})
	}
}

// TestOpenCodePluginLostTurnEndKeepsLaterAsks covers a turn end lost with
// the stream and proven later — by a viewed acknowledgement, or by the
// conversation's next start: the repair closes the prompts asked before that
// turn end, but a background sub-agent's ask made after it, delivered after
// the reconnect, is still open, whether the pane is on that conversation or
// comes back to it.
func TestOpenCodePluginLostTurnEndKeepsLaterAsks(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		want         []string
	}{
		{"same conversation", `
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
    yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
    yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "p1" } }
    throw new Error("stream failed") // A's turn ends at 200 in the gap, p1 with it
  },
  async function* () {
    yield { type: "permission.asked", created: 300, data: { sessionID: "bg", id: "p2" } } // bg asks after A's turn ended
    yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // proves the lost turn end
    yield { type: "permission.replied", data: { sessionID: "bg", requestID: "p2" } }
    finished()
  },`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p1
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p2
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // the repair closes p1 only: p2 still waits
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // p2 answered
		}},
		{"while away", `
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
    yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
    yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "p1" } }
    yield { type: "session.viewed", data: { sessionID: "B", idle: 120 } } // the pane looks at finished B
    throw new Error("stream failed") // A's turn ends at 200 in the gap, p1 with it
  },
  async function* () {
    yield { type: "permission.asked", created: 300, data: { sessionID: "bg", id: "p2" } } // counted against A
    yield { type: "session.viewed", data: { sessionID: "A", idle: 200 } } // back to A: proves the lost turn end
    yield { type: "permission.replied", data: { sessionID: "bg", requestID: "p2" } }
    finished()
  },`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p1
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // the switch to B
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // back in A: p1 closed, p2 still waits
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // p2 answered
		}},
		{"next start", `
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
    yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
    yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "p1" } }
    throw new Error("stream failed") // A's turn ends at 200 in the gap, p1 with it
  },
  async function* () {
    yield { type: "permission.asked", created: 300, data: { sessionID: "bg", id: "p2" } } // bg asks after the reconnect
    yield { type: "session.execution.started", created: 400, data: { sessionID: "A" } } // A still live: proves the lost turn end
    yield { type: "permission.replied", data: { sessionID: "bg", requestID: "p2" } }
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
  },`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p1
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p2
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // the start closed p1 only: p2's reply opens the turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		}},
		{"next start after two reconnects", `
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
    yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
    yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "p1" } }
    throw new Error("stream failed") // A's turn ends at 200 in the gap, p1 with it
  },
  async function* () {
    yield { type: "permission.asked", created: 300, data: { sessionID: "bg", id: "p2" } } // bg asks after the reconnect
    throw new Error("stream failed") // a second gap
  },
  async function* () {
    yield { type: "session.execution.started", created: 400, data: { sessionID: "A" } } // proves the lost turn end
    yield { type: "permission.replied", data: { sessionID: "bg", requestID: "p2" } }
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
  },`, []string{
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p1
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // p2
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // p2 outlived the second gap: its reply opens the turn
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
let finished
const done = new Promise((resolve) => { finished = resolve })
const streams = [`+tc.events+`
]
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
			assertHookCalls(t, got, tc.want...)
		})
	}
}

// TestOpenCodePluginLostTurnEndAcrossSubscriptions covers the next-start
// repair's notion of "since the reconnect" across subscriptions: a plugin
// re-activation (a new setup() after the old one is disposed) is a gap like
// a stream error, so an ask from before it is closed; and another
// directory's reconnect is no gap for this one, so an ask delivered on the
// same subscription as the start stays open.
func TestOpenCodePluginLostTurnEndAcrossSubscriptions(t *testing.T) {
	t.Run("re-activation", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
const location = { directory: "/work", workspaceID: "w1" }
const stop1 = p.setup({ location, event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "per_1" } }
  await new Promise(() => {}) // parked until disposal
} } })
await new Promise((resolve) => setTimeout(resolve, 30))
stop1() // re-activation: A's turn end and per_1's reply fall in the gap
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop2 = p.setup({ location, event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 400, data: { sessionID: "A" } } // A still live: proves the lost turn end
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
  await new Promise(() => {})
} } })
await done
stop2()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // per_1
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // per_1 came before the gap: closed, busy reported
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		)
	})
	t.Run("another directory reconnects", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished, xAsked, yBack
const done = new Promise((resolve) => { finished = resolve })
const asked = new Promise((resolve) => { xAsked = resolve })
const yReentered = new Promise((resolve) => { yBack = resolve })
let xCalls = 0
const xStreams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.created", data: { sessionID: "bg", parentID: "A" } }
    yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
    yield { type: "permission.asked", created: 150, data: { sessionID: "childA", id: "per_1" } }
    throw new Error("stream failed") // A's turn ends at 200 in the gap, per_1 with it
  },
  async function* () {
    yield { type: "permission.asked", created: 300, data: { sessionID: "bg", id: "per_2" } } // after the reconnect
    xAsked()
    await yReentered // /y's stream fails and reconnects meanwhile
    yield { type: "session.execution.started", created: 400, data: { sessionID: "A" } } // proves the lost turn end
    yield { type: "permission.replied", data: { sessionID: "bg", requestID: "per_2" } }
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
    await new Promise(() => {})
  },
]
const stopX = p.setup({ location: { directory: "/x" }, event: { subscribe: () => (xStreams[xCalls++] ?? (async function* () { await new Promise(() => {}) }))() } })
let yCalls = 0
const stopY = p.setup({ location: { directory: "/y" }, event: { subscribe: () => {
  if (yCalls++ === 0) return (async function* () { await asked; throw new Error("stream failed") })()
  yBack()
  return (async function* () { await new Promise(() => {}) })()
} } })
await done
stopX()
stopY()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"session.busy","session_id":"A"}`,       // A's turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // per_1
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // per_2
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // the start closed per_1 only, whatever /y did
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		)
	})
}

// TestOpenCodePluginMissingFirstStartLatchesRoot covers a stream gap that
// loses the launched conversation's first start. When its sub-agent's events
// arrive first, the latch takes the root conversation its recorded ancestry
// names rather than the child — the child's lifecycle is ignored, its
// prompts and every report's session id are the root's. When the root's own
// ask comes first, it is noted like any other, so its late reply cannot
// clear a newer prompt. And a root latched that way is taken as live, so a
// later lost turn end can still be repaired.
func TestOpenCodePluginMissingFirstStartLatchesRoot(t *testing.T) {
	t.Run("child speaks first", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
let finished
const done = new Promise((resolve) => { finished = resolve })
const streams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "A" } }
    throw new Error("stream failed") // A's first start falls in the gap
  },
  async function* () {
    yield { type: "session.created", data: { sessionID: "child", parentID: "A" } }
    yield { type: "session.execution.started", created: 200, data: { sessionID: "child" } } // a sub-agent: never the latch
    yield { type: "permission.asked", created: 250, data: { sessionID: "child", id: "p1" } }
    yield { type: "permission.replied", data: { sessionID: "child", requestID: "p1" } }
    yield { type: "session.execution.succeeded", data: { sessionID: "child" } } // ignored: lifecycle is root-only
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
  },
]
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"permission.asked","session_id":"A"}`,   // the child's prompt, under A
			`{"hook_event_name":"permission.replied","session_id":"A"}`, // answered
			`{"hook_event_name":"session.idle","session_id":"A"}`,       // A's turn end
		)
	})
	t.Run("root's own ask comes first", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "permission.asked", created: 50, data: { sessionID: "A", id: "p1" } } // the first event: A's start was missed
  yield { type: "session.execution.interrupted", data: { sessionID: "A" } } // closes p1
  yield { type: "session.execution.started", created: 100, data: { sessionID: "A" } }
  yield { type: "permission.asked", created: 150, data: { sessionID: "A", id: "p2" } }
  yield { type: "permission.replied", data: { sessionID: "A", requestID: "p1" } } // late: must not clear p2
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // p1, noted though it came first
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // interrupted
			`{"hook_event_name":"session.busy","session_id":"A"}`,     // the next turn
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // p2: p1's late reply reported nothing
			`{"hook_event_name":"session.idle","session_id":"A"}`,
		)
	})
	t.Run("then a lost turn end", func(t *testing.T) {
		got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
let finished
const done = new Promise((resolve) => { finished = resolve })
const streams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "A" } }
    throw new Error("stream failed") // A's first start falls in the gap
  },
  async function* () {
    yield { type: "session.created", data: { sessionID: "child", parentID: "A" } }
    yield { type: "session.execution.started", created: 200, data: { sessionID: "child" } } // latches A, taken as live
    yield { type: "permission.asked", created: 300, data: { sessionID: "A", id: "p1" } }
    throw new Error("stream failed") // p1's reply and A's turn end fall in the gap
  },
  async function* () {
    yield { type: "session.viewed", data: { sessionID: "A", idle: 400 } } // proves the lost turn end
    finished()
  },
]
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
		assertHookCalls(t, got,
			`{"hook_event_name":"permission.asked","session_id":"A"}`, // p1
			`{"hook_event_name":"session.idle","session_id":"A"}`,     // the repair: A was taken as live
		)
	})
}

// TestOpenCodePluginBoundKeepsLiveConversationState covers the bound on
// per-session bookkeeping: it must evict the least recently written entry,
// not the first ever inserted. A long-lived conversation re-starts turn
// after turn while thousands of sub-agents come and go; if its turn start
// were evicted mid-turn, a stale retried acknowledgement would pass the
// staleness guard and report idle while the turn still runs.
func TestOpenCodePluginBoundKeepsLiveConversationState(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } } // A's turn ends at 2
  for (let i = 0; i < 4094; i++) yield { type: "session.execution.started", created: 3, data: { sessionID: "c" + i } } // sub-agents come and go
  yield { type: "session.execution.started", created: 5000, data: { sessionID: "A" } } // A's next turn, re-written as newest
  for (let i = 4094; i < 4097; i++) yield { type: "session.execution.started", created: 5001, data: { sessionID: "c" + i } } // past the bound
  yield { type: "session.viewed", data: { sessionID: "A", idle: 2 } } // a stale retry for the previous turn: must be ignored
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,
		`{"hook_event_name":"session.idle","session_id":"A"}`,
		`{"hook_event_name":"session.busy","session_id":"A"}`, // A's next turn
		`{"hook_event_name":"session.idle","session_id":"A"}`, // only its real end: the stale ack repaired nothing
	)
}

// TestOpenCodePluginGenerationStopsStaleLoop pins the import-generation
// mechanism behaviorally: a second setup() for the same location (what a
// re-import performs) must stop the first module's loop from dispatching an
// event that was already pending in its stream — the per-dispatch guard, not
// just the loop condition, is what keeps the two modules from sharing prompt
// state.
func TestOpenCodePluginGenerationStopsStaleLoop(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let release
const parked = new Promise((resolve) => { release = resolve })
let calls = 0
const streams = [
  async function* () { // the old module's stream parks mid-flight
    yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
    await parked // the re-import lands while this is pending
    yield { type: "permission.asked", data: { sessionID: "childA" } } // must never dispatch
  },
  async function* () { // the new module's stream
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
  },
]
let finished
const done = new Promise((resolve) => { finished = resolve })
const subscribe = () => {
  const next = streams[calls++] ?? (async function* () {})
  return next()
}
const location = { directory: "/work", workspaceID: "w1" }
const stop1 = p.setup({ location, event: { subscribe } })
await new Promise((resolve) => setTimeout(resolve, 20)) // the old module dispatches its first event
const stop2 = p.setup({ location, event: { subscribe } }) // the re-import: bumps the location's generation
await done
release()
await new Promise((resolve) => setTimeout(resolve, 20)) // let the stale event reach the old loop's guard while its controller is still live
stop1()
stop2()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`, // dispatched before the re-import
		`{"hook_event_name":"session.idle","session_id":"A"}`, // the new module's event
	)
}

// TestOpenCodePluginLocationsRunSideBySide covers OpenCode running one plugin
// instance per directory in the same process, each subscription seeing only
// its own directory's events: a second directory's setup() is not a
// re-import, and must not stop the first directory's loop — that froze the
// pane's row the moment another directory opened in the same TUI.
func TestOpenCodePluginLocationsRunSideBySide(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let release
const later = new Promise((resolve) => { release = resolve })
let finished
const done = new Promise((resolve) => { finished = resolve })
const stopX = p.setup({ location: { directory: "/x" }, event: { subscribe: async function* () {
  try {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
    await later // the second directory's setup() lands meanwhile
    yield { type: "permission.asked", data: { sessionID: "childA" } } // must still dispatch
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  } finally {
    finished() // also when a stopped loop abandons this stream, so a regression fails instead of hanging
  }
} } })
await new Promise((resolve) => setTimeout(resolve, 20))
const stopY = p.setup({ location: { directory: "/y" }, event: { subscribe: async function* () {} } })
release()
await done
stopX()
stopY()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // directory /x kept reporting after /y's setup()
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginSwitchBeforeFirstReport covers the user switching
// conversations before the launched one ever reports: the latch and the
// reporting id both take the switched-to conversation, which is what the
// row's owner would have latched anyway.
func TestOpenCodePluginSwitchBeforeFirstReport(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the first event ever
  yield { type: "session.execution.started", created: 1, data: { sessionID: "B" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "B" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.idle","session_id":"B"}`,
		`{"hook_event_name":"session.busy","session_id":"B"}`,
		`{"hook_event_name":"session.idle","session_id":"B"}`,
	)
}

// TestOpenCodePluginIncompleteAncestryIsUnknown covers a chain with a lost
// intermediate edge: the walk must not treat the orphaned middle node as a
// confirmed root, cache it as the prompter's owner, and drop a live
// conversation's prompt — an incomplete chain reads as unknown, and the
// prompt is accepted under the current root without being recorded.
func TestOpenCodePluginIncompleteAncestryIsUnknown(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  // middle -> A was lost with a stream gap; only leaf -> middle arrived.
  yield { type: "session.created", data: { sessionID: "leaf", parentID: "middle" } }
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "leaf" } } // unknown, not middle: accepted under A
  yield { type: "permission.replied", data: { sessionID: "leaf" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // leaf's prompt: incomplete ancestry is unknown, not a wrong owner
		`{"hook_event_name":"permission.replied","session_id":"A"}`,
		`{"hook_event_name":"session.idle","session_id":"A"}`,
	)
}

// TestOpenCodePluginSameRootViewedRepairsLostTurnEnd covers a terminal lost
// with the stream: when a gap swallows a turn's terminal event and its
// prompt replies, a fresh viewed acknowledgement for the conversation still
// believed live proves the turn ended — the count closes and idle is
// reported — while a repeat, once that turn is known to be over, stays
// silent.
func TestOpenCodePluginSameRootViewedRepairsLostTurnEnd(t *testing.T) {
	// Stream one dies with a prompt open and the turn unfinished; the reply
	// and the terminal event happen during the gap and are lost. Stream two
	// carries the TUI regaining focus on the same conversation.
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let calls = 0
const streams = [
  async function* () {
    yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } }
    yield { type: "session.execution.started", created: 1850, data: { sessionID: "A" } }
    yield { type: "permission.asked", data: { sessionID: "childA" } }
    throw new Error("stream failed")
  },
  async function* () {
    yield { type: "session.viewed", data: { sessionID: "A", idle: 1900 } } // same root: the ack (1900) postdates the turn's start (1850) even though this stream delivers it late — applies the lost turn end
    yield { type: "permission.replied", data: { sessionID: "childA" } } // stale: the repair closed the count, nothing to decrement
    yield { type: "session.viewed", data: { sessionID: "A", idle: 1900 } } // repeat: the turn is known to be over, silent
    yield { type: "session.execution.started", created: 2000, data: { sessionID: "A" } }
    yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
    finished()
  },
]
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({
  event: {
    subscribe: () => {
      const next = streams[calls++] ?? (async function* () {})
      return next()
    },
  },
})
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childA's prompt
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // the repair: viewed says idle, the lost turn end is behind us
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's next turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // and its end
	)
}

// TestOpenCodePluginChildAncestryAfterSwitch covers the first prompt of a
// child arriving after a switch: its conversation must come from verified
// ancestry (the creation event's parent chain), not from whatever the pane is
// looking at when it asks — otherwise the child is attributed to the wrong
// conversation, reported as waiting while that conversation sits idle, and
// then poisoned against its own conversation on return.
func TestOpenCodePluginChildAncestryAfterSwitch(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.created", data: { sessionID: "childA", parentID: "A" } } // the ancestry everything rides on
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "session.execution.started", created: 1, data: { sessionID: "childA" } } // rejected: lifecycle is root-only
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane moves on
  yield { type: "permission.asked", data: { sessionID: "childA" } } // not reported: childA belongs to A, first prompt or not (A's interrupt clears it)
  yield { type: "session.execution.interrupted", data: { sessionID: "A" } } // rejected: the pane left A
  yield { type: "session.viewed", data: { sessionID: "A", idle: Date.now() + 1000 } } // back to A
  yield { type: "session.execution.started", created: 2, data: { sessionID: "A" } }
  yield { type: "permission.asked", data: { sessionID: "childA" } } // live again: the ownership was never poisoned
  yield { type: "session.execution.succeeded", data: { sessionID: "A" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch to B
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch back to A
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's next turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childA live again under A
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // and the turn's end
	)
}

// TestOpenCodePluginGlobalFormOwnership covers the one prompt identity that
// carries no session: a 2.x "global" MCP form. Its late cancellation must not
// clear the prompt count of the conversation the pane moved to (the form is
// owned by the conversation that was latched when it opened, told apart by
// form id), while a form opened under the current conversation still reports
// and overlaps normally.
func TestOpenCodePluginGlobalFormOwnership(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.execution.started", created: 1, data: { sessionID: "A" } }
  yield { type: "form.created", data: { form: { id: "F1", sessionID: "global" } } } // F1 opens under A
  yield { type: "session.execution.interrupted", data: { sessionID: "A" } } // A's turn ends; F1 is left open
  yield { type: "session.viewed", data: { sessionID: "B", idle: Date.now() + 1000 } } // the pane moves on
  yield { type: "session.execution.started", created: 1, data: { sessionID: "B" } }
  yield { type: "session.created", data: { sessionID: "childB", parentID: "B" } }
  yield { type: "permission.asked", data: { sessionID: "childB" } } // B's tree asks
  yield { type: "form.cancelled", data: { id: "F1", sessionID: "global" } } // stale: F1 belongs to A, must not clear childB's prompt
  yield { type: "form.created", data: { form: { id: "F2", sessionID: "global" } } } // F2 opens under B
  yield { type: "form.replied", data: { id: "F2", sessionID: "global" } } // live: overlaps childB, count stays open
  yield { type: "session.execution.succeeded", data: { sessionID: "B" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // A's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // F1 opens, gated under A
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // A's turn ends
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // the switch: viewed implies idle
		`{"hook_event_name":"session.busy","session_id":"A"}`,     // B's turn
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // childB's prompt
		`{"hook_event_name":"permission.asked","session_id":"A"}`, // F2 opens under B
		`{"hook_event_name":"session.idle","session_id":"A"}`,     // B's turn ends — F1's cancellation and F2's reply produced nothing
	)
}

// TestOpenCodePluginHotUpgradeKeepsReportingId covers the upgrade from the
// pre-switch template: the old module's ROOT_ID latch survives the re-import
// (shared through globalThis) but no reporting id does, so setup() seeds
// FLEET_ID from the inherited root — otherwise the first post-upgrade report
// latches whatever session happens to speak first and files the row's status
// under an id fleet does not know.
func TestOpenCodePluginHotUpgradeKeepsReportingId(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
// What the previous module left behind: a root latch, no reporting id.
globalThis[Symbol.for("fleet.status.rootId")] = "oldroot"
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.viewed", data: { sessionID: "other", idle: Date.now() + 1000 } } // the switch that must not steal the reporting id
  yield { type: "session.execution.started", created: 1, data: { sessionID: "other" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "other" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.idle","session_id":"oldroot"}`,
		`{"hook_event_name":"session.busy","session_id":"oldroot"}`,
		`{"hook_event_name":"session.idle","session_id":"oldroot"}`,
	)
}

// TestOpenCodePluginHotUpgradeAssumesInheritedTurnLive covers the upgrade
// from a template that kept no turn state: the old module can leave a prompt
// counted with the turn's terminal lost in the re-import gap. The inherited
// root is taken as live, so a fresh viewed acknowledgement still closes that
// turn out and the next start reports busy instead of holding it back.
func TestOpenCodePluginHotUpgradeAssumesInheritedTurnLive(t *testing.T) {
	got := runOpenCodePlugin(t, `
process.argv.push("--stdio")
// What the previous module left behind: a root latch with a prompt counted.
globalThis[Symbol.for("fleet.status.rootId")] = "R"
globalThis[Symbol.for("fleet.status.openPrompts")] = 1
let finished
const done = new Promise((resolve) => { finished = resolve })
const stop = p.setup({ event: { subscribe: async function* () {
  yield { type: "session.viewed", data: { sessionID: "R", idle: 1900 } } // R's turn ended in the gap
  yield { type: "session.execution.started", created: 2000, data: { sessionID: "R" } }
  yield { type: "session.execution.succeeded", data: { sessionID: "R" } }
  finished()
} } })
await done
stop()
`)
	assertHookCalls(t, got,
		`{"hook_event_name":"session.idle","session_id":"R"}`, // the lost terminal, closed out
		`{"hook_event_name":"session.busy","session_id":"R"}`, // the next turn is not held back
		`{"hook_event_name":"session.idle","session_id":"R"}`,
	)
}
