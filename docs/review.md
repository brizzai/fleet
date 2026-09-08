# Code review in fleet — build plan

Replacing the GitHub review tab. Decisions are settled; this is the order.

**Shape:** a review is an ordinary fleet session, grouped under a `reviews` folder
inside its repo. No new row kind, no new mode, no full-screen surface.

**Decided**

| | |
|---|---|
| Terminal width | 318 cols measured — preview pane is ~250. Full-screen room not needed. |
| Where reviews live | `reviews` folder in the repo group, `c` to open the queue |
| Worktree per review | One each, no dependency install (128 MB), deleted when done |
| Multi-account bugs first | No — build review first |
| Claude posting | Small stuff only; blockers wait for you |
| Claude's identity | Your account with a marker, for now |
| Diff rendering in fleet | Done — two panels, tinted rows, syntax, word diff |
| Unstarted reviews in `Space` | No. Only reviews you've opened |

---

## 1 · The queue — DONE

- [x] `internal/github/reviews.go` — `ReviewsRequested` over GraphQL search
- [x] Bot detection on `author.__typename == "Bot"` (REST search's `is_bot` reports false for dependabot[bot])
- [x] `internal/ui/palette_reviews.go` — rows grouped by author, bots folded into one counted row
- [x] `PaletteKindReview` + `PaletteTabReviews` + `SetReviews`/`ReviewsLoaded`
- [x] `c` opens the palette straight onto the reviews tab
- [x] GraphQL search, not `gh search prs` — REST search allows 30 req/**minute**; GraphQL is 5000/hour and serves more fields
- [x] 60s cache so repeated `c` presses cost nothing
- [x] Fetch errors surface instead of rendering as "reviews 0"
- [x] `⏎` starts the review, `ctrl+p` opens Chrome (plain `p` is filter text in the palette)
- [x] Keybinding registered in `keybindings.go` and CLAUDE.md
- [ ] Tests: bot folding, author grouping, age rendering, empty/error states
- [ ] Prefetch on the worker so the first `c` is instant (cold fetch is ~4.7s)
- [ ] Changelog fragment

## 2 · Launch the review session — DONE

- [x] `internal/git/review.go` — fetch `pull/<n>/head` onto `fleet-review-<n>`, worktree at `<repo>-reviews/<n>/`
- [x] Existence check runs **before** the fetch: git refuses to fetch into a branch checked out in a worktree, so re-opening a started review failed
- [x] Idempotent — second open is 65ms vs 3.3s cold
- [x] No dependency install (153MB per review worktree, measured)
- [x] `review_prompt` config, `{pr}` substituted, empty by default
- [x] Repo resolved from the PR's `owner/name` via fleet's existing origin keys
- [ ] Tests: path derivation, reuse, IsReviewWorktree
- [ ] Delete the worktree when the review is done, with the usual undo window

## 3 · Reviews folder in the sidebar — DONE

- [x] Sessions under `<repo>-reviews/` fold into one `reviews` node instead of a group each
- [x] The node's origin resolves from a real worktree inside it — the folder has no remote of its own
- [x] Sorts last in its origin, so your own branches stay one contiguous run
- [x] Hidden entirely when you have no reviews
- [x] Collapsing it hides every review in one keystroke; `z` mutes them all (both free — it is an ordinary group key)
- [x] `⏎` on a PR you already started jumps to that session instead of refetching
- [x] `d` on the node is refused with a reason — it is a folder of worktrees, not a checkout
- [x] Tests: grouping, origin inheritance, no-empty-row
- [x] Visually distinct: `⌘` in accent carries the identity, label stays dim — no new hue, since every hue in the sidebar already means a status (U+2318 — Neutral width, verified present in Menlo's cmap)
- [x] Carries a status pill like every other checkout, so a collapsed node still says whether a review wants you
- [x] `C` jumps to this origin's reviews folder
- [ ] Expand the bot fold on `⏎` — never a batch-approve verb
- [x] `d` on a review session removes its 153MB worktree and its `fleet-review-*` branch

## 4 · The diff surface — file list DONE, patches next

- [x] `internal/github/files.go` — files + viewed state in one GraphQL call
- [x] `internal/github/salience.go` — deterministic demotion: lockfiles by exact basename, vendored dirs, generated suffixes, snapshots, tests
- [x] Preview pane shows the file list for a review row; `v` flips to the agent pane
- [x] Fold names what it hid and why ("23 tests"), never just a count; `s` unhides
- [x] `viewerViewedState` read and shown as `n/N viewed` — **never written** (no mutation restores DISMISSED)
- [x] Biggest file first — the only ordering signal available without a model
- [x] Fetched on the tick with a 3-minute TTL, off the Update goroutine
- [x] Tests: 14 classification cases incl. `package.json` vs `package-lock.json`, order preservation
- [x] Patch text per file — switched to REST (`pulls/N/files`); GraphQL serves no patch
- [x] `--paginate` returns one array PER PAGE, so a plain Unmarshal silently drops every page but the first
- [x] Measured: PR 4959 goes 34 files → 11 shown, 23 tests folded; ~70KB of patch

## 5 · The reader — DONE

Decisions: **A1** full screen · **B1** stream · **C1** ⏎ reads · **D1** inline comment
· **E4** no agent findings in the reader · **F3** fleet's own read marks · **G1** fold.

- [x] `internal/review/stream.go` — patches flattened into one scrollable line stream
- [x] No selection anywhere: a scroll position plus jump targets, hunk's model
- [x] `↑↓` row · `⇧↑↓` hunk · `⇧←→` file · `pgdn/pgup` page · `⌃d/⌃u` half · `g/G` ends (`jk ][ .,` are aliases)
- [x] Jumps land the target 2 rows from the top, not wherever scrolling left it
- [x] Jumps hold at the ends rather than wrapping into a file you already read
- [x] Two panels: file tree left, diff right — the tree answers "where am I", the diff "what changed"
- [x] Tree collapses single-child directories (`pkg/shared/`, not `pkg/` then `shared/`) like GitHub's
- [x] Tree follows the diff, never leads; both ordered by path so they agree
- [x] Tree drops entirely below 90 columns rather than truncating to nothing
- [x] `⌃q` quits, matching attach — `esc` also works
- [x] `m` marks read (was `x`, which said nothing) and the footer names every key
- [x] `c` comments on the line under the cursor — refused on headers, which GitHub cannot anchor
- [x] Comments queue locally; posting is its own step with its own confirm
- [x] `x` marks a file read — fleet's own record, GitHub's checkbox untouched
- [x] Caret on every row kind, including headers, where `.`/`,` leave you
- [x] Tabs expanded to 4 so the gutter stays a column
- [x] Full-screen surface: `modalOpen`, `routeToModal` (raw keys — the comment box takes text), `renderBody`
- [x] Tests: line numbering incl. blank context rows, no-patch files, jump bounds, hunk header parsing
- [x] Persist read marks and pending comments to SQLite
- [x] Submit the batch to GitHub — `S`, verdict cycler, one POST

## 5b · The reader, second pass — DONE

Measured against [tuicr](https://tuicr.dev/) and [lumen](https://github.com/jnsahaj/lumen).
Decisions: **A3** both layouts · **B3** tint + word diff · **C2** chroma ·
**D3** inline typed comments · **E2** bordered panels · **F2** expand from the
worktree · **G2** search · **H1** no commits pane yet.

- [x] **Two colour channels.** Background says added/deleted, foreground says what the
      code is. Painting the foreground green to mean *added* spent the only channel that
      could carry syntax, and made word diff impossible. See `docs/design-system.md` §5.
- [x] `internal/ui/design_review.go` — every diff tint and syntax colour **derived** from
      the palette, so all six themes work and a seventh gets one free
- [x] `internal/review/syntax.go` — chroma, per file and per side, never per line
      (a lexer is a state machine; line-by-line reopens every multi-line string)
- [x] chroma and not tree-sitter: tree-sitter needs cgo, and fleet ships pure Go on
      purpose — `modernc.org/sqlite` is in `go.mod` for the same reason
- [x] `internal/review/worddiff.go` — word-level diff on paired −/+ lines, LCS over
      identifier/whitespace/punctuation tokens, refused below 34% similarity
- [x] `internal/review/doc.go` — one owner for patches, expansions and comments, because
      row indexes are only meaningful against a single consistent flattening
- [x] **Expansion reads the review worktree**, not the API: the PR is already checked out
      at `<repo>-reviews/<pr>/`, so widening a hunk is a file read — instant and offline.
      `⏎` steps 20 lines each way, `+` opens the gap
- [x] Side-by-side above 160 columns, unified below, `|` overrides and pins the choice
- [x] Deletions paired with the additions that replaced them, padded so the sides stay level
- [x] Comments render **in the stream at their anchor**, typed (issue/nit/question/
      suggestion, `⇥` cycles), composed in place, `e` edits and `d` deletes
- [x] Comments survive closing the reader; the reader's copy is authoritative on close,
      so a deleted one stays deleted
- [x] Refused on expanded context, with the reason — GitHub anchors inside the diff it was sent
- [x] `/` search with `n`/`N`, wrapping (a search is a set you asked for; a hunk jump is not)
- [x] Mode pill + live key hints + toast in the footer; hints change with the row and
      whole hints drop before `⌃q quit` does
- [x] `s` really does toggle the folded files now — the tree footer advertised that key
      and nothing handled it
- [x] `space` marks the file read and jumps to the next **unread** one — the verb you press
      most. Idempotent, never a toggle; `m` is the toggle-in-place correction
- [x] `?` opens the full key sheet, two columns, any key closes
- [x] `⇥` moves the keyboard between the tree and the diff — the sidebar-and-preview
      relationship, not a second cursor: the tree's selection scrolls the diff as it moves,
      so the two panels can never disagree about which file you are on. The accent border
      follows the keyboard (design-system §4), `⏎` hands it back, `esc` steps back one
      level before it leaves, `⌃q` always quits
- [x] The tree uses the **session list's two selection states, verbatim**: `SelectionPill`
      accent-filled while it holds the keyboard, muted band when it does not — so the tree
      keeps showing which file you are in without competing with the diff, exactly as the
      sidebar does while the preview is focused
- [x] Directories fold: `⏎` or `←`/`→` on a folder, `←` on a file climbs to its folder.
      Uses `chevronGlyph`, so a folded directory looks like a folded sidebar group and
      honours the user's triangle/plus-minus setting
- [x] The fold state restores the selection by **identity**, not index — folding shifts
      every row below it, so an index would leave you standing on something else
- [x] Taking focus unfolds whatever is hiding the file you were reading, rather than
      dropping the selection somewhere else
- [x] **The tree and the diff never disagree.** Scrolling the diff by any means drags the
      tree's selection along; selecting a directory carries the diff to the first file
      under it. The left panel is a picture of where you are, not a stale one you have to
      press `⇥` to refresh
- [x] The diff's cursor row is highlighted across the **whole line**, not marked in the
      gutter — a highlight that stops after the line numbers marks a column. The tint is
      derived by lifting whatever tint the row already carries toward `ColorBorder`, so an
      added line under the cursor is still visibly added (hue kept, luminance raised)
- [x] **Bug found:** the tree budgeted filenames against 4 fixed columns when a row has 5,
      so the change count truncated — `+11` rendered as `+1`. A wrong number is worse than
      a shortened name. Indent is one space per level (BuildTree already collapses
      single-child dirs, so two spent a quarter of a narrow panel on whitespace)
- [x] **Second instance of the same bug:** `space` was bound as `case " "`, which
      `String()` never produces, so it had done nothing since it was written
- [x] Filename inset in the panel border instead of a row that scrolls away
- [x] **Bug found:** `KeyPressMsg.String()` returns `"space"` for the space bar, so the
      old `len(s)==1` test dropped every space typed into a comment. Reads `msg.Text` now,
      which also fixes non-Latin comments
- [x] Tests: exact width/height at five terminal sizes, the two colour channels, word-diff
      runs, tint derivation in all six themes, expansion from a real worktree, comment
      anchoring and wrapping, spaces and chords in the composer
- [ ] `e` to hand a comment to `$EDITOR` (today it edits inline)
- [ ] A commits pane, scoping the diff to a revision range (**H2**, deferred)

## 5 · The tour — DONE

Decisions: **steps are one idea with several anchors** · **generated on reader
open, async** · **second mode of the file tree** · **the guide chooses the road,
you do the reviewing**.

- [x] `internal/review/tour.go` — the route, its parsing, and what makes it safe to render
- [x] `internal/claudeaccount/ask.go` — one non-interactive `claude -p` call on a chosen account
- [x] `t` swaps the left panel between the file list and the route; `↑↓` walks it and the diff follows
- [x] Cached in `review_tours`, keyed on the head SHA; a push is what makes fleet ask again
- [x] Tests: fenced/prefixed JSON, unsafe diagrams, anchor binding, exact size at four terminal sizes

**The guide chooses the road; you do the reviewing.** The instruction forbids
praise, concerns, suggestions and verdicts outright, and a test pins that it
does. A tour that volunteered an opinion before you had read anything would be
worse than no tour, because you would trust it — the same discipline that keeps
fleet's read marks its own rather than GitHub's, and stops the reader guessing
where a comment moved after a force-push.

**A step is one IDEA, not one place.** A design lives across files — the model,
what flattens it, what hangs off it — so a step carries several anchors and only
the selected step shows them. One anchor per step would have turned a tour about
the design into a tour about locations, and twenty rows in a quarter-width panel
is a file tree again.

**Every anchor is checked against the stream the reader is actually showing**
(`Tour.Bind`). The model answers about a diff, not about the reader's flattening.
A line that is not in any hunk snaps to its file's first hunk *and loses its line
number*, because being sent to the right file is the substance and claiming a
line would be a lie. A file that is not in the change at all is dropped — there
is nowhere honest to send you. A step whose anchors all fail keeps its place: it
still has a title, a brief and possibly a diagram.

**A step with no anchors takes the whole diff panel.** An opening step that
explains the shape of a change before any one file makes sense is worth more
than a file it could have pointed at, and a diagram needs the wide side of the
screen rather than the quarter.

**Diagrams are dropped, never truncated.** A drawing cut at the right edge is not
a smaller drawing, it is a wrong one — the arrow saying where the data goes is
the part that gets cut. So `safeDiagram` refuses anything over 72 columns or 24
lines, or using a glyph outside the box-drawing set fleet already vets for
width-1 and Menlo coverage. Fixed at 72 because a tour is cached against a
commit and then has to survive every terminal size after it.

**Instruction in argv, changeset on stdin.** argv is world-readable through
`ps`, so fleet's own words may go there and other people's code may not — and a
300KB argument is not something to hand a shell anyway. The input is the
salience-filtered file list, budgeted, and the model is told when files were
dropped so the tour never claims to have seen them.

**The reviewing session's account pays**, when there is one: the tour is part of
the same piece of work, and running it elsewhere would split one review across
two quotas. Otherwise the ordinary strategy decides, honouring the origin's
allowlist — a call fleet makes for itself is still a call on someone's
subscription. No `--model` flag: the account's own default.

### The preview is the pull request page

The review row's preview was the changed-file list behind a "Loading files for
#N…" — a **second** fetch (`/pulls/N/files`, seconds long) in front of a
question the queue could already answer. It now renders what a pull request
page opens on: title, `base ← head`, state, size, review decision, checks,
labels, and the author's description.

- **All of it rides the queue's existing GraphQL call.** `baseRefName`,
  `headRefName`, `labels`, `reviewRequests`, `comments.totalCount` and the head
  commit's `statusCheckRollup` were added as fields, not as round trips — so
  the preview paints the instant the row exists.
- **The queue is cached in SQLite** (one row: it is a snapshot of a search, and
  half of an old queue is not a queue), restored at launch and corrected by a
  refresh fired from `Init`. The cost is a pull request that merged while fleet
  was closed rendering as open for a few seconds; the alternative is an empty
  panel on every launch.
- **An empty queue is never cached.** A failed or unauthenticated fetch produces
  one too, and overwriting a good cache with it trades a slightly stale preview
  for no preview at all.
- **Absent checks are not passing checks.** An empty rollup says so rather than
  rendering nothing, which would read as green.
- **Template comments are stripped.** A repo with a pull request template hands
  every description a `<!-- -->` block of instructions to the author, which
  GitHub hides — without stripping it the first screen of most descriptions is a
  form nobody filled in.
- **Markdown is hand-rolled, and the tables are why.** A general renderer
  (glamour) wraps the whole document to one width and lays tables out without
  wrapping their cells — the opposite of what a preview pane needs, where a
  paragraph wants a reading measure and a four-column table wants the pane.
- **Prose caps at 90 columns; tables and fenced code do not.** Those are laid
  out rather than read line by line, and squeezing a table to match a paragraph
  wraps every cell for nothing.
- **Table cells wrap, never truncate.** In a before/after table the tail of the
  cell is usually the part that mattered. Columns take their natural width where
  the pane allows and are shrunk widest-first where it does not, never below a
  word.
- **Style first, then wrap — ANSI-aware.** The obvious order is wrong: emphasis
  around a sentence is longer than the measure, so wrapping first puts the
  opening marker on one line and the closing marker on the next and both render
  raw. `ansi.Wordwrap` counts columns, so styled text can be wrapped safely.
- **Nested inline markup recurses.** `_a **bold** word_` and ``**a `code`
  span**`` used to emit their inner text verbatim, leaving the inner markers on
  screen.
- **`_` is a word character in code.** `skip_migrations` and `is_admin` are
  everywhere in a description, so emphasis only opens at a word boundary
  followed by non-space. Found by reading real output, not by a test.

The file list moved into the reader, where the tree and the tour already answer
"where do I start". `s` on a review row now says what it changed, since it no
longer changes anything on that screen — it decides which list the reader opens
on.

### Three tabs

`1` tour · `2` diff · `3` session. `t` survives as the 1↔2 toggle it was.

- **A tab, not a mode**, by the design system's own test: switching one does not
  move the keyboard — only `⏎` on the session tab hands it over. So the bar
  renders as a selection.
- **The diff is the default view.** The tour takes tens of seconds to arrive, and
  opening onto "reading the pull request…" puts a wait in front of the thing you
  came for.
- **`1` refuses while there is no tour, and says why.** A digit that silently
  does nothing is indistinguishable from an unbound key.
- **The session tab is the drawer's machinery**, not a capture: tmux
  control-mode `%output` into the vterm emulator, seeded with `capture-pane`
  before attach (control mode replays nothing, so an idle pane would stay blank
  forever), resized in lockstep with the panel, wakes coalesced through a CAS
  latch. Torn down whenever the tab is not the visible view — a terminal nobody
  is looking at is a `tmux -C attach` and a PTY held open for nothing.
- **The reader keeps its own stream state rather than sharing the drawer's.** The
  two can be pointed at different panes at the same time, so one set of fields
  would have them fighting over the emulator. The *sequence* is the drawer's,
  because that is the one proven against real panes — the duplication is worth
  revisiting once this settles.
- **It takes the whole width.** A pane is not a list of anything, and one
  squeezed into three quarters wraps differently from what the agent drew.
- **No agent is the common case** — most reviews are read without ever starting
  one — so the tab is usually an offer, and `⏎` routes through the same
  `startReview` the `c` queue uses.

Open: `n`/`p` to walk the route from the diff panel without focusing the tour
(`n` is taken by search).

## 6 · Comment and post

- [ ] Comment on a line, `e` hands off to `$EDITOR` (edits inline today)
- [x] Submit the batch, choose the verdict

`S` raises a sheet: the verdict on `⇥` (comment / approve / request changes),
the summary typed underneath, `⏎` to send. One
`POST /repos/{o}/{r}/pulls/{n}/reviews` carrying every queued comment, because a
review is atomic on GitHub — N separate comment POSTs arrive as N notifications
and leave half a review behind when the third one fails.

Three decisions worth keeping:

- **COMMENT leads the cycle, not APPROVE.** Approve is the one verdict a stray
  keypress must not reach, and COMMENT is the one GitHub refuses without a
  summary — so `S` `⏎` on an untouched sheet does nothing at all.
- **`commit_id` is sent.** Left off, GitHub anchors to the current head and a
  push during the review re-points every line number at code nobody read. Sent,
  an overtaken review renders as outdated, which is true. It rides the queue's
  GraphQL query as `headRefOid`, so it costs no round trip.
- **The reader asks, the app posts.** The reader owns the comments and the sheet
  and nothing else — it does not know the head SHA, does not hold the queue, and
  must never shell out to `gh` from the Update goroutine. Same shape as
  `reviewCommentMsg`.

### Persistence

`review_read(repo, pr, path, head_sha, read_at)` and
`review_comments(id, repo, pr, path, line, kind, body, head_sha, created_at)`.

- **The key is (repo, pr), never the number alone.** PR #12 exists in most
  repositories, so the in-memory maps had to be re-keyed on `reviewKey` too —
  they were colliding already, handing one repo's diff and one repo's queued
  comments to another. Survivable while they died with the process; a
  wrong-target post once they are on disk.
- **A kind persists as its NAME.** `CommentKinds` is an iota, so storing the
  ordinal would silently reinterpret every saved row the day a fifth kind is
  added in the middle. `ParseCommentKind` falls back to Issue.
- **`head_sha` is the submit anchor**, and the anchor for step 7's "what moved".
  A read mark or a draft with no commit behind it cannot answer what it refers
  to.
- **Comments save on every change; read marks save on reader close.** The reader
  owns the read map while it is open and mutates it directly, so close is the
  moment the app learns anything moved. A crash mid-review costs that sitting's
  marks — cheaper than a write on every press of `space`.
- **A submit clears the table**, or the review would come back as a draft on the
  next launch and offer to post itself twice.

### Anchoring

**The comments decide which commit a review is anchored to, not the head.** Their
line numbers were read off one particular diff, and that is the only commit they
are true against. A draft written before a push and submitted after it lands as
an *outdated* comment on the code it was actually about, instead of silently on
whatever occupies that line now.

- **The SHA comes from the files fetch, never from the review queue.** The queue
  is a cache of a different age, and a pull request drops out of it once you
  have reviewed it — so it goes empty exactly when someone returns for a second
  round.
- **`FetchPRFiles` reads the head SHA FIRST, then the patches.** No endpoint
  serves both (the files route carries no SHA; GraphQL, which does, serves no
  patch text), so there is a ~1s window in which the author can push. Reading
  the SHA first leaves an *old* sha with *new* line numbers, which GitHub renders
  as outdated — visibly wrong. The other order leaves a new sha with old line
  numbers, which lands silently. Both are wrong; only one says so. That window
  is dwarfed by the minutes the reader then holds those patches, which is the
  staleness the SHA exists to record.
- **A comment carries its own anchor** (`review.Comment.HeadSHA`), set by the app
  and never by the reader, so a draft survives a restart and several pushes with
  the commit it belongs to. `reviewAnchor` uses it when the batch agrees, and
  falls back to the loaded diff's SHA when it does not — GitHub takes one commit
  per review, so a mixed batch has no right answer.
- **An empty anchor is survivable**: GitHub falls back to the current head, which
  is what it did before any of this was recorded.

Not built: telling the user on the submit sheet that they are anchoring to
something behind the current head. That is step 7's job — "since you last
looked" is the feature that notices a push.

## 7 · Since you last looked

- [ ] Record the reviewed SHA; show only what moved when the author pushes
- [ ] The refresh that `PrepareReviewWorktree` deliberately does not do on revisit

## Cheap wins, any order

- [ ] Widen the `reviewThreads` GraphQL query — bodies/authors/positions already cross the wire in `getUnresolvedThreadCount` and get reduced to an int
- [ ] `P` opens Chrome at the exact file and line; `y` yanks a permalink
- [ ] `fleet review <url>` + a "Review in fleet" button in the Chrome extension

## Not doing

Separate review mode · shared review worktree · tree-sitter (needs cgo) ·
guessing where comments moved after a force-push · inline images ·
request-changes gates · snooze-until-CI · a tour file format with its own schema ·
tuicr's agent handoff loop (it is for self-review; you review other people's PRs)
