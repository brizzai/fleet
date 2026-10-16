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
// was verified against OpenCode 2.0.28 and its stream and switch handling
// against 2.0.30 (the 1.x side against 1.19.0); re-check it when bumping either.
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
//	session.execution.failed              -> session.error, then session.idle
//	                                         (error, then finished — as 1.x)
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
// takes the latch. Prompts are the exception to root-only: a sub-agent's or a
// "global" prompt still blocks the root's turn, so it is reported under the
// root (see reportAs). 2.x re-imports a local plugin whenever the file changes
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
// The 2.x subscription is the in-process bus (unbounded — the 4096-event cap
// belongs to the HTTP event feed, not to plugins), but the stream still ends
// for good on its first error: the adapter encodes every event against the
// event schema on the way out (packages/plugin/src/promise/adapter.ts), and
// one failure fails the stream. setup() therefore re-enters the subscription
// after a pause instead of exiting. OpenCode disposes a plugin's previous
// activation (our cleanup aborts the loop) before it sets up a new revision
// (packages/core/src/plugin.ts); an import-generation counter, re-checked per
// dispatch, still guards a host that does not, so a re-imported module's
// loop never runs beside the stale one. It is kept per location: OpenCode
// runs one plugin instance per directory, each seeing only its own
// directory's events, and one directory's setup() must never stop another's
// loop. The feed is live-only, so a re-entry cannot replay an old turn;
// events lost in the gap are gone, and a busy period has exactly one start
// and one terminal, so a start (or a viewed acknowledgement) for a
// conversation still believed live proves its terminal was lost and is
// applied then.
//
// A switch to another session inside the same TUI moves the latch: 2.x's TUI
// reports the conversation its user is looking at through session.viewed
// (mapped to its root session first — packages/tui/src/context/session-tabs.tsx
// root()s the route before posting — so a sub-agent never fires it), and
// without following it the latch would keep the conversation the pane started
// on and reject the active session's events until restart. session.viewed
// only fires for an idle, unread session while the TUI is focused, so the
// latch moves at the switch when the target session already finished and
// after its current turn ends otherwise — and switching back to an idle
// session that is already read fires nothing, so the latch also moves on the
// first user message typed into a conversation known to be a root
// (session.inbox.enqueued with a user item): one the latch has held, or one
// created or forked during this server's life. A sub-agent's task is a user
// item too, but a child always has a parent, so it is never a known root
// (background completions arrive as synthetic items). A message typed into a
// conversation mid-turn joins its running busy period instead of starting a
// new one, so that switch reports busy itself; the TUI hides its composer
// while any prompt of the routed conversation's tree is pending, so none can
// be open then. A viewed switch reports idle (or waiting, while a background
// sub-agent of the target still has a prompt open), and only on an
// acknowledgement newer than the target's last turn start: the TUI retries
// failed view posts with backoff, and a retried acknowledgement for the
// previous turn's watermark can be published while the next turn runs,
// because the server's idle watermark is only advanced at terminals. A
// viewed acknowledgement for the conversation already under the latch
// reports nothing unless that conversation is still believed live, which
// means its terminal was lost with the stream: it is applied then (the
// prompts asked before it closed, idle reported unless a later one is still
// open), and a live background sub-agent's prompt after a turn that did end
// is left alone. A prompt's conversation is established
// from verified ancestry — its session's creation event names its parent,
// and the chain walks up to the conversation it belongs to, and only a chain
// terminating at a known root is an answer: an incomplete one (a gap lost an
// intermediate edge) reads as unknown, never as a wrong owner — so a child
// of a conversation the pane has left is never reported no matter when it
// first prompts, however many switches ago; its prompts are counted against
// that conversation instead, and come back with it the moment it is under
// the latch again. A known root owns its own prompts, the departing one
// included, since it asks under its own id. A 2.x "global" MCP form has no
// ancestry and is owned by the conversation latched when it opened, keyed by
// form id; a prompter whose creation event was missed is accepted under the
// current root without being recorded, so a stream gap can never poison the
// ownership against its true conversation. Reports keep carrying the
// conversation the pane reports for regardless (see notify — seeded from an
// inherited root on a hot upgrade, so a rewrite never changes the reporting
// id either): status follows the pane, the row's identity stays put. 1.x
// keeps today's first-session behavior — it has no verified session.viewed
// equivalent.
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

