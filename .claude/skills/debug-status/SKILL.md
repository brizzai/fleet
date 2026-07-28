---
name: debug-status
description: >
  Debug fleet session status issues. Traces through the full status pipeline
  (hooks → watcher → UpdateStatus → pane capture) to find where expected and actual
  status diverge. Use when a session shows the wrong status.
allowed-tools: Read, Grep, Glob, Bash, Agent
user-invocable: true
---

# Debug Session Status

Diagnose why a fleet session shows the wrong status. Read-only — report findings and recommend fixes, never edit code.

For architecture details, see [reference.md](reference.md).

## Approach

Status flows through a pipeline. Your job is to trace the pipeline and find where it breaks:

```text
Claude Code → hook event → hook-handler binary → status file → fsnotify → UpdateStatus() → TUI
                                                                              ↓
                             pane capture + conversation-log (JSONL) + Claude session status file — fallback/supplement
```

At each stage, ask: **what does this stage think the status is, and is it correct?**

The **conversation log** (`~/.claude/projects/*/<claude_session_id>.jsonl`) is a fourth, often-decisive signal: it is appended on real conversation events (tool calls, results, thinking), so unlike the pane it is immune to repaint/spinner/queued-message quirks. Its key use: **if the log's last entry is newer than a `waiting` hook's timestamp, the user already acted and the agent resumed** — the session is running even though the hook still says waiting. (It can only *confirm running*, not waiting: a genuine unanswered prompt and a long-running tool both leave the log static — see Step 2.)

