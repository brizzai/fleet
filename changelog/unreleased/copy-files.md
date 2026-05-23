---
type: added
---

Per-repo `copy_files.paths` config in `.fleet.json` / `.fleet.local.json` copies declared gitignored files/dirs/globs from the source repo into each new worktree. Opt-in; additive merge across both files; applies to both git-worktree and shell providers.
