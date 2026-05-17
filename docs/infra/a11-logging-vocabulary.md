# A11 — Logging vocabulary doc (skip the sweep)

**Status:** open
**Tier:** A
**Effort:** 30 minutes
**Stack:** Both (doc + light Go helper)

## Problem

247 Go logger calls with inconsistent key names (`err` 95×, `error` 36×, similarly `session_id` vs `sessionId` vs `session`). Swift `FleetLog` is a homegrown logfmt string formatter with no field discipline.

No shared vocabulary → can't filter logs cleanly → every future log-search/observability tool has to guess.

## Why now

Pairs with [A7 (request IDs)](./a7-request-ids.md): without a vocabulary, A7 adds yet another field name to the inconsistent pile.

Pragmatist critic was explicit: *"A one-page vocabulary doc is fine (30 min). Doing a sweep of 247 logger calls to rename err/error is paint."* Agree — write the doc, don't retroactively sweep.

## Proposed solution

1. **Create `docs/infra/LOGGING.md`** with the field vocabulary:

   | Field | Type | Used when |
   |---|---|---|
   | `msg` | string | Always (first positional in slog) |
   | `err` | string | Error description (use `%w` wrap in fmt.Errorf, log the wrapped) |
   | `request_id` | string (UUID) | Per-RPC trace (see A7) |
   | `client` | string | One of `tui`, `mac`, `cli`, `daemon` (the source) |
   | `rpc` | string | RPC method name when applicable |
   | `session_id` | string | Session instance ID (8hex-unix format) |
   | `repo_root` | string | Absolute path to repo |
   | `dur_ms` | int64 | Duration in milliseconds |
   | `pid` | int | Process ID where relevant |
   | `path` | string | Filesystem path |

   Plus: forbidden synonyms (`error` → use `err`, `sessionId` → use `session_id`, `duration` → use `dur_ms`).

2. **Go side helper** in `internal/debuglog/`:
   - `WithRPC(ctx, method, requestID, client) *slog.Logger` — child logger with the standard fields.
   - `WithSession(sessionID string) *slog.Logger` — child logger.
   - Used by new code; old code unchanged.

3. **Swift side note** in the doc: when `FleetLog` is replaced with `swift-log` (future ticket), use the same field names.

## Acceptance criteria

- [ ] `docs/infra/LOGGING.md` exists, one page, table + forbidden-synonyms list.
- [ ] `debuglog.WithRPC` + `debuglog.WithSession` helpers exist with tests.
- [ ] LOGGING.md is referenced from CLAUDE.md / CONTRIBUTING.md so future contributors find it.
- [ ] **No retroactive sweep** — that's explicitly out of scope.

## Out of scope

- Renaming the 247 existing logger calls to match the vocabulary. (Paint.)
- Migrating Swift `FleetLog` to `swift-log`. (Separate, larger ticket.)
- Structured logging in the TUI bubbletea event loop (different lifecycle).
- Enforcement via linter — would catch real value but not for $5/year of bugs.

## References

- `internal/debuglog/` — where helpers go
- `app/Sources/Fleet/Util/FleetLog.swift` — Swift side reference
- Related: [A7 request IDs](./a7-request-ids.md), [S1 daemon singleton lock](./s1-daemon-singleton-lock.md) (uses logging too)
