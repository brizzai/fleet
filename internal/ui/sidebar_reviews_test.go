package ui

import (
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
)

// originFor pretends every path under /code/brizzai* belongs to one origin,
// which is what git would report for a repo and its worktrees.
func originFor(string) string { return "github.com/brizzai/brizzai" }

func sess(id, title, path string) *session.Session {
	return &session.Session{ID: id, Title: title, ProjectPath: path}
}

// Review worktrees must collapse onto one row. Without this every review is a
// checkout header of its own, wedged among the user's branches — the exact
// crowding the reviews folder exists to prevent.
func TestReviewWorktreesCollapseToOneGroup(t *testing.T) {
	sessions := []*session.Session{
		sess("1", "work", "/code/brizzai"),
		sess("2", "#5163 sliced rebuild", "/code/brizzai-reviews/5163"),
		sess("3", "#5164 agent fab", "/code/brizzai-reviews/5164"),
	}

	items := BuildFlatItems(sessions, nil, map[string]bool{}, "", nil, nil, nil,
		time.Now(), originFor, func(string) bool { return false })

	var checkouts []string
	reviewSessions := 0
	for _, it := range items {
		if it.IsCheckoutHeader {
			checkouts = append(checkouts, it.RepoPath)
		}
		if it.Session != nil && isReviewGroup(it.RepoPath) {
			reviewSessions++
		}
	}

	if len(checkouts) != 2 {
		t.Fatalf("want 2 checkout rows (brizzai + reviews), got %d: %v", len(checkouts), checkouts)
	}
	if reviewSessions != 2 {
		t.Errorf("want both review sessions under the reviews row, got %d", reviewSessions)
	}
	// Reviews last: the user's own branches stay one contiguous run.
	if !isReviewGroup(checkouts[len(checkouts)-1]) {
		t.Errorf("reviews should sort last, got order %v", checkouts)
	}
}

// The reviews folder has no remote of its own. Resolving its origin from the
// folder rather than from a real worktree inside it would strand it in an
// origin group of one, detached from the repo it belongs to.
func TestReviewGroupInheritsRepoOrigin(t *testing.T) {
	sessions := []*session.Session{
		sess("1", "work", "/code/brizzai"),
		sess("2", "#5163", "/code/brizzai-reviews/5163"),
	}

	items := BuildFlatItems(sessions, nil, map[string]bool{}, "", nil, nil, nil,
		time.Now(), originFor, func(string) bool { return false })

	origins := map[string]bool{}
	for _, it := range items {
		if it.IsOriginHeader {
			origins[it.OriginKey] = true
		}
	}
	if len(origins) != 1 {
		t.Errorf("reviews must sit under the repo's origin, got %d origins: %v", len(origins), origins)
	}
}

// A repo with no reviews must not grow a permanent empty row.
func TestNoReviewsRowWhenNoReviews(t *testing.T) {
	sessions := []*session.Session{sess("1", "work", "/code/brizzai")}

	items := BuildFlatItems(sessions, nil, map[string]bool{}, "", nil, nil, nil,
		time.Now(), originFor, func(string) bool { return false })

	for _, it := range items {
		if it.IsCheckoutHeader && isReviewGroup(it.RepoPath) {
			t.Fatal("a repo with no reviews should have no reviews row")
		}
	}
}
