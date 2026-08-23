---
type: fixed
---

**Scrolling no longer pegs the CPU.** fleet put your terminal into mouse-reporting mode but never handled a mouse event, so a trackpad scroll over the TUI fired ~300 ignored redraws a second and held fleet at 220% CPU until you stopped. Drag-select now works without holding a modifier.
