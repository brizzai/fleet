---
type: added
---
**Start a worktree already working.** `fleet wt fix-242 -p "fix the flaky worker test"` creates the worktree and opens the agent on that first message. `-p -` reads stdin, so `gh issue view 242 | fleet wt fix-242 -p -` hands the agent the whole issue.
