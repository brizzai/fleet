# Self-review — design spec (v1, schema v2)

Status: building. Branch `feat/self-review` (fresh from master). Supersedes PR #299's reader for the self-review use case; #299's diff engine is ported, its PR-queue reader is not.

## Why

Agents write more code than a person can read. Reading every line is not the goal — **deciding the few things only you can decide, and knowing what was safe to skip and why**, is. Industry practice (Nov 2026): machines verify first; humans decide product calls, risky paths, owned trade-offs, and read *evidence*, not diffs. Nothing in fleet's world does this for a session's own worktree before it becomes a PR.

## The decisions already made

| Question | Decision |
|---|---|
| Where you answer | **In fleet**, one full-screen view with **two tabs** from **one** reviewer call: **Decide** (default — verdict + what the change does → decision list, answer with one key → a card that leads with the reviewer's recommendation, explains the mechanism and shows only that decision's hunks) and **Walkthrough** (how the change works, chapter by chapter, every file in context). The full diff one key away. |
| Ask *and* explain | A comparison with Plannotator's Guided Review on the real DELTA-2531 (Zephyr EDR) PR: fleet found the risks Plannotator missed, but the person answered 3 of 4 decisions "ask" — "are we okay with it? how do others handle it?". Asking without explaining isn't enough. So each decision carries a recommendation, how the mechanism works and how comparable code in the repo does it, and the Walkthrough explains the change the way Plannotator does — heart → consequences → glue → docs — while still asking. |
| Trigger (v1) | **Manual `v`** on any session row or checkout header (main clone or worktree): prepares (1–2 min) or opens. Auto-on-finish is a later opt-in. |
| What a review belongs to | **The checkout** (repo or worktree root — the sidebar's checkout header), not a session. Several sessions can work one worktree, and the change under review is the tree's, whoever typed it. It shows as its own sidebar row, first under the checkout header. |
| Reviewer (v1) | A **fresh `claude -p`** (Opus default, `review_model` config), read-only tools, worktree as cwd, the session's account (or `claudeaccount.Select` if exhausted). Cross-family (Codex) later. |
| Codebase | Fresh branch; **port** #299's `internal/review` engine, theme colours, file classifier, `claude -p` runner. Do not port `ReaderDialog`, the PR queue, `c/C/s` keys, or `(repo,pr)` tables. |

## Vocabulary

Words the screens use, and the one definition of every number. The CLI, the sidebar's review row, the view's header and its footer all print `selfreview.Counts` (`CountsOf(run, answers)`) and its phrases — none of them counts on its own. They used to: the row said 9, the CLI 6, and "lines need you" was 463 in the TUI against 106 in the CLI for the same run. `TestReviewCountsAgreeEverywhere` (ui) and `TestPrintReviewCountsMatchTheOneDefinition` (cmd/fleet) pin them to the real DELTA-2524 run (`internal/selfreview/testdata/delta2524_run.json`): **4 to decide · 106 of 1,489 changed lines need you · 14 files safe to skip**.

- **Review run** — one reviewer job over one snapshot of a checkout. Rounds 1..3.
- **Review row** — the checkout's review in the sidebar, first under its checkout header: `✎ Review · 4 to decide` / `preparing…` / `ready to send` / `nothing to change` / `2 files not reviewed` / `nothing needs you` / `⚠ code changed` / `sent · agent working` / `press v for round 2` / `failed`.
- **Send target** — the session `S` types the answers into: one of the checkout's sessions, chosen with `t`, stored on the run.
- **Verdict** — line 1, ≤18 words, starts with `Ready`, `Ready once …` or `Not ready: …`, then what it hinges on in plain words.
- **What it does** (`summary`) — ≤3 short sentences, ≤60 words, plain words a product manager understands. Under the verdict on both tabs, wrapped whole.
- **Recommend** — the reviewer's own answer to each decision: `keep` (yes, the code stays) or `change` + the concrete change (≤15 words). Leads the card in bold; `n` opens the change note prefilled with it.
- **How it works** (`how`) — ≤40 words: the mechanism around the decision, what a person must understand to answer. **Elsewhere** (`elsewhere`, optional) — ≤25 words, how comparable code in this repo handles the same thing, ending with the path the reviewer read.
- **Scope decision** — what the user (or the ticket) asked for and the change doesn't do. Kind `scope`, always decision 1, belongs to no chapter. `y` ships without it (the message asks the agent to note the follow-up in the PR description); `n` opens prefilled `Also implement: <missing items>`. Its why lists every missing item and is never cut.
- **Chapter** — one step of the Walkthrough: ≤7-word title, a role (`heart` → `consequence` → `glue` → `docs`, the reading order), ≤70 words on HOW that part works (mechanism, not risk), its files, and the decisions whose code is in it. Every changed file sits in exactly one chapter.
- **Decision** (a *stop* in the schema) — something only a person can settle. Each has one **Decide** question: yes/no, where *yes* means "keep the code as is". **"N to decide"** = decisions with no answer yet. Decisions are never hidden past a cap.
- **Not reviewed** — a changed file the reviewer covered nowhere (computed by fleet). One folded row under the list (⏎ lists the files, first), **never counted as a decision**; `x` raises one as a change of your own, `r` reviews again. On the Walkthrough each sits in its chapter as `⚠ not reviewed — <fleet's reason>`.
- **Safe to skip** — every skip-list entry: **auto-skipped** (fleet's rules: tests, lockfiles, generated) or **reviewer: safe to skip** (with its reason). Fleet still flags what it can: an auto-skipped test file whose diff removes assertions reads `⚠ tests changed — assertions removed`.
- **Changed lines need you** — the changed lines inside a decision's hunks, out of every changed line.
- **Not verified yet** — verification not finished (CI running, tests not run). Never a decision; a line about CI drops once the live rollup has finished.
- **Answer** — per decision: `✓ ok as is` (y — the footer says `y keep`), `✎ change…` (n + note, prefilled with the recommended change), `? ask agent…` (a + question), `· later` (l). A mode (accent + underline), never a fill. Only change, ask and an accepted scope are sent; when every answer is ok or later, `S` offers to approve instead of waking the agent.

**Nothing the person can't act on appears as an alert.** Every warning is a decision (an intent gap is the scope decision, not a red banner), a status with a key that fixes it (`⚠ code changed since this review: r to refresh`, `⚠ Not reviewed · 2 files ⏎`), or dim information (`high risk`, `· scope: decision 1`). Red stays semantic — an error (`✕ CI failed`), never a question.
- **Added by you** — a skipped or unreviewed file raised with `x` (a change with your note).

## User flow (v1)

1. Sessions in a checkout finish work. Press **`v`** on any of them, or on the checkout header (also: `.` menu "Review Changes", palette "Review This Checkout's Changes"). An origin header names no single checkout and refuses ("pick a checkout").
2. No run for this snapshot → fleet starts one (tea.Cmd, never Update) and the row reads `✎ Review · preparing…` at once. Same snapshot already reviewed → opens it. Snapshot changed since → opens the old one with `⚠ code changed since this review: r to refresh` in its status line.
3. Ready → `✎ Review · 4 to decide` (bold until opened, dim after). The row's preview shows the verdict, the counts and the keys; a session row in the checkout shows the verdict under its prompt strip.
4. ⏎ on the row (or `v` on any row of the checkout) opens the **Review view** on the first unanswered decision. Space rotates through review rows with unopened decisions like waiting sessions. `d` on the row discards the review (y/n); `.` offers Open / Review Again / Discard.
5. Answer from the list with one key — `y n a l`; `n`/`a` open a one-line note (`n` starts as the recommended change: ⏎ sends it as is). **Each answer moves to the next unanswered decision.** ⏎ moves the keyboard into the card (`J/K` next/previous change, `e` more context). `s` lists the files with no decision (not reviewed first, then safe to skip; `x` raises one), `d` shows the full diff, `?` lists every key and who reviewed it.
5b. **`2` Walkthrough** when you need to understand before you decide: the scope decision above the chapters, then each chapter with its ◆N decisions (or `safe to skip`). ⏎ moves into a chapter's card: ⏎ on a decision opens it on Decide, ⏎ on a file opens that file's diff (esc comes back). `1` returns to Decide. Answers happen only on Decide.
6. When every decision has an answer the cursor lands on the finish row — `✓ All 4 decisions answered` over the button `S send to ✻ api` (or `A approve: nothing to change`); the card beside it lists exactly what will be sent. Confirmations: `A` with unanswered decisions (`Approve with 2 decisions unanswered?`), `r` with unsent answers (`Review again? Your 3 answers here won't be sent.`), `S` with only ok/later answers (`Nothing to change. Approve instead?`), `S` with no live session (`Start a new ✻ session with your answers?`).
7. **`S` sends** to the target: writes `.fleet/review/round-N.md` (git-excluded first) and delivers one compact message through `internal/deliver` (`RefuseRunning` — never queued; a refusal toasts in plain sentences, e.g. `Not sent: api has stopped. Restart it from the sidebar.`). The view becomes the **sent screen**: `✓ Sent to ✻ api: 1 change, 1 question, 1 ok, 1 left for later.`, `● The agent is working on your answers.` and what was sent. The row reads `sent · agent working` until the agent finishes, then `press v for round 2`.
8. `v` → round N+1 (previous run + answers as context; "never re-ask an answered decision"; the header says `Your 2 changes from round 1: 1 done, 1 not done yet`). If the checkout hasn't moved since the send, `v` says so and reopens the sent run. Cap 3 rounds.
9. **`A` approves** this snapshot: the view closes and the row goes away.

### Message delivered to the agent (example)

```
[fleet review · round 1 · tree 3f2a1c9]
2 to change, 1 question, 3 ok. Apply the changes, answer the question, commit, then stop.
1. CHANGE Full snapshot every 12h (libs/schemas/python/src/schemas/pipelines.py:270): start at 0 */24 *, 12h only via a flag.
2. CHANGE Cursor saved before ingest ack (pipelines/collectors/internal/collectors/scanboxSC/collector.go:402): save after the zip uploads.
3. ASK A 403 that is not permissions: does endCheck book a 200 with a non-zero error_code twice?
Details: .fleet/review/round-1.md
```
Only the human's answers + the stop titles/anchors are sent — never raw model prose (prompt-injection hygiene: the diff is attacker-influenced content). A change note that is the recommendation, sent as prefilled, is fine: the person read it and pressed ⏎, so it is their answer. A scope decision answered `y` is listed too — `1. SCOPE OK <title>: the user accepts shipping without it; note it as a follow-up in the PR description` — and counts as acting (it wakes the agent); the missing items themselves stay in `round-N.md`.

## Screens

Captured from the drive-fleet sandbox seeded with the real DELTA-2524 run (prose hand-corrected to the reviewer rules below). `┏`-style accent borders aren't visible in text: the panel with the keyboard has the accent border; the selected row is a fill (band, `▸` in accent).

Hierarchy, top to bottom: the verdict, `What it does: …` (dim label, wrapped whole; on a terminal too short for it, it gives way whole rather than being cut), then **one** status line — exceptions first (`high risk` as plain information, `✕ CI failed: …`, `⚠ code changed since this review: r to refresh`), then `106 of 1,489 changed lines need you · N files not reviewed · 14 files safe to skip · scope: decision 1 · CI ✓`, lower parts dropping whole when the line is short; round ≥2's follow-up gets its own line. There is no intent-gap banner: the gap is decision 1. Progress (`0 of 4 answered`) is the title bar's right inset. The reviewer's model, time, files read, cost and snapshot live in `?`, not the header. The list is number, title (wrapped, never cut) and answer glyph; under a rule, one folded row each for **Not reviewed** and **Safe to skip**. The card leads with **`Recommend: …`** and the **Decide question**, both bold, then How it works, Elsewhere, Why it matters, Checked (only unverified items are marked: `(agent's word, not checked)`), your answer, and the code — trimmed to the cited lines ±3 (a new file is one hunk; the card must not open on line 1), `◆ collector.go · L229–268   1 of 2`. Its title is `1 of 4 · deploy order` with the kind as a dim phrase (product call, customer impact, likely bug, deploy order, data-loss risk, edge case). The footer is one line — the row's keys, then `S …` and `? keys` flush right; no hints in panel borders.

### Decide, wide (≥100 cols) — the real DELTA-2531 run, prose hand-upgraded to v2

Tabs in the title bar: `1 Decide  2 Walkthrough`, the active one a selection pill (switching moves the keyboard). The card leads with the recommendation (`n` → the note starts as that change), then the question, How it works, Elsewhere (its cited path on a line of its own, kept by its tail), Why it matters, Checked, code.

```
╭─ Review · DELTA-2531-zephyr-edr-dry-run    1 Decide   2 Walkthrough ─────────────────────────────── 0 of 5 answered ─╮
│ Ready once Zephyr never rejects queries over a temporary limit and incremental collection gets its own ticket.       │
│ What it does: Zephyr EDR plans now pass a dry run before they collect: seven checks that prove the key and each      │
│ event stream. Every plan collects all five event streams, and a stream that fails the same way every run is skipped  │
│ instead of failing the whole run.                                                                                    │
│ 273 of 1,341 changed lines need you · 10 files safe to skip · scope: decision 1                                      │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ Decide ───────────────────────────────────────╮╭─ 3 of 5 · data-loss risk ──────────────────────────────────────────╮
│  1  Ticket's incremental collection not done   ││                                                                    │
│  2  Customers who turned streams off get       ││ Recommend: change — retry a failed query once before treating      │
│     them                                       ││ the stream as failing   n                                          │
│▸ 3  Temporary query failures could silently    ││                                                                    │
│     drop data                                  ││ Treat every Zephyr FAIL answer as a permanent stream failure?      │
│  4  Older on-prem collectors may reject new    ││                                                                    │
│     plans                                      ││ How it works   Connect lets a 403 through in dry runs. On normal   │
│  5  Dry run can take nearly half hour          ││                runs a stream is skipped when its query fails every │
│                                                ││                run, and Zephyr's FAIL status now counts as that    │
│ ────────────────────────────────────────────── ││                kind of failure, like a 403.                        │
│  Not reviewed · none                           ││                                                                    │
│  Safe to skip · 10 files                     s ││ Elsewhere      EdgeWatcher retries a failed query once before      │
│                                                ││                giving up                                           │
│  Not verified yet: CI is still running on head ││                …ctors/internal/collectors/edgewatcher/collector.go │
│  c1cc6a6881                                    ││                                                                    │
│                                                ││ Why it matters A query Zephyr answers FAIL is skipped as           │
│                                                ││                permanent. If FAIL can also mean a temporary limit  │
│                                                ││                such as quota, that run silently misses the         │
│                                                ││                stream's events.                                    │
│                                                ││                                                                    │
│                                                ││ Checked        ✓ failsEveryRun treats errXQLQueryRejected (the     │
│                                                ││                  FAIL status) the same as a 403 (collector.go 489) │
│                                                ││                ✓ The poll paginator now wraps every FAIL status as │
│                                                ││                  errXQLQueryRejected (collector.go 855)            │
│                                                ││                ✓ The README says FAIL fails the same way every     │
│                                                ││                  run, with no live tenant example                  │
│                                                ││                                                                    │
│                                                ││ ◆ collector.go · L449–495                                   1 of 2 │
```

### Decide, narrow (<100 cols): each title with its question and `rec: keep|change`; ⏎ opens the card full-width (the summary steps aside while it is open)

```
╭─ Review · DELTA-2531-zephyr-…    1 Decide   2 Walkthrough ─ 0 of 5 answered ─╮
│ Ready once Zephyr never rejects queries over a temporary limit and           │
│ incremental collection gets its own ticket.                                  │
│ What it does: Zephyr EDR plans now pass a dry run before they collect: seven │
│ checks that prove the key and each event stream. Every plan collects all     │
│ five event streams, and a stream that fails the same way every run is        │
│ skipped instead of failing the whole run.                                    │
│ 273 of 1,341 changed lines need you · scope: decision 1                      │
╰──────────────────────────────────────────────────────────────────────────────╯
▸ 1  Ticket's incremental collection not done                     rec: keep
     Ship without incremental collection and track it as a follow-up?
  2  Customers who turned streams off get them                    rec: keep
     Collect every stream for existing plans that had some turned off?
  3  Temporary query failures could silently drop data            rec: change
     Treat every Zephyr FAIL answer as a permanent stream failure?
  4  Older on-prem collectors may reject new plans                rec: change
     Can this ship without waiting for customer-run collectors to upgrade?
  5  Dry run can take nearly half hour                            rec: keep
     Accept a Zephyr EDR dry run of up to 28 minutes?

 ──────────────────────────────────────────────────────────────────────────────
  Not reviewed · none
 y keep  n change…  a ask…  ⏎ details         S send (answer one first)  ? keys
```

### Walkthrough, wide: scope above the chapters; a chapter's files with their fate

```
╭─ Review · DELTA-2531-zephyr-edr-dry-run   1 Decide   2 Walkthrough  ─────────────────────────────── 0 of 5 answered ─╮
│ Ready once Zephyr never rejects queries over a temporary limit and incremental collection gets its own ticket.       │
│ What it does: Zephyr EDR plans now pass a dry run before they collect: seven checks that prove the key and each      │
│ event stream. Every plan collects all five event streams, and a stream that fails the same way every run is skipped  │
│ instead of failing the whole run.                                                                                    │
│ 273 of 1,341 changed lines need you · 10 files safe to skip · scope: decision 1                                      │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ Walkthrough ──────────────────────────────────╮╭─ Chapter 3 of 5 · the glue ────────────────────────────────────────╮
│  Scope  ◆1 Ticket's incremental collection     ││                                                                    │
│         not done                               ││ Removing the four stream checkboxes                                │
│ ────────────────────────────────────────────── ││                                                                    │
│  1  Dry-run checks and the reshaped Collect    ││ The four per-stream switches leave the integration form: the       │
│     ◆2 ◆3 ◆5                                   ││ OpenAPI schema drops the fields, the generated Go, Python and      │
│  2  Skip only streams that always fail         ││ TypeScript models follow, and the on-prem form's required list     │
│     ◆3                                         ││ loses them. The webapp's important-modules warnings for those      │
│▸ 3  Removing the four stream checkboxes        ││ streams go too, with their tests and the form snapshot.            │
│     ◆2 ◆4                                      ││                                                                    │
│  4  Turning on the dry-run gate                ││ Decisions here                                                     │
│     safe to skip                               ││   ◆2 Customers who turned streams off get them                     │
│  5  Documentation                              ││   ◆4 Older on-prem collectors may reject new plans                 │
│     safe to skip                               ││                                                                    │
│                                                ││ Files · 8                                                          │
│                                                ││   ◆ openapi.yaml                in decision 2                      │
│                                                ││ ▸ ◆ important-modules.ts        in decision 2                      │
│                                                ││   ⚠ important-modules.test.ts   tests changed — assertions         │
│                                                ││                                 removed (auto-skipped tests)       │
│                                                ││   ✓ …lden-master.test.tsx.snap  safe to skip — snapshot (auto-     │
│                                                ││                                 skipped)                           │
│                                                ││   ◆ model_zephyr_edr_config.go  in decision 4                      │
│                                                ││   ✓ zephyr_edr_config.py        safe to skip — regenerated from    │
│                                                ││                                 openapi.yaml                       │
│                                                ││   ✓ models.ts                   safe to skip — regenerated from    │
│                                                ││                                 openapi.yaml                       │
│                                                ││   ◆ data_gen.go                 in decision 4                      │
│                                                ││                                                                    │
│                                                ││                                                                    │
│                                                ││                                                                    │
╰────────────────────────────────────────────────╯╰────────────────────────────────────────────────────────────────────╯
 ⏎ read its diff  j/k move  esc chapters                                                                         ? keys
```

### Walkthrough, narrow

```
╭─ Review · DELTA-2531-zephyr-…   1 Decide   2 Walkthrough  ─ 0 of 5 answered ─╮
│ Ready once Zephyr never rejects queries over a temporary limit and           │
│ incremental collection gets its own ticket.                                  │
│ What it does: Zephyr EDR plans now pass a dry run before they collect: seven │
│ checks that prove the key and each event stream. Every plan collects all     │
│ five event streams, and a stream that fails the same way every run is        │
│ skipped instead of failing the whole run.                                    │
│ 273 of 1,341 changed lines need you · scope: decision 1                      │
╰──────────────────────────────────────────────────────────────────────────────╯
▸ Scope  ◆1 Ticket's incremental collection not done
 ──────────────────────────────────────────────────────────────────────────────
  1  Dry-run checks and the reshaped Collect
     ◆2 ◆3 ◆5
  2  Skip only streams that always fail
     ◆3
  3  Removing the four stream checkboxes
     ◆2 ◆4
  4  Turning on the dry-run gate
     safe to skip
  5  Documentation
     safe to skip

 ⏎ decide it  j/k move  1 Decide  esc back                               ? keys
```

### Finish row (every decision answered)

```
╭─ Decide ───────────────────────────────────────╮╭─ ready to send ────────────────────────────────────────────────────╮
│  1  Backfill lost if collector ships first   ✎ ││                                                                    │
│  2  Fixes collected for orgs that opted out  ? ││ S sends 1 change and 1 question to ✻ Scanbox SC cursor:            │
│  3  Twice-daily full reads on Orca's         ✓ ││                                                                    │
│     appliance                                  ││   ✎ Backfill lost if collector ships first                         │
│  4  Fixes lost when an upload fails          · ││     ship the ingestor first; say so in the rollout notes           │
│                                                ││   ? Fixes collected for orgs that opted out                        │
│  ✓ All 4 decisions answered                    ││     does Dana know closed fixes are now collected for opted-out    │
│▸    S send to ✻ Scanbox SC cursor              ││     orgs?                                                          │
│                                                ││                                                                    │
│ ────────────────────────────────────────────── ││ 2 answers marked ok or later stay here: the agent has nothing to   │
│  Not reviewed · none                           ││ do for them.                                                       │
│  Safe to skip · 14 files                     s ││                                                                    │
│                                                ││                                                                    │
│                                                ││                                                                    │
│                                                ││                                                                    │
```

### After send

```
╭─ Review · DELTA-2524-scanbox-sc-pipeline ───────────────────────────────────────────────────────────────────── sent ─╮
│ Ready once you settle deploy order and whether opted-out orgs may have fixes collected.                              │
│ 106 of 1,489 changed lines need you · 14 files safe to skip                                                          │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯

   ✓ Sent to ✻ Scanbox SC cursor: 1 change, 1 question, 1 ok, 1 left for later.

   ● The agent is working on your answers.
   When it finishes, press v to review what it changed (round 2).

   What you sent:
     ✎ Backfill lost if collector ships first
       ship the ingestor first; say so in the rollout notes
     ? Fixes collected for orgs that opted out
       does Dana know closed fixes are now collected for opted-out orgs?

   v round 2   d full diff   esc back to the sidebar
```

### Nothing needs you

```
╭─ Review · DELTA-2524-scanbox-sc-pipeline ────────────────────────────────────╮
│ Ready: only docs, tests and a schedule constant changed, and none needs a    │
│ decision.                                                                    │
│ 14 files safe to skip                                                        │
╰──────────────────────────────────────────────────────────────────────────────╯

   ✓ Nothing needs you.

   The reviewer found no decision for you. 14 files are safe to skip; s shows
   why.

   A approves it. d shows the full diff if you want to read it anyway.
 A approve  s why skipped  d read anyway  esc back			 ? keys
```

### Main screen

```
│                                                    │ │  ⏎ open   . more   d discard                                  │
│▾ delta 1  2 ●                                      │ │                                                               │
│  ▾ DELTA-2524-scanbox-… *  2 ●                     │ │  reviewed by opus 61s · 2 sessions in this checkout           │
│    ✎ Review · 4 to decide                          │ │                                                               │
│    ● ✻ Scanbox SC cursor                           │ │                                                               │
│    ● ✻ Closed-findings tests                       │ │                                                               │
```

The review row (`SidebarItem.IsReview`, `review_row.go`) is the first child under its checkout header, at the session rows' indent, shown while a run exists or is preparing. Not a header (⏎ opens the review, it doesn't fold anything) and not a session (`selectedSession` is nil there; `w`/`a`/`p` act on its checkout). Bold ColorText while it needs you (ready, decisions waiting, not opened), dim otherwise — never coloured, the status dot owns status colour. A narrow sidebar takes the short form (`sent · working`, `v for round 2`), then drops "Review", and never cuts a word. `✎` is U+270E (Dingbats, width 1, in Menlo). `v` on the row while it reads `press v for round 2` starts that round; ⏎ opens the sent run.

Review rows appear and vanish on a timer (a prepare lands, the 5s sweep sees a run another process made), shifting the rows below. `rebuildFlatItems` therefore keeps the cursor on its row **by identity** (`targetForCursor` / `contextMenuTarget.find`, which knows `reviewRepo`) whenever the set of review rows changed.

## The reviewer job

### Inputs (assembled by fleet, budgets in brackets)
- **Snapshot**: `git.SnapshotTree` (temp `GIT_INDEX_FILE`, `add -A`, `write-tree` — includes uncommitted + untracked, never touches the real index). Anchor = tree SHA.
- **Diff**: `git diff -M -U3 <merge-base(base, HEAD)> <tree>`; classifier-folded files as numstat only. Cap ≈40k tokens; overflow files as stats (the model can `Read` them).
- **Base**: the `sessions.base_ref` (written at worktree creation) of the most recently created session in the checkout that has one → PR baseRefName → `git.GetDefaultBranch`.
- **Intent**: user prompts from **every session in the checkout** — `session.ReadClaudeTurns` for Claude, `FirstPrompt` for the rest — merged in time order and, when more than one session contributed, prefixed `[<session title>] `; fleet's own `[fleet review …` messages dropped. Budgets unchanged (first + latest prompts, ≤1.5k chars each). Plus `.fleet/ticket/*/ticket.md` if present. The reviewer's `SESSION:` line is the checkout's branch plus `(sessions: a, b)`.
- **Claims**: the newest closing reply across the checkout's sessions (≤3k chars), labelled with its session when several contributed — CLAIMS, not evidence.
- **Evidence**: PR CI rollup + failing check names (expose the per-check list `github/pr.go` already parses); `git log --oneline base..HEAD`.
- **Rules**: `REVIEW.md` at repo root + nearest ancestors of changed paths (≤4k).
- **Round ≥2**: previous run JSON + the human's answers.

### Call
`claude -p <instruction> --model <review_model|opus> --output-format json --verbose --allowedTools Read Grep Glob --disallowedTools Edit Write NotebookEdit --tools Read,Grep,Glob --strict-mcp-config --no-session-persistence`, input on stdin, `cmd.Dir` = worktree, `CLAUDE_CONFIG_DIR` per account, timeout 5 min, one repair retry on invalid JSON (same flags with `--tools ""`: no tools at all). Max 2 concurrent jobs; in-flight dedupe per (checkout, cache key). Flags verified against `claude --help` (Claude Code 2.1.327).

**Transparency.** `--verbose` turns the single result envelope into the full event array (init, each assistant message with its `tool_use` blocks, the `result` event last) — the plain envelope reports no tool calls, so "did the reviewer open any file?" was unanswerable. `selfreview.Parse` reads both the review and a `ReviewerMeta{turns, cost_usd, duration_ms, tools (calls by name), files (distinct paths Read/Grep/Glob touched, worktree-relative), files_read}`, stored as the run's computed `reviewer` field (no migration). Shown in the view header (`opus · 74s · 9 files read`, or a dim `read no files`) and in `fleet review`'s header and `--json`.

**No shell, no MCP.** The reviewer reads attacker-influenced diffs, so its toolset is the three read tools and nothing else: `--tools` decides which built-in tools *exist* for the call (the allow/deny lists only decide which run without a prompt), and `--strict-mcp-config` with no `--mcp-config` loads no MCP server whatever the user's settings name. There are deliberately no `Bash(git …)` allowances: `git log --output=<file>` writes and `git diff --ext-diff` executes, and fleet already hands over the commit log. A Claude Code too old for `--tools`/`--strict-mcp-config` (detected once per process from `claude --help`, failures not cached) runs with the allow/deny lists only and logs one warning.

### Prompt rules (versioned constant `PromptVersion`, now `sr-v2.0`)
1. Verdict first, one sentence, ≤18 words: starts with "Ready", "Ready once …" or "Not ready: …", then what it hinges on in plain words. No code identifiers, no praise, no hedging.
2. `summary`: what the change does, ≤3 short sentences and 60 words, plain words a product manager understands.
3. A stop exists only if a human must decide/verify something neither the code nor the evidence settles: product/customer-visible behaviour, facts outside the repo (prod data, external API shape), shared-infra blast radius, migrations/contracts, deploy order, security/credential loosening, an owned trade-off. A bug you are sure of → kind `bug`, Decide phrased so yes keeps it ("Keep this behaviour?"). At most 7, ordered by consequence.
4. Scope: if the change doesn't do everything asked, say what is missing in `intent_gap` **and** add a stop of kind `scope` first — title the gap, decide "Ship without <it> and track it as a follow-up?", why and `missing` list every item, no hunks.
5. Prose: **decide** is ONE yes/no question where yes = keep the code as is, ≤15 words, never two asks or an either/or; **title** is the consequence in ≤7 plain words (no snake_case/camelCase); **why** is ≤30 words in ≤2 sentences — what happens, then who is hurt; ≤3 checked items each ≤18 words; skip reason ≤10. Round ≥2 stops keep their why and checked.
6. `recommend` on every stop: "keep", or "change" + the concrete change (≤15 words).
7. **Look before recommending**: Grep/Glob for comparable code in this repo (sibling collectors, handlers, clients), Read the closest, and put it in `elsewhere` (≤25 words, ending with the path read). Nothing comparable → "".
8. `how`: ≤40 words, how the mechanism around the decision works.
9. Plain words everywhere a person reads: no internal jargon or shorthand — "a temporary limit (e.g. quota)", never "a passing limit"; identifiers only in checked items, how and chapters.
10. Every checked item cites a source: `test|ci|transcript|code|git|claim`. No source → don't write it.
11. `chapters`: 3–7 (fewer only for a one- or two-file change), heart → consequence → glue → docs; title ≤7 words; text ≤70 words on HOW it works (mechanism, not risk); every changed file in exactly one chapter, skipped ones too; the decisions whose code is in it. The scope stop is in none.
12. Every changed file appears in a stop's hunks or in skipped (`scope: rest` for the remainder of a file that has a stop).
13. Every skip names a `kind`: `mechanical` (no behaviour change) | `docs` | `fixture` | `config` | `generated`. "Covered by tests" is never a reason to skip a logic change; a file whose behaviour changed is a stop or stays out of skipped.
14. Pending verification → `pending`, never a stop or a checked item.
15. Don't restate the diff. Plain text, no markdown.
16. Round ≥2: never re-ask an answered stop; report each previous `change` as addressed / not addressed.

### Schema (v2; a stored v1 run still reads, the new fields absent)
```json
{"v":2,"verdict":"…","summary":"…","risk":"low|med|high","intent_gap":"",
 "stops":[{"id":"s1","kind":"scope|product|customer|logic|tuning|data|edge|security|migration|deploy|bug",
   "title":"…","decide":"…?","recommend":{"answer":"keep|change","text":"…"},
   "how":"…","elsewhere":"… — path/to/comparable.go","why":"…","missing":["…"],
   "checked":[{"text":"…","src":"test|ci|transcript|code|git|claim"}],
   "hunks":[{"path":"…","side":"new|old","start":1,"end":9}]}],
 "chapters":[{"id":"c1","title":"…","role":"heart|consequence|glue|docs","text":"…","files":["…"],"decisions":["s1"]}],
 "skipped":[{"path":"…","kind":"mechanical|docs|fixture|config|generated","reason":"…","scope":"file|rest"}],
 "pending":["…"]}
```
`intent_gap` stays for compatibility; what the screens show is the scope decision made from it.

### Fleet enforces (Go, not the model)
Prose and completeness rules are checked by `selfreview.Lint` on the reply before anything is cut — `summary` present (≤60 words, ≤3 sentences, no identifiers), `recommend` on every decision (keep|change, a change names its text, ≤15 words), `how` present (≤40), `elsewhere` ≤25 words and citing a path, an `intent_gap` with its scope stop first, chapters present (≤7, a known role, title ≤7, text ≤70, at least one file), verdict prefix and ≤18 words, decide one yes/no question ≤15 words (no second `?`, `, and is`, ` or will`, `instead of`, `and|or` + auxiliary), title ≤7 words, why ≤30 words in ≤2 sentences, no snake_case/camelCase/paths outside Checked — and a reply that fails gets **one** repair call (it may add a missing field from what the object already says) through the runner (no tools; the JSON plus the problem list; keep whichever version has fewer problems; never fails the review). Then: **an intent gap becomes the scope decision**, listed first (fleet makes one — `selfreview.ScopeStop`, no recommendation, its why the whole gap — when the model gave a gap but no scope stop; `selfreview.Upgrade` does the same for stored runs on load); **chapter coverage** — a file named in two chapters stays in the first, files in none go into an "Other changes" chapter fleet appends last, chapters sort heart → consequence → glue → docs, and each lists every decision whose hunks touch its files; a `recommend` that is not keep|change is dropped. Summary, how, elsewhere, recommend, the intent gap and chapter text are **never cut** — Lint holds their budgets and the UI wraps them. Then: truncate fields past generous hard caps (verdict 24, title 10, why 45, decide 22 words) with `…` (the verdict at its last clause boundary — `;` `,` `:` `—` or before "and"/"but" — when that keeps ≥8 words, never mid-clause otherwise than at the budget); a checked item that is really pending ("still running", "not run", "not yet", "pending", "in progress", "waiting for", or a near-copy of a `pending` line) moves to pending; stops are never dropped past 7 (a warning instead — never hide a decision); a checked item claiming CI passed while the live rollup is pending or failed is dropped; all line counts computed by fleet; files in no stop and no skip → **Not covered**; classifier `by rule` skips merged in; high-risk paths (migrations, auth, CI workflows, dependency manifests, `REVIEW.md always-human:` globs) can never be `by guide` skipped — moved to Not covered; a `by guide` skip's kind must fit the path (`docs`: .md/.mdx/.rst/.txt or under `docs/`; `fixture`: testdata/fixtures/test_data/`__snapshots__`, or .json/.yaml/.yml/.csv under a tests dir; `config`: .yaml/.yml/.toml/.ini/.cfg/.json/.conf/.env.example, Dockerfile, Makefile; `generated`: the classifier's conventions), a source file with ≥30 changed lines (scope `rest`: lines outside the stops) may only be skipped as `mechanical`, and `mechanical` must stay under 80 lines — a file with **≤5 changed lines is never refused over its kind** (`SmallSkipMaxLines` — the real EDR run sent a two-line gate-list edit to Not reviewed because the reviewer called it config); a refused skip goes to Not covered with fleet's reason on the card (`not_covered_why`, e.g. "skipped as 'covered by tests' — 152 changed lines of logic"); `ci` claims cross-checked against `CIStatus` and rendered dim as `claim` when unverifiable; hunks bound to diff rows (fallback: file header).

### Cache key
`sha256(PromptVersion, model, treeSHA, baseSHA, hash(intent), hash(REVIEW.md), round, prevRunID)`. A stale run renders immediately while a new one generates.

## Data

`state.db`:
- `sessions.base_ref TEXT` (migration; set in `GitWorktreeProvider.Create` callers + `fleet worktree`).
- `review_runs(id, checkout, session_id, worktree, round, base_ref, base_sha, tree_sha, head_sha, cache_key, status preparing|ready|failed|sent|approved, model, account, json, error, elapsed_ms, created_at, seen_at)` — **keyed by `checkout`** (the checkout root, `session.GetRepoRoot` — the sidebar's checkout key; indexed). `session_id` is only the run's **send target** ('' = none chosen yet): set by `t` and by a send. `DeleteSession` keeps the runs and clears them as targets; the review row's discard deletes the checkout's runs and answers (`DeleteReviewRunsForCheckout`). Rows from before the column are backfilled once at open by resolving their `worktree` (the session's ProjectPath at the time) through `GetRepoRoot`.
- `review_answers(run_id, stop_id, answer fine|change|ask|later, note, updated_at, sent_at)`

Worktree: `.fleet/review/round-N.md` written on send (excluded via `git.AddFleetExclude`).

## Packages

| Package | Contents | Origin |
|---|---|---|
| `internal/git/diff.go` | `MergeBase`, `SnapshotTree`, `DiffFiles` (→ `[]git.FileDiff{Path,OldPath,Status,Additions,Deletions,Binary,Patch}`), numstat; timeouts + `--` hardening like `FetchBaseRef` | new |
| `internal/review` | doc, stream, syntax (chroma), worddiff, tree, salience classifier; `review.File` replaces `github.PRFile` | port from #299 |
| `internal/selfreview` | input assembly, prompt, schema types, parse (fence-tolerant), Enforce, cache key, answer→message + round-N.md composer, `Runner` interface + Claude runner | new (+ ask.go plumbing from #299) |
| `internal/deliver` | `Deliver(ctx, …) error` with typed refusal reasons, lifted from `cmd/fleet/send.go` | lift |
| `internal/ui/design_review.go`, `review_code.go` | palette-derived diff colours; code-line renderers as free functions; `buildRows` | port from #299 |
| `internal/ui/review_view.go` | the Review view (stop card + file list as pure render funcs with golden tests) | new |
| `internal/ui/review_view_walk.go` | the Walkthrough tab: scope row + chapter list, chapter card (mechanism, decisions, files with their fate), jumps to Decide and to a file's diff | new |
| `cmd/fleet/review.go` | `fleet review <session \| path \| branch> [--json] [--wait] [--walkthrough]` — prepare/print a checkout's run (agents + testing): verdict, `What it does:`, each decision with `Recommend:`, `How it works:`, `Elsewhere:` (the scope decision first, with `Missing:`); `--walkthrough` adds every chapter with each file's fate; `--json` carries everything. A path (`.`, `./wt`, `/abs`, `~/x`) names its root; a branch is matched against known checkouts (sessions' + pinned) first, so two sessions in one worktree never make it ambiguous; then every `fleet send` session tier → that session's checkout; then a directory. `--json` carries `checkout` and `target`. | new |

## Phases

- **v1 (this branch)**: everything above.
- **v2**: auto-prepare on running→finished (opt-in `review_auto`, 30s debounce, ≥20 lines after classifier, diff-hash dedupe, ≤2 concurrent); interdiff re-review; transcript tool-evidence parser (Bash commands + exit codes); `A` → open PR / mark draft ready with the decision ledger in the PR body; `fleet review request` for agents; open-in-Plannotator export (`plannotator guide import`).
- **v3**: others' PRs (#299's `FetchPRFiles` + `review_submit`), line comments in `d`, cross-family reviewer (Codex), REVIEW.md suggestions mined from repeated `change` answers, opt-in gate/auto-loop.

## Risks we accept knowingly

The skip list is the product and the hazard (mitigated: by-rule/by-guide labels, Not covered, `x` promote, full diff one key away, high-risk never skippable). "Checked" can be invented (source tags, fleet verifies what it can, rest renders as `claim`). Same-family reviewer blind spots until v3. Non-Claude agents get thinner intent/evidence.
