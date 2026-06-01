---
type: changed
---
`Space` (jump to next waiting/finished) now cycles in on-screen order and skips sessions inside a **collapsed origin** — fold an origin to mute its sessions from the jump cycle. A collapsed branch/checkout under an *expanded* origin is still reached: jump expands just that checkout to reveal the target. (This also fixes jump sometimes only moving in one direction.) Jumping to a slot-bound session with the digit keys still reveals it, expanding its origin group and checkout if folded. `Space` is now labelled "Jump" in the session footer.
