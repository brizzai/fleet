# S6 — Snapshot proto unification

**Status:** open
**Tier:** S
**Effort:** 2 hours
**Stack:** Both (proto + Go + Swift)

## Problem

There are **two independent snapshot systems** today:

- **TUI:** `internal/ui/snapshot.go` writes a *directory* `~/.config/fleet/snapshots/<ts>_<title>/` containing `pane_raw.txt`, `pane_clean.txt`, `debug_tail.txt`, `snapshot.json` (structured, with `detection.mismatch` field). The `.claude/skills/debug-status` skill reads this format.
- **Mac:** `app/Sources/Fleet/DaemonClient/SnapshotWriter.swift` writes a single *markdown blob* `snapshot-YYYYMMDD-HHMMSS.md` containing the daemon's `GetDiagnostics` response. The skill doesn't know how to read it.

Worse: `internal/daemonsrv/diagnostics.go:35` explicitly says *"the markdown shape is human-readable and intentionally not stable."* That's actively hostile to AI-assisted debugging.

## Why now

Both critics flagged: small effort, real payoff. The AI debugging workflow you built for the TUI should just work for the Mac.

## Proposed solution

1. **Define `Snapshot` proto** in `proto/fleet/v1/fleet.proto`:
   ```proto
   message Snapshot {
     int32 schema_version = 1;
     ClientInfo client = 2;          // {tui|mac|cli, version, pid}
     DaemonState daemon = 3;         // version, uptime, db_size, schema_version
     string log_tail = 4;            // last N lines of debug.log
     optional string pane_capture = 5;   // TUI-side: pane content
     optional string pane_clean = 6;     // ANSI-stripped
     optional string view_dump = 7;      // Mac-side: AppModel state dump
     google.protobuf.Timestamp created = 8;
   }
   ```
2. **TUI writer (`internal/ui/snapshot.go`):** populate the new `Snapshot` message, write `snapshot.pb.json` (JSON encoding of proto) + keep the supplementary `.txt` files for human readability.
3. **Mac writer (`app/Sources/Fleet/DaemonClient/SnapshotWriter.swift`):** switch from markdown blob to the same dir-of-files format. Include AppModel state dump in `view_dump`, FleetLog tail in `log_tail`.
4. **Skill update (`.claude/skills/debug-status/SKILL.md`):** add a paragraph "snapshots from Mac use the same format; read `snapshot.pb.json` for structured fields, supplementary `.txt` files for raw content."
5. **Bonus (optional):** mark the existing Mac markdown blob as deprecated, generate it alongside the new format for one version, then remove.

## Acceptance criteria

- [ ] A snapshot from the TUI and a snapshot from the Mac contain the same `snapshot.pb.json` schema (`schema_version=1`, same field set, `client.app` differs).
- [ ] `debug-status` skill reads both successfully (manual test: one snapshot of each, run through skill).
- [ ] Mac snapshot includes a usable AppModel state dump in `view_dump` (top 50 sessions, current theme, sheet flags, last 20 actions).
- [ ] Old snapshots not broken — skill falls back gracefully when `snapshot.pb.json` is absent (treat as `schema_version=0`).
- [ ] No daemon-side changes needed beyond proto regeneration.

## Out of scope

- Streaming snapshot upload (e.g., to a server).
- Snapshot diff tool ("compare two snapshots").
- Auto-snapshot on crash. Manual `Cmd-Shift-D` / TUI `D` key only.
- Migrating existing snapshots in `~/.config/fleet/snapshots/`.

## References

- `internal/ui/snapshot.go` — TUI snapshot writer
- `app/Sources/Fleet/DaemonClient/SnapshotWriter.swift` — Mac snapshot writer
- `internal/daemonsrv/diagnostics.go:35` — current "not stable" warning to remove
- `.claude/skills/debug-status/SKILL.md` — skill to extend
- `proto/fleet/v1/fleet.proto` — add `Snapshot` message
