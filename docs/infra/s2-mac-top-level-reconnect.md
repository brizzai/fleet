# S2 — Mac top-level reconnect + crash-loop guard

**Status:** open
**Tier:** S (resilience)
**Effort:** 0.5 day
**Stack:** Swift (Mac app) + Go (autospawn)

## Problem

`DaemonClient.swift:22-71` exits the `withGRPCClient { ... }` closure when the gRPC transport drops, sets `connectionState = .disconnected`, and **stops**. The user has to relaunch the app.

`StreamConsumers.swift` handles *in-stream* errors (server cancels, deadline exceeded) — it does NOT handle transport-level disconnects. If `fleet daemon` segfaults, the Mac app appears alive but every action silently fails or sits forever.

Adjacent issue: `internal/daemonclient/autospawn.go:30-46` has **no crash-loop guard**. If the daemon dies on startup (port conflict, db lock, panic), every client launch tries to respawn it forever, never surfacing the actual error.

## Why now

- This makes the Mac app a demo, not a shipped product.
- Veteran critic: "Mac top-level reconnect — P0 before any user sees this."
- Pairs with [S1](./s1-daemon-singleton-lock.md): once the daemon can't double-start, transient crashes need a recovery story.

## Proposed solution

### Swift side

1. Wrap `DaemonClientRunner.run` in a top-level retry `Task` with the same exp-backoff schedule used by `Reconnect.swift:6` (250ms → 500ms → 1s → 2s → 5s, capped).
2. On disconnect, set `connectionState = .reconnecting` (banner already exists in `ContentView.swift`). Drop to `.disconnected` only after N consecutive transport failures (e.g., 5).
3. Reuse `Reconnect.swift` schedule so behavior matches stream-level reconnect.

### Go side

4. Track restart timestamps in `daemonclient/autospawn.go`. If the daemon dies 3 times within 60 seconds, refuse to respawn and surface the last stderr:
   - Return an `ErrDaemonCrashLoop` to the caller.
   - Print: `fleet daemon crashing repeatedly. Last error: <stderr>`.
5. Reset the counter on a successful 5-second uptime (the daemon "stuck the landing").

## Acceptance criteria

- [ ] **Daemon crash recovery (Mac):** start daemon + Mac app, `kill -9 $(cat ~/.config/fleet/daemon.pid)`, daemon respawns via autospawn, Mac shows "reconnecting…" banner, recovers within 5s.
- [ ] **No CPU spin during disconnect:** Activity Monitor shows Mac process <5% CPU while daemon is dead.
- [ ] **Crash-loop guard fires:** simulate via daemon binary that exits immediately. After 3 attempts in 60s, autospawn returns clear error. Mac app shows banner with stderr content.
- [ ] **Successful 5s uptime resets counter:** restart daemon successfully, kill it later, autospawn should retry (counter cleared).
- [ ] No regression in stream-level reconnect (test by `tmux kill-server` while daemon stays alive).

## Out of scope

- Daemon health-check RPC. Transport drop is the signal we need today.
- Persistent disconnect history across Mac app launches.
- Surfacing daemon log tail in the Mac UI (separate UX ticket).

## References

- `app/Sources/Fleet/DaemonClient/DaemonClient.swift:22-71` — current disconnect path
- `app/Sources/Fleet/DaemonClient/Reconnect.swift:6` — backoff schedule to reuse
- `app/Sources/Fleet/DaemonClient/StreamConsumers.swift` — stream-level reconnect (don't break)
- `internal/daemonclient/autospawn.go:30-46` — where the crash-loop guard goes
- `internal/daemonclient/stream.go:20-25` — Go-side backoff schedule (matches Swift)
- `app/Sources/Fleet/Views/ContentView.swift` — `.reconnecting` banner already wired
