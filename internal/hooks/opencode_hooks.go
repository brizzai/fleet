package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
)

// openCodePluginName is fleet's status plugin file. fleet owns this file by name
// and only ever rewrites it — other plugins in the dir are left untouched.
const openCodePluginName = "fleet-status.ts"

// GetOpenCodeConfigDir returns OpenCode's config directory
// ($OPENCODE_CONFIG_DIR or ~/.config/opencode).
func GetOpenCodeConfigDir() string {
	if dir := os.Getenv("OPENCODE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".config", "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

// openCodePluginTemplate is the generated status plugin. OpenCode auto-loads
// .ts files from <config>/plugin/ (1.x and 2.x alike). The single %s is the
// JSON-quoted absolute fleet binary path.
//
// One default export serves both plugin APIs, because neither accepts the
// other's shape:
//
//	1.x: a default export with an `id` is a plugin module (1.4+); its server()
//	     returns the hooks, and the `event` hook sees every bus event as
//	     { type, properties }. A bare { id, setup } fails here with "must
//	     default export an object with server()".
//	2.x: the default export must be { id, setup } (or { id, effect }); setup(ctx)
//	     reads ctx.event.subscribe(), which yields { type, data }. The old named
//	     `export const FleetStatus` fails here with "Plugin must export a default
//	     definition with an id and an effect or setup function."
//
// 2.x's loader ignores the extra `server` key, so one file loads in both,
// whichever OpenCode reads the shared config dir. 1.19's newer core loader also
// accepts { id, setup } and calls setup() next to the server() hook; setup()
// stays inert there only because a 1.x process never has --stdio in its argv (see
// below). That gate is load-bearing for 1.x too: replace it with anything a 1.x
// process can satisfy and 1.x reports every status twice. The 2.x contract below
// was verified against OpenCode 2.0.28 (and the 1.x side against 1.19.0); re-check
// it when bumping either.
//
// Status mapping (root session only — see the rootId latch below):
//
//	OpenCode 1.x
//	session.status{busy} -> session.busy        (running)
//	session.status{idle} / session.idle -> session.idle  (finished)
//	session.error        -> session.error        (error)
//	question.asked / permission.asked     -> permission.asked   (waiting)
//	question.replied / question.rejected /
//	  permission.replied                  -> permission.replied (running)
//
//	OpenCode 2.x
//	session.execution.started             -> session.busy       (running)
//	session.execution.succeeded /
//	  session.execution.interrupted       -> session.idle       (finished)
//	session.execution.failed              -> session.error      (error)
//	form.created / permission.asked       -> permission.asked   (waiting)
//	form.replied / form.cancelled /
//	  permission.replied                  -> permission.replied (running)
//
// Both event families are live in OpenCode 1.14.x and mean different things:
// question.* is the AskUserQuestion tool prompt, permission.* is a tool-permission
// prompt (the user's `permission: ask` config). Listening for only one is the
// stuck-at-running bug — add to these branches, never swap them. 2.x keeps
// permission.* and moves the question tool onto forms (form.*), whose session id
// sits at data.form.sessionID on form.created. 2.x publishes no session.status
// at all: a busy period is exactly one session.execution.started and one
// terminal event, so nothing re-announces busy after a reply and the 2.x
// *.replied / form.cancelled branches are the only way back to running.
//
// Root vs sub-agents: the session fleet launched goes busy before any sub-agent
// spawns, so the first event carrying a sessionID is always the root. The plugin
// latches that id and forwards only its events; sub-agent sessions (distinct ids)
// are ignored — no dependency on observing their session.created event, which the
// old exclude-set could miss and then falsely flip the root to Finished. 2.x
// sub-agents are child sessions with their own ids too. A 2.x MCP elicitation
// form is owned by the "global" sentinel rather than a session, so it never
// takes the latch. 2.x re-imports a local plugin whenever the file changes
// (fleet rewrites it after an upgrade), so the latch lives on globalThis rather
// than in the module: a fresh module-level latch would take whichever session
// speaks next, a sub-agent if one is mid-turn.
//
// Which process runs it: 2.x moved plugins out of the TUI fleet launched and into
// a server. A private server (`serve --stdio`, see OpenCode's
// packages/cli/src/services/standalone.ts: `opencode --standalone`, any client
// when the background service is turned off, `opencode run --standalone`,
// `opencode acp`) belongs to one client and inherits its environment, so
// FLEET_INSTANCE_ID and the root latch mean what they do in 1.x. The shared
// background service (`serve --service`) hosts every client's sessions and keeps the environment of whichever client
// spawned it, so forwarding from there would file one pane's status, and its
// session id, under another pane's FLEET_INSTANCE_ID. setup() therefore stays
// inert unless it runs in a --stdio server.
//
// Each is forwarded to `fleet hook-handler`, reusing the agent-neutral status
// pipeline. The call is synchronous (spawnSync): it blocks until the status file
// is written, which guarantees ordering (no stale status from out-of-order
// spawns) and — crucially — flushes the final status before OpenCode exits,
// rather than losing it to an unflushed async child. This matches how Claude and
// Codex hooks block synchronously. notify() early-returns when FLEET_INSTANCE_ID
// is unset, so a standalone (non-fleet) opencode session that loads this global
// plugin never forks fleet. Only a few events per turn are forwarded.
const openCodePluginTemplate = `// fleet status plugin — auto-generated by fleet; do not edit.
// Reports OpenCode session status to fleet via the fleet hook-handler.
import { spawnSync } from "node:child_process"

const FLEET_BIN = %s

function notify(event, sessionID) {
  if (!process.env.FLEET_INSTANCE_ID) return
  try {
    spawnSync(FLEET_BIN, ["hook-handler", "--fleet-hook"], {
      input: JSON.stringify({ hook_event_name: event, session_id: sessionID }),
      stdio: ["pipe", "ignore", "ignore"],
      env: process.env,
    })
  } catch (_e) {}
}

// The rootId latch holds the first session fleet's process drives. Its turn goes
// busy before any sub-agent spawns, so the first sessionID seen is the root;
// events from any other (sub-agent) session are then ignored. "global" owns
// OpenCode 2.x MCP elicitation forms; it is not a session, so it never latches.
// It is kept on globalThis because 2.x re-imports this file when fleet rewrites
// it, and a module-level latch would start over.
const ROOT_ID = Symbol.for("fleet.status.rootId")

function isRoot(sessionID) {
  if (!sessionID || sessionID === "global") return false
  if (globalThis[ROOT_ID] === undefined) globalThis[ROOT_ID] = sessionID
  return sessionID === globalThis[ROOT_ID]
}

// OpenCode 1.x bus event: { type, properties }.
function onEventV1(event) {
  const type = event && event.type
  const props = (event && event.properties) || {}
  const sessionID = props.sessionID
  if (!isRoot(sessionID)) return
  if (type === "session.status") {
    const st = props.status && props.status.type
    if (st === "busy") notify("session.busy", sessionID)
    else if (st === "idle") notify("session.idle", sessionID)
  } else if (type === "session.idle") {
    notify("session.idle", sessionID)
  } else if (type === "session.error") {
    notify("session.error", sessionID)
  } else if (type === "question.asked" || type === "permission.asked") {
    notify("permission.asked", sessionID)
  } else if (
    type === "question.replied" ||
    type === "question.rejected" ||
    type === "permission.replied"
  ) {
    notify("permission.replied", sessionID)
  }
}

// OpenCode 2.x event: { type, data }. form.created nests its session in data.form.
function onEventV2(event) {
  const type = event && event.type
  const data = (event && event.data) || {}
  const sessionID = data.sessionID || (data.form && data.form.sessionID)
  if (!isRoot(sessionID)) return
  if (type === "session.execution.started") {
    notify("session.busy", sessionID)
  } else if (
    type === "session.execution.succeeded" ||
    type === "session.execution.interrupted"
  ) {
    notify("session.idle", sessionID)
  } else if (type === "session.execution.failed") {
    notify("session.error", sessionID)
  } else if (type === "form.created" || type === "permission.asked") {
    notify("permission.asked", sessionID)
  } else if (
    type === "form.replied" ||
    type === "form.cancelled" ||
    type === "permission.replied"
  ) {
    notify("permission.replied", sessionID)
  }
}

export default {
  id: "fleet-status",
  // OpenCode 1.x
  server: async (_input) => ({
    event: async ({ event }) => onEventV1(event),
  }),
  // OpenCode 2.x. Only a private (--stdio) server belongs to this TUI; the shared
  // background service carries whichever client's FLEET_INSTANCE_ID spawned it.
  // 1.19+ calls setup() too, and this gate is what keeps it quiet there.
  setup(ctx) {
    if (!process.argv.includes("--stdio")) return
    const controller = new AbortController()
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) onEventV2(event)
      } catch (_e) {}
    })()
    return () => controller.abort()
  },
}
`

// InjectOpenCodePlugin writes (or refreshes) fleet's status plugin into
// OpenCode's global plugin dir (<configDir>/plugin/fleet-status.ts). Returns
// true if the file was written (changed). Idempotent: the resolved fleet binary
// path is baked in, so the file is rewritten only when fleet moves/upgrades.
//
// Unlike Claude/Codex (declarative hook JSON), OpenCode's hook mechanism is a
// JS/TS plugin, so fleet ships this generated file rather than merging JSON.
func InjectOpenCodePlugin(configDir string) (bool, error) {
	pluginDir := filepath.Join(configDir, "plugin")
	pluginPath := filepath.Join(pluginDir, openCodePluginName)

	binLiteral, err := json.Marshal(FleetBinaryPath())
	if err != nil {
		return false, fmt.Errorf("marshal fleet path: %w", err)
	}
	content := fmt.Sprintf(openCodePluginTemplate, binLiteral)

	if existing, err := os.ReadFile(pluginPath); err == nil {
		if bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace([]byte(content))) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", openCodePluginName, err)
	}

	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return false, fmt.Errorf("create plugin dir: %w", err)
	}
	tmpPath := pluginPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("write %s.tmp: %w", openCodePluginName, err)
	}
	if err := os.Rename(tmpPath, pluginPath); err != nil {
		os.Remove(tmpPath)
		debuglog.Logger.Error("opencode plugin: rename failed", "err", err)
		return false, fmt.Errorf("rename %s: %w", openCodePluginName, err)
	}

	debuglog.Logger.Info("opencode status plugin injected", "path", pluginPath)
	return true, nil
}
