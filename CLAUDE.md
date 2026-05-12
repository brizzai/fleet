# fleet

> This file provides context for AI-assisted development with Claude Code.

TUI tool for managing multiple Claude Code sessions in parallel using tmux.

## Tech Stack
- Go 1.26+, Bubble Tea + Lipgloss, tmux, SQLite (WAL mode)

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
internal/session/storage.go  # SQLite persistence (sessions + claude_session_id)
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
internal/ui/settings.go      # Settings dialog (S key)
internal/ui/bugreport.go     # Bug report dialog (! key) with diagnostics, error history, action log
internal/ui/actionlog.go     # Ring buffer tracking user actions (steps to reproduce)
internal/ui/errors.go        # Ring buffer keeping error history (errors that flash and vanish)
internal/ui/keybindings.go   # Centralized keybinding definitions
internal/ui/workspace_picker.go  # Worktree dialog (base branch + new branch + existing worktrees)
internal/ui/workspace_create.go  # Create workspace sub-dialog + PendingWorkspace phantom entries
internal/ui/command_palette.go   # Command palette dialog (: / Ctrl+P) with fuzzy search
internal/chrome/protocol.go      # Command/Response types, action constants, socket path
internal/chrome/native_host.go   # Native messaging host with Unix socket bridge
internal/chrome/client.go        # TUI-side client (connects to socket, sends commands)
internal/chrome/install.go       # NMH manifest auto-install to Chrome's NativeMessagingHosts dir
chrome-extension/                # Chrome MV3 extension (service worker, manifest, icons)
```

## Conventions
- Tmux session prefix: `fleet_`
- Session ID format: `<8hex>-<unix_timestamp>`
- SQLite DB: `~/.config/fleet/state.db`
- Sessions grouped by git repo root in sidebar with tree lines (├─/└─)
- Status: Running, Waiting, Finished, Idle, Error, Starting
- Status icons: ● (running/finished), ◐ (waiting), ○ (idle/starting), ✕ (error)
- Keybindings: j/k nav, Enter attach, Space jump to next waiting/finished, a new session (instant, repo-scoped), n new session (any repo, path autocomplete), w new worktree session (base branch + new branch), d delete (Y to also destroy workspace, D to also remove repo), z undo delete (5s window), r restart, R rename, e editor, p open PR in browser, Y quick approve (waiting sessions), / filter, : or Ctrl+P command palette, S settings, ! bug report/diagnostics, ? help, q quit
- Session hotkeys (RTS-style): `Alt+0-9` (or `=` then digit) binds the selected session to a slot; re-pressing `Alt+<N>` on a session already in slot N unbinds; `==` then digit clears any slot; plain `0-9` jumps to the bound session (double-tap within 400ms also attaches); `[N]` badge in sidebar marks bound sessions; bindings persist in SQLite `slot_bindings` table (FK cascade on session delete)
- Command palette (: / Ctrl+P): fuzzy-searchable list of all actions; palette-only commands include "Reload All Sessions" (restarts all dead/error sessions)
- Undo delete: `z` key restores last deleted session within 5s window (stacked — multiple deletes each undoable). Tmux kept alive during window for full restore.
- Pinned repos: repos auto-pinned on session creation, persist in SQLite. Empty repos show dimmed with "(empty)". `d` on empty repo header unpins it. `D` in delete dialog also removes repo.
- Tmux status bar configured per session with detach hint (ctrl+q)
- Attach uses PTY with Ctrl+Q intercept for clean detach (creack/pty + golang.org/x/term)
- Repo headers show branch name (), dirty indicator (*), and PR badge (#N)
- Git info refreshes every 2s (branch/dirty), PR info every 60s via `gh` CLI
- PR badge: green ✓ (approved+CI passed), yellow (pending), red ✕ (CI fail) / ↩ (changes requested or unresolved threads), purple ⇡ (merged), hidden (closed)
- PR info includes unresolved review thread count via GitHub GraphQL API
- `gh` CLI optional — PR info hidden if not installed
- Preview strips OSC-8 hyperlink sequences to prevent dotted underline artifacts
- Status detection: hook-based (primary, no time expiry) via Claude Code hooks + pane capture (fallback, ANSI-stripped)
- Agent team status: sub-agents don't fire hooks, so pane detection handles team states via structural checks (numbered menu `❯ 1.`+`2.`+`Esc to cancel`, box-drawing `│`+`Waiting for team lead`); hook=running is never overridden to waiting by pane (avoids false-positives from code in scrollback)
- All blocking I/O (tmux, git, gh) runs in background worker goroutine, never in Bubble Tea Update()
- Hook status files: `~/.config/fleet/hooks/{session_id}.json`
- Hook handler: `fleet hook-handler` (invoked by Claude Code hooks, reads FLEET_INSTANCE_ID env)
- Hooks auto-installed into `~/.claude/settings.json` on TUI launch
- Debug log: `~/.config/fleet/debug.log` (slog, init in TUI and hook-handler)
- Config file: `~/.config/fleet/config.json` (tick_interval_sec, default_project_path, editor, theme, auto_name_sessions, copy_claude_settings)
- Workspace: built-in git worktree support (zero config), per-repo `.fleet.json` (or legacy `.bc.json`) overrides with custom shell commands
- Workspace creation is non-blocking: dialog closes immediately, phantom "Creating..." entry with spinner appears in sidebar, user can keep navigating
- Worktree creation copies `.claude/settings.local.json` from source repo (configurable via `copy_claude_settings`, default true)
- `.fleet.json` / `.fleet.local.json` in repo root (legacy `.bc.json` / `.bc.local.json` still read): `{"workspace": {"list": "cmd", "create": "cmd {{name}} {{branch}}", "destroy": "cmd {{name}}"}}`
- `.fleet.json` / `.fleet.local.json` may also set `{"pr_checks": {"ignore": ["glob", ...]}}` to drop matching CI checks from the PR-badge rollup (path.Match globs; lists from both files merge additively; opt-in, empty by default)
- Claude session resume: captures Claude session_id from hooks, uses `claude --resume <id>` on restart
- Editor: config.editor > $EDITOR > "code" (VS Code)
- Themes: tokyo-night (default), catppuccin-mocha, rose-pine, nord, gruvbox — configurable via settings (S key)
- Settings dialog: S key opens settings overlay, live theme preview, auto-name toggle, copy .claude toggle, auto-saves on close
- Bug report: `!` key opens dialog showing error history, action log, system diagnostics; `g` opens GitHub issue with pre-filled markdown via `gh issue create --web`
- Error history: ring buffer (max 50) of errors that flash for 5s — persists for bug reporting
- Action log: ring buffer (max 100) of user actions (attach, delete, restart, editor, approve, etc.) for "steps to reproduce"
- Diagnostics: app version, macOS version, tmux/claude/gh versions, config, last 100 lines of debug.log; home dir sanitized to `~`
- Auto-naming: sessions auto-titled from user prompt via smart heuristic (filler stripping, word-boundary truncation)
- Auto-naming pipeline: UserPromptSubmit hook → status file → HookWatcher → Session.FirstPrompt → worker cycle → naming.GenerateTitle
- Retitle: after 3 prompts, title regenerated from latest prompt (better reflects session scope)
- Manual rename (R key) sets ManuallyRenamed flag, prevents auto-rename
- Chrome tab control: `p` opens PR in Chrome via extension (reuses existing tab), falls back to `open <url>` if unavailable
- Chrome extension architecture: TUI →[unix socket]→ native host (`fleet chrome-host`) →[stdio]→ Chrome service worker
- Native messaging host: `fleet chrome-host` subcommand (also auto-detected when Chrome passes `chrome-extension://...` arg)
- Unix socket: `~/.config/fleet/chrome.sock` (created by native host, mode 0600)
- NMH manifest: auto-installed to `~/Library/Application Support/Google/Chrome/NativeMessagingHosts/com.brizzai.fleet.tabcontrol.json` on TUI startup
- Chrome extension ID: `haphpcoecelhofejcklinnlbfijgdnih` (stable via `key` in manifest.json)
- Extension commands: `open_or_focus`, `close_tab`, `create_tab_group`, `ping`
- Service worker reconnects to native host on disconnect (2s delay)
- Claude Code only, Mac only
