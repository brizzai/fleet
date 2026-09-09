---
type: fixed
---

**Transcripts keep saving.** A tmux server first started from inside a Claude Code session hands every pane a `CLAUDE_CODE_CHILD_SESSION` marker, and sessions that inherited it silently stopped writing a transcript — taking `r` restart, `F` fork, idle-suspend wake and agent auto-naming with them, with nothing on screen saying why. fleet now clears the marker on every launch.
