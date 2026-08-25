---
type: fixed
---

**`fleet add` no longer strands an upgrade** — running it before any other command could leave your old `brizz-code` sessions and settings unreachable. It now migrates them first, and keeps its log lines in `debug.log` instead of printing them over your terminal.
