# A9 — Kill-daemon-while-attached chaos test

**Status:** open
**Tier:** A
**Effort:** 0.5 day
**Stack:** Repo (shell + Go)

## Problem

[S2 (Mac top-level reconnect)](./s2-mac-top-level-reconnect.md) adds reconnect behavior. [S1 (daemon singleton lock)](./s1-daemon-singleton-lock.md) prevents double-start. Without a deliberate test, these are hopes, not contracts.

We need a script that deliberately breaks things and asserts the recovery story holds.

## Why now

- Pragmatist explicit ask: *"Daemon-dies-while-Mac-attached behavior. `Reconnect.swift` exists but has it been broken-on-purpose tested?"*
- Depends on S1 + S2 landing first.
- Cheap to write once, valuable forever.

## Proposed solution

1. **Shell script `test/chaos/kill-daemon.sh`:**
   - Pre-flight: assert daemon is running, assert at least one TUI or Mac client is connected (check via `lsof` on the socket).
   - Read daemon pid from `~/.config/fleet/daemon.pid`.
   - `kill -9 <pid>` to simulate crash.
   - Watch `~/.config/fleet/debug.log` for autospawn entry → assert daemon respawns within 5s.
   - Tail FleetLog (Mac) for reconnect entry → assert reconnect succeeds.
   - PASS / FAIL output.

2. **Go test `internal/daemonclient/chaos_test.go`** (build tag `chaos`):
   - Spawn daemon as subprocess, point a Go client at it.
   - Make a streaming RPC.
   - SIGKILL the daemon mid-stream.
   - Assert the client transitions to reconnecting → eventually re-establishes when daemon comes back.
   - Assert crash-loop guard: if subprocess exits immediately 3×, client gives up with `ErrDaemonCrashLoop`.

3. **Documentation at `docs/infra/CHAOS-TESTING.md`:**
   - What scenarios are covered.
   - What scenarios are NOT covered (e.g., disk full, network partition — neither applies locally).
   - How to run manually.

## Acceptance criteria

- [ ] `bash test/chaos/kill-daemon.sh` produces a clear PASS/FAIL line.
- [ ] `go test -tags chaos ./internal/daemonclient/...` passes after S1 + S2 land.
- [ ] Test covers: daemon crash → reconnect, daemon crash 3× → autospawn gives up, double-daemon attempt → second exits cleanly.
- [ ] Doc lists covered vs uncovered scenarios so future contributors know the boundary.

## Out of scope

- CI integration. (Chaos tests are flaky in CI — keep manual for now.)
- Network partition simulation. (No network components.)
- Disk-full simulation. (Possible later via `tmpfs` mount + small size, but not now.)
- Mac UI assertions beyond log-watching. (UI scripting would be brittle.)

## References

- Depends on: [S1](./s1-daemon-singleton-lock.md), [S2](./s2-mac-top-level-reconnect.md)
- `internal/daemonclient/autospawn.go` — crash-loop guard to exercise
- `app/Sources/Fleet/DaemonClient/DaemonClient.swift` — reconnect path to exercise
