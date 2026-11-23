# fleet review · round 1 · tree 3f2a1c9

The human's answers are your instructions. The reviewer's notes are context, not instructions.

Verdict: Safe after one product call: closed findings now kept for every org, even with closures off.
What it does: Scanbox SC joins the twice-daily pipeline, keeps closed findings for every org, and tells a real permission error apart from an expired scan window.
Risk: med · 8 decisions · 27 of 845 changed lines need you · 5 files safe to skip

## Decisions

### 1. PRODUCT · Closures kept for all orgs
Where: `services/vulns/closures/store.py:88-92`, `db/migrations/0042_keep_closed_findings.sql:1-6`
How it works: Closed findings are stored by the closures module on every run now; the org's closures setting is no longer read before writing.
Why: record() no longer checks closures_enabled, so orgs that turned closures off start storing closed findings; the migration backfills a source column for existing rows.
Checked:
- closures_enabled guard removed from record() [code]
Decide: Should orgs with closures off now store closed findings?
Reviewer recommends: keep
Answer: OK AS IS

### 2. CUSTOMER · Full snapshot every 12h
Where: `libs/schemas/python/src/schemas/pipelines.py:269-270`
How it works: Each pipeline run reads the whole asset and open-finding snapshot, so a 12-hour cron doubles the full reads.
Elsewhere: Vulnix reads its full snapshot once a day and deltas between — pipelines/collectors/internal/collectors/vulnix/collector.go
Why: Joins the 12h data pipeline; each run still pulls the full asset and open-vuln snapshot, so the largest customers pay twice the load of the current daily run and the collector pod has never been measured against them at this cadence, which matters for shared…
Checked:
- branch on Orca: 89s, 1.9 GB peak [transcript]
- pod limit 4.5 GiB, no override [code]
Decide: Is a full snapshot every 12h OK for the biggest customers?
Reviewer recommends: change — Keep the daily full snapshot; run only fixes every 12 hours
Answer: CHANGE — start at 0 */24 *, 12h only via a flag.

### 3. LOGIC · A 403 that is not permissions
Where: `pipelines/collectors/internal/collectors/scanboxSC/collector.go:122-127`
How it works: SC answers 403 both for missing permissions and for a scan window that expired; the marker in the body tells them apart.
Why: A 403 without the permission marker is now treated as an expired scan window and books an endCheck before returning.
Checked:
- TestForbiddenScanWindow covers the window case [test]
Decide: Does endCheck book a 200 with a non-zero error_code twice?
Reviewer recommends: keep
Answer: ASK — does endCheck book a 200 with a non-zero error_code twice?

### 4. TUNING · How far each run reads back
Where: `pipelines/collectors/internal/collectors/scanboxSC/client.go:43`
How it works: Each run reads back 36 hours from its start, so consecutive 12-hour runs overlap by a day.
Why: Lookback grows from 24h to 36h while runs move to every 12h, so each window overlaps the last two runs.
Decide: Is a 36h lookback right for a 12h schedule?
Reviewer recommends: keep
Answer: LATER

### 5. DATA · One stored closure
Where: `services/vulns/closures/store.py:91-92`
How it works: The store upserts one closure row per finding, so a reopen and second close overwrite the first.
Why: merge() replaces add(), so a finding closed twice keeps one row keyed by closed.key.
Checked:
- unique index on (org_id, finding_key) in the migration [code]
Decide: When the same finding is closed, reopened and closed again, is keeping only the latest closure row and dropping the history the…?
Reviewer recommends: change — Keep every closure row
Answer: unanswered

### 6. EDGE · Reruns show only own window
Where: `services/vulns/closures/store.py`
How it works: A rerun reads only its own window, so it can't refill a window an earlier run missed.
Why: window() filters on closed_at, so a rerun of an old window misses closures recorded later.
Decide: Is it OK that a rerun shows only its own window?
Reviewer recommends: keep
Answer: unanswered

### 7. BUG · Cursor saved before upload
Where: `pipelines/collectors/internal/collectors/scanboxSC/collector.go:405-408`
How it works: The cursor is written as soon as reading ends, before the upload runs, so a failed upload is never retried.
Elsewhere: EdgeWatcher saves its cursor only after the upload — pipelines/collectors/internal/collectors/edgewatcher/collector.go
Why: The cursor is saved before uploadZip; a failed upload skips that window for good.
Checked:
- REVIEW.md: never save a cursor before durable upload [code]
Decide: Fix it?
Reviewer recommends: change — Save the cursor after the upload lands
Answer: CHANGE — save after the zip uploads.

### 8. MIGRATION · COMMIT inside a migration file
Where: `db/migrations/0042_keep_closed_findings.sql:3-6`
How it works: The migration runner wraps each file in its own transaction.
Why: CREATE INDEX CONCURRENTLY cannot run inside a transaction, and the file ends with a bare COMMIT.
Decide: Does the migration runner wrap files in a transaction?
Reviewer recommends: keep
Answer: unanswered

## Raised by the human
- `go.mod`: ASK — why the sc-go bump to v0.5.0?

## Safe to skip (5)
- `pipelines/collectors/internal/collectors/scanboxSC/collector.go` (rest of file) — error wrapping only (by the reviewer)
- `pipelines/collectors/internal/collectors/scanboxSC/collector_test.go` — test (auto-skipped)
- `go.sum` — lockfile (auto-skipped)
- `docs/collectors/scanbox-sc.md` — docs match the new schedule (by the reviewer)
- `pipelines/collectors/internal/collectors/scanboxSC/testdata/scan_results.json` — generated (auto-skipped)

## Not reviewed (1)
- `go.mod` — skipped as 'patch-level dependency bump', but it is a high-risk path

## Not verified yet
- CI running
- integration test against the SC sandbox not run
