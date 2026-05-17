# S1 — Daemon singleton lock + db integrity + nightly backup

**Status:** open
**Tier:** S (resilience)
**Effort:** 1 day
**Stack:** Go (daemon)

## Problem

`PrepareSocket` in `internal/daemonsrv/socket.go:38-60` correctly handles the Unix socket file (dial-probe-then-remove for staleness). But the daemon has **no flock on `state.db`**.

If two `fleet daemon` invocations race — or `fleet daemon` races a TUI in `--standalone` mode that opens its own in-process `SessionService` — both briefly open the same SQLite file. modernc/sqlite WAL with `busy_timeout=5000` (`storage.go:60-62`) serializes writes, but you've got two `SessionService` instances racing tmux state and slot bindings concurrently. There's no `flock`, no pidfile, no `PRAGMA integrity_check` on open, no rotating backup.

This is the **only failure mode in the brainstorm that can destroy user data** silently. Veteran critic flagged it as the #1 silent corruption vector.

## Why now

- TUI has real Homebrew users today.
- The Mac app is about to start sharing the same db via the daemon.
- A bad `&` in a shell script + `fleet daemon` is enough to trigger the race.
- Recovery story today: none. The user loses sessions, slot bindings, claude resume IDs.

## Proposed solution

1. **`flock` on `state.db` at daemon startup.**
   - `syscall.Flock(fd, LOCK_EX | LOCK_NB)` immediately after `storage.Open`.
   - On `EWOULDBLOCK`: print a clear "fleet daemon already running (pidfile: …)" error and exit 1.
2. **Pidfile at `~/.config/fleet/daemon.pid`** for debug visibility (overwrite atomically; remove on graceful shutdown).
3. **`PRAGMA integrity_check` on `storage.Open`.** Run once; on `ok` result continue, otherwise fail-fast with: "state.db corrupted (`<error>`). Restore from `~/.config/fleet/state.db.bak-*` or remove to start fresh."
4. **Rotating backup.** On daemon start, after passing integrity check, copy `state.db` → `state.db.bak-YYYY-MM-DD-HHMMSS`. Keep last 3, delete older.

## Acceptance criteria

- [ ] Run `fleet daemon` twice in parallel → second instance exits within 1s with a clear error mentioning the pidfile path.
- [ ] Pidfile is removed on `SIGTERM` shutdown; remains on `SIGKILL` (manual cleanup OK).
- [ ] Corrupting `state.db` (e.g., `dd if=/dev/urandom of=state.db conv=notrunc bs=1 count=10 seek=100`) produces an actionable error on next daemon start.
- [ ] After daemon starts 4 times, exactly 3 `state.db.bak-*` files exist (newest 3).
- [ ] Existing tests still pass (`go test -race ./...`).

## Out of scope

- SQLite migration framework — separate ticket [A8](./a8-schema-versioning.md).
- Off-host backup (S3, dropbox, etc.) — manual user responsibility.
- Auto-recovery from corruption — fail-fast is the right call.

## References

- `cmd/fleet/daemon.go:81-101` — daemon startup sequence
- `internal/daemonsrv/socket.go:38-60` — stale-socket cleanup (model for the pidfile pattern)
- `internal/session/storage.go:43-85` — current Open/Close, where flock + integrity_check go
- `internal/migration/migrate.go` — first-run migration scaffold (don't conflate with schema migrations)
