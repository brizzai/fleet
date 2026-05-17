# A7 — Request IDs + per-client trace field

**Status:** open
**Tier:** A
**Effort:** 1 day
**Stack:** Both (Go daemon + clients, Swift Mutator)

## Problem

Zero hits for `request_id` / `RequestID` in the codebase. No gRPC interceptors registered in `daemonsrv/server.go`. Daemon logs can't tell whether an RPC came from TUI, Mac, or CLI.

Practical example: a user reports *"Restart Session doesn't work"*. The daemon log has 20 `Restart` RPCs in the past hour from three sources. Which one was theirs? Today: 30+ minutes of grep. After this ticket: one filter on `client` + `request_id`.

## Why now

Critics split:
- **Pragmatist (3/10 urgency):** "you've triaged zero cross-client bugs so far."
- **Veteran (promote):** "every support email is detective work without it. With a real TUI user base on Homebrew + Mac app coming, side with veteran."

Decision: do it now, but **skip OpenTelemetry**. Just `request_id` UUID + `client` field in slog. Don't over-build.

## Proposed solution

1. **Add `grpc-ecosystem/go-grpc-middleware/v2`** to `go.mod`.
2. **Daemon-side interceptor** in `daemonsrv/server.go`:
   - Mint UUID `request_id` per RPC.
   - Extract `client` from metadata header (`fleet-client: tui|mac|cli`).
   - Attach both to ctx via context value.
   - Log RPC entry + exit (with duration + error) using consistent fields.
3. **Go client (`daemonclient/client.go`):** unary + stream client interceptors that inject `fleet-client: tui` (or `cli`) header on outbound RPCs.
4. **Swift client (`Mutator.swift`):** set `fleet-client: mac` in metadata via `ClientInterceptor` from grpc-swift-2.
5. **Helper:** `debuglog.WithRPC(ctx)` returns a child logger with the standard fields. Use in every server-side RPC handler.

## Acceptance criteria

- [ ] Every daemon log line for an RPC includes `client_id` + `request_id` fields.
- [ ] `grep daemon.log | jq '.request_id' | sort -u | wc -l` shows unique IDs per RPC.
- [ ] Mac and TUI sending the same RPC produce distinct `client` values.
- [ ] CLI commands (`fleet list`, `fleet add`) set `client: cli`.
- [ ] Streaming RPCs (`ListSessions`) have one request_id per stream; intra-stream messages share it.
- [ ] No regression in `go test -race ./...`.

## Out of scope

- OpenTelemetry exporter, collector, Jaeger. (Defer — single-process daemon doesn't benefit yet.)
- Distributed tracing across processes (Mac → daemon is a single hop).
- Surfacing request_id in user-facing error messages. (Could be nice for bug reports — separate ticket.)
- Retroactive sweep of existing log calls.

## References

- `internal/daemonsrv/server.go` — add server interceptors
- `internal/daemonclient/client.go` — add client interceptors
- `app/Sources/Fleet/DaemonClient/Mutator.swift` — add Swift client interceptor
- `internal/debuglog/` — add `WithRPC` helper
- [grpc-ecosystem/go-grpc-middleware/v2](https://github.com/grpc-ecosystem/go-grpc-middleware)
- Related: [A11 logging vocabulary](./a11-logging-vocabulary.md) (defines the field names)
