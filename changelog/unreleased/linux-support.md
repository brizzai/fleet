---
type: added
---
fleet now runs natively on **Linux**: worktree cleanup reads `/proc` instead of macOS `lsof`, copy-mode selections route through `wl-copy`/`xclip`/`xsel` (OSC 52 fallback), and tmux 3.2 servers no longer lose their session options. Ships with a `Dockerfile`, `.deb`/`.rpm` packaging, and an optional systemd user unit.
