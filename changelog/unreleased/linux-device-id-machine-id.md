---
type: changed
---
**Linux device ID uses your machine ID.** On Linux, fleet's anonymous telemetry ID now comes from a hash of your machine ID (never the ID itself) instead of your hostname, which anyone who could guess it could reproduce. Installs switch on their next launch; a machine with no machine ID (some containers) keeps its current ID.
