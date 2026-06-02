---
type: fixed
---

Sessions that finish while a background agent is still running no longer get stuck showing **Waiting**. A long-running sub-agent kept redrawing the pane, which an internal guard misread as "still working" and used to hold the prior status indefinitely; the guard now releases once the finishing hook is no longer fresh.
