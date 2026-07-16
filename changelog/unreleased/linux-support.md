---
type: added
---
**Native Linux support.** fleet now runs on Linux — install via `.deb`/`.rpm` or Docker. Worktree cleanup, copy-to-clipboard (Wayland and X11, with an OSC 52 fallback), idle-session suspend, and tmux 3.2+ compatibility are all handled natively, and the worktree-delete dialog shows tidy process names (`vite`, not a full path). An optional systemd user unit keeps the tmux server alive across logins.
