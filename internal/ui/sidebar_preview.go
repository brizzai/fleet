package ui

import (
	"sync"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

// This file powers the live sidebar preview shown in the Appearance settings
// category and the first-run onboarding. It feeds a small fixed set of synthetic
// data through the *real* BuildFlatItems flattening and RenderSidebar render
// path, so both the row structure (grouping, counts, inter-group spacers) and
// the rendering (active palette, every display toggle, density) come from
// production code and can't drift from the actual sidebar. The synthetic paths
// are pre-seeded into the repo-root cache, so nothing shells out to git or
// touches disk.

const (
	mockRepoMain    = "/preview/fleet"
	mockRepoFeat    = "/preview/fleet-feat"
	mockRepoScratch = "/preview/scratch"

	// Session ID referenced by the slot-binding map so the finished session
	// renders its "[1]" badge.
	mockSessFinished = "preview-finished"
)

// Synthetic preview fixture, built once. The data never changes between frames
// (only the display flags / palette do, and those are read at render time), so
// there's no reason to reallocate it on every render. The same session slice
// backs both BuildFlatItems and RenderSidebar, so item.Session pointers match
// the sessions argument.
var (
	previewFixtureOnce sync.Once
	previewSess        []*session.Session
	previewGit         map[string]*git.RepoInfo
	previewSlotMap     map[int]string
)

func buildPreviewFixture() {
	mock := func(id, title string, a agent.Type, st session.Status, path string) *session.Session {
		return &session.Session{ID: id, Title: title, Agent: a, Status: st, ProjectPath: path}
	}
	previewSess = []*session.Session{
		mock("preview-running", "Refactor sidebar", agent.Claude, session.StatusRunning, mockRepoMain),
		mock("preview-waiting", "Add test coverage", agent.Codex, session.StatusWaiting, mockRepoMain),
		mock(mockSessFinished, "Fix flaky preview", agent.Claude, session.StatusFinished, mockRepoFeat),
		mock("preview-idle", "Scratch notes", agent.Claude, session.StatusIdle, mockRepoScratch),
	}
	previewGit = map[string]*git.RepoInfo{
		mockRepoMain:    {Branch: "main", PR: &github.PR{Number: 128, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS"}},
		mockRepoFeat:    {Branch: "feat/preview", IsDirty: true, IsWorktreeRepo: true},
		mockRepoScratch: {Branch: "main"},
	}
	previewSlotMap = map[int]string{1: mockSessFinished}

	// Resolve each synthetic checkout to itself so BuildFlatItems' GetRepoRoot
	// lookups hit the cache instead of shelling out to git.
	for _, p := range []string{mockRepoMain, mockRepoFeat, mockRepoScratch} {
		session.SeedRepoRoot(p, p)
	}
}

// previewOriginOf groups the two fleet checkouts under one origin; scratch falls
// through to a local origin (BuildFlatItems synthesizes "local:scratch").
func previewOriginOf(repo string) string {
	if repo == mockRepoMain || repo == mockRepoFeat {
		return "brizzai/fleet"
	}
	return ""
}

func previewIsWorktreeOf(repo string) bool { return repo == mockRepoFeat }

// RenderSidebarPreview renders the synthetic sidebar at the given size through
// the real BuildFlatItems + RenderSidebar path, so it honors the active palette,
// every display flag, and the density toggle. cursor is -1 (nothing selected) to
// keep the sample neutral for reading.
func RenderSidebarPreview(width, height int) string {
	previewFixtureOnce.Do(buildPreviewFixture)
	items := BuildFlatItems(previewSess, nil, nil, "", nil, nil, previewOriginOf, previewIsWorktreeOf)
	return RenderSidebar(items, previewSess, previewGit, previewSlotMap, -1, 0, width, height)
}
