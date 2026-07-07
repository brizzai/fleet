# Architecture

This document explains how fleet works internally, for contributors.

## Overview

fleet is a Go TUI that orchestrates multiple Claude Code and Codex sessions running in tmux (agent chosen per session). The core challenge is **status detection** — knowing what each session is doing without interfering with it. Both agents expose hooks with matching payloads, so the detection pipeline is shared; Codex adds a narrow pane check for its hook-less wait prompts.

```text
┌──────────────────────────────────────────────────┐
│  Bubble Tea TUI (ui/app.go)                      │
│  ┌────────────┐  ┌─────────────┐                 │
│  │  Sidebar    │  │  Preview    │  ← render only  │
│  └────────────┘  └─────────────┘                 │
│         ▲                                        │
│         │ tea.Msg (async)                        │
│  ┌──────┴───────────────────────────┐            │
│  │  Worker Goroutine (~2s cycle)    │            │
│  │  • Sync hook status              │            │
│  │  • Round-robin pane captures     │            │
│  │  • Git/PR refresh                │            │
│  │  • Auto-naming                   │            │
│  └──────┬───────────────────────────┘            │
└─────────┼────────────────────────────────────────┘
          │
    ┌─────┴──────┐     ┌──────────────┐     ┌────────────┐
    │   tmux     │     │ Hook Watcher │     │  SQLite    │
    │  sessions  │     │  (fsnotify)  │     │  (WAL)     │
    └────────────┘     └──────┬───────┘     └────────────┘
                              │
                    ┌─────────┴─────────┐
                    │  Status Files     │
                    │  ~/.config/fleet/ │
                    │  hooks/*.json     │
                    └─────────┬─────────┘
                              │
                    ┌─────────┴─────────┐
                    │  hook-handler     │
                    │  (subprocess)     │
                    └─────────┬─────────┘
                              │
                    ┌─────────┴─────────┐
                    │  Claude Code      │
                    │  (hooks API)      │
                    └───────────────────┘
```

**Key principle:** All blocking I/O runs in the worker goroutine, never in the Bubble Tea UI thread.

## Package Structure

```text
cmd/fleet/
  main.go              CLI entry point, command routing
  hook_handler.go      Subprocess invoked by Claude Code hooks

internal/
  session/
    session.go         Session model + status detection (the core logic)
    storage.go         SQLite persistence (WAL mode)
  tmux/
    tmux.go            Tmux abstraction (create, kill, capture)
    pty.go             PTY-based attach with Ctrl+Q detach
  hooks/
    claude_hooks.go    Hook injection into ~/.claude/settings.json
    hook_watcher.go    Watches status files via fsnotify
  ui/
    app.go             Bubble Tea model, worker cycle, message routing
    sidebar.go         Sidebar rendering (repo groups, status icons)
    preview.go         Preview pane (session output)
    keybindings.go     Centralized key definitions
    palette.go         Theme palettes (5 built-in)
    workspace_*.go     Worktree dialogs
  git/                 Branch, dirty, worktree operations
  github/              PR info via gh CLI + GraphQL
  workspace/           Provider interface (git worktree / custom shell)
  config/              JSON config (~/.config/fleet/config.json)
  naming/              Auto-title from user prompt (heuristic, no LLM)
  chrome/              Chrome extension bridge (native messaging host)
```

## Status Detection

This is the most complex part of the codebase. Three layers, in priority order:

### Layer 1: Hooks (primary, authoritative)

Claude Code has a hooks API. On TUI launch, `hooks.InjectClaudeHooks()` adds entries to `~/.claude/settings.json` that call `fleet hook-handler` on events like `UserPromptSubmit`, `PermissionRequest`, `Stop`.

```text
Claude fires event
  → forks `fleet hook-handler` with JSON on stdin
  → handler reads FLEET_INSTANCE_ID env var
  → writes status file: ~/.config/fleet/hooks/{session_id}.json
```

### Layer 2: Hook Watcher (in-memory cache)

`hook_watcher.go` uses fsnotify to watch the hooks directory. On file change (debounced 100ms), it parses the JSON and updates a thread-safe in-memory map. The worker goroutine reads this map — no disk I/O in the hot path.

### Layer 3: Pane Capture (fallback)

If no hook data exists for a session, `UpdateStatus()` falls back to capturing the tmux pane output and pattern-matching:
- **Running:** spinner characters, "ctrl+c to interrupt"
- **Waiting:** permission prompts, "(Y/n)"
- **Finished:** prompt indicator ("❯")

**Critical rule:** Hooks are authoritative for "waiting" — pane detection never overrides it. This prevents false positives from menu selectors like "❯ 1. Yes, allow once".

**Background agents:** a session that delegates to `run_in_background` agents fires its lead `Stop` hook and would read idle, so pane capture also detects the background-agents dock and reports `running`. Why this is pane-scraped rather than read from a "more first-party" source — and the signal-durability analysis behind it — is a decision record in [background-agent-detection.md](./background-agent-detection.md).

## Worker Cycle

`statusWorkerCycle()` in `app.go` runs every ~2s in a background goroutine:

1. **Refresh tmux cache** — single `tmux list-windows -a` call for all sessions
2. **Sync hook status** — read from HookWatcher's in-memory map (fast)
3. **Auto-name one session** — `naming.GenerateTitle()` from first prompt
4. **Round-robin status updates** — 5 sessions per cycle via `UpdateStatus()`
5. **Git info refresh** — one repo per cycle (branch, dirty, PR badge)
6. **Send UI message** — triggers Bubble Tea re-render

Round-robin spreading prevents capture timeouts when managing many sessions.

## Concurrency Model

| Thread | Responsibility | Lock |
|--------|---------------|------|
| **Bubble Tea** | Keyboard input, rendering | `workerMu` for session reads |
| **Worker goroutine** | All blocking I/O (tmux, git, gh) | `workerMu` for session writes |
| **HookWatcher** | fsnotify → in-memory status map | Own `sync.RWMutex` |
| **PTY attach** | Raw terminal during session attach | None (exclusive terminal access) |

## Session Lifecycle

```text
CREATE:  User presses 'a'/'n' → dialog → tmux.NewSession() → set FLEET_INSTANCE_ID
         → send claude command → storage.SaveSession()

RUNNING: Hook events → status files → HookWatcher → worker syncs → UI renders

ATTACH:  Enter → fork PTY → raw terminal mode → Ctrl+Q detaches cleanly

APPROVE: Y → tmux send-keys "y" + Enter (works for both Y/n and menu prompts)

RESTART: r → kill tmux → respawn with claude --resume <saved_session_id>

DELETE:  d → storage.Delete() → tmux kill → optionally workspace.Destroy()
```

## Workspace System

Two providers behind the `workspace.Provider` interface:

- **GitWorktreeProvider** (default) — `git worktree add/remove`, zero config
- **ShellProvider** — custom commands from `.fleet.json` (or legacy `.bc.json`) in repo root

Workspace creation is **non-blocking**: dialog closes immediately, a phantom "Creating..." entry appears in the sidebar with a spinner, replaced by the real session on completion.

## Chrome Extension

Optional. For `p` key to reuse Chrome tabs instead of opening new ones.

```text
TUI → unix socket → native messaging host (chrome-host) → stdio → Chrome service worker
```

Falls back to `open <url>` if the extension isn't installed.
