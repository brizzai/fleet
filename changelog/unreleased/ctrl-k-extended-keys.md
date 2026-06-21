---
type: fixed
---
The Ctrl+K command palette now opens in terminals and tmux configs (e.g. gpakosz/.tmux) that enable extended/CSI-u key reporting, which previously encoded Ctrl+K in a form fleet couldn't read so the keystroke did nothing.