// The conversation fleet's pane reports for, latched from the first report
// and never moved. A switch inside the TUI changes which session drives the
// row, but reporting the switched-to session's own id would run it through
// fleet's Claude rotation checks, which have no transcripts to consult for
// OpenCode and would freeze the row on the old id. (If the user switches
// before the launched session ever reports, the latch takes the switched-to
// conversation — the same id the owner would have latched pre-switch.) Kept
// on globalThis for the same reason as the latch below.
const FLEET_ID = Symbol.for("fleet.status.fleetId")

function notify(event, sessionID) {
  if (!process.env.FLEET_INSTANCE_ID) return
  if (globalThis[FLEET_ID] === undefined) globalThis[FLEET_ID] = sessionID
  try {
    spawnSync(FLEET_BIN, ["hook-handler", "--fleet-hook"], {
      input: JSON.stringify({ hook_event_name: event, session_id: globalThis[FLEET_ID] }),
      stdio: ["pipe", "ignore", "ignore"],
      env: process.env,
    })
  } catch (_e) {}
}

// The rootId latch holds the first session fleet's process drives. Its turn goes
// busy before any sub-agent spawns, so the first sessionID seen is the root
// (on 2.x the latch is taken in onEventV2, ahead of the prompt routing, and a
// session with recorded ancestry latches its root conversation instead,
// should a gap lose the root's first start; isRoot latches only on 1.x);
// events from any other (sub-agent) session are then ignored. "global" owns
// OpenCode 2.x MCP elicitation forms; it is not a session, so it never latches.
// It is kept on globalThis because 2.x re-imports this file when fleet rewrites
// it, and a module-level latch would start over.
const ROOT_ID = Symbol.for("fleet.status.rootId")

// Import generations, one per location: 2.x re-imports this file when fleet
// rewrites it, and an older setup() loop must not keep resubscribing beside
// the newer one. OpenCode 2.x disposes the old activation first, so this is
// a guard for a host that does not. OpenCode also calls setup() once per
// directory it serves, and those instances run side by side by design, so
// the counter is keyed by location — a second directory's setup() must not
// stop the first's loop. Shared through globalThis like the latches, so
// every import sees one map.
const GENERATION = Symbol.for("fleet.status.generation")

// The sessions positively known to be conversation roots: the latch's own
// history, plus every session whose creation event named no parent (a
// sub-agent's always names one) and every fork. An ancestry walk only trusts
// a chain that terminates here — a
// terminal with no parent edge could be a root, or could be an intermediate
// whose edge was lost with the stream, and the second guess is not worth
// making: it would cache a wrong owner and drop a live conversation's
// prompts.
const ROOTS_SEEN = Symbol.for("fleet.status.rootsSeen")

function rootsSeen() {
  return globalThis[ROOTS_SEEN] ?? (globalThis[ROOTS_SEEN] = new Set())
}

// Records a known root, moving it to the newest position so the bound (see
// BOUNDED) never evicts a conversation the latch has just taken.
function knownRoot(sessionID) {
  rootsSeen().delete(sessionID)
  rootsSeen().add(sessionID)
}

// On 2.x the latch is already taken in onEventV2, ahead of reportAs, so the
// latch here only ever fires for 1.x.
function isRoot(sessionID) {
  if (!sessionID || sessionID === "global") return false
  if (globalThis[ROOT_ID] === undefined) {
    globalThis[ROOT_ID] = sessionID
    knownRoot(sessionID)
  }
  return sessionID === globalThis[ROOT_ID]
}

// A switch to another session in the same TUI moves the latch to the
// conversation the TUI is looking at (session.viewed, or a user message typed
// into a known root — see the notes above the template). The open-prompt
// count follows the gate: the departing conversation's is kept with the
// counts of conversations away from the latch (see AWAY_PROMPTS), and the
// target's comes back, since a viewed target is idle but a background
// sub-agent of it may still be asking. The departing conversation's later
// prompts are counted there rather than reported (see reportAs), so they
// never land under the new gate.
// A viewed event proves its target idle only when the acknowledgement is
// newer than the target's last turn start, both read from producer-side
// timestamps (the event envelopes' "created", the viewed payload's "idle"):
// the TUI retries failed view posts with backoff, and a retried
// acknowledgement for the previous turn's watermark can be published while
// the next turn runs — the server's idle watermark is never cleared at turn
// start, only advanced at terminals (packages/core/src/session/projector.ts).
// Consumption time would not do here: a slow subscriber can handle a turn's
// start after that turn already ended, which would make a genuine
// acknowledgement look stale.
function viewedAssertsIdle(data) {
  if (data.idle === undefined) return false
  const turns = globalThis[TURNS]
  const started = turns === undefined ? undefined : turns.get(data.sessionID)
  return started === undefined || data.idle > started
}

