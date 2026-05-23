---
type: fixed
---
`debuglog.Init()` is now idempotent (guarded by `sync.Once`). The previous behavior re-created the global `slog.Logger` on every call, which raced with long-lived goroutines (e.g. the crash-dump writer) that hold a reference to it. Fixes a `go test -race` data race surfaced in CI.
