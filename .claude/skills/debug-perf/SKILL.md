---
name: debug-perf
description: >
  Debug fleet TUI performance issues — UI loop stalls, frozen sidebar, sluggish
  attach, or background CPU spikes. Reads perfwatch stall dumps and the debug.log
  heartbeat to identify what blocked the Bubble Tea Update() loop or stole cycles
  from the PTY. Trigger on: "TUI is stuck/frozen/laggy", "UI hangs", "everything
  froze", "attach feels slow", "what blocked the loop", "perfwatch", "stall dump",
  "Bubble Tea slow", or any complaint that fleet became unresponsive even briefly.
allowed-tools: Read, Grep, Glob, Bash
user-invocable: true
---

# Debug Performance / UI Stalls

Diagnose why the fleet TUI froze, stuttered, or felt laggy by reading the artifacts the `perfwatch` package writes when `FLEET_DEBUG=1` is set. Read-only — report findings and recommend fixes, never edit code.

## What perfwatch records

When `FLEET_DEBUG=1`, `internal/perfwatch` instruments every `Update()` call and continuously samples runtime state:

- **Stall dumps** — `~/.config/fleet/stalls/<ts>_update_stall_<msgType>_<ms>ms.txt` written automatically when `Update()` runs longer than 500ms. Re-dumps every 1.5s while still stuck. Contains: in-flight message, recent message ring buffer (last 32 with durations), full goroutine stacks (`debug=2`), block profile, mutex profile, GC stats.
- **Heartbeat** — every 5s in `~/.config/fleet/debug.log`: goroutine count, process CPU%, heap KB, total/slow update counts, max update duration. Keeps logging during attach (when the Bubble Tea loop is suspended) so background-worker CPU usage is visible even when the TUI itself isn't running.
- **Slow Update warnings** — any `Update()` ≥100ms gets a `perfwatch: slow Update msg=… duration_ms=…` WARN line in debug.log even if it didn't trigger a stall dump.
- **SIGUSR1** — `kill -USR1 $(pgrep -f 'fleet$')` forces a snapshot on demand. Filename ends in `_manual_sigusr1`.

## Step 0: Verify perfwatch is enabled

If the user hasn't set `FLEET_DEBUG`, no dumps exist and you can't help. Check:

```bash
grep "perfwatch: enabled" ~/.config/fleet/debug.log | tail -1
```

If empty, instruct the user:
```text
Re-launch with: FLEET_DEBUG=1 ./build/fleet
Reproduce the freeze, then run /debug-perf again.
```

If the user reports laggy attach but no TUI stall, also tell them: while attach feels laggy, run `kill -USR1 $(pgrep -f 'fleet$')` from another terminal to force a snapshot of background goroutines. Stalls during attach won't trigger the watchdog (Bubble Tea loop is suspended), so manual snapshots are the only signal.

## Step 1: Find the relevant dump

```bash
ls -lt ~/.config/fleet/stalls/ | head -20
```

Pick the dump whose timestamp matches the freeze the user described. Filename encodes the trigger:
- `*_update_stall_<msgType>_<ms>ms.txt` — automatic, fired by the watchdog. `<msgType>` is the Bubble Tea message type that was blocking (e.g., `ui.tickMsg`, `tea.KeyMsg`, `ui.statusUpdateMsg`).
- `*_manual_sigusr1.txt` — user-triggered snapshot, typically captured during attach lag.

If multiple consecutive dumps exist within ~2s of each other, the freeze lasted long enough that the watchdog re-fired. Use the **earliest** — it captures the first moment of the stall before goroutines piled up.

## Step 2: Read the dump header

```bash
head -60 ~/.config/fleet/stalls/<file>
```

Key fields:
- **Update() IN FLIGHT: msg=… elapsed=…** — this is the message type that was blocking when the dump fired. The biggest single clue.
- **Goroutines: N** — baseline is ~20-30. >100 = goroutine leak suspect.
- **HeapAlloc / NumGC** — sudden GC pressure or huge heap can starve goroutines.
- **Counters: total / slow / max_ms** — context for severity.

## Step 3: Read the recent message ring

```bash
sed -n '/Recent Update/,/Goroutine stacks/p' ~/.config/fleet/stalls/<file>
```

The ring shows up to 32 prior `Update()` calls with durations. Look for:
- A pattern of slow updates leading up to the stall (gradual degradation vs. sudden spike).
- Repeated handling of the same message type (e.g., 30× `ui.tickMsg`) — suggests a tea.Cmd loop misfiring.
- A single recent message with abnormal duration matching the stall msg — confirms the blocking call is reproducible per-message-type.

## Step 4: Read the goroutine stacks

```bash
sed -n '/Goroutine stacks/,/Block profile/p' ~/.config/fleet/stalls/<file>
```

Find the goroutine running `Home.Update`. Its stack tells you exactly what blocked:

**Common blocking patterns in fleet:**

