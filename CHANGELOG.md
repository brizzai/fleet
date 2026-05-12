# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/brizzai/fleet/compare/v2.1.0...HEAD
[2.1.0]: https://github.com/brizzai/fleet/releases/tag/v2.1.0
[2.0.0]: https://github.com/brizzai/fleet/releases/tag/v2.0.0
[1.3.0]: https://github.com/brizzai/fleet/releases/tag/v1.3.0
[1.2.0]: https://github.com/brizzai/fleet/releases/tag/v1.2.0
[1.1.0]: https://github.com/brizzai/fleet/releases/tag/v1.1.0
[1.0.0]: https://github.com/brizzai/fleet/releases/tag/v1.0.0
