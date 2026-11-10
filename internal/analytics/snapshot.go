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

// Snapshot boundaries, sent as the `boundary` property on every snapshot
// event so a start snapshot pairs with its launch without a time window.
const (
	SnapshotStart = "start"
	SnapshotQuit  = "quit"
)

// EmitSnapshot records the current TUI state as Mixpanel events via
// analytics.Gauge and analytics.Distribution. Safe to call repeatedly;
// no-op when telemetry is disabled.
func EmitSnapshot(stats SnapshotStats, boundary string) {
	props := func() map[string]interface{} {
		return map[string]interface{}{"boundary": boundary}
	}
	Gauge(MetricReposTotal, float64(stats.ReposTotal), props())
	Gauge(MetricWorktreeReposTotal, float64(stats.WorktreeReposTotal), props())
	Gauge(MetricSessionsTotal, float64(stats.SessionsTotal), props())
	Gauge(MetricSlotBindingsTotal, float64(stats.SlotBindingsTotal), props())

	for status, count := range stats.SessionsByStatus {
		p := props()
		p["status"] = status
		Gauge(MetricSessionsByStatus, float64(count), p)
	}

	for _, n := range stats.SessionsPerRepo {
		Distribution(MetricSessionsPerRepo, float64(n), props())
	}
}
