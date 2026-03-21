# Shell Sessions

Open a plain terminal in any session's working directory without leaving brizz-code.

## Usage

1. Select a session in the sidebar
2. Press **`x`** to open a shell in that session's `ProjectPath`
3. Use the terminal normally (full shell with colors, aliases, environment)
4. Press **Ctrl+Q** to detach back to the TUI

The shell session appears in the sidebar like any other session, titled `shell: <dirname>`.

## Lifecycle

Shell sessions are first-class sessions:

| Action | Key | Behavior |
|--------|-----|----------|
| Open | `x` | Spawns shell in selected session's directory |
| Attach | `Enter` | Re-attach to an existing shell session |
| Restart | `r` | Kill and recreate the shell session |
| Delete | `d` | Kill the tmux session and remove from sidebar |
| Detach | `Ctrl+Q` | Return to TUI, shell stays alive |

Sessions are persisted in SQLite and survive app restarts.

## Status Indicators

Shell sessions show real-time status in the sidebar:

| Icon | Status | Meaning |
|------|--------|---------|
| `●` (green) | Running | A foreground command is actively executing |
| `○` (dim) | Idle | At shell prompt, last command succeeded |
| `✕` (red) | Error | At shell prompt, last command had non-zero exit code |

### How detection works

**Running vs idle** is detected by comparing tmux's `pane_current_command` against the user's `$SHELL`. When the foreground process differs from the shell (e.g., `make` running in a `zsh` session), the session shows as running. Polled every 500ms via the preview tick.

**Exit code tracking** uses a `precmd` (zsh) / `PROMPT_COMMAND` (bash) hook injected when the shell session starts. After each command completes, the hook writes `{"exit_code": N}` to `~/.config/brizz-code/hooks/<session_id>_exit.json`. The status poller reads this file to detect non-zero exit codes.

## Implementation

### Key files

| File | Role |
|------|------|
| `internal/ui/app.go` | `x` key handler, `shellStatusDoneMsg`, preview tick polling |
| `internal/session/session.go` | `Command` field, `IsShellSession()`, `updateShellStatus()` |
| `internal/session/storage.go` | `command` column in SQLite |
| `internal/tmux/tmux.go` | `PaneCurrentCommand()`, `SetupShellExitHook()` |
| `internal/ui/keybindings.go` | `x` keybinding entry |

### Session model

The `Session` struct has a `Command` field. When non-empty, the session is a shell session:

- `Start()` opens the user's shell without sending a command (vs Claude sessions which send `claude`)
- `Restart()` and `RespawnClaude()` respect the `Command` field
- `UpdateStatus()` delegates to `updateShellStatus()` for shell sessions

### Status polling

Shell sessions bypass the hook-based status detection used by Claude sessions. Instead, `updateShellSessionStatuses()` runs every 500ms (piggybacked on the preview tick) and checks `PaneCurrentCommand()` for each live shell session.
