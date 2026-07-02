---
type: fixed
---
Fewer periodic UI stutters — the per-repo status refresh now spawns 3 git subprocesses instead of 5, easing fork/exec-lock contention that occasionally stalled the preview by ~0.5s.