// Turn bookkeeping, recorded before the root-only gate: a switch target's
// turn may have started while another conversation held the latch, and its
// start time is what tells a delayed viewed acknowledgement from a live idle
// signal.
const TURNS = Symbol.for("fleet.status.turns")

// The sessions whose busy period is running: a start seen and no terminal
// yet, recorded before the root-only gate like TURNS. A busy period has
// exactly one start and one terminal, so a second start, or a fresh viewed
// acknowledgement, for a session still in here proves its terminal was lost
// with the stream.
const LIVE = Symbol.for("fleet.status.live")

function live() {
  return globalThis[LIVE] ?? (globalThis[LIVE] = new Set())
}

function switchRoot(sessionID) {
  if (!sessionID || sessionID === "global" || globalThis[ROOT_ID] === sessionID) return
  // The departing conversation stays a known root, so it owns its late
  // prompts (see rootAncestor): its asks must not report the new
  // conversation as waiting, and its replies must not clear the new
  // conversation's open prompts — they count against its own, kept away.
  const from = globalThis[ROOT_ID]
  if (from !== undefined && globalThis[OPEN_PROMPTS]) awayPrompts().set(from, globalThis[OPEN_PROMPTS])
  knownRoot(sessionID)
  globalThis[ROOT_ID] = sessionID
  globalThis[OPEN_PROMPTS] = awayPrompts().get(sessionID) || 0
  awayPrompts().delete(sessionID)
}

// A permission or question prompt from any session this process drives (a
// sub-agent's child session, or a 2.x "global" MCP form) blocks the root's turn
// until the user answers, so it is gated under the root. Lifecycle events
// (busy/idle/error) stay root-only. The root goes busy before any sub-agent
// spawns, so it has latched by the time a child prompts. A prompt's
// conversation comes from verified ancestry first — the creation event's
// parent chain, trusted only when it terminates at a conversation known to
// be a root — with "global" forms owned by the conversation latched when
// they opened (keyed by form id, since "global" names no session) and a
// known root owning its own; a prompter with no verified ancestry is
// accepted under the current root and not recorded, so a missed creation
// event or a lost intermediate edge can never poison the ownership. Kept on
// globalThis so a re-import keeps it all.
const PROMPT_EVENTS = new Set([
  "question.asked", "question.replied", "question.rejected",
  "form.created", "form.replied", "form.cancelled",
  "permission.asked", "permission.replied",
])

const PROMPT_OWNERS = Symbol.for("fleet.status.promptOwners")

// Every open 2.x prompt, keyed by its id: the conversation its ask was
// counted under, the ask's producer timestamp ("created") and the
// subscription it arrived on (see EPOCH). A reply settles the count its ask
// went to, wherever the latch is now and however the prompter's ownership
// has resolved since (an unknown prompter's ask counts under the
// conversation latched then; the note is never an ownership record). And a
// turn end proven lost closes only the asks made before it, not a
// background sub-agent's made after it. An entry goes with its reply or
// with its conversation's turn end; bounded like SETTLED_ASKS, since a
// prompt can outlive both unanswered — an evicted ask's reply falls back to
// ownership, and a repair closes it like any prompt counted without a note.
const OPEN_ASKS = Symbol.for("fleet.status.openAsks")

// The ids of prompts a turn end (or a typed message's switch) closed while
// they were still noted open: a reply that outlives them is dropped by id
// rather than taken for a reply to whatever is open now, in this
// conversation or another. Bounded,
// since a permission interrupted mid-wait is removed without any reply.
const SETTLED_ASKS = Symbol.for("fleet.status.settledAsks")

// Numbers every subscription — each setup()'s first and every re-entry
// alike, across locations and re-imports — so asks and starts record the one
// that delivered them (see START_EPOCHS).
const EPOCH = Symbol.for("fleet.status.epoch")

// The subscription dispatching right now: onEventV2 runs synchronously per
// event, and the loop sets this just before each dispatch.
let delivering = 0

