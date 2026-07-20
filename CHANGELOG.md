# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.22.0] - 2026-07-20

### Highlights

- **Snooze the noise** — `z` mutes a session, worktree, or repo for `30m`/`1h`/`4h`/tomorrow or a duration you type (`15m`, `2d`), so work you're not touching today stops claiming a slot in the `Space` rotation and stops counting toward the header pills. The session keeps running; snoozing a repo folds the whole group, and `z` again wakes it early.

### Added

- **Native Linux support.** fleet now runs on Linux — install via `.deb`/`.rpm` or Docker. Worktree cleanup, copy-to-clipboard (Wayland and X11, with an OSC 52 fallback), idle-session suspend, and tmux 3.2+ compatibility are all handled natively, and the worktree-delete dialog shows tidy process names (`vite`, not a full path). An optional systemd user unit keeps the tmux server alive across logins.
- **Snooze the noise** — `z` mutes a session, worktree, or repo for `30m`/`1h`/`4h`/tomorrow or a duration you type (`15m`, `2d`), so work you're not touching today stops claiming a slot in the `Space` rotation and stops counting toward the header pills. The session keeps running; snoozing a repo folds the whole group, and `z` again wakes it early.

### Improved

- **Hop between groups** — `Shift+↑`/`Shift+↓` jumps the sidebar cursor to the previous/next group header instead of one row at a time, so a fleet with a dozen origins no longer means holding the arrow key. At the top and bottom of the list it lands on the first/last row.

## [2.21.0] - 2026-07-13

### Added

- **JetBrains IDEs now open with `e`.** Set your editor to `goland`, `pycharm`, `idea`, or any other JetBrains IDE and fleet opens the project in it — no need to install the Toolbox command-line launcher first. The Settings editor list now shows only the editors you actually have installed.

### Fixed

- **Codex `/rename` now sticks.** Renaming a thread with `/rename` inside Codex retitles the session in fleet's sidebar within ~30s, the way Claude's `/rename` already did — no more Codex rows stuck on `do this:`.
- **Status updates stopped dropping.** Two agent hooks firing at once could lose one update with a cryptic `rename … no such file or directory` (Codex asks permission per concurrent tool call); both now land.

## [2.20.0] - 2026-07-12

### Changed

- **Telemetry backend.** Usage analytics moved to PostHog. Your telemetry mode (`full` / `minimal` / `off`) and the `FLEET_TELEMETRY_DISABLED` / `DO_NOT_TRACK` opt-outs behave exactly as before — nothing changes in what you control.

### Fixed

- **Hooks survive running from source.** Launching fleet with `go run` baked a throwaway build path into `~/.claude/settings.json`, and Go deletes that binary on exit — so every hook afterward failed with "No such file or directory" and session status silently stopped updating. `make run` now execs a real build, and fleet won't write a `go run` path into your hooks even if you launch one by hand.

## [2.19.0] - 2026-07-12

### Highlights

- **Press `.` for the menu.** A context menu opens on the row under your cursor with exactly the actions that apply to it — sessions, worktrees, repos, and origins each get their own list. Actions that can't run right now stay visible and dimmed with the reason, and every entry shows its keybinding.

### Added

- **Press `.` for the menu.** A context menu opens on the row under your cursor with exactly the actions that apply to it — sessions, worktrees, repos, and origins each get their own list. Actions that can't run right now stay visible and dimmed with the reason, and every entry shows its keybinding.

## [2.18.0] - 2026-07-09

### Changed

- **Analytics moved in-house** — usage telemetry now goes to fleet's own endpoint instead of Mixpanel. The opt-in prompt, `minimal`/`off` modes, and the `FLEET_TELEMETRY_DISABLED`/`DO_NOT_TRACK` kill-switches all work exactly as before.
- **Anonymous telemetry captures more shape** — with telemetry on, session events now record which agent ran (Claude/Codex/OpenCode), and fleet reports auto-update health and coarse tmux/git/gh failure counts (no paths, args, or repo names). Minimal and off modes send nothing new.

## [2.17.0] - 2026-07-09

### Improved

- **Re-viewable now.** Reopening What's New shows the recent highlights again instead of "you're all caught up". The badge spells the key `Shift+W` (so it's not read as lowercase `w`), and `Tab` flips between the highlights reel and the full release notes.

## [2.16.1] - 2026-07-09

### Highlights

- **What's New, now actually visible.** If you updated *into* the release that introduced this reel, its own debut got marked read before you ever saw it — so `W` just said "nothing new". Existing installs now light up the `✦ What's New` badge for the release you updated to, instead of only fresh installs. (Fittingly, this highlight is that fix — saying hello.)

### Fixed

- **What's New, now actually visible.** If you updated *into* the release that introduced this reel, its own debut got marked read before you ever saw it — so `W` just said "nothing new". Existing installs now light up the `✦ What's New` badge for the release you updated to, instead of only fresh installs. (Fittingly, this highlight is that fix — saying hello.)

