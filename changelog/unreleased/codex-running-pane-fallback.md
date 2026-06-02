---
type: fixed
---
Codex sessions no longer get stuck showing **idle** while actively working. Codex status is hook-driven, but when its hooks don't fire (e.g. hook trust lapses), the session had no way back to running. fleet now falls back to the pane: an in-progress Codex turn (`Working … esc to interrupt`) is detected as running, mirroring the existing waiting-prompt detection.