// The subscription that delivered each session's latest start. A turn end
// lost with the stream fell after that subscription ended — the event would
// have arrived on it otherwise — so asks delivered no later than it are
// provably closed by that turn end; any later one's closure is unproven,
// and it stays open.
const START_EPOCHS = Symbol.for("fleet.status.startEpochs")

function openAsks() {
  return globalThis[OPEN_ASKS] ?? (globalThis[OPEN_ASKS] = new Map())
}

function noteAsk(type, pid, conv, at) {
  if (pid === undefined || !isAsk(type)) return
  const asks = openAsks()
  asks.set(pid, { conv, at, epoch: delivering })
  if (asks.size > 256) asks.delete(asks.keys().next().value)
  // An id is never both open and settled.
  const settled = globalThis[SETTLED_ASKS]
  if (settled !== undefined) settled.delete(pid)
}

// Drops the noted asks a conversation's turn end closed — all of them, or
// only those "stillOpen" rejects — recording them as settled, and returns
// how many stay open.
function forgetAsks(conv, stillOpen) {
  const asks = globalThis[OPEN_ASKS]
  if (asks === undefined) return 0
  const settled = globalThis[SETTLED_ASKS] ?? (globalThis[SETTLED_ASKS] = new Set())
  let open = 0
  for (const [pid, ask] of asks) {
    if (ask.conv !== conv) continue
    if (stillOpen !== undefined && stillOpen(ask)) {
      open++
      continue
    }
    asks.delete(pid)
    settled.add(pid)
    if (settled.size > 256) settled.delete(settled.values().next().value)
  }
  return open
}

// The prompt's own id, which its reply names: a permission's id and the
// reply's requestID, a form's id on both sides. 2.x only.
function promptID(type, data) {
  if (type === "permission.asked") return data.id
  if (type === "permission.replied") return data.requestID
  if (type === "form.created") return data.form && data.form.id
  if (type === "form.replied" || type === "form.cancelled") return data.id
  return undefined
}

function isAsk(type) {
  return type === "question.asked" || type === "form.created" || type === "permission.asked"
}

// Verified ancestry: a session's creation event names its parent (forks use
// a separate fork id, so parentID is purely sub-agent descent), and the
// chain walks up to the conversation a session belongs to — knowable no
// matter which conversation the pane is looking at when it first prompts.
const PARENTS = Symbol.for("fleet.status.parents")

// The ownership key for a prompt: the form id of a 2.x "global" MCP form
// ("global" names no session, so its asks and replies are told apart by form
// id), or null for prompts carrying a real session id. (Callers pass no key
// at all on the 1.x path, where there are no forms and the latch never
// moves — undefined skips ownership entirely, so a new 2.x caller that means
// "use ancestry" must pass null, not nothing.)
function promptKey(type, data) {
  if (type === "form.created") return data.form && data.form.sessionID === "global" ? "form:" + data.form.id : null
  if (type === "form.replied" || type === "form.cancelled") return data.sessionID === "global" ? "form:" + data.id : null
  return null
}

// rootAncestor walks a session's parent chain to the conversation it belongs
// to; a known root belongs to itself. Only a chain terminating at a
// positively known root is an answer; one that runs out of edges first is
// incomplete (a gap lost an intermediate edge) and reads as unknown rather
// than as a wrong root. Bounded, so a malformed cycle reads as unknown too;
// undefined when the session is no known root and has no recorded parent
// edge at all.
function rootAncestor(sessionID) {
  const seen = globalThis[ROOTS_SEEN]
  if (seen !== undefined && seen.has(sessionID)) return sessionID
  const parents = globalThis[PARENTS]
  if (parents === undefined) return undefined
  let id = parents.get(sessionID)
  if (id === undefined) return undefined
  for (let i = 0; i < 64; i++) {
    if (seen !== undefined && seen.has(id)) return id
    const next = parents.get(id)
    if (next === undefined) return undefined
    id = next
  }
  return undefined
}

