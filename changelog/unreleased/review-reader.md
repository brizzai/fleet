---
type: added
highlight: true
---

**Read and answer pull requests in fleet.** `c` lists the reviews waiting on you and `⏎` opens a full-screen diff reader — syntax colours, word-level highlighting, side-by-side on wide terminals, `space` to walk file by file, `c` to comment on the line you're looking at, and `S` to post the batch to GitHub as one review: comment, approve or request changes. Three tabs: `1` the tour, `2` the diff, `3` the agent working on the review — its live pane, without leaving the reader. The tour asks Claude for a route through the change — ordered steps, each pointing at the code it's about — because a 34-file PR sorted by path tells you nothing about where to start. What you've read and what you've written survive a restart, so closing fleet mid-review no longer means abandoning it. `?` lists every key.
