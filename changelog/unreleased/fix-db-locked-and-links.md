---
type: fixed
---
**No more "database is locked".** Background writes no longer fail with `SQLITE_BUSY`, even with the CLI running alongside the TUI. Links agents print are now Cmd+clickable through fleet's tmux, including inside zellij, and a dead session no longer writes a new crash dump on every launch.
