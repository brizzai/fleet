# A8 — Schema versioning + migration playbook

**Status:** open
**Tier:** A
**Effort:** 0.5 day
**Stack:** Go (storage)

## Problem

`internal/migration/migrate.go` handles the one-shot `brizz-code` → `fleet` rename (config dir, env vars, settings.json scrubbing). It does **not** version the SQLite schema.

The Mac app now reads the same `state.db` via the daemon. Scenario:
1. User on v2.1.0 (TUI-only, no Mac, no daemon) has a `state.db` with schema vN.
2. They `brew upgrade fleet` to v2.2.0. New daemon binary opens their db.
3. v2.2.0 adds a column (e.g., `sessions.preview_template`). On first read, the daemon's prepared statements expect the column. **Crash, or silent NULL where data should be.**

There's no `schema_version` table, no migrations runner, no "old db needs upgrade" detection.

## Why now

Pragmatist explicitly flagged this as the one risk that can destroy user data (alongside [S1](./s1-daemon-singleton-lock.md)). Adding a column on `fleet-ui` today would already trigger this — the Mac app's daemon binary will open a TUI user's db.

## Proposed solution

1. **`schema_version` table** (one row, one column: `version int not null`). Initialize to current version on first open.
2. **Migrations dir** at `internal/session/migrations/`:
   - Files: `001_initial.sql`, `002_add_slot_bindings.sql`, etc.
   - Loaded via `embed.FS`.
   - Applied in order; transactional per migration.
3. **`storage.Open`:**
   - Read `schema_version` (or treat absence as v0).
   - For each pending migration: run inside `BEGIN` … `COMMIT`. On failure: rollback, fail-fast with "schema migration failed: <error>. Backup at state.db.bak-<ts>. Restore + report bug."
   - On success: write new `schema_version`.
4. **Playbook at `docs/infra/SCHEMA-MIGRATIONS.md`:**
   - How to add a column (additive, NULL-able, default-able).
   - How to drop a column (don't — write a new migration that creates a new table and copies, in a future ticket).
   - What NOT to do mid-development on `fleet-ui` (no destructive renames; assume any DB out there might still need to upgrade).
   - When migrations require downtime (never — single-process, fast schema changes).

## Acceptance criteria

- [ ] Fresh install creates `schema_version` table at the latest version.
- [ ] Existing db without `schema_version` table is treated as v0 and migrated forward.
- [ ] Each migration runs in a transaction; failure leaves db at previous version.
- [ ] Test: copy `state.db` from current `master` (no schema_version), open with new code, end up at latest version with no data loss.
- [ ] `docs/infra/SCHEMA-MIGRATIONS.md` exists with clear "what to do / what not to do" guidance.
- [ ] No regression in `go test -race ./internal/session/...`.

## Out of scope

- Downgrade / rollback support. (Forward-only.)
- Online schema changes for high-concurrency. (We're single-process; not needed.)
- Backup before migration. ([S1](./s1-daemon-singleton-lock.md) handles backup separately.)
- External migration tooling (`golang-migrate`, etc.) — overkill for our schema size.

## References

- `internal/session/storage.go:43-85` — current Open path (where the migration runner goes)
- `internal/migration/migrate.go` — existing first-run migration scaffold (different purpose; don't conflate)
- [S1 daemon singleton lock](./s1-daemon-singleton-lock.md) — provides the pre-migration backup
