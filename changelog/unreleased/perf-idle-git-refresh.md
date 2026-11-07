---
type: improved
---
**Much lower idle CPU.** fleet no longer runs `git status` in every repo every 2 seconds: repos with a running agent still refresh every 2s, idle ones every 30s, and ones holding only suspended sessions every 5 minutes. On a fleet of large worktrees that's most of a CPU core back.
