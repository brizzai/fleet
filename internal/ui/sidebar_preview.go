package ui

import (
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

// This file powers the live sidebar preview shown in the Appearance settings
// category and the first-run onboarding. It renders the *real* RenderSidebar
// over a small fixed set of synthetic data, so the preview reflects every
// display toggle and theme change automatically and can never drift from the
// actual sidebar. The mock paths are identity keys only — nothing touches disk.

const (
	mockRepoMain    = "/preview/fleet"
	mockRepoFeat    = "/preview/fleet-feat"
	mockRepoScratch = "/preview/scratch"

	// Session IDs are referenced by the slot-binding map so the finished
	// session renders its "[1]" badge.
	mockSessFinished = "preview-finished"
)

// mockPreviewSession builds a synthetic session. Only the exported fields the
// renderer reads are set; the zero-value mutex is safe for GetStatus().
func mockPreviewSession(id, title string, a agent.Type, st session.Status, path string) *session.Session {
	return &session.Session{
		ID:          id,
		Title:       title,
		Agent:       a,
		Status:      st,
		ProjectPath: path,
	}
}

// previewSessions returns the synthetic sessions backing the preview rows.
func previewSessions() []*session.Session {
	return []*session.Session{
		mockPreviewSession("preview-running", "Refactor sidebar", agent.Claude, session.StatusRunning, mockRepoMain),
		mockPreviewSession("preview-waiting", "Add test coverage", agent.Codex, session.StatusWaiting, mockRepoMain),
		mockPreviewSession(mockSessFinished, "Fix flaky preview", agent.Claude, session.StatusFinished, mockRepoFeat),
		mockPreviewSession("preview-idle", "Scratch notes", agent.Claude, session.StatusIdle, mockRepoScratch),
	}
}

// previewGitInfo returns synthetic per-checkout git info: an approved PR on the
// main checkout, a dirty worktree, and a plain scratch repo.
func previewGitInfo() map[string]*git.RepoInfo {
	return map[string]*git.RepoInfo{
		mockRepoMain: {
			Branch: "main",
			PR:     &github.PR{Number: 128, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS"},
		},
		mockRepoFeat: {
			Branch:         "feat/preview",
			IsDirty:        true,
			IsWorktreeRepo: true,
		},
		mockRepoScratch: {
			Branch: "main",
		},
	}
}

// previewSlots binds the finished session to slot 1 so the "[1]" badge renders.
func previewSlots() map[int]string {
	return map[int]string{1: mockSessFinished}
}

// counts is a tiny helper for building a header's per-status breakdown.
func counts(pairs ...any) map[session.Status]int {
	m := make(map[session.Status]int)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i].(session.Status)] = pairs[i+1].(int)
	}
	return m
}

// previewItems builds the flattened sidebar rows by hand (rather than via
// BuildFlatItems, which would touch the filesystem resolving repo roots). The
// density toggle is honored here the same way BuildFlatItems honors it: the
// inter-origin spacer is dropped in compact mode.
func previewItems() []SidebarItem {
	sess := previewSessions()
	items := []SidebarItem{
		// Origin: fleet (2 checkouts; running + waiting + finished across them).
		{
			IsRepoHeader: true, IsOriginHeader: true,
			OriginKey: "brizzai/fleet", OriginLabel: "fleet", Expanded: true,
			SessionCount: 2,
			StatusCounts: counts(session.StatusRunning, 1, session.StatusWaiting, 1, session.StatusFinished, 1),
		},
		// Checkout: main (PR #128 approved).
		{
			IsRepoHeader: true, IsCheckoutHeader: true,
			OriginKey: "brizzai/fleet", RepoPath: mockRepoMain, Expanded: true,
			SessionCount: 2,
			StatusCounts: counts(session.StatusRunning, 1, session.StatusWaiting, 1),
		},
		{OriginKey: "brizzai/fleet", RepoPath: mockRepoMain, Session: sess[0]}, // running · Claude
		{OriginKey: "brizzai/fleet", RepoPath: mockRepoMain, Session: sess[1]}, // waiting · Codex
		// Checkout: feat/preview worktree (dirty).
		{
			IsRepoHeader: true, IsCheckoutHeader: true,
			OriginKey: "brizzai/fleet", RepoPath: mockRepoFeat, Expanded: true,
			SessionCount: 1,
			StatusCounts: counts(session.StatusFinished, 1),
		},
		{OriginKey: "brizzai/fleet", RepoPath: mockRepoFeat, Session: sess[2]}, // finished · Claude · slot [1]
	}

	if SidebarDensity != "compact" {
		items = append(items, SidebarItem{IsSpacer: true})
	}

	items = append(items,
		// Origin: scratch (one idle session — idle is omitted from pills).
		SidebarItem{
			IsRepoHeader: true, IsOriginHeader: true,
			OriginKey: "local:scratch", OriginLabel: "scratch", Expanded: true,
			SessionCount: 1,
			StatusCounts: counts(session.StatusIdle, 1),
		},
		SidebarItem{
			IsRepoHeader: true, IsCheckoutHeader: true,
			OriginKey: "local:scratch", RepoPath: mockRepoScratch, Expanded: true,
			SessionCount: 1,
			StatusCounts: counts(session.StatusIdle, 1),
		},
		SidebarItem{OriginKey: "local:scratch", RepoPath: mockRepoScratch, Session: sess[3]}, // idle · Claude
	)
	return items
}

// RenderSidebarPreview renders the synthetic sidebar at the given size using the
// real render path, so it honors the active palette and every display flag.
// cursor is -1 (nothing selected) to keep the sample neutral for reading.
func RenderSidebarPreview(width, height int) string {
	items := previewItems()
	return RenderSidebar(items, previewSessions(), previewGitInfo(), previewSlots(), -1, 0, width, height)
}
