# fleet

> This file provides context for AI-assisted development with Claude Code.

TUI tool for managing multiple Claude Code and OpenAI Codex sessions in parallel using tmux.

## Tech Stack
- Go 1.26+, Bubble Tea v2 + Lip Gloss v2 (`charm.land/.../v2`), `charmbracelet/x/vt` (drawer's live terminal), tmux, SQLite (WAL mode)

## Build
```bash
make build    # build to build/fleet
make run      # go run
make test     # go test -race
make fmt      # go fmt
make lint     # golangci-lint
make coverage # test coverage report
```

## Commit Convention
Use [conventional commits](https://www.conventionalcommits.org/). Version is auto-computed from commits:
- `fix: ...` → patch (v0.1.0 → v0.1.1)
- `feat: ...` → minor (v0.1.0 → v0.2.0)
- `feat!: ...` or `BREAKING CHANGE: ...` → major (v0.1.0 → v1.0.0)
- `chore:`, `docs:`, `refactor:`, `test:`, `style:` → patch
- Scopes are optional: `fix(hooks): ...`, `feat(ui): ...`

## Changelog
- Each PR adds a fragment file: `changelog/unreleased/<name>.md` with `type:` frontmatter (added/improved/fixed/changed/removed)
- **Keep fragments concise: 1–2 sentences, lead with the user-facing change.** No implementation detail, marketing tone, or exhaustive sub-points — a user skimming the release notes should grasp it at a glance. e.g. "Status updates now respond in ~150ms instead of up to 2s."
- CI checks for fragment presence; comment `/no-changelog` to skip
- At release time, fragments are merged into CHANGELOG.md and deleted

## Release
- Comment `/ship` on any issue or PR to prepare a release
- `/ship 2.0.0` to override the version
- CI opens a release PR with changelog rolled — review and merge to release
- Merging the release PR triggers GoReleaser (binaries, GitHub Release, Homebrew)
- Install: `brew install brizzai/tap/fleet` or run `bash install.sh` (requires `gh` CLI)

## Package Structure
```text
cmd/fleet/main.go      # CLI entry point
internal/tmux/tmux.go        # Tmux abstraction (create, kill, capture)
internal/tmux/pty.go         # PTY-based attach with Ctrl+Q detach
internal/session/session.go  # Session model, status detection, claude --resume
internal/session/storage.go  # SQLite persistence (sessions, shells, slot_bindings, pinned_repos, …)
internal/shell/shell.go      # Shell model: non-agent drawer terminals + tmux-derived status (idle/running/exited)
internal/git/git.go          # Git operations (branch, dirty, worktree)
internal/git/repo_info.go    # RepoInfo cache + refresh logic
internal/github/pr.go        # GitHub PR info via gh CLI
internal/hooks/              # Hook-based status detection (claude_hooks, hook_watcher, status_file)
internal/workspace/provider.go     # Provider interface + GitWorktreeProvider + ShellProvider
internal/workspace/repo_config.go  # Per-repo .fleet.json loading (legacy .bc.json supported) + ResolveProvider
internal/config/config.go    # JSON config (~/.config/fleet/config.json)
internal/naming/naming.go    # Auto-name sessions via smart heuristic (filler stripping, title-case)
internal/debuglog/           # slog-based debug logging to ~/.config/fleet/debug.log
internal/diagnostics/diagnostics.go  # System diagnostics collector for bug reports
internal/ui/                 # Bubble Tea TUI (app, sidebar, preview, dialogs, styles)
internal/ui/palette.go       # Theme palette definitions (5 built-in themes)
internal/ui/settings.go      # Settings dialog (S key) — master-detail: Appearance/Behavior categories + live preview
internal/ui/sidebar_preview.go   # Synthetic sidebar fixtures + RenderSidebarPreview (drives Appearance preview + onboarding)
internal/ui/onboarding.go    # First-run theme picker + annotated "how to read the sidebar" sample
internal/ui/bugreport.go     # Bug report dialog (! key) with diagnostics, error history, action log
internal/ui/actionlog.go     # Ring buffer tracking user actions (steps to reproduce)
internal/ui/errors.go        # Ring buffer keeping error history (errors that flash and vanish)
internal/ui/keybindings.go   # Centralized keybinding definitions
internal/ui/workspace_picker.go  # Worktree dialog (base branch + new branch + existing worktrees)
internal/ui/workspace_create.go  # Create workspace sub-dialog + PendingWorkspace phantom entries
internal/ui/command_palette.go   # Command palette dialog (Ctrl+K) — fuzzy search over commands + repos/worktrees, renders as overlay
internal/ui/drawer.go            # Terminal drawer (` key): render, slide animation, focus/key handling, shell lifecycle + live-stream wiring (syncShellStream → OutputReader + vterm)
internal/tmux/control_output.go  # Control-mode %output reader (live shell streaming) + octal decoder
internal/vterm/vterm.go          # Insulated charmbracelet/x/vt wrapper (drawer live rendering; strips ESC k titles)
internal/chrome/protocol.go      # Command/Response types, action constants, socket path
internal/chrome/native_host.go   # Native messaging host with Unix socket bridge
internal/chrome/client.go        # TUI-side client (connects to socket, sends commands)
internal/chrome/install.go       # NMH manifest auto-install to Chrome's NativeMessagingHosts dir
chrome-extension/                # Chrome MV3 extension (service worker, manifest, icons)
```

## Conventions
- Tmux session prefix: `fleet_` (agent sessions); drawer shells use a distinct `fleetsh_` prefix — intentionally not a prefix of `fleet_`, so shells never leak into agent-session enumeration (`tmux.ListSessions`)
- Session ID format: `<8hex>-<unix_timestamp>`
- SQLite DB: `~/.config/fleet/state.db`
- Sessions grouped by git repo root in sidebar with tree lines (├─/└─)
- Status: Running, Waiting, Finished, Idle, Error, Starting
- Status icons: ● (running/finished), ◐ (waiting), ○ (idle/starting), ✕ (error)
- Agent glyph: each session row shows a dim, monochrome per-agent sigil between the status dot and the title — `✻` Claude, `◇` Codex, `△` OpenCode (`agentGlyph` + `AgentGlyphStyle` in `sidebar.go`/`styles.go`); all are width-1 glyphs from well-covered Unicode blocks (Dingbats / Geometric Shapes — same block as the status dots) so they stay aligned in base mono fonts; identity is carried by shape so the status dot keeps sole ownership of color; empty/legacy `Agent` falls back to Claude
- Keybindings: j/k nav, Enter attach, Space jump to next waiting/finished, a new session (instant, repo-scoped, default agent), A new session with agent picker (Claude/Codex/OpenCode), n new session (any repo, path autocomplete), w new worktree session (base branch + new branch), F fork to worktree (Claude-only), d delete (scope follows cursor: session = that session; worktree header = sessions + git worktree remove; repo header = forget repo from fleet, folder untouched; empty repo header = unpin; origin header = forget the whole group, checkbox-gated), u undo delete (5s window), r restart (confirm, configurable), R rename, e editor, p open PR in browser, Y quick approve (waiting sessions), / filter, Ctrl+K command palette, `` ` `` toggle terminal drawer, S settings, X dismiss on-screen tip, ! bug report/diagnostics, ? help, ctrl+c quit
- Terminal drawer (`` ` `` key): a collapsible panel holding plain non-agent "shells" (dev servers, log tails, scratch commands), scoped to the selected repo/worktree. Separate from sessions (`internal/shell`, `shells` SQLite table) — never in the sidebar, no hooks/auto-naming. **Placement is layout-aware** (see `renderBody` in `app.go`): in **dual** it splits the right column — preview on top, terminal below (`lipgloss.JoinVertical`), session list untouched and full-height; in **single/stacked** it falls back to a full-width band at the bottom that shrinks `contentHeight`. `renderDrawer(width, maxOuterH)` clamps its outer height to `maxOuterH` (in dual that's `contentHeight - drawerMinPreviewRows`, so the preview keeps ≥5 rows). Renders as a bordered panel (fleet's panel vocabulary, accent border when focused) with tabs inset in the top border and a loud `● TYPING → <shell>` label top-right. **The body is a live virtual-terminal emulator** (`internal/vterm`, wrapping `charmbracelet/x/vt`) fed by a tmux **control-mode `%output` reader** (`internal/tmux/control_output.go` → `OutputReader`, attached per active shell, re-pointed on tab switch/restart): byte- and cursor-accurate, **event-driven** (no capture-pane polling), rendered each frame on the View thread. `syncShellStream`/`startShellStreamAsync`/`teardownShellStream` (drawer.go) own the reader+emulator lifecycle — the attach (a `tmux -C` + PTY fork) and teardown (`Close` = Kill+Wait) run **off the Update goroutine** (async dispatch → `shellStreamReadyMsg`, installed only if the requested target+size are still current; `attachShell` uses a synchronous teardown before its full-screen takeover). On attach the fresh emulator is **seeded via `capture-pane`** (`tmux.CapturePaneANSI` → `drawerSeedBytes`): control mode replays nothing on attach, so without the seed the body is blank until the next output. The reader sizes the pane to the drawer body so wrap points match (`renderDrawer` records `drawerInnerW/H`), and the drawer is a **stable-height viewport** (capped by `drawer_height`, clamped to `[DrawerHeightMin,DrawerHeightMax]`=`[4,14]`), not content-fit. The reader writes bytes into the mutex-guarded emulator and schedules a single coalesced `shellOutputMsg` render wake (`shellWake` CAS); a slow `drawerSyncInterval` tick is the lifecycle/resize backstop. `vterm` strips screen/tmux `ESC k … ST` set-title escapes that x/vt would otherwise leak as visible text. **Always-typing, 2 states** (`drawerMode`: hidden/typing — see `internal/ui/drawer.go`): `` ` `` opens straight into TYPING (auto-creates a shell if the repo has none) — keystrokes forward to the shell pane via the focus-mode control client (`forwardKeyToPane`, which maps any `Ctrl+<letter>` → tmux `C-<letter>` so the shell's own line-editing keeps working; `Esc` passes through to the shell). **No menu mode**; chrome is a small set of Ctrl chords intercepted before forwarding: `Ctrl+T` new shell, `Ctrl+W` close (twice to confirm a running one, armed via `drawerCloseArmed`), `PgUp`/`PgDn` switch tab (plain or Ctrl-modified both accepted; chosen for reliable delivery — no modifier a terminal might swallow; costs the shell's PageUp/PageDown inside the drawer), `Ctrl+G` full attach (Ctrl+Q returns), `` ` `` close drawer. An **exited** shell restarts on `Enter` (no live process to type to). Cost: the shell loses `Ctrl+T` (transpose) and `Ctrl+W` (delete-word) to the drawer. Status (○ idle / ● running / ✕ exited+code) derives from tmux `pane_current_command` + pane-dead, no hooks. Shell tmux sessions use the `fleetsh_` prefix; removing a worktree kills its shells first (`killShellsForRepo`) so the dir frees for `git worktree remove`. Max body height via `drawer_height` config (default 12).
- Session hotkeys (RTS-style): `Alt+0-9` (or `=` then digit) binds the selected session to a slot; re-pressing `Alt+<N>` on a session already in slot N unbinds; `==` then digit clears any slot; plain `0-9` jumps to the bound session (double-tap within 400ms also attaches); `[N]` badge in sidebar marks bound sessions; bindings persist in SQLite `slot_bindings` table (FK cascade on session delete)
- Command palette (Ctrl+K): renders as an overlay over the sidebar/preview (not a full-screen takeover); fuzzy-searches commands plus every repo/worktree currently in the sidebar (name, branch, full path all matched); picking a repo/worktree jumps the sidebar cursor to that header (auto-expand if collapsed); palette-only commands include "Reload All Sessions" (restarts all dead/error sessions). For a native Cmd+K feel on macOS, map Cmd+K → Ctrl+K in your terminal prefs (iTerm2: Profiles → Keys → Key Mappings → +; Ghostty: `keybind = cmd+k=text:\x0b`).
- Contextual tips: a bottom-right hint box surfaces actionable tips when a condition holds (catalog in `internal/ui/tips.go` → `tipRegistry`). Two policies: `tipRecurring` (condition-driven, e.g. ≥4 error sessions → "Reload All Sessions" hint; dismissal is in-memory and the tip recurs when the condition repeats) and `tipOnce` (no recurrence, e.g. command-palette discovery; auto-expires after `tipOnceBudget` and is persisted in config `seen_tips`). `refreshTips` runs on the ~2s tick; `Shift+X` (`dismissActiveTip`) hides the visible tip; rendered via `renderTip` + bottom-right overlay (stacked above any toast), suppressed while a modal is open (`modalOpen`)
- Undo delete: `u` key restores last deleted session within 5s window (stacked — multiple deletes each undoable). Tmux kept alive during window for full restore.
- Pinned repos: repos auto-pinned on session creation, persist in SQLite. Empty repos show dimmed with "(empty)". `d` on empty repo header unpins it.
- Delete scope follows the cursor (single-purpose `y`/`n` confirm, no extra buttons): `d` on a session deletes only that session; `d` on a worktree header deletes its sessions + runs `git worktree remove` + unpins; `d` on a real-repo header "forgets" the repo (deletes its sessions + unpins, folder untouched); `d` on an empty real-repo header unpins instantly (no dialog). Worktree-vs-repo detected via cached `RepoInfo.IsWorktreeRepo`, falling back to a direct `git.IsWorktree` check for empty headers the worker hasn't cached yet. Header deletes route through the same deferred-delete machinery, so `u` undo works (LIFO, per-session).
- Origin-row delete (`d` on the top-level origin header): forgets the **whole group** — every checkout under the origin (main repo + all worktrees) and their sessions. Because it wipes a group at once, the confirm dialog is **checkbox-gated** (`ConfirmDialog.RequireCheckbox` — `space` toggles `◉`, `y`/`Enter` inert until ticked). `confirmDeleteOrigin` gathers checkouts via `checkoutsForOrigin` (scans sessions + pinned + pending, deduped — works even when the origin is collapsed) and fans out to `deferDeleteRepo` per checkout, so each follows its own rule and `u`-undo still works. Whether worktree dirs are also removed from disk follows the `origin_delete_removes_worktrees` config (default true; main repo folder always kept).
- Worktree removal kills holding processes: leftover dev daemons (process-compose, air, vite/node) that detached from the tmux pane pin the worktree dir and make `git worktree remove` fail with "Directory not empty". Before removing, `destroyWorktree` (app.go) finds holders via `internal/proc` (`lsof -Fpcn`, prefix-match) and SIGTERM→grace→SIGKILLs them — sparing a denylist (editors, language servers, shells, tmux, fleet, ssh). `GitWorktreeProvider.Destroy` falls back to `os.RemoveAll` + `git worktree prune` (package mutex serializes concurrent removes). The delete dialog pre-lists warnings: uncommitted-changes, running-session count (instant from cache), and an async "Will terminate N process(es): …" line (`ConfirmDialog.StartScan`/`SetScan`, gen-guarded). If removal still fails, the worktree is re-pinned + flagged `✕ removal failed — d to retry` (`failedWorktreeRemovals`), so `d` retries.
- Tmux status bar configured per session with detach hint (ctrl+q)
- Attach uses PTY with Ctrl+Q intercept for clean detach (creack/pty + golang.org/x/term)
- Copy-to-clipboard: a copy-mode selection (drag, double/triple-click, keyboard copy) reaches the macOS clipboard via `tmux.EnsureCopyCommand` → `set-option -s copy-command pbcopy`. tmux's default copy bindings run `copy-pipe-and-cancel` with no arg, which pipes to `copy-command`; unset, they fall back to OSC 52 — which iTerm2 blocks by default and Apple Terminal doesn't support — so the copy never lands. `pbcopy` is a local pipe, terminal-independent. `copy-command` is a **server** option (fleet shares the user's tmux server, no dedicated socket), so it's set **only when unset**, leaving a user's own `copy-command` alone; it re-checks the live server on each call (no process-level cache) so a tmux server created fresh after a restart still gets `pbcopy`. Set `FLEET_NO_COPY_COMMAND` (truthy) to opt out entirely — e.g. when deliberately relying on OSC 52 (remote tmux → local clipboard over SSH). Called from `Start` (server exists) + the startup bootstrap (existing-session users)
- Warp can't drag-copy at all: Warp doesn't deliver mouse drag-selection to terminal apps (no tmux entry into copy-mode), so neither `copy-command` nor any tmux binding helps — unfixable from our side. `ApplyStatusBar` shows an amber "⚠ Warp doesn't support drag-to-copy (Warp bug)" on the bottom-right when `isWarpTerminal()` (`TERM_PROGRAM=WarpTerminal`)
- `mouse on` (set per session) is required for scroll: Claude/Codex render to the main screen (not the alternate screen), so with `mouse off` the wheel scrolls the outer terminal's scrollback (stale frames) instead of session history. Cost of `mouse on` is that native click-drag selection needs a terminal modifier (Shift; Option in iTerm2)
- Repo headers show branch name (), dirty indicator (*), and PR badge (#N)
- Git info refreshes every 2s (branch/dirty), PR info every 60s via `gh` CLI
- PR badge: green ✓ (approved+CI passed), yellow (pending), red ✕ (CI fail) / ↩ (changes requested or unresolved threads), purple ⇡ (merged), gray ◌ (draft; ✕ appended on CI fail), hidden (closed)
- PR info includes unresolved review thread count via GitHub GraphQL API
- `gh` CLI optional — PR info hidden if not installed
- Preview strips OSC-8 hyperlink sequences to prevent dotted underline artifacts
- Status detection: hook-based (primary, no time expiry) via Claude Code hooks + pane capture (fallback, ANSI-stripped)
- Agent team status: sub-agents don't fire hooks, so pane detection handles team states via structural checks (numbered menu `❯ 1.`+`2.`+ a menu footer — `Esc to cancel`, or `approve with this feedback` for ExitPlanMode plan-approval prompts; box-drawing `│`+`Waiting for team lead`); hook=running is never overridden to waiting by pane (avoids false-positives from code in scrollback)
- All blocking I/O (tmux, git, gh) runs in background worker goroutine, never in Bubble Tea Update()
- Hook status files: `~/.config/fleet/hooks/{session_id}.json`
- Hook handler: `fleet hook-handler` (invoked by Claude Code hooks, reads FLEET_INSTANCE_ID env)
- Hooks auto-installed into `~/.claude/settings.json` on TUI launch
- Debug log: `~/.config/fleet/debug.log` (slog, init in TUI and hook-handler)
- Config file: `~/.config/fleet/config.json` (tick_interval_sec, default_project_path, editor, theme, auto_name_sessions, copy_claude_settings, confirm_before_restart, origin_delete_removes_worktrees, drawer_height)
- Workspace: built-in git worktree support (zero config), per-repo `.fleet.json` (or legacy `.bc.json`) overrides with custom shell commands
- Workspace creation is non-blocking: dialog closes immediately, phantom "Creating..." entry with spinner appears in sidebar, user can keep navigating
- Worktree creation copies `.claude/settings.local.json` from source repo (configurable via `copy_claude_settings`, default true)
- `.fleet.json` / `.fleet.local.json` in repo root (legacy `.bc.json` / `.bc.local.json` still read): `{"workspace": {"list": "cmd", "create": "cmd {{name}} {{branch}}", "destroy": "cmd {{name}}"}}`
- `.fleet.json` / `.fleet.local.json` may also set `{"pr_checks": {"ignore": ["glob", ...]}}` to drop matching CI checks from the PR-badge rollup (path.Match globs; lists from both files merge additively; opt-in, empty by default)
- `.fleet.json` / `.fleet.local.json` may also set `{"copy_files": {"paths": ["path", "dir", "glob/*", ...]}}` to copy gitignored files/dirs/globs from the source repo into each new worktree (filepath.Glob semantics, repo-relative only; lists from both files merge additively; opt-in, empty by default; applies to both git-worktree and shell providers; independent of `copy_claude_settings`)
- Multi-agent: per-session agent (Claude, Codex, or OpenCode), chosen at creation (`A` key picker or `default_agent` config used by `a`). Stored in SQLite `agent` column; `internal/agent` owns binary name + launch command (`claude` / `codex resume <id>` / `codex fork <id>` / `opencode --session <id>` / `opencode --session <id> --fork`).
- Codex status: driven entirely by Codex hooks (no pane scraping); same pipeline as Claude — `fleet hook-handler` is agent-neutral (`hook_event_name`/`session_id`/`prompt` match Claude). Hooks installed to `~/.codex/hooks.json` (`InjectCodexHooks`, only when `codex` on PATH). Codex has no SessionEnd → `dead` from tmux pane-death. Claude pane heuristics never run for Codex sessions.
- OpenCode status: driven entirely by a generated TS plugin (no pane scraping); same agent-neutral `fleet hook-handler` pipeline. Unlike Claude/Codex declarative hook JSON, OpenCode's hook mechanism is a JS/TS plugin, so fleet writes `~/.config/opencode/plugin/fleet-status.ts` (`InjectOpenCodePlugin`, only when `opencode` on PATH; the resolved fleet binary path is baked in). The plugin's `event` hook maps OpenCode-native bus events → fleet statuses via `spawnSync` (synchronous so the final status flushes before OpenCode exits, and ordering is preserved): `session.status{busy}`→running, `session.idle`→finished, `permission.asked`→waiting (only fires if the user set `permission: ask`; OpenCode defaults to allow-all). Sub-agent sessions carry a `parentID` and are filtered so they don't flip the root session's status. No dir-trust seeding needed (OpenCode has no trust gate). No SessionEnd → `dead` from tmux pane-death. `UpdateStatus` routes OpenCode through `applyHookStatus` (shared with Codex); no pane heuristics run.
- Codex trust: dir-trust pre-seeded to `~/.codex/config.toml` (`[projects."<path>"] trust_level="trusted"`, via `EnsureCodexDirTrust`) before launch; hook-trust is a one-time global TUI prompt the user accepts on first Codex launch (persists in config.toml `[hooks.state]`).
- Session resume: captures the agent's session_id from hooks, uses `claude --resume <id>` / `codex resume <id>` on restart
- Editor: config.editor > $EDITOR > "code" (VS Code)
- Themes: fleet-pink (default, flagship brand accent `#dc88c0`), tokyo-night, catppuccin-mocha, rose-pine, nord, gruvbox — configurable via settings (S key)
- Settings dialog: S key opens a master-detail overlay — category rail (Appearance / Behavior) on the left, that category's settings on the right; `tab` switches panes, `j/k` move, `←→`/`h/l` cycle a value, auto-saves on `esc`. The **Appearance** category renders a live mock-sidebar preview (synthetic data via `RenderSidebarPreview` → the real `RenderSidebar`, so it can't drift) and holds: Theme, Status style (icon/bar), Agent icons, Slot badges, PR badges, Dirty marker, Status pills, Header counts, Chevron style (triangle/plusminus), Density (normal/compact). Behavior holds editor, tick, auto-name, auto-update, copy .claude, confirm before restart, drawer height (rows, capped at the 14-row body max), origin forget removes worktrees, enter mode, telemetry, default agent.
- Display toggles: each Appearance toggle is a `*bool`/string config field with a default-on getter (`config.go`) feeding a package global in `styles.go` (`ShowAgentGlyphs`, `ShowStatusPills`, `ShowPRBadges`, `ShowDirtyIndicator`, `ShowSlotBadges`, `ShowHeaderCounts`, `ChevronStyle`, `SidebarDensity`) — the same pattern as `StatusIndicatorMode`. Globals are synced from config via `ApplyDisplayConfig(cfg)` in `NewHome` (startup) and after each Appearance change; the sidebar `render*` funcs read them at render time.
- First-run onboarding: on first launch, after the analytics consent prompt, a one-time theme picker (`internal/ui/onboarding.go`, gated by `display_onboarding_seen` config flag) shows the theme cycler beside an annotated sample sidebar that teaches how to read status dots, agent glyphs, branch, worktree, dirty `*`, and PR badge. `enter` keeps the previewed theme; `s`/`esc` reverts and skips. Either way it's marked seen.
- Bug report: `!` key opens dialog showing error history, action log, system diagnostics; `g` opens GitHub issue with pre-filled markdown via `gh issue create --web`
- Error history: ring buffer (max 50) of errors that flash for 5s — persists for bug reporting
- Action log: ring buffer (max 100) of user actions (attach, delete, restart, editor, approve, etc.) for "steps to reproduce"
- Diagnostics: app version, macOS version, tmux/claude/codex/gh versions, config, last 100 lines of debug.log; home dir sanitized to `~`
- Auto-naming: sessions titled from Claude's own session title, read from the conversation JSONL (`session.ReadClaudeSessionName`); falls back to fleet's prompt heuristic (filler stripping, word-boundary truncation) only when Claude has written no title yet
- Title precedence: manual R-key rename (`ManuallyRenamed`) > Claude `custom-title` (`/rename` in-session) > Claude `ai-title` (auto, model-generated, evolves with the work) > fleet prompt heuristic
- Auto-naming pipeline: worker cycle re-reads the JSONL each cycle until a Claude title appears (prompt pickup within ~2s), then ~every 30s to follow `ai-title`/`custom-title` drift; heuristic fallback uses UserPromptSubmit hook → status file → HookWatcher → Session.FirstPrompt → naming.GenerateTitle
- Manual rename (R key) sets ManuallyRenamed flag, prevents auto-rename
- Chrome tab control: `p` opens PR in Chrome via extension (reuses existing tab), falls back to `open <url>` if unavailable
- Chrome extension architecture: TUI →[unix socket]→ native host (`fleet chrome-host`) →[stdio]→ Chrome service worker
- Native messaging host: `fleet chrome-host` subcommand (also auto-detected when Chrome passes `chrome-extension://...` arg)
- Unix socket: `~/.config/fleet/chrome.sock` (created by native host, mode 0600)
- NMH manifest: auto-installed to `~/Library/Application Support/Google/Chrome/NativeMessagingHosts/com.brizzai.fleet.tabcontrol.json` on TUI startup
- Chrome extension ID: `haphpcoecelhofejcklinnlbfijgdnih` (stable via `key` in manifest.json)
- Extension commands: `open_or_focus`, `close_tab`, `create_tab_group`, `ping`
- Service worker reconnects to native host on disconnect (2s delay)
- Claude Code + OpenAI Codex + OpenCode, Mac only
