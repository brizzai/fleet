---
type: added
---

**Choose where worktrees live** — new worktrees no longer have to be siblings like `myrepo-feature`. Set `Worktree location` in Settings → Behavior (or `worktree_dir` in config, or per-repo `.fleet.json` `workspace.dir`) to a path template like `{{parent}}/{{repo}}.worktrees/{{name}}` to group them under a tidy `myrepo.worktrees/` folder. Placeholders: `{{parent}}`, `{{repo}}`, `{{name}}`, and `~`.
