---
type: fixed
---
Worktree creation now works in git-crypt repos. git-crypt resolves its key via the per-worktree git dir, which has no key, so the smudge filter aborted checkout with `git-crypt: Error: Unable to open key file`. fleet now detects git-crypt repos and creates the worktree with `--no-checkout`, links the shared key into the worktree's git dir, then checks out.
