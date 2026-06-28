---
type: fixed
---
Word-wise line editing in the focus-preview pane: Option/Alt+Backspace, Alt+B/F/D word motions, Alt+arrows, Ctrl+arrows and Shift+Tab are now forwarded to the focused session with their modifier intact instead of degrading to an unmodified keypress (e.g. Option+Backspace had been deleting one character instead of a word, and Ctrl+Left was being typed as the literal text `ctrl+left`).
