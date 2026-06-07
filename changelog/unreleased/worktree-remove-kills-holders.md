---
type: fixed
---
Removing a worktree now stops the leftover dev processes still holding it open (sparing your editor, language servers, and shells), so it no longer fails with "Directory not empty". The confirm dialog lists what will be terminated, and if removal still fails the worktree is kept and flagged to retry with `d`.
