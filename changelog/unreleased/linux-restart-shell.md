---
type: fixed
---
**Restarted sessions stop reading as exited on Linux.** After an in-place restart on Debian or Ubuntu, `fleet status` no longer calls a running agent "exited", and `fleet send` no longer refuses it as a shell.
