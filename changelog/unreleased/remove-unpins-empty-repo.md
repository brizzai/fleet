---
type: fixed
---
**No more phantom repos** — `fleet remove` now unpins a repo when it takes the last session in it, so a create/remove loop stops leaving empty headers for worktrees that are already gone from disk.
