package ui

import (
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/session"
)

// collectSnapshot builds an analytics.SnapshotStats from the current TUI
// state. Called at the two boundary events — app_started and app_quit —
// to emit gauges of the user's "shape": how many repos, worktrees,
// sessions, slot bindings they're juggling.
//
// workerMu protects sessions/gitInfoCache from the status worker; we hold
// it for the whole function because GroupByRepo and the iterations below
// all touch h.sessions, and the snapshot fires while the worker is live.
func (h *Home) collectSnapshot() analytics.SnapshotStats {
	h.workerMu.Lock()
	defer h.workerMu.Unlock()

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

// fireStartupAnalytics initializes the analytics client and emits the
// standard launch-time events (TrackAppStarted + boundary snapshot + first-
// launch onboarding milestone). Split out from the main load handler
// because it's now triggered both from "consent already given" and from
// the consentResultMsg handler. The pre-discovered Identity is passed in
// so Init does no shell-outs on the UI thread.
func (h *Home) fireStartupAnalytics(repoCount int) {
	analytics.Init(h.cfg.IsTelemetryEnabled(), h.version, h.identity)

	effectiveTheme := h.cfg.Theme
	if effectiveTheme == "" {
		effectiveTheme = DefaultPaletteName
	}
	h.workerMu.Lock()
	sessionCount := len(h.sessions)
	h.workerMu.Unlock()
	analytics.TrackAppStarted(
		h.version,
		sessionCount,
		repoCount,
		effectiveTheme,
		h.cfg.GetEnterMode(),
		h.cfg.IsAutoNameEnabled(),
		h.cfg.IsCopyClaudeSettingsEnabled(),
	)
	analytics.EmitSnapshot(h.collectSnapshot())
	if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstLaunch) {
		analytics.Track(analytics.EventOnboardingFirstLaunch, nil)
	}
}

// anyAttached reports whether the user attached to at least one session this
// install — used by the first_quit milestone to flag "ghost quitters" who
// never engaged with a session.
func (h *Home) anyAttached() bool {
	h.workerMu.Lock()
	defer h.workerMu.Unlock()
	for _, s := range h.sessions {
		if !s.LastAccessedAt.IsZero() {
			return true
		}
	}
	return false
}
