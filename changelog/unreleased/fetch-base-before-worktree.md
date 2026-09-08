---
type: improved
---

**Fresh base branches** — creating a worktree now refreshes `origin/<base>` first, so a new branch starts at the tip the remote actually has instead of whatever this clone last fetched, and doesn't need a merge as its first act. The round trip hides behind the `Creating…` spinner; offline it gives up after about 5s and branches from the refs you already have.
