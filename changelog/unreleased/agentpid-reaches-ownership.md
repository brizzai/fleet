---
type: fixed
---

**Stuck sessions recover.** The v2.24.0 fix for this never took effect — the check that decides whether a session's old conversation has ended was never handed the process id it needs, so a frozen dot stayed frozen. Now wired through.
