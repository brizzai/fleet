---
type: fixed
---
**Shift+Enter inserts a newline.** In a fleet session `Shift+Enter` now breaks the line instead of submitting your message, on terminals that report modified keys the xterm way — iTerm2 works as-is, kitty needs `map shift+enter send_text all \x1b[13;2u` in `kitty.conf`. Skipped on tmux 3.5 and 3.6.x, where turning the feature on trips an upstream key-parsing bug; `FLEET_NO_EXTENDED_KEYS` opts out everywhere else.
