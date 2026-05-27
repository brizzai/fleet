package ui

import (
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/session"
)

// collectSnapshot builds an analytics.SnapshotStats from the current TUI
// state. Called at boundary events (app_started, app_quit, session create /
// delete) to emit gauges of the user's "shape" — how many repos, worktrees,
// sessions, slot bindings they're juggling.
func (h *Home) collectSnapshot() analytics.SnapshotStats {
	groups := session.GroupByRepo(h.sessions)

	worktreeCount := 0
	perRepo := make([]int, 0, len(groups))
	for repo, sessions := range groups {
		perRepo = append(perRepo, len(sessions))
		if info := h.gitInfoCache[repo]; info != nil && info.IsWorktreeRepo {
			worktreeCount++
		}
	}

	byStatus := make(map[string]int, 6)
	for _, s := range h.sessions {
		byStatus[string(s.GetStatus())]++
	}

	// Use pinned repos as the "total repos" count so empty pinned repos count
	// too — that's a structural signal even without sessions.
	reposTotal := len(h.pinnedRepos)
	if reposTotal < len(groups) {
		reposTotal = len(groups)
	}

	return analytics.SnapshotStats{
		ReposTotal:         reposTotal,
		WorktreeReposTotal: worktreeCount,
		SessionsTotal:      len(h.sessions),
		SessionsByStatus:   byStatus,
		SessionsPerRepo:    perRepo,
		SlotBindingsTotal:  len(h.slotBindings),
	}
}

// anyAttached reports whether the user attached to at least one session this
// install — used by the first_quit milestone to flag "ghost quitters" who
// never engaged with a session.
func (h *Home) anyAttached() bool {
	for _, s := range h.sessions {
		if !s.LastAccessedAt.IsZero() {
			return true
		}
	}
	return false
}
