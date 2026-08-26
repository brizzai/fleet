---
type: fixed
---

**Status survives a deleted binary.** Hooks pointed at the exact `fleet` binary that installed them, so removing that git worktree — or upgrading past that Homebrew version — silently killed status detection for every session while screen scraping quietly stood in. fleet now re-points them within a minute and names the sessions you need to restart.
