---
type: added
---

**Pick the model per session** — `fleet add` and `fleet worktree` now take `--model` and `--effort`, so a session can start on `opus` at `xhigh` without touching your defaults. `--effort` is Claude and Codex only; OpenCode has no equivalent flag, so fleet tells you instead of dropping it silently.
