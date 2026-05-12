---
type: added
---
Crash dumps for dying sessions: when a session transitions to `error`, fleet writes a forensic snapshot to `~/.config/fleet/crashes/<id>_<ts>.txt` containing the tmux exit status/signal (with human-readable annotation — e.g. `9 (SIGKILL — likely OOM/Jetsam or external kill)`), last 200 lines of pane content (raw ANSI), the SessionEnd hook reason from Claude Code, and the last 6 perfwatch heartbeats — enough to tell a kernel kill from a panic from a clean exit at a glance. Tmux now uses `remain-on-exit on` so dead panes can be inspected before fleet cleans them up. Perfwatch heartbeats also gained a `sys_free_mb` field (system-wide free memory) so the heartbeat trail records memory-pressure collapse leading up to a crash.
