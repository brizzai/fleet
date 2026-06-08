---
type: fixed
---
Session statuses no longer freeze across the whole app when a `git` or `gh` call hangs — those calls now time out (8s/15s) instead of wedging the background status worker indefinitely.
