---
type: fixed
---
Your settings (theme, telemetry consent, onboarding state) no longer reset when an older fleet build saves the config — config now preserves fields it doesn't recognize instead of dropping them. Every config write is also logged with the build that made it, for easier diagnosis.
