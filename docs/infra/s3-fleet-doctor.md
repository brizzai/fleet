# S3 — `fleet doctor` subcommand

**Status:** open
**Tier:** S (resilience)
**Effort:** 0.5 day
**Stack:** Go (CLI)

## Problem

When a user hits a weird bug, today's triage path is "tail debug.log + dig through ~/.config/fleet/ + run `tmux ls`." There's no single command that says "here is the health of your fleet install."

This is going to keep happening, and you'll keep grepping the same files. Build it once.

## Why now

- Both critics flagged "you'll write this anyway the third time you debug a support issue."
- Pairs with [S6 (snapshot proto)](./s6-snapshot-proto.md): doctor output can be a piece of every snapshot.
- Mac app is about to ship to users — gives them something useful to paste in a bug report.

## Proposed solution

New `fleet doctor` subcommand. Prints a structured report covering:

### Sections

1. **Fleet versions** — daemon binary version, TUI version (same binary?), Mac app version (if installed), proto version.
2. **Daemon state** — alive? socket path? pid (from pidfile)? uptime? db size? last backup file?
3. **Database integrity** — run `PRAGMA integrity_check`, report result. List schema_version if [A8](./a8-schema-versioning.md) has landed.
4. **tmux state** — `tmux ls` filtered to `fleet_*` sessions, count. Mismatch with db?
5. **Hooks** — `~/.config/fleet/hooks/` listing (count + age of oldest/newest). `~/.claude/settings.json` has fleet entries?
6. **Config directory** — `du -sh ~/.config/fleet/`, perms, sub-dir contents.
7. **External tools** — `tmux`, `claude`, `gh`, `code`, `git` versions + paths. Flag missing.
8. **Chrome NMH** — manifest installed at expected path? Extension installed?

### Output

- Default: human-readable, color-coded (PASS/FAIL/INFO).
- `--json`: structured JSON for AI consumption (include in snapshot).
- `--quiet`: only failures.

### Reuse

`internal/diagnostics/diagnostics.go` already collects a chunk of this for bug reports. Refactor: split into reusable check functions, both `fleet doctor` and `GetDiagnostics` RPC call into the same helpers.

## Acceptance criteria

- [ ] `fleet doctor` prints all 8 sections in <3s on a healthy install.
- [ ] `fleet doctor --json` returns valid JSON; can be piped to `jq`.
- [ ] Each section fails independently — if daemon is down, db/tmux/config sections still print.
- [ ] Output usable as the first thing in a bug report (color-coded, scannable).
- [ ] No new dependencies on the daemon being running — works when daemon is dead.

## Out of scope

- Auto-fix anything ("doctor heal" — future feature).
- Network diagnostics (we don't have network components).
- Streaming live state (it's a snapshot, not a watch).

## References

- `internal/diagnostics/diagnostics.go:53-96` — existing diagnostic collector to refactor
- `cmd/fleet/main.go` — where subcommands are registered
- `internal/session/storage.go` — db path + integrity check
- `internal/hooks/` — hook file dir + settings.json verification
