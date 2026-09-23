package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func homeWithSessions(sess ...*session.Session) *Home {
	h := &Home{
		cfg:           &config.Config{},
		confirmDialog: NewConfirmDialog(),
		actionLog:     NewActionLog(100),
		toasts:        NewToastStack(),
		sessions:      sess,
	}
	for _, s := range sess {
		h.flatItems = append(h.flatItems, SidebarItem{Session: s})
	}
	return h
}

func reviewSession(id string, pr string) *session.Session {
	return &session.Session{
		ID:            id,
		Title:         "#" + pr + " a pull request",
		ProjectPath:   git.ReviewWorktreePath("/tmp/fleet-test/brizzai", 283),
		WorkspaceName: pr,
	}
}

// The review checkout is ~150MB of someone else's branch, and the row delete is
// the only place it can go: the reviews node is a folder of many worktrees and
// its own header delete refuses by design. Every review deleted before this
// left its checkout on disk with nothing in the UI hinting it was there.
func TestDeletingAReviewRemovesItsCheckout(t *testing.T) {
	s := reviewSession("rev11111-1700000000", "283")
	h := homeWithSessions(s)

	h.confirmDeleteSelected()

	if !strings.Contains(h.confirmDialog.title, "Review") {
		t.Errorf("confirm titled %q — it should say a review is going, not a session",
			h.confirmDialog.title)
	}
	joined := strings.Join(h.confirmDialog.details, " | ")
	if !strings.Contains(joined, "checkout") {
		t.Errorf("confirm details %q never mention the checkout being removed — "+
			"the directory is the expensive half of what this key does", joined)
	}

	msg, ok := h.confirmDialog.onYes().(sessionDeleteMsg)
	if !ok {
		t.Fatalf("confirming produced %T, want sessionDeleteMsg", h.confirmDialog.onYes())
	}
	if !msg.destroyWorkspace {
		t.Error("the worktree is not destroyed — this is the leak")
	}
	if msg.workspaceName == "" {
		t.Error("no workspace name: finalizeDelete guards on it, so the destroy " +
			"would be skipped in silence")
	}
	if !msg.unpinRepo {
		t.Error("the checkout stays pinned after being removed, leaving an " +
			"\"(empty)\" row pointing at a directory that no longer exists")
	}
	if msg.repoPath != session.GetRepoRoot(s.ProjectPath) {
		t.Errorf("repoPath = %q, want the review worktree", msg.repoPath)
	}
}

// An ordinary worktree session must be untouched by this: your own branch is
// work, and it has a header to remove it from.
func TestDeletingAnOrdinarySessionKeepsItsWorktree(t *testing.T) {
	s := &session.Session{
		ID: "own11111-1700000000", Title: "my feature",
		ProjectPath: "/tmp/fleet-test/brizzai-myfeature", WorkspaceName: "myfeature",
	}
	h := homeWithSessions(s)

	h.confirmDeleteSelected()
	msg, ok := h.confirmDialog.onYes().(sessionDeleteMsg)
	if !ok {
		t.Fatalf("confirming produced %T", h.confirmDialog.onYes())
	}
	if msg.destroyWorkspace || msg.unpinRepo {
		t.Errorf("an ordinary session's worktree was destroyed: %+v", msg)
	}
	if !strings.Contains(strings.Join(h.confirmDialog.details, " "), "Worktree kept") {
		t.Error("the nudge pointing at the header is gone")
	}
}

// Only the LAST session in the checkout takes it with them. A second session
// opened in the same review still needs the directory it is running in.
func TestASecondSessionInTheSameReviewKeepsTheCheckout(t *testing.T) {
	first := reviewSession("rev11111-1700000000", "283")
	second := reviewSession("rev22222-1700000000", "283")
	h := homeWithSessions(first, second)

	h.confirmDeleteSelected()
	msg, ok := h.confirmDialog.onYes().(sessionDeleteMsg)
	if !ok {
		t.Fatalf("confirming produced %T", h.confirmDialog.onYes())
	}
	if msg.destroyWorkspace {
		t.Error("removed the checkout out from under a session still using it")
	}
}
