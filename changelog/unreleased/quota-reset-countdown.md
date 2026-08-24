---
type: fixed
---

**Reset countdown survives the reset.** A spent account used to lose its countdown at the exact moment it hit zero, leaving a bare red `100%`. It now reads `100%(now)`, and the quota poll refreshes within a tick instead of waiting out its 3-minute throttle.
