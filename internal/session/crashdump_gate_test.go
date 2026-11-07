package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
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
// 29 for one session). A death is recorded once: when it happens. A session
// that reached StatusError while still alive (an agent-reported error) and
// THEN dies is a new death, and still gets its dump.
func TestCrashDumpOnlyOnANewDeath(t *testing.T) {
	debuglog.Init()
	for _, tc := range []struct {
		name    string
		session *Session
		want    int
	}{
		{"running session's tmux vanished",
			&Session{ID: "d", Status: StatusRunning, paneCapturer: &mockPane{dead: true, tmuxGone: true}}, 1},
		{"loaded from state.db already dead (next launch)",
			&Session{ID: "d", Status: StatusError, loadedDead: true, paneCapturer: &mockPane{dead: true, tmuxGone: true}}, 0},
		{"running session's pane died",
			&Session{ID: "d", Status: StatusRunning, paneCapturer: &mockPane{dead: true}}, 1},
		{"loaded dead pane re-detected",
			&Session{ID: "d", Status: StatusError, loadedDead: true, paneCapturer: &mockPane{dead: true}}, 0},
		{"a launch that dies is still dumped",
			&Session{ID: "d", Status: StatusStarting, paneCapturer: &mockPane{dead: true, tmuxGone: true}}, 1},
		{"agent error while alive, then the pane dies",
			&Session{ID: "d", Status: StatusError, paneCapturer: &mockPane{dead: true}}, 1},
		{"hook-reported death (applyHookStatus)",
			&Session{ID: "d", Agent: agent.OpenCode, Status: StatusRunning, paneCapturer: &mockPane{},
				hookStatus: "dead", hookUpdatedAt: time.Now()}, 1},
		{"hook-reported death on a loaded-dead session (applyHookStatus)",
			&Session{ID: "d", Agent: agent.OpenCode, Status: StatusError, loadedDead: true, paneCapturer: &mockPane{},
				hookStatus: "dead", hookUpdatedAt: time.Now()}, 0},
		{"hook-reported death (Claude hook path)",
			&Session{ID: "d", Agent: agent.Claude, Status: StatusRunning, paneCapturer: &mockPane{},
				hookStatus: "dead", hookUpdatedAt: time.Now()}, 1},
		{"hook-reported death on a loaded-dead session (Claude hook path)",
			&Session{ID: "d", Agent: agent.Claude, Status: StatusError, loadedDead: true, paneCapturer: &mockPane{},
				hookStatus: "dead", hookUpdatedAt: time.Now()}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dumps := recordDumps(t)
			tc.session.UpdateStatus()
			if tc.session.GetStatus() != StatusError {
				t.Fatalf("status = %s, want error — the case is not reaching a death branch", tc.session.GetStatus())
			}
			if got := countDumps(dumps); got != tc.want {
				t.Errorf("wrote %d crash dumps, want %d", got, tc.want)
			}
		})
	}
}

// FromRow is where the latch is seeded: a row persisted as error is a death an
// earlier process already recorded.
func TestFromRowMarksPersistedErrorAsLoadedDead(t *testing.T) {
	if !FromRow(&SessionRow{ID: "a", Status: string(StatusError)}).loadedDead {
		t.Error("a row persisted as error must load as already dead")
	}
	if FromRow(&SessionRow{ID: "b", Status: string(StatusFinished)}).loadedDead {
		t.Error("a finished row must not load as dead")
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
