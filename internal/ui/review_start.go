package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

// reviewWorktreeMsg carries the prepared review worktree back to Update.
type reviewWorktreeMsg struct {
	pr        int
	title     string
	path      string
	originKey string // which origin's review_prompt applies
	err       error
}

// findReview returns the queued review with this URL.
//
// The queue is held on Home rather than re-derived from the palette's rows
// because a row carries only what it renders — the session needs the PR number
// and the repo, and reparsing those back out of a display string would be a
// second, worse source of truth.
func (h *Home) findReview(url string) (github.ReviewRequest, bool) {
	for _, r := range h.reviewQueue {
		if r.URL == url {
			return r, true
		}
	}
	return github.ReviewRequest{}, false
}

// sessionForReview returns the session already reviewing this PR, if any.
//
// Matched on the worktree path rather than the session title: a title is a
// display string the user can rename, while the path is what fleet created and
// is the same fact `PrepareReviewWorktree` is idempotent on.
func (h *Home) sessionForReview(r github.ReviewRequest) *session.Session {
	repo := h.repoForOrigin(r.Repo)
	if repo == "" {
		return nil
	}
	want := git.ReviewWorktreePath(repo, r.Number)
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == want {
			return s
		}
	}
	return nil
}

// repoForOrigin finds the local checkout of an owner/name GitHub repo.
//
// Matched on the origin key fleet already computes for grouping, so a repo
// cloned over SSH and one cloned over HTTPS resolve identically. Returns the
// main worktree, never a linked one: review worktrees hang off the main
// checkout, and creating one from inside another worktree would nest them.
func (h *Home) repoForOrigin(ownerRepo string) string {
	if ownerRepo == "" {
		return ""
	}
	want := "/" + strings.ToLower(ownerRepo)
	for repo := range h.pinnedRepos {
		if strings.HasSuffix(strings.ToLower(h.originKeyFor(repo)), want) {
			if main := git.GetMainWorktreePath(repo); main != "" {
				return main
			}
			return repo
		}
	}
	return ""
}

// startReview prepares a worktree at the PR's head and opens a session in it.
//
// The fetch and checkout run off the Update goroutine: a cold fetch on a large
// monorepo takes seconds, and the UI loop may never block on git.
func (h *Home) startReview(url string) (tea.Model, tea.Cmd) {
	r, ok := h.findReview(url)
	if !ok {
		h.setError(fmt.Errorf("review not found in the queue"))
		return h, nil
	}

	// Already reviewing it? Go there. Re-running the fetch would be wasted
	// work, and landing you on a fresh session beside the one already holding
	// your review is the kind of duplicate that only shows up later.
	if s := h.sessionForReview(r); s != nil {
		return h.jumpToSessionID(s.ID)
	}

	repo := h.repoForOrigin(r.Repo)
	if repo == "" {
		// Naming the repo is the whole point of the message: the fix is to
		// open a session in that clone once so fleet knows where it is.
		h.setError(fmt.Errorf("no local checkout of %s — open it in fleet once first", r.Repo))
		return h, nil
	}

	h.setInfo(fmt.Sprintf("Preparing review #%d…", r.Number))
	pr, title := r.Number, r.Title
	// Resolved here, on the Update goroutine, where the origin cache lives.
	origin := h.originKeyFor(repo)
	return h, func() tea.Msg {
		path, err := git.PrepareReviewWorktree(repo, pr)
		return reviewWorktreeMsg{pr: pr, title: title, path: path, originKey: origin, err: err}
	}
}

// handleReviewWorktree opens the session once its worktree exists.
func (h *Home) handleReviewWorktree(msg reviewWorktreeMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Error("review: worktree failed", "pr", msg.pr, "err", msg.err)
		h.setError(msg.err)
		return h, nil
	}

	cfg := config.Load()
	name := strconv.Itoa(msg.pr)
	return h.handleSessionCreate(sessionCreateMsg{
		path:          msg.path,
		title:         fmt.Sprintf("#%d %s", msg.pr, msg.title),
		workspaceName: name,
		// Empty unless the user configured review_prompt. fleet does not ship
		// a review command of its own — how you review is yours.
		prompt: cfg.GetReviewPrompt(msg.originKey, msg.pr),
	})
}

// jumpToReviewsGroup moves the cursor to the reviews folder of whichever origin
// the cursor is currently in.
//
// Scoped to the current origin rather than jumping to the first reviews node
// anywhere: with several repos on screen, "go to reviews" means the reviews of
// the thing you are looking at, and a jump that leaves the origin you were in
// is a jump you have to undo.
func (h *Home) jumpToReviewsGroup() (tea.Model, tea.Cmd) {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return h, nil
	}
	origin := h.flatItems[h.cursor].OriginKey
	if origin == "" {
		h.setInfo("No origin under the cursor")
		return h, nil
	}
	for _, it := range h.flatItems {
		if it.IsCheckoutHeader && it.OriginKey == origin && isReviewGroup(it.RepoPath) {
			return h.jumpToRepoHeader(it.RepoPath)
		}
	}
	// Naming the origin matters: with several repos on screen, "no reviews"
	// is only useful if it says whose.
	h.setInfo("No reviews started in " + labelForOrigin(origin) + " — press c")
	return h, nil
}