function reportAs(type, sessionID, key, pid, at) {
  if (!sessionID || !PROMPT_EVENTS.has(type)) return sessionID
  if (globalThis[ROOT_ID] === undefined) return sessionID
  // A reply to a noted ask settles the count that ask went to (see
  // OPEN_ASKS).
  if (key !== undefined && pid !== undefined && !isAsk(type)) {
    const settled = globalThis[SETTLED_ASKS]
    if (settled !== undefined && settled.delete(pid)) return null
    const asks = globalThis[OPEN_ASKS]
    const ask = asks === undefined ? undefined : asks.get(pid)
    if (ask !== undefined) {
      asks.delete(pid)
      if (ask.conv === globalThis[ROOT_ID]) return ask.conv
      countAway(type, ask.conv)
      return null
    }
  }
  if (key || (sessionID !== globalThis[ROOT_ID] && sessionID !== "global")) {
    const id = key ?? sessionID
    const owners = globalThis[PROMPT_OWNERS] ?? (globalThis[PROMPT_OWNERS] = new Map())
    let owner = owners.get(id)
    if (owner === undefined) {
      if (key === null) {
        const ancestor = rootAncestor(id)
        if (ancestor !== undefined) {
          owner = ancestor
          owners.set(id, ancestor)
        }
      } else if (type === "form.created") {
        // A "global" form has no ancestry: its owner is the conversation
        // latched when it opened, which is the ask itself.
        owner = globalThis[ROOT_ID]
        owners.set(id, owner)
      }
    }
    if (owner !== undefined && owner !== globalThis[ROOT_ID]) {
      countAway(type, owner)
      noteAsk(type, pid, owner, at)
      return null
    }
    // An unknown prompter — its creation event was missed with the stream,
    // or predates the subscription — is accepted under the root it prompts
    // under and deliberately not recorded as owned: a guess here could poison
    // the map against its true conversation on a later switch.
  }
  if (key !== undefined) noteAsk(type, pid, globalThis[ROOT_ID], at)
  return globalThis[ROOT_ID]
}

// Prompts can overlap (two sub-agents each asking for permission), so the open
// ones are counted, on globalThis like the latch so a re-import keeps them. A
// reply reports running only once none is left open, and busy is held back
// while one is (1.x re-announces busy straight after every reply). A reply
// with nothing open is stale, e.g. a form cancelled by an interrupt that already
// ended the turn, and would flip a finished turn back to running. The turn's
// end (idle/error) clears the count, so a reply that never arrives can't hold
// the next turn at waiting. The count is per conversation (see AWAY_PROMPTS);
// on 2.x every prompt is also noted by id (see OPEN_ASKS), so a reply that
// outlives its turn's end is dropped by id. Where no id was noted — 1.x, or
// a prompt counted before a hot upgrade — such a reply can still decrement
// a newer prompt's count, a slack kept because fleet's status file holds a
// single waiting/running state per pane regardless.
const OPEN_PROMPTS = Symbol.for("fleet.status.openPrompts")

// The open-prompt counts of conversations not under the latch, keyed by
// conversation. Their prompts are not reported, but still counted, asks and
// replies alike, so a return finds the count as it stands: a background
// sub-agent can ask after its root's turn ended while the pane looks
// elsewhere, and a prompt open when the pane left can be answered meanwhile.
// A conversation's turn end clears its entry, as it clears OPEN_PROMPTS for
// the latched one.
const AWAY_PROMPTS = Symbol.for("fleet.status.awayPrompts")

function awayPrompts() {
  return globalThis[AWAY_PROMPTS] ?? (globalThis[AWAY_PROMPTS] = new Map())
}

function countAway(type, owner) {
  const away = awayPrompts()
  const open = away.get(owner) || 0
  away.delete(owner) // re-set as newest: the bound evicts the stalest
  if (isAsk(type)) away.set(owner, open + 1)
  else if (open > 1) away.set(owner, open - 1)
}

function promptAsked(sessionID) {
  globalThis[OPEN_PROMPTS] = (globalThis[OPEN_PROMPTS] || 0) + 1
  notify("permission.asked", sessionID)
}

function promptReplied(sessionID) {
  const open = globalThis[OPEN_PROMPTS] || 0
  if (open === 0) return
  globalThis[OPEN_PROMPTS] = open - 1
  if (open === 1) notify("permission.replied", sessionID)
}

function busy(sessionID) {
  if (!globalThis[OPEN_PROMPTS]) notify("session.busy", sessionID)
}

function turnEnded(event, sessionID) {
  globalThis[OPEN_PROMPTS] = 0
  forgetAsks(sessionID)
  notify(event, sessionID)
}

