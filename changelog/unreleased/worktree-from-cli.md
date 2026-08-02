---
type: added
---

**Worktrees from your shell.** `fleet worktree <branch>` creates the worktree and starts a session in it without opening the TUI — `--base`, `--path` and `--agent` to steer it, `--no-session` to print just the path for `cd "$(fleet worktree foo --no-session)"`.
