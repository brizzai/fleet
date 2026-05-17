# F1 — Decide: is the in-process service fallback worth maintaining?

**Status:** open — **DECISION NEEDED**
**Tier:** F (framing question; resolve before more infra work)
**Effort:** Decision (1 hour) + 1–2 days execution
**Stack:** Go (architectural)

## Problem

`cmd/fleet/main.go:318-347` — when daemon spawn fails, the TUI degrades gracefully to an **in-process `service.SessionService`**. The Mac app has no such fallback.

Two consequences:

1. **Contract surprise:** same backend, different failure semantics. A daemon crash kills the Mac forever (until [S2](./s2-mac-top-level-reconnect.md) lands), but the TUI continues seamlessly. Users won't predict this.
2. **Dual-implementation tax:** every new daemon feature has to be implemented twice — in `service.SessionService` *and* in `daemonclient.Client` (so the TUI can call it via either path). This is the same drift problem as the action registry, **invisible because both are in Go**.

The veteran critic called this *"the worst of both worlds"* and asked you to pick a side.

## The decision

### Option A: Kill the in-process path. Daemon-always.

- TUI startup spawns the daemon (autospawn already exists). No fallback.
- Remove `service.NewSessionService` callsites from the TUI command.
- Single implementation (`daemonclient.Client`) for everyone.
- Mac stops being a second-class citizen.

**Pros:** kills the drift problem; one code path; matches what `fleet --tui` already does for users with daemon healthy.

**Cons:** breaks `--standalone` flag (would need to remove or repurpose); if daemon refuses to start, TUI is dead. (Mitigated by [S1](./s1-daemon-singleton-lock.md) + [S2](./s2-mac-top-level-reconnect.md) making daemon more reliable.)

### Option B: Commit to always-fallback.

- Mac learns to use `service.SessionService` in-process if daemon spawn fails.
- Means the fallback path stays maintained intentionally.
- Future features explicitly need to work in both modes.

**Pros:** maximum resilience; Mac never appears broken even with daemon down.

**Cons:** every new feature is 2× the work; the in-process path can't do everything daemon does (e.g., multi-client streaming makes no sense in-process).

### Recommendation

**Option A.** The original reason for the fallback (daemon is new, might be flaky) is fading — [S1](./s1-daemon-singleton-lock.md) + [S2](./s2-mac-top-level-reconnect.md) make daemon trustworthy. The dual-implementation tax compounds with every feature; killing it now is a one-time cost.

If you decide Option A, the rest of this ticket is the execution.

## Acceptance criteria (Option A)

- [ ] `service.NewSessionService` direct usage removed from `cmd/fleet/main.go` for the `--tui` flow.
- [ ] TUI startup spawns daemon via autospawn; if daemon spawn fails 3× ([S2 crash-loop guard](./s2-mac-top-level-reconnect.md)), TUI exits with clear error.
- [ ] `--standalone` flag either removed or repurposed (and documented).
- [ ] CI green, no test regressions.
- [ ] `service.Service` interface either keeps its single implementation (`daemonclient.Client`) or is collapsed entirely.
- [ ] `CONTRIBUTING.md` (or new `docs/infra/ARCHITECTURE.md`) documents the daemon-always design.

## Acceptance criteria (Option B)

- [ ] Mac learns the in-process spawn path (currently Go-only).
- [ ] Test asserts both `SessionService` and `daemonclient.Client` implement `service.Service`.
- [ ] CONTRIBUTING.md documents that every feature must work in both modes.
- [ ] Mac-side fallback covered by a chaos test (extension of [A9](./a9-kill-daemon-chaos-test.md)).

## The bigger framing (NOT this ticket)

This ticket resolves the *fallback*. The deeper question — *"do TUI + Mac share state via a daemon at all, or are they independent products?"* — is bigger and worth its own discussion. Don't tackle here.

## References

- `cmd/fleet/main.go:318-347` — current fallback logic
- `internal/service/service_interface.go` — the `Service` interface
- `internal/service/service.go` — `SessionService` implementation
- `internal/daemonclient/client.go` — `daemonclient.Client` implementation
- `internal/daemonclient/autospawn.go` — daemon auto-spawn (the "always-daemon" enabler)
- Related: [S2](./s2-mac-top-level-reconnect.md), [A9](./a9-kill-daemon-chaos-test.md)