// OpenCode 1.x bus event: { type, properties }.
function onEventV1(event) {
  const type = event && event.type
  const props = (event && event.properties) || {}
  const sessionID = reportAs(type, props.sessionID)
  if (!isRoot(sessionID)) return
  if (type === "session.status") {
    const st = props.status && props.status.type
    if (st === "busy") busy(sessionID)
    else if (st === "idle") turnEnded("session.idle", sessionID)
  } else if (type === "session.idle") {
    turnEnded("session.idle", sessionID)
  } else if (type === "session.error") {
    turnEnded("session.error", sessionID)
  } else if (type === "question.asked" || type === "permission.asked") {
    promptAsked(sessionID)
  } else if (
    type === "question.replied" ||
    type === "question.rejected" ||
    type === "permission.replied"
  ) {
    promptReplied(sessionID)
  }
}

// Per-session bookkeeping lives as long as the server, so each of these maps
// and sets is bounded, least recently written first: an entry updated in
// place (a start, a known root, an away count) is re-inserted as newest, so
// a live conversation's state outlives the sub-agents that came and went.
// An evicted entry reads as never seen — ancestry incomplete, an owner
// recomputed, a start or watermark unknown — the same degraded paths a
// stream gap takes.
const BOUNDED = [ROOTS_SEEN, PARENTS, PROMPT_OWNERS, TURNS, START_EPOCHS, LIVE, AWAY_PROMPTS]

function trim() {
  for (const key of BOUNDED) {
    const entries = globalThis[key]
    while (entries !== undefined && entries.size > 4096) entries.delete(entries.keys().next().value)
  }
}

