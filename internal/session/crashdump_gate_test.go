package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// recordDumps swaps the crash dump writer for a recorder for one test. The
// writer runs on its own goroutine, so dumps arrive on a channel.
func recordDumps(t *testing.T) chan string {
	t.Helper()
	reasons := make(chan string, 8)
	prev := crashDumpWriter
	crashDumpWriter = func(_, _, _, reason, _ string) { reasons <- reason }
	t.Cleanup(func() { crashDumpWriter = prev })
	return reasons
}

// countDumps collects what arrives within a short window.
func countDumps(ch chan string) int {
	n := 0
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case <-ch:
			n++
		case <-deadline:
			return n
		}
	}
}

// A session persisted as errored is re-detected as dead on every launch, and
// the in-memory deathRecorded latch starts out false each time. That wrote a
// fresh dump per dead session per launch (580 files in one user's crashes dir,
// 29 for one session). A death is recorded once: when it happens.
func TestCrashDumpOnlyOnANewDeath(t *testing.T) {
	debuglog.Init()
	for _, tc := range []struct {
		name string
		old  Status
		pane *mockPane
		want int
	}{
		{"running session's tmux vanished", StatusRunning, &mockPane{dead: true, tmuxGone: true}, 1},
		{"already-errored session re-detected (next launch)", StatusError, &mockPane{dead: true, tmuxGone: true}, 0},
		{"running session's pane died", StatusRunning, &mockPane{dead: true}, 1},
		{"already-errored dead pane re-detected", StatusError, &mockPane{dead: true}, 0},
		{"a launch that dies is still dumped", StatusStarting, &mockPane{dead: true, tmuxGone: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dumps := recordDumps(t)
			s := &Session{ID: "d", Status: tc.old, paneCapturer: tc.pane}
			s.UpdateStatus()
			if s.GetStatus() != StatusError {
				t.Fatalf("status = %s, want error — the case is not reaching a death branch", s.GetStatus())
			}
			if got := countDumps(dumps); got != tc.want {
				t.Errorf("wrote %d crash dumps, want %d", got, tc.want)
			}
		})
	}
}

func TestPruneCrashDumpsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("s%d_x.txt", i))
		if err := os.WriteFile(p, []byte("dump"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// Not a dump: must survive whatever the count.
	other := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	pruneCrashDumps(dir, 3)

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{"notes.md", "s5_x.txt", "s6_x.txt", "s7_x.txt"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("after prune: %v, want %v", names, want)
	}
}
