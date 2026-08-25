---
type: added
---

**Prompt `fleet add`** — `fleet add [path] -p "<task>"` now starts the session already working on it, the same as `fleet worktree -p`. `-p -` reads stdin, `--agent` and `--account` pick the agent and Claude subscription, and the path defaults to the current directory.
