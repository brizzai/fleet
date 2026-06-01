---
type: improved
---

Sidebar redesigned around a calmer origin → checkout → session tree. Worktrees of the same GitHub repo collapse under one origin header (e.g. `brizzai/fleet`); no-remote repos get their own `local:<name>` group. `z` folds/unfolds idle sessions for a checkout; `u` is the new undo-delete (was `z`).

Boot is now gated by a one-shot bootstrap that resolves every repo's origin + branch + PR status in parallel (8 workers, 6s deadline) — the sidebar paints once in its final shape instead of regrouping as data trickles in. While bootstrap runs, fleet shows a gradient FLEET wordmark splash with rotating ops-humor labels and a progress bar.

Steady-state git/PR refresh now fans out across all session repos every 2s (bounded 4-worker pool) instead of round-robining one repo per tick, so branch/dirty/PR badges feel near-instant after any change.
