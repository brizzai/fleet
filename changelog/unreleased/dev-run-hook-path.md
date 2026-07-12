---
type: fixed
---

**Hooks survive running from source.** Launching fleet with `go run` baked a throwaway build path into `~/.claude/settings.json`, and Go deletes that binary on exit — so every hook afterward failed with "No such file or directory" and session status silently stopped updating. `make run` now execs a real build, and fleet won't write a `go run` path into your hooks even if you launch one by hand.
