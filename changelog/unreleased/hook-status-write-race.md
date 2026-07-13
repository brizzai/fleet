---
type: fixed
---
**No more dropped status updates.** Two hooks firing at once (Codex asks permission per concurrent tool call) collided on a shared temp file, losing one update with a cryptic `rename … no such file or directory`. Each writer now gets its own.
