---
type: fixed
---

**Status stops going dark.** Every session could lose status detection at once, silently, if the `fleet` binary that installed the hooks was later deleted or upgraded away — the sidebar kept showing a status, just a guessed one. fleet now repairs the hooks within a minute and names the sessions to restart.