## [2.16.0] - 2026-07-08

### Highlights

- **Yes, this is What's New telling you about What's New.** The shimmering `✦ What's New` badge (top-right) lights up when a release has highlights worth your time — press `W` to read them, capped to the last 7 days. Open it once and it quietly retires until the next release. Fittingly, its very first highlight is itself.

### Added

- **Mark as unread** — Press `m` (or `Ctrl+K` → "Mark as Unread") to push an idle session back to Finished, re-flagging it for attention. The inverse of "Mark All as Read".
- A new "Release Notes" command in the Ctrl+K palette opens a scrollable changelog of every release — marking the version you're running and grouping newer releases you can update to.
- **Yes, this is What's New telling you about What's New.** The shimmering `✦ What's New` badge (top-right) lights up when a release has highlights worth your time — press `W` to read them, capped to the last 7 days. Open it once and it quietly retires until the next release. Fittingly, its very first highlight is itself.

### Improved

- Discovery tips now stop appearing once you've clearly learned the feature — e.g. the command-palette hint retires after you've opened `Ctrl+K` a handful of times.

### Fixed

- Sessions that delegate to background agents now show as Running while those agents work, instead of appearing idle.

## [2.15.0] - 2026-07-06

### Added

- Fleet can now auto-suspend idle sessions under memory pressure — freeing their memory (and the shared tmux server's buffers) so a big fleet can't OOM-crash all your sessions at once. Suspended sessions resume instantly on Enter with the conversation intact. Tune it in Settings › Behavior › "Idle-session suspend" (off/light/balanced/aggressive; default light).
- Projects under `~/Documents`, `~/Desktop`, or `~/Downloads` now show a clear notice when macOS blocks tmux from reading them, with a one-hop `Ctrl+K → "Open Full Disk Access Settings"` fix instead of a cryptic "Operation not permitted".

### Improved

- Terminal drawer tabs are now labeled with the latest command each shell ran (e.g. `npm run dev`), the selected tab gets a highlighted background, and when the drawer is closed its 1–3 shells show inline in the panel's bottom border.
- You can now press `w` on an origin header to create a worktree for that project — it bases the new worktree on the group's main clone, instead of failing with "no repo selected".

### Fixed

- Creating a new worktree from an existing worktree no longer snowballs the name (repo-a → repo-a-b → repo-a-b-c); new worktrees are now always named as siblings of the main repo.

## [2.14.0] - 2026-07-05

### Improved

- Quitting now shows a "Shutting down…" indicator and exits faster, instead of appearing frozen for a couple of seconds.

### Changed

- Telemetry is now three-way: Full (usage + git name/email), Minimal (an anonymous daily-active ping only, no identity), or Off. On first launch, accepting sends Full and declining sends Minimal; switch to Off any time in Settings.

### Fixed

- Sessions now show as running (not idle) while Claude compacts the conversation, via `/compact` or auto-compaction.
- Settings › Behavior no longer wraps long labels and cuts off the last rows (Telemetry, Default agent); each setting fits on one line.

## [2.13.0] - 2026-07-02

### Added

- New terminal drawer (press `` ` ``) for live, repo-scoped shells — dev servers, log tails, scratch commands — with faithful colors and full-screen tools (vim, htop, lazygit). Type straight in; `Ctrl+T` new, `Ctrl+W` close, `PgUp/PgDn` switch tabs, `Ctrl+G` full-screen attach.

### Improved

- Smoother, lower-flicker rendering — the TUI now redraws only the cells that changed, via Bubble Tea v2's cell-diff renderer with synchronized output.

### Fixed

- The Ctrl+K command palette now opens in terminals and tmux configs (e.g. gpakosz/.tmux) that enable extended/CSI-u key reporting, which previously encoded Ctrl+K in a form fleet couldn't read so the keystroke did nothing.
- Fewer periodic UI stutters — the per-repo status refresh now spawns 3 git subprocesses instead of 5, easing fork/exec-lock contention that occasionally stalled the preview by ~0.5s.

## [2.12.1] - 2026-06-22

### Improved

- Forking a session now logs the source and parent conversation ids, making it easier to diagnose a fork that opens the wrong conversation.

### Fixed

- The `?` help overlay no longer gets cut off on short terminals — it lays bindings out in columns sized to the window and scrolls (↑↓) when there's still more than fits.

## [2.12.0] - 2026-06-22

### Added

- Press `d` on an origin row to forget the whole repo group at once (every checkout and its sessions), gated by a safety checkbox you must tick first. A new Behavior setting controls whether its worktree directories are also removed.

### Fixed

- Emoji and other font-dependent glyphs in a session's preview no longer push the top navbar off-screen or scramble the layout.

## [2.11.1] - 2026-06-22

### Fixed

- Sessions no longer get stuck showing "running" while genuinely waiting for a permission prompt after a `/clear`-style session rotation.

## [2.11.0] - 2026-06-18

### Changed

- Quit is now Ctrl+C instead of q.

### Fixed

- Plan-approval prompts ("Claude has written up a plan… proceed?") now show **waiting** immediately instead of briefly flashing as running for ~15s.

## [2.10.0] - 2026-06-16

### Added

- OpenCode is now supported as a third agent alongside Claude and Codex — pick it in the `A` new-session dialog (or set it as your default agent), with live status detection (running / waiting / finished).

## [2.9.0] - 2026-06-16

### Added

- Restarting a session with `r` now asks for confirmation first, so an accidental keypress no longer kills in-progress work. Disable it in Settings → Behavior ("Confirm before restart").
- Contextual tips now appear in the bottom-right when relevant — e.g. when several sessions have stopped (after a reboot), a hint points you to Ctrl+K → "Reload All Sessions". Dismiss any tip with Shift+X.

### Fixed

- Selecting text in an attached session (drag, double-click, keyboard) now reliably copies to the system clipboard, including in iTerm2 and Apple Terminal where it previously didn't. Warp can't drag-copy (a Warp bug) and now shows a hint saying so.
- Forking a session that was itself forked now correctly forks that session, not its original ancestor — a forked session reliably adopts its own conversation id once it diverges.
- Sessions no longer show a stale "waiting" or "finished" state while the agent is actively working — after a granted permission prompt, an answered question, or during a long resumed turn. fleet now consults the conversation transcript to confirm whether the turn actually resumed or ended.
- Session statuses no longer freeze across the app when a background status check gets stuck.

## [2.8.2] - 2026-06-07

### Fixed

- Your settings (theme, telemetry consent, onboarding state) no longer reset when an older fleet build saves the config — config now preserves fields it doesn't recognize instead of dropping them. Every config write is also logged with the build that made it, for easier diagnosis.

## [2.8.1] - 2026-06-07

### Fixed

- Ctrl+Q detach now works when the host tmux config enables `extended-keys` (common in oh-my-tmux / iTerm2 setups). Previously the terminal encoded Ctrl+Q as `CSI 113;5 u` or `CSI 27;5;113 ~` instead of byte 17, so the interceptor missed it and the keystroke was forwarded to the pane.
- Sessions no longer get stuck showing "running" after Claude rotates its session id mid-conversation (compaction, /clear, or continue).
- Removing a worktree now stops the leftover dev processes still holding it open (sparing your editor, language servers, and shells), so it no longer fails with "Directory not empty". The confirm dialog lists what will be terminated, and if removal still fails the worktree is kept and flagged to retry with `d`.

## [2.8.0] - 2026-06-06

### Improved

- Collapsed origins and repos/worktrees now stay collapsed across restarts.

### Removed

- Removed the `z` idle-session fold.

## [2.7.0] - 2026-06-06

### Added

- Settings (`S`) is now a master-detail panel with an **Appearance** category that shows a live mock-sidebar preview as you change things. Pick a theme and toggle agent icons, PR badges, the dirty marker, status pills, slot badges, header counts, chevron style, and density — each updates the preview instantly. First launch also opens a one-time theme picker with an annotated sample sidebar that teaches how to read status dots, agent glyphs, branches, worktrees, and PR badges.

## [2.6.0] - 2026-06-04

### Added

- Session rows now show a per-agent glyph between the status dot and the title — `✻` for Claude, `◇` for Codex — so you can tell at a glance which agent a session runs. The glyph is dim and monochrome (identity is carried by shape), leaving the status dot's color to mean status alone.
- OpenAI Codex support — choose Claude or Codex per session. Press `A` to create a session with an agent picker, or set a default agent in Settings (`a` uses it). Codex sessions get the same live status, auto-naming, and resume via Codex's hooks.
- Draft PRs now show a dimmed `◌ #N` badge so they stand out from ready PRs awaiting review (which look the same yellow `#N`). A failing CI check still surfaces as `◌ #N ✕`.

### Fixed

- Codex sessions no longer get stuck showing **idle** while actively working. Codex status is hook-driven, but when its hooks don't fire (e.g. hook trust lapses), the session had no way back to running. fleet now falls back to the pane: an in-progress Codex turn (`Working … esc to interrupt`) is detected as running, mirroring the existing waiting-prompt detection.
- Sessions that finish while a background agent is still running no longer get stuck showing **Waiting**.
- A session waiting on a permission prompt no longer briefly shows as **running** for ~15 seconds right after the prompt appears — the prompt's own loading spinner no longer overrides the fresh waiting status.
- Worktree creation now works in git-crypt repos. git-crypt resolves its key via the per-worktree git dir, which has no key, so the smudge filter aborted checkout with `git-crypt: Error: Unable to open key file`. fleet now detects git-crypt repos and creates the worktree with `--no-checkout`, links the shared key into the worktree's git dir, then checks out.
- Sessions whose Claude spawns nested Claude processes (eval harnesses, sub-agents launched as separate `claude` runs) no longer flash a false "error" status or resume the wrong conversation when a child exits.
- A PR's CI badge no longer shows failed when a check failed and was then re-run green on the same commit — fleet now reads only the latest run per check, matching GitHub.

## [2.5.0] - 2026-06-02

### Added

- First-run launchpad: when the fleet is empty, fleet now scans your Claude Code history (`~/.claude/projects`) and offers the repos & worktrees you've recently worked in — grouped by origin with nested branches, exactly like the sidebar — instead of a blank "No Sessions Yet" screen. They're all pre-checked, so a single `↵` adds your whole working set: each repo/worktree is pinned and its last conversation resumed (`claude --resume`). `space` toggles a row, `A` selects all/none, `n` types a path, `Esc` falls back to the bare empty state. The boot splash stays up through the scan and reveals the launchpad in one transition. Non-git and missing paths are dropped.
- Linux support. `install.sh` now installs on Linux (x86_64/arm64), release builds ship Linux binaries, in-app self-update fetches the matching `linux` asset, browser launches (PR open, bug-report URL) use `xdg-open`, and the Chrome native-messaging host installs into the Chrome/Chromium config dirs under `~/.config/` (Chromium and the beta/unstable channels when present).
- When you decline the first-launch analytics prompt, fleet now sends a single anonymous `telemetry_declined` event so opt-out rates are visible alongside opt-ins. It carries only the anonymous device hash fleet already generates — never your git name/email, file paths, repo/branch names, or prompts — and fires exactly once per install. It is fully suppressed when telemetry is disabled via `FLEET_TELEMETRY_DISABLED` or `DO_NOT_TRACK`.

### Improved

- Session titles now use Claude's own model-generated title. fleet reads Claude Code's `ai-title` from the conversation transcript — the same title that evolves as the work shifts — instead of guessing from your first prompt and re-running a heuristic every few prompts. An in-session `/rename` (`custom-title`) still takes precedence, and fleet's `R` rename always wins. When Claude writes no title (e.g. title generation disabled), the old prompt heuristic remains as a fallback. Claude's occasional kebab-case slug titles (e.g. `native-ai-title-integration`) are spaced out for readability (`native ai title integration`) with their casing preserved, so acronyms like `API` survive.
- Sidebar redesigned around a calmer origin → checkout → session tree. Worktrees of the same GitHub repo collapse under one origin header (e.g. `brizzai/fleet`); no-remote repos get their own `local:<name>` group. `z` folds/unfolds idle sessions for a checkout; `u` is the new undo-delete (was `z`).
Boot is now gated by a one-shot bootstrap that resolves every repo's origin + branch + PR status in parallel (8 workers, 6s deadline) — the sidebar paints once in its final shape instead of regrouping as data trickles in. While bootstrap runs, fleet shows a gradient FLEET wordmark splash with rotating ops-humor labels and a progress bar.
Steady-state git/PR refresh now fans out across all session repos every 2s (bounded 4-worker pool) instead of round-robining one repo per tick, so branch/dirty/PR badges feel near-instant after any change.
- Sidebar and Preview now live in their own rounded-border cards with corner-inset titles (`╭─ Sessions ─...─╮` / `╭─ Preview ─...─╮`), so the two regions read as distinct surfaces instead of one stream split by a hairline. The focused panel switches its border to the accent color in focus mode.
New `fleet-pink` flagship theme (accent `#ff77c6`) is the default for first-run users. Tokyo Night, Catppuccin Mocha, Rosé Pine, Nord and Gruvbox remain available via the `S` settings dialog.
Sidebar cleanup: selected sessions drop the leading `▶` (it collided with the `▸` chevron used on collapsed headers — the inverted-background title was already carrying the selection signal). Worktree branch names render in italic so you can tell the main clone apart from its worktrees at a glance without an extra prefix column. In the default `icon` indicator mode, idle/starting sessions render a dim `·` anchor so the eye has a leftmost mark on every row (bar mode keeps them blank — the gutter bar carries the signal there). Selection background is one contiguous span across each row (PR badge and dirty marker sit inside the highlighted pill instead of bleeding out as separate boxes).
Sidebar width is now responsive: targets 65 absolute columns capped at 45% of terminal width. On a Mac 14" (~150 cols) that's ~43% so long titles fit; on a wide monitor it shrinks to ~26% so the preview keeps its share. Scroll indicators replaced the `⋮` glyph (which rendered as `:` in some fonts) with `… N more above/below`.
Visual rhythm pass: one blank row between origin groups carries the section break, and the indent tightened across the tree so long titles get more horizontal room. On boot, the cursor lands on the first session instead of the first origin, so your first keystroke does something useful.
New `Status style` setting (`icon` default, or `bar` for a VS-Code gutter-style `┃`) lets you pick how non-idle state is shown. Toggle live in the settings dialog.
Running/waiting/idle counts moved out of the top header and into a right-aligned pill embedded in the Sessions panel's top border, next to the title (`╭─ Sessions ── 2 RUN · 1 WAIT · 51 idle ─╮`). The header is just the `❯_ fleet` wordmark now.
Command palette dims the underlying UI via an SGR-faint backdrop so it visually lifts above the content instead of merging with the preview pane.

### Changed

- Command palette is now `Ctrl+K` (replacing `:` / `Ctrl+P`), renders as an overlay over the sidebar/preview instead of taking over the screen, and fuzzy-searches your repos and worktrees in addition to commands. Picking a repo/worktree jumps the sidebar cursor to that header. Tip: map `Cmd+K → Ctrl+K` in your terminal prefs (iTerm2 Key Mappings; Ghostty: `keybind = cmd+k=text:\x0b`) for the native macOS feel.
- `Space` (jump to next waiting/finished) now cycles in on-screen order and skips sessions inside a **collapsed origin** — fold an origin to mute its sessions from the jump cycle. A collapsed branch/checkout under an *expanded* origin is still reached: jump expands just that checkout to reveal the target. (This also fixes jump sometimes only moving in one direction.) Jumping to a slot-bound session with the digit keys still reveals it, expanding its origin group and checkout if folded. `Space` is now labelled "Jump" in the session footer.

### Fixed

- Sessions now flip from **Waiting → Running** within ~500ms after you approve a permission, instead of lagging several seconds (or tens of seconds) when many sessions are open. No Claude hook fires on permission approval, so that transition can only be seen by scanning the session's pane — and the background worker only scanned 5 sessions every 2s, so at ~40 sessions each one was revisited just once every ~18s. The worker now re-checks active sessions (running/waiting/starting) on a fast ~500ms cadence while keeping the heavier work (git/PR refresh, idle-session sweep, auto-naming, tmux status bars) on the existing ~2s cadence. The reverse `running → waiting` flip (e.g. a sub-agent hitting a permission prompt) is just as responsive.
- Worktree creation now works in git-crypt repos. git-crypt resolves its key via the per-worktree git dir, which has no key, so the smudge filter aborted checkout with `git-crypt: Error: Unable to open key file`. fleet now detects git-crypt repos and creates the worktree with `--no-checkout`, links the shared key into the worktree's git dir, then checks out.

## [2.4.1] - 2026-05-28

### Added

- Documentation site at brizzai.github.io/fleet — landing page with an interactive in-page TUI demo (arrow keys, Enter to approve, `a` to spawn, Space to jump to attention) plus full docs ported from the README. Built on Next.js + Fumadocs and auto-deployed to GitHub Pages on every push to master.

### Fixed

- `brew install brizzai/tap/fleet` no longer trips macOS Gatekeeper on first launch — the cask now strips the `com.apple.quarantine` attribute via a postflight hook. Also fixed the legacy `brizz-code` shim's brew-path message to point at `brew uninstall brizz-code` + `brew install brizzai/tap/fleet` instead of the incorrect `rm -f` line.

## [2.4.0] - 2026-05-28

### Added

- Compatibility shim for users still on the legacy `brizz-code` binary. Released as `brizz-code_<version>_darwin_<arch>.tar.gz` so v1.x auto-updates land on a small wrapper that prints a deprecation warning (rate-limited to once/day), then either execs `fleet` if installed, falls back to auto-installing the latest fleet release next to itself (verifying via `checksums.txt`), or — if running from a Homebrew prefix — points the user at `brew install brizzai/tap/fleet`.

## [2.3.0] - 2026-05-27

### Added

- Fork a Claude session into a different worktree — press `Shift+F` to open the worktree picker (pick an existing worktree or create a new one), and the forked conversation continues in the chosen destination instead of the parent's cwd. Available as "Fork to Worktree" in the command palette. Claude-only for this release.

### Changed

- Switched the analytics backend from Amplitude to Mixpanel and added a first-launch consent prompt: on the first run you'll see a dialog explaining exactly what fleet sends (including your git `user.name` and `user.email`, used as the Mixpanel `distinct_id` so the same person shows up across machines) and choose Yes or No with one keystroke. Choice persists in `~/.config/fleet/config.json` and can be changed any time in Settings (`S`). The standard opt-outs (`FLEET_TELEMETRY_DISABLED`, `DO_NOT_TRACK`, `telemetry: false`) still work and skip the prompt entirely. Significantly expanded the event set — onboarding funnel, shape gauges (repos, worktrees, sessions, slot bindings), engagement distributions, and frustration signals — to help spot when new users get stuck. Mixpanel HTTP calls run on a buffered worker goroutine so the TUI's Update() loop never blocks on the network.

## [2.2.0] - 2026-05-23

### Added

- Per-repo `copy_files.paths` config in `.fleet.json` / `.fleet.local.json` copies declared gitignored files/dirs/globs from the source repo into each new worktree. Opt-in; additive merge across both files; applies to both git-worktree and shell providers.
- Crash dumps for dying sessions: when a session transitions to `error`, fleet writes a forensic snapshot to `~/.config/fleet/crashes/<id>_<ts>.txt` containing the tmux exit status/signal (with human-readable annotation — e.g. `9 (SIGKILL — likely OOM/Jetsam or external kill)`), last 200 lines of pane content (raw ANSI), the SessionEnd hook reason from Claude Code, and the last 6 perfwatch heartbeats — enough to tell a kernel kill from a panic from a clean exit at a glance. Tmux now uses `remain-on-exit on` so dead panes can be inspected before fleet cleans them up. Perfwatch heartbeats also gained a `sys_free_mb` field (system-wide free memory) so the heartbeat trail records memory-pressure collapse leading up to a crash.
- `FLEET_AUTO_UPDATE_DISABLED=1` env var to skip the auto-updater on a per-launch basis (handy when running a local dev build you don't want overwritten by the latest release)
- "Mark All as Read" command in the command palette to acknowledge all finished sessions at once, transitioning them to idle.
- Storm detector in perfwatch: dumps a snapshot when sustained Update() throughput exceeds 200/s, catching tea.Cmd loops that flood the loop without any single Update going slow (the stall watchdog can't see those)
- Per-repo `pr_checks.ignore` config in `.fleet.json` / `.fleet.local.json` (path.Match globs) to drop noisy CI checks like gitstream's `minimum-review/default_reviewers` from the PR-badge rollup, so a single non-actionable failure no longer turns the whole badge red.

### Changed

- Delete is now scoped to whatever the cursor is on, so the confirm dialog is a single `y`/`n` (the old `Y +Workspace` / `D +Remove Repo` buttons are gone). `d` on a session deletes just that session; `d` on a worktree header deletes its sessions and runs `git worktree remove`; `d` on a repo header "forgets" the repo from fleet (deletes its sessions + unpins, the folder is left untouched); `d` on an empty repo header still unpins instantly. Header deletes route through the same 5s deferred-delete machinery, so `z` undo keeps working.
- The "new worktree" dialog now pre-fills the base branch as `origin/<default>` (e.g. `origin/master`) instead of the local branch, so new worktrees start from the remote tip rather than a possibly-stale local ref. Falls back to local `main`/`master` for repos without an `origin` remote.

### Fixed

- Status flickering between `waiting` and `running` while navigating Claude's AskUserQuestion dialog
- Restarting a dead session no longer briefly flashes back to `error` (with a misleading crash dump) before the new Claude process fires its first hook. Restart now clears the previous Claude's hook state — both in memory and the on-disk status file — so the worker doesn't trust an 8-minute-old `status=dead` during the relaunch window. The crash-dump quota also re-arms when a fresh hook reports the session is alive again, so a real death following a false-positive flash still produces a dump.
- TUI freezing for ~500ms when the 5-second undo-delete window expired while scrolling — the tmux kill now runs off the Update loop
- `debuglog.Init()` is now idempotent (guarded by `sync.Once`). The previous behavior re-created the global `slog.Logger` on every call, which raced with long-lived goroutines (e.g. the crash-dump writer) that hold a reference to it. Fixes a `go test -race` data race surfaced in CI.

## [2.1.0] - 2026-05-07

### Added

- PgUp / PgDn navigate the sidebar by a full page; cursor stays in view at list edges

## [2.0.0] - 2026-04-29

### Added

- AI-powered PR review config via CodeRabbit: `.coderabbit.yaml` tunes the reviewer for Go (chill tone, golangci-lint + gitleaks wired in, path instructions that flag Bubble Tea anti-patterns and public-repo workflow hazards). Once the CodeRabbit GitHub App is installed on the repo, every PR gets an auto-generated walkthrough and inline review comments. Free for public repos — contributors don't need anything set up on their side.

### Improved

- Worktree dialogs now guide you toward a valid git branch name as you type: spaces become `-`, and chars git forbids anywhere (`~ ^ : ? * [ \` and control chars) are dropped live. Rules that can't be fixed silently (leading `-`, `..`, trailing `.lock`, etc.) show a friendly inline error on submit instead of a cryptic `git worktree add` failure.

### Changed

- **Renamed `brizz-code` to `fleet`.** Binary, config dir, tmux prefix, env vars, Chrome native messaging host, and Homebrew formula all renamed:
- Binary: `brizz-code` → `fleet`
- Config dir: `~/.config/brizz-code/` → `~/.config/fleet/`
- Tmux prefix: `brizzcode_` → `fleet_`
- Env vars: `BRIZZCODE_INSTANCE_ID` → `FLEET_INSTANCE_ID`, `BRIZZ_DEBUG` → `FLEET_DEBUG`, `BRIZZ_TELEMETRY_DISABLED` → `FLEET_TELEMETRY_DISABLED`, `BRIZZ_DEMO_PREFIX` → `FLEET_DEMO_PREFIX`
- Per-repo workspace config: `.fleet.json` / `.fleet.local.json` (legacy `.bc.json` / `.bc.local.json` still read for compatibility)
- NMH manifest: `com.brizzai.fleet.tabcontrol.json`
- Homebrew: `brew install brizzai/tap/fleet`
**Auto-migration on first launch:** existing `~/.config/brizz-code/` is moved to `~/.config/fleet/`, live `brizzcode_*` tmux sessions are renamed to `fleet_*`, and stale `brizz-code hook-handler` entries are stripped from `~/.claude/settings.json`. Legacy `BRIZZ*` env vars are accepted as fallback for one release window so in-flight Claude processes survive the upgrade. The Chrome extension keeps the same extension ID (stable via `key` in manifest), so no reinstall is needed.
To upgrade from `brizz-code`:
```bash
brew uninstall brizz-code
brew install brizzai/tap/fleet
```
Or run `fleet` directly — the migration shim handles config moves, tmux session renames, and hook cleanup transparently.

### Fixed

- Status detection: sessions in extended thinking mode (with `· ↓ tokens · thinking with high effort` format) now correctly stay "running" instead of oscillating between running/finished every 10 seconds.
- Status detection: hook events now reflect in the TUI within ~100ms (was 4–6s, waiting on the worker's round-robin). Stale "running" hooks no longer oscillate between idle/running/finished on pane-content changes (survey popups, cursor blinks, scrollback redraws).

## [1.3.0] - 2026-04-15

### Added

- RTS-style session hotkeys: bind the selected session to a numbered slot with `Alt+0-9` (or `=` then a digit), jump with plain `0-9`, double-tap within 400ms to also attach. Unbind by re-pressing `Alt+<N>` on the already-bound session, or `==` then the digit to clear any slot. Bound sessions show a `[N]` badge in the sidebar and persist across restarts.
- Undo delete (z key): restore deleted sessions within 5 seconds. Sticky repos: empty repo groups persist in sidebar until dismissed.

### Fixed

- Fatal "concurrent map read and map write" crash caused by unlocked reads of the git info cache during render
- Status detection: sessions with `hook=finished` no longer flap between "running" and "finished" during active sub-agent work. `applyHookFinished` now corroborates pane-detected "finished" with tmux window activity — if the pane was written to in the last 3 seconds, hold the previous state instead of flipping.
- Status detection: permission menus where the cursor is on option 2 or 3 (not just option 1) are now correctly detected as "waiting" instead of flipping the session to idle.
- Status detection: sessions running Explore sub-agents (with the `· ↑ tokens` output counter) stay marked as "running" instead of collapsing to "idle" when a stale waiting hook is in play.
- Status detection: idle sessions no longer get stuck at "running" when their scrollback contains text that mentions the whimsical token counter (e.g. commit messages or docs referencing `· ↓`/`· ↑` + `tokens`).

## [1.2.0] - 2026-04-09

## [1.2.0] - 2026-04-09

### Added

- Agent team status detection: sub-agent permission prompts and "Waiting for team lead approval" now correctly show as waiting
- Command palette (`:` or `Ctrl+P`) — fuzzy-searchable list of all actions with shortcut hints, plus "Reload All Sessions" for bulk restart of dead/error sessions
- Terminal environment and rendering stats in bug reports to help diagnose scroll/rendering issues

### Improved

- Status updates now respond in ~150ms instead of up to 2s via event-driven hook notifications

### Fixed

- Agent team sessions showing idle/running instead of waiting when sub-agent needs approval
- Bug report dialog freezing permanently when `gh` CLI is not installed
- "Last used" time now updates on all interactions (approve, restart, new prompt), not just attach
- Status showing stale data immediately after detaching from a session
- Status oscillating between idle and finished when stale waiting hook is present
- Session stuck at "waiting" status after user interrupts/escapes a permission prompt


## [1.1.0] - 2026-03-21

### Added

- Anonymous usage analytics to help improve fleet (opt out via Settings, config, or `DO_NOT_TRACK=1`)

## [1.0.0] - 2026-03-21

Initial open-source release.

### Added

- TUI for managing multiple Claude Code sessions in parallel using tmux
- Real-time status detection via Claude Code hooks (no polling)
- Sessions grouped by git repo with branch name, dirty indicator, and PR badges
- Jump to next waiting session (`Space`) and quick approve (`Y`)
- Git worktree integration with branch picker (`w`)
- Session fork to branch off Claude conversations (`f`)
- Session resume with `claude --resume` on restart
- Auto-naming sessions from first user prompt
- 5 built-in themes: tokyo-night, catppuccin-mocha, rose-pine, nord, gruvbox
- Settings dialog with live theme preview (`S`)
- Full PTY attach with Ctrl+Q detach and split/focus mode
- Chrome extension for tab control (reuse PR tabs with `p`)
- Bug report dialog with diagnostics, error history, and action log (`!`)
- Auto-update mechanism with `fleet update`
- Install via Homebrew, shell script, or `go install`
- Per-repo workspace config via `.bc.json` / `.bc.local.json`
- `/ship` release workflow — comment `/ship` on any issue or PR to release
- Changelog check on PRs with `/no-changelog` escape hatch

[Unreleased]: https://github.com/brizzai/fleet/compare/v2.22.0...HEAD
[2.22.0]: https://github.com/brizzai/fleet/releases/tag/v2.22.0
[2.21.0]: https://github.com/brizzai/fleet/releases/tag/v2.21.0
[2.20.0]: https://github.com/brizzai/fleet/releases/tag/v2.20.0
[2.19.0]: https://github.com/brizzai/fleet/releases/tag/v2.19.0
[2.18.0]: https://github.com/brizzai/fleet/releases/tag/v2.18.0
[2.17.0]: https://github.com/brizzai/fleet/releases/tag/v2.17.0
[2.16.1]: https://github.com/brizzai/fleet/releases/tag/v2.16.1
[2.16.0]: https://github.com/brizzai/fleet/releases/tag/v2.16.0
[2.15.0]: https://github.com/brizzai/fleet/releases/tag/v2.15.0
[2.14.0]: https://github.com/brizzai/fleet/releases/tag/v2.14.0
[2.13.0]: https://github.com/brizzai/fleet/releases/tag/v2.13.0
[2.12.1]: https://github.com/brizzai/fleet/releases/tag/v2.12.1
[2.12.0]: https://github.com/brizzai/fleet/releases/tag/v2.12.0
[2.11.1]: https://github.com/brizzai/fleet/releases/tag/v2.11.1
[2.11.0]: https://github.com/brizzai/fleet/releases/tag/v2.11.0
[2.10.0]: https://github.com/brizzai/fleet/releases/tag/v2.10.0
[2.9.0]: https://github.com/brizzai/fleet/releases/tag/v2.9.0
[2.8.2]: https://github.com/brizzai/fleet/releases/tag/v2.8.2
[2.8.1]: https://github.com/brizzai/fleet/releases/tag/v2.8.1
[2.8.0]: https://github.com/brizzai/fleet/releases/tag/v2.8.0
[2.7.0]: https://github.com/brizzai/fleet/releases/tag/v2.7.0
[2.6.0]: https://github.com/brizzai/fleet/releases/tag/v2.6.0
[2.5.0]: https://github.com/brizzai/fleet/releases/tag/v2.5.0
[2.4.1]: https://github.com/brizzai/fleet/releases/tag/v2.4.1
[2.4.0]: https://github.com/brizzai/fleet/releases/tag/v2.4.0
[2.3.0]: https://github.com/brizzai/fleet/releases/tag/v2.3.0
[2.2.0]: https://github.com/brizzai/fleet/releases/tag/v2.2.0
[2.1.0]: https://github.com/brizzai/fleet/releases/tag/v2.1.0
[2.0.0]: https://github.com/brizzai/fleet/releases/tag/v2.0.0
[1.3.0]: https://github.com/brizzai/fleet/releases/tag/v1.3.0
[1.2.0]: https://github.com/brizzai/fleet/releases/tag/v1.2.0
[1.1.0]: https://github.com/brizzai/fleet/releases/tag/v1.1.0
[1.0.0]: https://github.com/brizzai/fleet/releases/tag/v1.0.0
