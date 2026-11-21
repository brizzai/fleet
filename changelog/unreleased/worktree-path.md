---
type: added
---
**Choose where worktrees go.** You can now pick where worktrees go in Settings (`S` → Behavior → Worktree location), or set `worktree_path` in `config.json` (or a repo's `.fleet.local.json`) to keep worktrees in one place: `"../{{repo}}.worktrees"` puts them in `fleet.worktrees/<branch>` instead of scattering `fleet-<branch>` folders next to your repo.
