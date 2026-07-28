---
type: fixed
---

**No more idle flicker.** Sessions whose activity line showed no token counter (`· Improvising… (51s)`) could read idle for a single frame and snap back, so a working session flashed the wrong dot. You now get a steady status for the whole turn.

**Compaction reads as running.** A session part-way through `/compact` no longer sits at idle waiting on a hook — the pane detects it directly.
