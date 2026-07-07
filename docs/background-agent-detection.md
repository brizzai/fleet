# Background-agent detection

> Decision record. How fleet detects that a session is running **background agents**
> (`Agent` tool with `run_in_background: true`), and why it does it by scraping the
> pane rather than using a "more first-party" signal.

## The problem

When a Claude session delegates to background agents, the **lead agent fires its `Stop`
hook** (its turn genuinely ends) and parks until those agents re-invoke it. Claude renders
a dock at the bottom of the terminal:

```text
✻ Waiting for 3 background agents to finish
  ⏺ main
  ◯ Explore  Map analytics/platform pages          2m 49s · ↓ 78.1k tokens
  ◯ Explore  Map AI assistant, labels, notifications 2m 35s · ↓ 104.1k tokens
  ◯ Explore  Classify settings/admin access model    2m 22s · ↓ 65.6k tokens
```

Without special handling the session reads **idle/finished** (the lead Stopped), even though
minutes of work — hundreds of thousands of tokens — are actively in flight.

## Decision

**Detect the dock from the pane and report `Running`.** `detectBackgroundAgentsRunning`
(`internal/session/session.go`) matches the in-flight dock rows
(`◯ <type>  <desc>  <elapsed> · ↓/↑ <n> tokens`) in the bottom lines of the capture, and
`detectRunning` returns `StatusRunning`. The existing `applyHookFinished` override
(`paneStatus == running` beats a `finished` hook) does the rest — no new state-machine
plumbing. Only the in-flight glyph (`◯`) matches, so the session **self-clears to finished**
when the agents complete.

This **reverses** an earlier decision that treated the same pane as `finished` (see the
flipped golden `finished_idle_background_agent.txt` and `TestScenarioBackgroundAgentsShowRunning`).
The old anti-"pin forever" concern was about a background agent keeping `window_activity`
fresh and pinning a stale **`waiting`**; showing an honest **`running`** while agents work
does not have that failure mode. Trade-off accepted: a long background job shows `Running`
for its full duration (it won't surface in the Space-to-jump attention queue until it
completes) — chosen over hiding live work under `idle`.

## Why not a "more first-party" signal

Investigated 2026-07-07. There is **no stable, documented signal** for "this session is
running background agents." Every option that covers the case is fragile; they differ mainly
in *blast radius when they break*.

| Signal | Covers bg agents? | Durability | Blast radius when it breaks |
|---|---|---|---|
| **Pane dock scrape** (chosen) | yes | low — UI can change | **tiny** — one function + one golden fixture; breaks *visibly* (wrong status) |
| Claude session status file (`~/.claude/sessions/<pid>.json`) — `busy` past the Stop hook | yes | low — Anthropic docs say the format is "internal … and scripts that parse these files directly **can break on any release**" | **large** — silent, pipeline-wide |
| `SubagentStop` hook | maybe (may not fire for bg agents) | undocumented schema, marked "not planned" (anthropics/claude-code#19170); shared `session_id` can't identify *which* agent (#7881) | not usable today |
| `Notification` (`agent_completed`) | unclear for `run_in_background` | documented, coverage unproven | medium |
| `claude agents --json` | **no** — tracks independent background *sessions*, not in-session subagents | stable & documented | — |
| Transcript / `isSidechain` | **no** — verified 0 sidechain entries and no separate `.jsonl` during the run | internal/unstable | — |

The counterintuitive conclusion: the pane scrape is not a stopgap, it is the **best available
long-term option** for this feature. Its fragility is *cheap and loud* to repair (one function,
one fixture, a visibly-wrong status), whereas the status file is fragile **and** breaks
*silently across the whole detection pipeline* on a Claude update the docs explicitly warn about.

### On-disk facts (verified locally, Claude Code 2.1.202)

- Background agents get **no status file of their own** — every `~/.claude/sessions/*.json`
  is `kind:"interactive"`, and `parentSessionId` / `agentId` are always null. They run
  *inside* the lead's process.
- They leave **no separate transcript and no live `isSidechain` entries**.
- So the only on-disk trace of running background agents is the lead process staying **`busy`
  past its `Stop` hook** — a real signal, but gated behind the "can break on any release" warning.

## Deferred opportunity: first-party `waiting`

The same status file gained a first-party waiting signal not present when our detection was
written:

```json
{ "status": "waiting", "waitingFor": "permission prompt" }
{ "status": "waiting", "waitingFor": "dialog open" }
```

This is exactly the case fleet handles with fragile structural pane-checks today (permission
approval / AskUserQuestion fire no hook). It is worth a **spike** — but as an *isolated,
version-tolerant corroborator* (a missing field drops the signal, never crashes), **not** a
pipeline foundation, given the same instability warning. Not a foundation for background-agent
detection either: it adds nothing over the pane there and carries the larger blast radius.

## The durable fix is upstream

The only path to a signal we could actually trust long-term is Anthropic shipping one. The
open requests to track / reference in a feature request:

- anthropics/claude-code#7881 — `SubagentStop` can't identify which subagent finished (no agent id).
- anthropics/claude-code#19170 — `SubagentStart` payload schema undocumented ("not planned").
- anthropics/claude-code#16424 — expose agent context in hook payloads.

Desired: a documented `claude session <id> --agents --json` (enumerate a session's background
agents + state) and/or a parent-linked `SubagentStop` payload.
