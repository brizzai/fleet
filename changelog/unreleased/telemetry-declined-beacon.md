---
type: added
---

When you decline the first-launch analytics prompt, fleet now sends a single anonymous `telemetry_declined` event so opt-out rates are visible alongside opt-ins. It carries only the anonymous device hash fleet already generates — never your git name/email, file paths, repo/branch names, or prompts — and fires exactly once per install. It is fully suppressed when telemetry is disabled via `FLEET_TELEMETRY_DISABLED` or `DO_NOT_TRACK`.
