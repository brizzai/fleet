package analytics

// SnapshotStats describes the structural state of the TUI at a moment in time.
// Used to emit boundary gauges at app_started and app_quit.
type SnapshotStats struct {
	ReposTotal         int
	WorktreeReposTotal int
	SessionsTotal      int
	SessionsByStatus   map[string]int // status name → count
	SessionsPerRepo    []int          // one entry per repo (used as Distribution samples)
	SlotBindingsTotal  int
}

// EmitSnapshot records the current TUI state as Sentry gauges + a
// sessions_per_repo distribution. Safe to call repeatedly; no-op when
// telemetry is disabled.
func EmitSnapshot(stats SnapshotStats) {
	Gauge(MetricReposTotal, float64(stats.ReposTotal), nil)
	Gauge(MetricWorktreeReposTotal, float64(stats.WorktreeReposTotal), nil)
	Gauge(MetricSessionsTotal, float64(stats.SessionsTotal), nil)
	Gauge(MetricSlotBindingsTotal, float64(stats.SlotBindingsTotal), nil)

	for status, count := range stats.SessionsByStatus {
		Gauge(MetricSessionsByStatus, float64(count), map[string]interface{}{
			"status": status,
		})
	}

	for _, n := range stats.SessionsPerRepo {
		Distribution(MetricSessionsPerRepo, float64(n), nil)
	}
}
