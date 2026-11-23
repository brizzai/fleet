---
type: improved
---
**Linear works without per-repo setup.** Once you connect Linear, ticket suggestions and lookups are on in every repo for the teams you're a member of, so you no longer need a `.fleet.local.json` line before anything happens. A repo can still name its own team, or opt out with `{"linear": {"enabled": false}}`.

**Tickets from My tickets always attach.** Picking a ticket in `t` now names the branch and hands the ticket to the agent even in a repo that tracks a different team, instead of quietly creating a plain branch.
