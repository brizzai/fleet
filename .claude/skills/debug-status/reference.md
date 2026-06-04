# Status Detection — Architecture Reference

## Pipeline

```
Claude Code hooks → hook-handler CLI → status file → fsnotify → HookWatcher → Session.UpdateStatus()
                                                                                    ↓
                                                                   pane capture + content hashing
```

## Status Model

| Status   | Icon | Meaning                                |
|----------|------|----------------------------------------|
| Starting | ○    | Session just created, tmux initializing |
| Running  | ●    | Claude actively processing              |
| Waiting  | ◐    | Permission/approval prompt showing      |
| Finished | ●    | Claude done, not yet acknowledged       |
| Idle     | ○    | Claude done, user acknowledged          |
| Error    | ✕    | Session crashed or tmux died            |

## Hook Event → Status Mapping

Defined in `cmd/fleet/hook_handler.go` `mapEventToStatus()`.

| Event             | Matcher                                | → Status  |
|-------------------|----------------------------------------|-----------|
| UserPromptSubmit  |                                        | running   |
| Stop              |                                        | finished  |
| PermissionRequest |                                        | waiting   |
| Notification      | `permission_prompt\|elicitation_dialog` | waiting   |
| Notification      | `idle_prompt`                          | finished  |
| SessionStart      |                                        | finished  |
| SessionEnd        |                                        | dead      |

## UpdateStatus() — Key Code Paths

Located in `internal/session/session.go`. Runs every tick (~2s).

Each `case` in the hook switch has different override/fallback behavior:
- Some cases allow pane overrides, some don't
- Some track content hash changes, some reset the hash
- The `Acknowledged` flag affects finished→idle transition

Read the actual code for current behavior — it changes as bugs are fixed.

## Pane Detection (detectStatus)

Scans last 50 non-empty lines of `tmux capture-pane` output after ANSI stripping.

**Priority order** (first match wins):
1. Busy patterns → Running (`ctrl+c to interrupt`, `esc to interrupt`)
2. Spinner characters → Running (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏✳✽✶✢) — **line-start only** (`HasPrefix` after trim), skips "waiting for" lines
3. Whimsical activity (`· ↓` + `tokens` on same line) → Running
4. Structural waiting checks (bottom 15 lines only) → Waiting:
   - Menu structure: `❯ 1.` + `2.` + `Esc to cancel` all present
   - Team box: line starting with `│` containing `Waiting for team lead`
   - Text fallback: `Yes, allow once`, `No, and tell Claude`, `Do you trust the files`, `Allow this MCP server`
5. Prompt indicators (❯ or >) → Finished
6. Idle patterns → Finished
7. No match → unknown (keeps previous)

**Fundamental limitation**: Pane is a flat text buffer. It contains stale content from previous turns mixed with current state. Pattern matching cannot reliably distinguish current from stale.

## Agent Team Status Detection

Sub-agents (Claude agent team feature) don't fire hooks. The parent fires `Stop` when delegating, then the sub-agent operates independently. This creates a gap where hooks say "finished" but the session is actually waiting.

**How it works:**
- `applyHookFinished` checks paneStatus: if waiting → override to waiting
- `detectWaiting` uses structural checks (not text patterns) to avoid false-positives
- `detectRunning` skips spinner chars on "waiting for" lines (team box has animated spinner)
- `applyHookRunning` does NOT override to waiting (false-positives from code in scrollback)

**Common false-positive sources for waiting detection:**
- Code diffs containing approval pattern strings (`(Y/n)`, `Continue?`)
- Conversation text discussing detection patterns (meta-problem)
- User numbered list input matching menu structure (`❯ 1. first item`, `2. second item`)
- Fix: structural checks require multiple co-occurring cues, checked only near bottom of pane

## Key Files

| File | What to look for |
|------|------------------|
| `internal/session/session.go` | UpdateStatus(), detectStatus(), Status type |
| `cmd/fleet/hook_handler.go` | mapEventToStatus(), hook payload parsing |
| `internal/hooks/claude_hooks.go` | Hook installation, event configs |
| `internal/hooks/hook_watcher.go` | fsnotify watcher |
| `internal/hooks/status_file.go` | Status file format |

## Key Logs

All in `~/.config/fleet/debug.log`:

| Log message | Meaning |
|-------------|---------|
| `hook-handler: writing status` | Hook handler received event, writing file |
| `hook-handler: no FLEET_INSTANCE_ID` | Env var missing, can't route hook |
| `hook-handler: unmapped event` | Event type not in mapEventToStatus |
| `status changed (hook)` | UpdateStatus changed status based on hook data |
| `status changed (pane)` | UpdateStatus changed status based on pane capture |
| `detectStatus: matched ...` | What pane pattern was detected |
| `hook says finished but pane shows running` | Pane override: spinner detected with stale finished hook |
| `hook says finished but pane shows waiting` | Pane override: team/sub-agent waiting with stale finished hook |
| `hook says waiting but pane shows idle prompt` | Stale waiting hook overridden by idle pane |
| `content changed while waiting` | Content hash changed during waiting state |
| `content stable >10s` | Content hasn't changed for 10s during running state |

## Status Snapshots

Secret `D` hotkey captures a point-in-time diagnostic snapshot of the selected session.

Location: `~/.config/fleet/snapshots/<timestamp>_<title>/`

| File | Contents |
|------|----------|
| `pane_raw.txt` | Raw ANSI pane capture (copy to `testdata/` for golden tests) |
| `pane_clean.txt` | ANSI-stripped for human reading |
| `snapshot.json` | Session state, hook state, content tracking, pane detection, `mismatch` flag, `claude_log` block |
| `debug_tail.txt` | Last 100 debug.log lines filtered for this session |
| `claude_session.jsonl` | Frozen full copy of the Claude conversation transcript at capture time (absent for Codex sessions) |

The `snapshot.json` `detection.mismatch` field is `true` when pane detection disagrees with the TUI status — the key signal for status bugs.

The `snapshot.json` `claude_log` block derives activity-recency from the transcript:

```json
"claude_log": {
  "file": "claude_session.jsonl",
  "entries": 202,
  "last_entry_at": "...Z", "last_entry_age": "1m2s",
  "last_lead_entry_at": "...Z",      // last non-sidechain entry (ignores sub-agent output)
  "seconds_past_hook": 3512.3,        // last_lead_entry_at minus the hook ts
  "advanced_past_hook": true,         // >2s past ⇒ user acted, agent resumed ⇒ a lingering "waiting" hook is stale
  "recent_gaps_s": [1.6, 0.5, 18.5]   // append cadence between recent entries (running leaves gaps up to ~50s)
}
```

`advanced_past_hook` is the decisive signal for the stuck-at-waiting class: a `waiting` hook never gets a resume event (no hook fires on permission-grant or AskUserQuestion-answer), so the transcript advancing past it proves the agent is running even when pane detection is blind.

Implementation: `internal/ui/snapshot.go` (capture logic), `internal/session/session.go` `SnapshotData()` (state export).

## Hook Status Files

Location: `~/.config/fleet/hooks/<instance_id>.json`

```json
{"status":"running","session_id":"<claude-uuid>","event":"UserPromptSubmit","ts":1773090529}
```

One file per fleet session. Last-write-wins. Cleaned up after 24h.
