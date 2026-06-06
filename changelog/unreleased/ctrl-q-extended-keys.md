---
type: fixed
---

Ctrl+Q detach now works when the host tmux config enables `extended-keys` (common in oh-my-tmux / iTerm2 setups). Previously the terminal encoded Ctrl+Q as `CSI 113;5 u` or `CSI 27;5;113 ~` instead of byte 17, so the interceptor missed it and the keystroke was forwarded to the pane.
