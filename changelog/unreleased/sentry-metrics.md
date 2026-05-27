---
type: changed
---
Switched the analytics backend from Amplitude to Sentry. The single telemetry opt-out (`FLEET_TELEMETRY_DISABLED`, `DO_NOT_TRACK`, or `telemetry: false` in config) now also gates crash and panic reporting. Significantly expanded the event set — onboarding funnel, shape gauges (repos, worktrees, sessions, slot bindings), engagement distributions (session lifetime, prompts per session, attached uptime), and frustration signals (orphaned sessions, manual rename after auto-name) — to help spot when new users get stuck.