The **Claude session status file** (`~/.claude/sessions/<pid>.json`, one per live `claude` process) is a fifth signal — the only one carrying Claude Code's *own* `status` flag (`busy` / `idle` / `shell`) rather than something fleet infers. It's keyed by pid but records `sessionId`, so you match it to a fleet session by the hook's `session_id`. Best use: a clean **Running-vs-Idle corroborator** when the pane is ambiguous. Three sharp limits: (1) **trust the value, not `statusUpdatedAt`** — it's event-driven, not a heartbeat, so a busy turn can carry a minutes-old timestamp; gate on whether the pid is alive instead. (2) **No `waiting` value** — a permission prompt reads `idle`/`busy` here, so it can't source Waiting. (3) **Blind to `/compact`** — compaction isn't counted as `busy`, so it stays `idle` right through a multi-minute compaction (see Step 2's compaction note). fleet does not consult this file for status detection yet, but the `D` snapshot **does** freeze it (`claude_session_status.json` + a `claude_status` block in `snapshot.json`).

## Step 0: Check for snapshots (only if user mentions they took one)

If the user says they captured a snapshot with the `D` hotkey, check it first. Pick the most recent one matching the timeframe they describe:

```bash
ls -lt ~/.config/fleet/snapshots/ | head -10
```

Then read the snapshot — it contains everything you need:

```bash
cat ~/.config/fleet/snapshots/<dir>/snapshot.json   # Session state, hook, detection, mismatch flag, claude_log block
cat ~/.config/fleet/snapshots/<dir>/pane_clean.txt   # Human-readable pane content
cat ~/.config/fleet/snapshots/<dir>/debug_tail.txt   # Last 100 debug log lines for this session
# pane_raw.txt          = ANSI-preserved capture, ready to copy to testdata/ for golden tests
# claude_session.jsonl  = frozen copy of the Claude conversation transcript at capture time
# claude_session_status.json = frozen copy of Claude Code's own status file (busy/idle/shell flag)
```

Two fields in `snapshot.json` resolve most cases immediately:
- `detection.mismatch` — pane detection disagrees with the TUI status.
- `claude_log.advanced_past_hook` — **`true` means the conversation advanced >2s past the `waiting` hook, so the agent already resumed and a lingering `waiting` is stale** (the classic stuck-at-waiting bug). Also surfaces `last_entry_age`, `seconds_past_hook`, and `recent_gaps_s` (append cadence). `claude_log` is absent for Codex sessions (no Claude transcript).

If a snapshot is available, you can often skip directly to Step 3 (Find the divergence). The frozen `claude_session.jsonl` lets you replay the exact transcript offline without racing the live file.

The snapshot also carries the **Claude session status file** (Step 2): `snapshot.json` → `claude_status` block (`status`, `pid_alive`, `status_age`, `name`, `version`) with the full file frozen as `claude_session_status.json`. `pid_alive: false` means the `status` is a dead process's last-known value, not live.

## Step 1: Identify the session

Ask the user: which session, what status they see, what they expect.

Find the instance ID and tmux session name:
```bash
grep "title=<session_title>" ~/.config/fleet/debug.log | head -1
```
Extract `session=XXXX` — this is the instance ID and hook filename.

Find the tmux session name (needed for pane capture):
```bash
tmux list-sessions -F "#{session_name}" | grep fleet_ | grep <partial_title>
```
Or query the DB:
```bash
sqlite3 ~/.config/fleet/state.db "SELECT tmux_session_name, title FROM sessions WHERE title LIKE '%<title>%'"
```

## Step 2: What does each layer say?

Check all five layers and compare:

**Hook file** (what hooks last reported):
```bash
cat ~/.config/fleet/hooks/<instance_id>.json
```
Note the `status`, `event`, and `ts` (unix timestamp). How old is it?

**Debug log** (what UpdateStatus decided):
```bash
grep "<instance_id>" ~/.config/fleet/debug.log | grep "status changed" | tail -10
```

**Pane** (what's actually on screen):
```bash
tmux capture-pane -t <tmux_session_name> -p | tail -20
```
Also check what detectStatus sees:
```bash
grep "<instance_id>" ~/.config/fleet/debug.log | grep "detectStatus" | tail -10
```

**Conversation log** (what the agent is actually doing — the ground truth pane scraping approximates):
```bash
CS=$(jq -r .session_id ~/.config/fleet/hooks/<instance_id>.json)
F=$(ls ~/.claude/projects/*/$CS.jsonl 2>/dev/null | head -1)
HOOK_TS=$(jq -r .ts ~/.config/fleet/hooks/<instance_id>.json)

# Did the conversation advance PAST the hook? (filter sub-agent sidechain entries)
echo "hook ts : $(date -r "$HOOK_TS" -u +%Y-%m-%dT%H:%M:%SZ)"
echo "last log: $(jq -rc 'select(.timestamp and (.isSidechain|not)) | .timestamp' "$F" | tail -1)"

# Append cadence — is it actively growing right now?
jq -rc 'select(.timestamp) | .timestamp' "$F" | tail -8
```
Read it as:
- **last log entry NEWER than the hook `ts`** → the user already answered/approved and the agent resumed → **running** (even if the hook still says `waiting` — no hook fires on permission-grant or AskUserQuestion-answer).
- **last entry == hook ts and file static** → genuinely still waiting (the prompt is unanswered).
- Timestamps are **UTC (`Z`)**; the hook `ts` and `debug.log` are **local** — convert before comparing.
- Caveats: a long-running tool or extended-thinking block leaves the log static for up to ~50s, so absence of growth ≠ idle. Sub-agents write `isSidechain:true` entries; filter them when asking "did the *lead* advance?". There is **no explicit status field** in the JSONL — infer from progress, not a flag.

**Claude session status file** (Claude Code's *own* status flag — `~/.claude/sessions/<pid>.json`, one per live `claude` process; from a snapshot, read the `claude_status` block / `claude_session_status.json` instead):
```bash
CS=$(jq -r .session_id ~/.config/fleet/hooks/<instance_id>.json)
grep -l "\"$CS\"" ~/.claude/sessions/*.json 2>/dev/null | while read -r f; do
  jq '{pid, status, statusUpdatedAt, name, cwd, version}' "$f"
  P=$(jq -r .pid "$f")
  ps -p "$P" >/dev/null 2>&1 && echo "  → pid $P ALIVE (status is live)" || echo "  → pid $P DEAD (status is last-known, likely stale)"
done
```
Read it as:
- `status` is Claude Code's own flag: **`busy`** (processing a turn) → running, **`idle`** (done / at prompt) → finished/idle, **`shell`** (running a shell command) → running. Keyed by pid but carries `sessionId`, so match on the hook's `session_id`.
- **Trust the value, not the timestamp.** `statusUpdatedAt` is event-driven, not a heartbeat — a genuinely-busy turn can show a `statusUpdatedAt` minutes old. Don't read "stale ⇒ wrong"; gate on **liveness** (is the pid alive?) instead. A dead pid's file is just its last-known status.
- **No `waiting` value exists.** A permission prompt still reads `idle`/`busy` here, so this **corroborates Running vs Idle but cannot source Waiting** — use hooks/pane for waiting.
- **Blind to `/compact`.** Compaction is not counted as `busy`, so the file stays `idle` through a multi-minute compaction — a blind spot it shares with the hook and the transcript. If the symptom is "idle during compaction," this file won't rescue it. The **pane** does see it: `detectRunning` matches the `· Compacting conversation… (2m 27s)` activity line, so a session reading idle mid-compaction points at the pane layer (or at nothing reaching it), not at compaction being undetectable.

## Step 3: Find the divergence

Compare the layers. The bug is where they disagree:

- **Hook file wrong, pane correct** → Hook event was missed or mapped incorrectly. Check `hook-handler: writing status` entries in the log — is there a gap? Which event should have fired but didn't?

- **Hook file correct, UpdateStatus wrong** → Bug in the `case` logic in `UpdateStatus()`. Read the code path for that hook status in `internal/session/session.go`.

- **Both correct but TUI shows wrong** → Acknowledged/Idle transition issue, or timing (status changed between ticks). Check if `Acknowledged` flag is involved.

- **Pane detection wrong** → Pattern matching issue in `detectStatus()`. Look at what pattern it matched and whether that content is current or stale scrollback.

- **Hook stuck on `waiting`, but the conversation log advanced past the hook `ts`** → the user approved a permission / answered an AskUserQuestion (no resume hook fires), and the `waiting→running` flip is starved because it depends entirely on pane detection (`applyHookWaiting`), which is blind when the activity line is pushed out of its line window (queued messages, long thinking). This is the known stuck-at-waiting class. Confirm with the conversation-log check in Step 2; the fix is detection-side (broaden `detectRunning`, or add a conversation-log progress signal).

## Step 4: Check the timeline

```bash
grep "<instance_id>" ~/.config/fleet/debug.log | grep -E "hook-handler|status changed" | tail -30
```

Build a timeline of events. Look for:
- **Gaps**: Long periods with no hook writes while pane shows activity
- **Rapid oscillation**: Status bouncing between two states every few seconds
- **Unexpected sequences**: Events in wrong order, missing expected events

## Step 5: Check for agent team scenarios

If the session uses Claude's agent team feature (sub-agents, `Explore(...)`, `@agent-name`):

**Key symptoms:**
- Hook file shows `Stop/finished` but pane shows a sub-agent permission prompt or "Waiting for team lead approval"
- Status oscillates between running/idle/finished (spinner on "waiting for" line intermittently matches)
- Hook is very stale (hookAge >> minutes) — parent delegated long ago, no new hooks from sub-agent

**What to check:**
- Does the pane show a numbered menu (`❯ 1. Yes`, `2. No`) with `Esc to cancel`? → Should be waiting
- Does the pane show a box (`│ ✻ Waiting for team lead approval │`)? → Should be waiting
- Is the spinner char on a line containing "waiting for"? → Should be skipped by detectRunning
- Is there text containing approval patterns (`(Y/n)`, menu text) in code diffs or conversation output? → False positive source

**Common agent team false-positives:**
- User typed a numbered list as input (`❯ 1. first item`) → looks like permission menu
- Session is editing/discussing status detection code → approval patterns appear in scrollback
- Fix: structural checks require `Esc to cancel` for menu detection, `│` at line start for team box

## Step 6: Go deeper if needed

**Claude conversation log** (verify user actions / pin the real resume moment):
```bash
# Find the log file
jq -r .session_id ~/.config/fleet/hooks/<instance_id>.json
# Then check ~/.claude/projects/*/<session_id>.jsonl
```
See Step 2's **Conversation log** check for the timestamp-vs-hook comparison. To find exactly when the agent resumed after a permission/question, look for the first entry after the hook `ts` — e.g. the answering `tool_result`, then the agent's next `tool_use`:
```bash
F=$(ls ~/.claude/projects/*/<session_id>.jsonl | head -1)
jq -rc 'select(.timestamp) | [.timestamp, .type, (.message.content[0].type // "")] | @tsv' "$F" | tail -40
```
The gap between that resume time and the TUI's `new=running` line in `debug.log` is the size of the stuck-waiting bug window.

**Hook installation** (verify hooks are registered):
```bash
cat ~/.claude/settings.json | python3 -m json.tool | grep -A5 "fleet"
```

**Hook handler execution** (verify binary runs):
```bash
grep "<instance_id>" ~/.config/fleet/debug.log | grep "hook-handler"
```

**Environment variable** (verify hook routing is wired up):
```bash
tmux show-environment -t <tmux_session_name> FLEET_INSTANCE_ID
```
If missing or wrong, hooks fire but the handler can't route them to the right session — they silently drop.

## Report

After diagnosis, report:
1. **Expected vs actual**: What status TUI shows vs what's really happening
2. **Divergence point**: Which pipeline stage has the wrong data
3. **Timeline**: Key events with timestamps showing where things went wrong
4. **Root cause**: Why that stage produced wrong output
5. **Recommended fix**: What would prevent this (code change, additional logging, etc.)

## Step 7: Add regression test

Every status bug should become a test so it never recurs. The project has two test frameworks for this:

### Golden file test (detection bugs)

For bugs where the pane content was misclassified (wrong `detectStatus` result):

1. **Capture the pane** that triggered the bug:
   ```bash
   tmux capture-pane -t <tmux_session_name> -p -e > internal/session/testdata/<bug-name>.txt
   ```
   The `-e` flag preserves ANSI escape codes — critical for testing the full `stripANSI → detectStatus` pipeline.

2. **Add a test entry** in `internal/session/golden_test.go`:
   ```go
   {"<bug-name>.txt", StatusWaiting, "description of what this pane shows"},
   ```

3. **Verify**: `go test -run TestGoldenDetection -v ./internal/session/` — should FAIL before fix, PASS after.

### Scenario test (state transition bugs)

For bugs where the status machine transitioned incorrectly (e.g., hook said waiting but `applyHookWaiting` overrode to finished):

1. **Add a scenario** in `internal/session/scenario_test.go`:
   ```go
   func TestScenarioBugName(t *testing.T) {
       runScenario(t, Scenario{
           Name: "description of the bug",
           Events: []ScenarioEvent{
               {At: 0, Hook: "waiting", Pane: "content or @fixture:file.txt"},
               // ... sequence of events that trigger the bug
           },
           Checks: []ScenarioCheck{
               {At: 0, Expected: StatusWaiting},
               // ... expected status at each point
           },
       })
   }
   ```

2. **Scenario events** can set: hook status (`Hook`), pane content (`Pane`), pane death (`PaneDead`), user acknowledgement (`Acknowledge`). Pane content can reference a golden fixture with `"@fixture:filename.txt"`.

3. **Scenario checks** assert the session status at a given time offset. The replay engine calls `UpdateStatus()` at each check point using a mock pane capturer (no real tmux needed).

4. **Verify**: `go test -run TestScenarioBugName -v ./internal/session/` — should FAIL before fix, PASS after.

### Which to use?

- **Detection misclassification** (detectStatus returns wrong status for given pane content) → Golden file test
- **State transition error** (correct detection but wrong status due to hook/pane interaction, timing, acknowledged flag) → Scenario test
- **Both** → Add both: golden fixture for the pane + scenario for the transition sequence
