package analytics

import "testing"

// Every snapshot event carries its boundary, so a start snapshot pairs with
// its launch by property rather than by how close the timestamps are.
func TestEmitSnapshotTagsEveryEventWithItsBoundary(t *testing.T) {
	f := withFake(t)
	Init(ModeMinimal, "9.9.9", testIdentity)
	EmitSnapshot(SnapshotStats{
		ReposTotal:         3,
		WorktreeReposTotal: 2,
		SessionsByStatus:   map[string]int{"idle": 1},
		SessionsPerRepo:    []int{1, 2},
	}, SnapshotStart)
	Shutdown()

	seen := 0
	for _, c := range f.captures() {
		if c.Event == EventAppActive {
			continue
		}
		seen++
		if c.Properties["boundary"] != SnapshotStart {
			t.Errorf("%s: boundary = %v, want %q", c.Event, c.Properties["boundary"], SnapshotStart)
		}
	}
	if seen != 7 { // 4 gauges + 1 per status + 1 per repo
		t.Errorf("got %d snapshot events, want 7", seen)
	}
	if c, ok := captureByEvent(f.captures(), MetricSessionsByStatus); !ok || c.Properties["status"] != "idle" {
		t.Errorf("sessions_by_status lost its status property: %v", c.Properties)
	}
}
