---
type: fixed
---

**Statuses no longer freeze while attached.** Every session's status stopped updating for as long as you were inside a session and only caught up on detach — one measured freeze lasted 16 minutes, so `Space` came back with nothing waiting. Status detection now runs on its own goroutine and keeps working throughout.
