---
type: fixed
---
Restarting a dead session no longer briefly flashes back to `error` (with a misleading crash dump) before the new Claude process fires its first hook. Restart now clears the previous Claude's hook state — both in memory and the on-disk status file — so the worker doesn't trust an 8-minute-old `status=dead` during the relaunch window. The crash-dump quota also re-arms when a fresh hook reports the session is alive again, so a real death following a false-positive flash still produces a dump.
