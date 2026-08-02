package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
)

// hookFileInfoAt writes a status file and stamps its mtime, returning the FileInfo
// buildSnapshotJSON takes.
func hookFileInfoAt(t *testing.T, modTime time.Time) os.FileInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook.json")
	if err := os.WriteFile(path, []byte(`{"status":"finished"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// The producer side of the bold-vs-plain decision. The renderer tests drive it
// from literal maps, so without this nothing pins how the flag is actually
// computed — which is exactly the seam that let a dropped AgentPID ship green.
func TestBuildSnapshotJSON_DivergenceSignificance(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// how far the file on disk sits ahead of the hook fleet applied
		skew        time.Duration
		wantSignif  bool
		wantPresent bool
	}{
		// #220's shape: applied hook hours behind the file.
		{"dropped hook", 18*time.Hour + 27*time.Minute, true, true},
		// Unix-second `ts` vs sub-second mtime means re-reading one file can show
		// up to 1s of skew with nothing wrong.
		{"second-granularity artifact", 900 * time.Millisecond, false, true},
		// fsnotify debounce + a worker cycle.
		{"pipeline lag", 2 * time.Second, false, true},
		{"just past the grace", divergenceLagGrace + time.Second, true, true},
		// File not newer than what we applied: nothing to explain.
		{"file not newer", 0, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			applied := now.Add(-tc.skew)
			snap := session.StatusSnapshot{
				HookStatus:    "running",
				HookUpdatedAt: applied,
			}
			m := buildSnapshotJSON(snap, []byte(`{"status":"finished","event":"Stop"}`),
				hookFileInfoAt(t, now), now, nil, nil, workerHeartbeat{})

			hook, ok := m["hook"].(map[string]any)
			if !ok {
				t.Fatal("snapshot has no hook block")
			}
			got, present := hook["divergence_significant"]
			if present != tc.wantPresent {
				t.Fatalf("divergence_significant present = %v, want %v (hook=%v)", present, tc.wantPresent, hook)
			}
			if tc.wantPresent && got != tc.wantSignif {
				t.Errorf("divergence_significant = %v, want %v (skew %s)", got, tc.wantSignif, tc.skew)
			}
			// The comparison itself must still be reported regardless of significance:
			// event Stop maps to finished, which is not the applied "running".
			if am := hook["applied_matches_file"]; am != false {
				t.Errorf("applied_matches_file = %v, want false", am)
			}
		})
	}
}

// The whole hook block must survive a round trip through encoding/json, since a
// Shift+D capture is read back off disk rather than out of memory.
func TestBuildSnapshotJSON_HookBlockMarshals(t *testing.T) {
	now := time.Now()
	snap := session.StatusSnapshot{
		HookStatus:     "running",
		HookUpdatedAt:  now.Add(-time.Hour),
		OwnerSessionID: "owner-aaa",
		OwnerPID:       4242,
	}
	m := buildSnapshotJSON(snap, []byte(`{"status":"finished","event":"Stop","session_id":"new-bbb"}`),
		hookFileInfoAt(t, now), now, nil, nil, workerHeartbeat{})

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("snapshot.json does not marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("snapshot.json does not round trip: %v", err)
	}
	hook := back["hook"].(map[string]any)

	// metaInt must survive the float64 that json.Unmarshal produces.
	if got := metaInt(hook, "owner_pid"); got != 4242 {
		t.Errorf("owner_pid after round trip = %d, want 4242", got)
	}
	if got := metaString(hook, "file_session_id"); got != "new-bbb" {
		t.Errorf("file_session_id after round trip = %q, want new-bbb", got)
	}
	if got := metaString(hook, "owner_session_id"); got != "owner-aaa" {
		t.Errorf("owner_session_id after round trip = %q, want owner-aaa", got)
	}
}