- `sync.(*Mutex).Lock` → `internal/ui/app.go` referencing `workerMu` — Update is waiting on the status worker. Look at the goroutine running `Home.statusWorkerCycle` or `Home.statusWorker` — what is *it* blocked on? Usually:
  - `os/exec.(*Cmd).Wait` on `git`, `gh`, or `tmux` → external process is slow. Fix: timeout or move off Update path.
  - `bufio.(*Reader).ReadBytes` on tmux pane capture → tmux is unresponsive.

- `os/exec.(*Cmd).Run/Output/Wait` directly in the Update goroutine → blocking I/O leaked into Update. Per CLAUDE.md this is forbidden — find the call site and move it to a tea.Cmd closure or the worker.

- `database/sql.(*DB).Exec/Query` in Update goroutine → SQLite write contention. Look for `storage.UpdateStatus`, `storage.SetAcknowledged`, etc., called synchronously.

- `chan send` blocked on a full channel → look at which channel (`statusTrigger`, `priorityStatusUpdates`, hook watcher channel) and who is supposed to be draining it. Receiver may be stuck.

- `runtime.gopark` / `select` with no obvious culprit → goroutine is idle. Not the cause; look elsewhere.

- `syscall.read` / `syscall.write` on PTY fds → the PTY io.Copy goroutine. During attach this is normal.

## Step 5: Read the block & mutex profiles

```bash
sed -n '/Block profile/,/Mutex profile/p' ~/.config/fleet/stalls/<file>
sed -n '/Mutex profile/,$p' ~/.config/fleet/stalls/<file>
```

These are cumulative since process start (>1ms blocks recorded). The function showing the most time at the top is the dominant contention point across the whole run, not just this stall. Useful to confirm what Step 4 found and to spot chronic contenders even when individual stalls don't reach 500ms.

## Step 6: Cross-reference with heartbeat

```bash
grep "perfwatch heartbeat" ~/.config/fleet/debug.log | tail -50
```

For each heartbeat near the stall timestamp, check:
- **goroutines spike** — leak suspicion. Trace which subsystem started spawning goroutines (compare to baseline before the spike).
- **cpu_pct spike** — busy loop or expensive work. If `cpu_pct > 50%` while the user reports attach is laggy, fleet's own goroutines are starving the PTY.
- **heap_kb growth without GC** — possible memory pressure causing GC pauses (which look like UI stalls).
- **updates_slow climbing fast** — many Updates went over 100ms even without hitting the 500ms stall threshold. Look for the WARN lines: `grep "perfwatch: slow Update" ~/.config/fleet/debug.log | tail -30`.

## Step 7: For attach-specific lag

If the user reports lag *inside* the attached tmux session (typing/output sluggish, not TUI freeze), the watchdog won't catch it (Bubble Tea is suspended during `tea.Exec`). Use:

1. **Heartbeats during attach** — fleet keeps logging while attached. Find the attach window:
   ```bash
   grep -E "attach|perfwatch heartbeat" ~/.config/fleet/debug.log | tail -100
   ```
   If `cpu_pct` is high (>30%) while attached, a fleet background goroutine is competing with the PTY. Look at goroutine count trend — leaking workers will show.

2. **Manual snapshot during lag** — instruct user:
   ```bash
   # In another terminal while attach feels laggy:
   kill -USR1 $(pgrep -f 'fleet$')
   ```
   Then read the resulting `*_manual_sigusr1.txt` and look at goroutine stacks. Common attach-lag culprits:
   - `Home.statusWorkerCycle` doing pane captures on a large session list every 2s → slows everything.
   - `hooks.HookWatcher` fsnotify storm if many hook files change rapidly.
   - `git.RepoInfoCache` refresh hitting many repos at once.
   - PTY `io.Copy` not the issue itself, but check if it's blocked on `syscall.write` to stdout for unusually long.

## Step 8: Report

Lead with the in-flight message and the blocking syscall/lock. Then explain the chain.

Format:
1. **Symptom**: stall duration, message type, what the user saw.
2. **Blocking call**: function + file:line that was on top of the Update goroutine's stack.
3. **Why it blocked**: held by which other goroutine, waiting on what (process, lock, channel).
4. **Chronic vs. one-off**: does the block/mutex profile confirm this is a recurring pattern?
5. **Recommended fix**: which of these applies?
   - Move blocking I/O off the Update goroutine into a tea.Cmd closure (per CLAUDE.md "All blocking I/O … runs in background worker goroutine, never in Bubble Tea Update()").
   - Add a timeout to an external command (`exec.CommandContext` with a deadline).
   - Reduce lock scope: hold `workerMu` only for in-memory state mutation, not for I/O.
   - Add backpressure to a channel (drop on full) instead of blocking sends.
   - Cache an expensive computation behind a TTL.
6. **Regression signal**: a heartbeat threshold (e.g., "alert if cpu_pct >50 sustained" or "alert if max_update_ms >1000") that would have caught this earlier.

Do not modify code unless the user explicitly asks. The job ends at the recommendation.
