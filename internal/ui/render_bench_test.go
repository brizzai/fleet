package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// renderFixture15 is a 15-session fleet over four checkouts, the size the
// idle-CPU investigation profiled. Paths resolve through the repo-root cache so
// rendering never shells out to git.
func renderFixture15() ([]*session.Session, map[string]*git.RepoInfo) {
	repos := []string{"/bench/api", "/bench/api-auth", "/bench/notes", "/bench/tools"}
	for _, r := range repos {
		session.SeedRepoRoot(r, r)
	}
	statuses := []session.Status{
		session.StatusRunning, session.StatusFinished, session.StatusWaiting, session.StatusIdle,
		session.StatusFinished, session.StatusSuspended, session.StatusError,
	}
	sessions := make([]*session.Session, 0, 15)
	for i := 0; i < 15; i++ {
		sessions = append(sessions, &session.Session{
			ID:          fmt.Sprintf("bench%02d", i),
			Title:       fmt.Sprintf("Task number %d with a realistic title", i),
			Agent:       agent.Claude,
			Status:      statuses[i%len(statuses)],
			ProjectPath: repos[i%len(repos)],
		})
	}
	infos := map[string]*git.RepoInfo{}
	for _, r := range repos {
		infos[r] = &git.RepoInfo{Branch: "main"}
	}
	return sessions, infos
}

func renderSidebarFrame(sessions []*session.Session, infos map[string]*git.RepoInfo) string {
	items := BuildFlatItems(sessions, nil, nil, "", nil, nil, nil, time.Time{},
		func(string) string { return "" }, func(string) bool { return false })
	return RenderSidebar(items, sessions, infos, nil, 0, 0, 64, 40, true)
}

// BenchmarkSidebarFrame15 measures one sidebar frame for a 15-session fleet —
// the part of View() that runs on every message. Run with -benchmem to watch
// allocations per frame.
func BenchmarkSidebarFrame15(b *testing.B) {
	sessions, infos := renderFixture15()
	b.ReportAllocs()
	for b.Loop() {
		_ = renderSidebarFrame(sessions, infos)
	}
}

// sidebarFrameAllocBudget caps allocations for one 15-session sidebar frame.
// The sidebar is rebuilt on every Bubble Tea message (ticks, worker repaints,
// mouse events that act), so allocations here are idle CPU through the GC.
// Measured at 1113 when written (~114µs, 68KB per frame); the budget leaves ~40%
// headroom. Raise it deliberately, with a reason, rather than to make a red
// test green.
const sidebarFrameAllocBudget = 1600

func TestSidebarFrameAllocBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation budget is skipped in -short mode")
	}
	sessions, infos := renderFixture15()
	allocs := testing.AllocsPerRun(20, func() { _ = renderSidebarFrame(sessions, infos) })
	if allocs > sidebarFrameAllocBudget {
		t.Errorf("one 15-session sidebar frame allocates %.0f times, budget %d", allocs, sidebarFrameAllocBudget)
	}
	t.Logf("sidebar frame: %.0f allocs", allocs)
}
