---
type: fixed
---

Sessions now flip from **Waiting → Running** within ~500ms after you approve a permission, instead of lagging several seconds (or tens of seconds) when many sessions are open. No Claude hook fires on permission approval, so that transition can only be seen by scanning the session's pane — and the background worker only scanned 5 sessions every 2s, so at ~40 sessions each one was revisited just once every ~18s. The worker now re-checks active sessions (running/waiting/starting) on a fast ~500ms cadence while keeping the heavier work (git/PR refresh, idle-session sweep, auto-naming, tmux status bars) on the existing ~2s cadence. The reverse `running → waiting` flip (e.g. a sub-agent hitting a permission prompt) is just as responsive.
