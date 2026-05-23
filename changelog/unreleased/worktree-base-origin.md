---
type: changed
---
The "new worktree" dialog now pre-fills the base branch as `origin/<default>` (e.g. `origin/master`) instead of the local branch, so new worktrees start from the remote tip rather than a possibly-stale local ref. Falls back to local `main`/`master` for repos without an `origin` remote.
