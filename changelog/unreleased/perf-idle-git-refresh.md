---
type: improved
---
**Idle fleet, idle CPU.** fleet now checks git every 2s only in repos with an active agent, instead of running `git status` in every repo every 2s — on large worktrees that's ~80% of a CPU core back while you're not doing anything.
