---
type: fixed
---

**No more half-second freezes** — The sidebar no longer locks up while you scroll or navigate. Reading a session's title used to re-scan its whole Claude transcript every 30s — up to 89MB, now ~15µs — which stalled the background worker and left arrow keys forking `tmux` on the UI thread.