// OpenCode 2.x event: { type, data }. form.created nests its session in data.form.
function onEventV2(event) {
  trim()
  const type = event && event.type
  const data = (event && event.data) || {}
  // The TUI's switch signal: moves the latch before the root-only gate below
  // would reject the newly active session's events. A stale acknowledgement —
  // one whose watermark predates the target's last turn start — is a retry
  // artifact, not a focus signal: the TUI reposts failed acknowledgements
  // with backoff, and acting on one would move the latch off a live
  // conversation. A genuine switch back to a mid-turn conversation reads as
  // stale too and waits for the turn's end, when a fresh acknowledgement
  // follows. A fresh acknowledgement proves its target idle, so a target
  // still believed live lost its terminal with the stream: under the latch,
  // that terminal is applied now; anywhere else, its busy period is closed
  // before the switch. A fresh acknowledgement for the latched conversation
  // whose turn did end reports nothing — idle was reported at the terminal,
  // and a background sub-agent may have asked since.
  if (type === "session.viewed") {
    if (!viewedAssertsIdle(data) || !data.sessionID || data.sessionID === "global") return
    const lost = live().delete(data.sessionID)
    if (globalThis[ROOT_ID] === data.sessionID) {
      if (lost) {
        // The lost turn end closed what was asked before it; a background
        // sub-agent's ask since then is still open.
        globalThis[OPEN_PROMPTS] = forgetAsks(data.sessionID, (ask) => ask.at !== undefined && ask.at > data.idle)
        notify(globalThis[OPEN_PROMPTS] ? "permission.asked" : "session.idle", data.sessionID)
      }
    } else {
      // A lost turn end closed what the target had asked before it.
      if (lost) {
        const open = forgetAsks(data.sessionID, (ask) => ask.at !== undefined && ask.at > data.idle)
        if (open) awayPrompts().set(data.sessionID, open)
        else awayPrompts().delete(data.sessionID)
      }
      switchRoot(data.sessionID)
      // The conversation is idle, but a background sub-agent of it can
      // still be waiting on the user.
      if (globalThis[OPEN_PROMPTS]) notify("permission.asked", data.sessionID)
      else notify("session.idle", data.sessionID)
    }
    return
  }
  // Verified ancestry: a session's creation names its parent, recorded
  // before any gate — the chain is knowable regardless of what the pane is
  // looking at when the session or its siblings prompt. A creation naming no
  // parent is a conversation root (a sub-agent always names one), and so is
  // a fork: it publishes session.forked instead, whose parentID is the
  // conversation it was copied from, not sub-agent descent.
  if (type === "session.created") {
    if (data.sessionID !== undefined && data.parentID !== undefined && data.parentID !== null) {
      const parents = globalThis[PARENTS] ?? (globalThis[PARENTS] = new Map())
      parents.set(data.sessionID, data.parentID)
    } else if (data.sessionID) {
      rootsSeen().add(data.sessionID)
    }
    return
  }
  if (type === "session.forked") {
    if (data.sessionID) rootsSeen().add(data.sessionID)
    return
  }
  // A user message names the conversation it was typed into, and moves the
  // latch there — the recovery for returning to a conversation that is
  // already read, which session.viewed cannot signal (nothing is unread on
  // the way back). Only a known root qualifies: a sub-agent's task is a user
  // item too, but a child is never a known root. A message for an idle
  // conversation is about to start a turn, whose start reports busy; one for
  // a conversation mid-turn joins the running busy period and no start
  // follows, so the switch reports busy itself. Nothing of that
  // conversation's can be open, or the TUI would not have taken the message,
  // so its count starts at zero whatever was counted while it was away.
  if (type === "session.inbox.enqueued") {
    if (
      data.item &&
      data.item.type === "user" &&
      data.sessionID !== undefined &&
      data.sessionID !== globalThis[ROOT_ID] &&
      rootsSeen().has(data.sessionID)
    ) {
      switchRoot(data.sessionID)
      globalThis[OPEN_PROMPTS] = 0
      forgetAsks(data.sessionID)
      if (live().has(data.sessionID)) notify("session.busy", data.sessionID)
    }
    return
  }
  // Turn bookkeeping, ungated so a switch target's start is known even while
  // another conversation holds the latch. The start time is the event's own
  // "created" timestamp — producer-side, like the viewed watermark it is
  // compared against — so buffered delivery cannot make a turn's start
  // postdate its own terminal. The watermark is retained after terminals:
  // a terminal does not make an older acknowledgement trustworthy — a fresh
  // one always carries the newest watermark, a retry artifact an old one —
  // and forgetting the start would let a stale acknowledgement move the
  // latch once its turn has merely finished.
  let lostTerminal = false
  let lostAfter = 0
  if (type === "session.execution.started" && data.sessionID !== undefined) {
    if (event.created !== undefined) {
      const turns = globalThis[TURNS] ?? (globalThis[TURNS] = new Map())
      turns.delete(data.sessionID) // re-set as newest: the bound evicts the stalest
      turns.set(data.sessionID, event.created)
    }
    lostTerminal = live().delete(data.sessionID)
    live().add(data.sessionID)
    const starts = globalThis[START_EPOCHS] ?? (globalThis[START_EPOCHS] = new Map())
    lostAfter = starts.get(data.sessionID) || 0
    starts.delete(data.sessionID)
    starts.set(data.sessionID, delivering)
  } else if (
    type === "session.execution.succeeded" ||
    type === "session.execution.interrupted" ||
    type === "session.execution.failed"
  ) {
    if (data.sessionID !== undefined) {
      live().delete(data.sessionID)
      awayPrompts().delete(data.sessionID)
      forgetAsks(data.sessionID)
    }
  }
  // Before anything has latched, the first event naming a session latches
  // here, ahead of reportAs, so even a first ask is noted like any other. A
  // session with a recorded parent is a sub-agent, never the conversation
  // fleet launched — a stream gap can lose that conversation's first start
  // and deliver its children's events first — so the latch takes the root
  // conversation its verified ancestry names; with the chain incomplete, the
  // event waits for a root's own. Latched from anything but its own start or
  // end, the root's turn state is unknown: it is taken as live, with its
  // turn started on this subscription — a turn end lost from here on falls
  // after it — so a later lost turn end can still be closed out.
  if (globalThis[ROOT_ID] === undefined) {
    const sid = data.sessionID || (data.form && data.form.sessionID)
    if (sid && sid !== "global") {
      const parents = globalThis[PARENTS]
      const root = parents !== undefined && parents.has(sid) ? rootAncestor(sid) : sid
      if (root === undefined) return
      globalThis[ROOT_ID] = root
      knownRoot(root)
      if (root !== sid || !(typeof type === "string" && type.startsWith("session.execution."))) {
        live().add(root)
        const starts = globalThis[START_EPOCHS] ?? (globalThis[START_EPOCHS] = new Map())
        if (!starts.has(root)) starts.set(root, delivering)
      }
    }
  }
  const sessionID = reportAs(type, data.sessionID || (data.form && data.form.sessionID), promptKey(type, data), promptID(type, data), event.created)
  if (!isRoot(sessionID)) return
  // A start for a conversation already live means the previous turn's
  // terminal event was lost with the stream: the prompts provably asked
  // before it (see START_EPOCHS) are stale, and holding busy back on them
  // would leave the row waiting through a whole new turn. An ask delivered
  // on a later subscription — a background sub-agent's, after the gap —
  // stays open. Otherwise the count stands: a background sub-agent can ask
  // between its root's turns, and that prompt is still open when the next
  // turn starts.
  if (type === "session.execution.started") {
    if (lostTerminal) globalThis[OPEN_PROMPTS] = forgetAsks(sessionID, (ask) => ask.epoch > lostAfter)
    busy(sessionID)
  } else if (
    type === "session.execution.succeeded" ||
    type === "session.execution.interrupted"
  ) {
    turnEnded("session.idle", sessionID)
  } else if (type === "session.execution.failed") {
    // failed is the busy period's only terminal event in 2.x, so nothing else
    // would clear the error; 1.x follows session.error with session.idle.
    turnEnded("session.error", sessionID)
    turnEnded("session.idle", sessionID)
  } else if (type === "form.created" || type === "permission.asked") {
    promptAsked(sessionID)
  } else if (
    type === "form.replied" ||
    type === "form.cancelled" ||
    type === "permission.replied"
  ) {
    promptReplied(sessionID)
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
    // A hot upgrade inherits the old module's root latch, but the template it
    // replaces kept no reporting id: seed one from the inherited root now,
    // before a switch or prompt can move the latch, or the first
    // post-upgrade report latches whatever session happens to speak first
    // and files the row's status under an id fleet does not know. The
    // inherited root also counts as a known root for ancestry walks, and —
    // the replaced template kept no turn state — as live: its turn may be
    // running, or its terminal may have fallen in the re-import gap with a
    // prompt still counted, and only a live session lets the next start or
    // a fresh viewed acknowledgement close that out. LIVE is created here
    // on the first setup() of this template, so later ones never re-seed.
    if (globalThis[LIVE] === undefined) {
      live()
      if (globalThis[ROOT_ID] !== undefined) live().add(globalThis[ROOT_ID])
    }
    if (globalThis[ROOT_ID] !== undefined) {
      if (globalThis[FLEET_ID] === undefined) globalThis[FLEET_ID] = globalThis[ROOT_ID]
      knownRoot(globalThis[ROOT_ID])
    }
    const controller = new AbortController()
    // 2.x re-imports this file when fleet rewrites it, and a stale loop must
    // not keep resubscribing beside the new one: every setup() takes the next
    // generation number for its location, and a loop exits once it no longer
    // holds its location's latest. Other locations' loops are untouched.
    const loc = ctx.location || {}
    const where = (loc.directory || "") + "\u0000" + (loc.workspaceID || "")
    const generations = globalThis[GENERATION] instanceof Map ? globalThis[GENERATION] : (globalThis[GENERATION] = new Map())
    const generation = (generations.get(where) || 0) + 1
    generations.set(where, generation)
    const current = () => globalThis[GENERATION] instanceof Map && globalThis[GENERATION].get(where) === generation
    void (async () => {
      // One stream error must not end reporting for this server: re-enter
      // the subscription after a pause. The feed is live-only, so a re-entry
      // cannot replay an old turn. The pause listens for the abort so
      // teardown never waits the second out, and removes its listener when the
      // timer wins, so a persistently failing stream cannot pile them up.
      while (!controller.signal.aborted && current()) {
        const subscription = (globalThis[EPOCH] = (globalThis[EPOCH] || 0) + 1)
        // The generation is re-checked per dispatch, not just per
        // subscription: a replacement module can land while an
        // already-resolved event is pending here, and one stale dispatch
        // would corrupt the prompt state both modules share.
        try {
          for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
            if (controller.signal.aborted || !current()) break
            delivering = subscription
            onEventV2(event)
          }
        } catch (_e) {}
        if (controller.signal.aborted) break
        await new Promise((resolve) => {
          let timer
          const onAbort = () => { clearTimeout(timer); resolve() }
          timer = setTimeout(() => {
            controller.signal.removeEventListener("abort", onAbort)
            resolve()
          }, 1000)
          controller.signal.addEventListener("abort", onAbort, { once: true })
        })
      }
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
