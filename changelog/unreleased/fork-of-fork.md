---
type: fixed
---
Forking a session that was itself forked now correctly forks that session, not its original ancestor — a forked session reliably adopts its own conversation id once it diverges.
