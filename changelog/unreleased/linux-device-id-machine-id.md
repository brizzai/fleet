---
type: changed
---
**Linux device ID stops using your hostname.** The anonymous device ID fleet sends with telemetry on Linux now comes from a fleet-keyed hash of `/etc/machine-id` instead of your hostname, which anyone who could guess the hostname could reproduce. Existing Linux installs switch over once, on their next launch.
