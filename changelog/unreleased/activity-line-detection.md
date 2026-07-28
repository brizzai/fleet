---
type: fixed
---

**Busy sessions stop flickering to idle.** Status detection missed activity lines without a token counter (`✽ Improvising… (51s · thinking with xhigh effort)`), so a working session could read idle for a frame and flip back. Mid-`/compact` sessions now read running too.
