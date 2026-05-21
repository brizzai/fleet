---
type: changed
---
Delete is now scoped to whatever the cursor is on, so the confirm dialog is a single `y`/`n` (the old `Y +Workspace` / `D +Remove Repo` buttons are gone). `d` on a session deletes just that session; `d` on a worktree header deletes its sessions and runs `git worktree remove`; `d` on a repo header "forgets" the repo from fleet (deletes its sessions + unpins, the folder is left untouched); `d` on an empty repo header still unpins instantly. Header deletes route through the same 5s deferred-delete machinery, so `z` undo keeps working.
