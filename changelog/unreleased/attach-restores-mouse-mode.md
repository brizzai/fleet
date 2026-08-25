---
type: fixed
---

**Scrolling stays fast after you detach.** Attaching to a session left your terminal reporting mouse events — `Ctrl+Q` kills the tmux client before tmux can clean up — so the next scroll brought back the ~270 redraws/sec that pinned fleet at 250% CPU. Detaching now puts the terminal back.
