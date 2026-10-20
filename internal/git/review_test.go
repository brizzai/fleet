package git

import (
	"path/filepath"
	"testing"
)

// MainRepoForReviewWorktree has to be the exact inverse of ReviewWorktreePath:
// it is called after the directory is gone, when nothing can be asked of git,
// and getting it wrong would run `git branch -D` against the wrong checkout.
func TestMainRepoForReviewWorktreeInvertsThePath(t *testing.T) {
	repo := filepath.Join(string(filepath.Separator), "code", "brizzai")
	got := MainRepoForReviewWorktree(ReviewWorktreePath(repo, 5163))
	want, _ := filepath.Abs(repo)
	if got != want {
		t.Errorf("round trip gave %q, want %q", got, want)
	}
}

func TestMainRepoForReviewWorktreeRefusesOtherPaths(t *testing.T) {
	for _, p := range []string{
		"/code/brizzai",                 // an ordinary checkout
		"/code/brizzai-worktrees/thing", // some other sibling folder
		"/code/-reviews/5163",           // a reviews folder with no repo name
		"",
	} {
		if got := MainRepoForReviewWorktree(p); got != "" {
			t.Errorf("MainRepoForReviewWorktree(%q) = %q, want \"\" — a wrong answer "+
				"here force-deletes a branch in a repo nobody named", p, got)
		}
	}
}

// The two must agree, or the sidebar folds a worktree the cleanup will not
// recognise (or the other way round).
func TestReviewPathPredicatesAgree(t *testing.T) {
	p := ReviewWorktreePath("/code/brizzai", 7)
	if !IsReviewWorktree(p) {
		t.Fatalf("IsReviewWorktree(%q) = false", p)
	}
	if MainRepoForReviewWorktree(p) == "" {
		t.Errorf("IsReviewWorktree accepts %q but MainRepoForReviewWorktree rejects it", p)
	}
}
