---
type: fixed
---
**Attach works inside tmux.** If you run fleet from within a tmux session, `⏎` now attaches instead of doing nothing — fleet was passing `$TMUX` through to `tmux attach-session`, which refuses a nested attach and printed its error somewhere you could never see it.
