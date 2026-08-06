---
type: added
---
**Message a session from the shell.** `fleet send fix-242 "now run the tests and push"` types into a running session without attaching — name it by title, branch, id or `@3` slot. It refuses when the agent is sitting on a permission prompt, where the message would be swallowed and its Enter would approve whatever was highlighted.
