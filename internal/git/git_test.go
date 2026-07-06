package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepoWithWorktree creates a temp git repo with one commit and a linked
// worktree, returning (mainRepo, linkedWorktree). It skips the test if git is
// unavailable.
func initRepoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	main := filepath.Join(root, "myrepo")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(root, "init", "-q", "myrepo")
	run(main, "config", "user.email", "test@example.com")
	run(main, "config", "user.name", "Test")
	run(main, "commit", "-q", "--allow-empty", "-m", "init")

	linked := filepath.Join(root, "myrepo-feature")
	run(main, "worktree", "add", "-q", linked, "-b", "feature")

	return main, linked
}

func TestGetMainWorktreePath(t *testing.T) {
	main, linked := initRepoWithWorktree(t)

	// EvalSymlinks so macOS /var → /private/var doesn't defeat equality.
	wantMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatalf("EvalSymlinks(main): %v", err)
	}

	resolve := func(from string) string {
		got := GetMainWorktreePath(from)
		abs, err := filepath.EvalSymlinks(got)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", got, err)
		}
		return abs
	}

	// From the main worktree itself → main.
	if got := resolve(main); got != wantMain {
		t.Errorf("GetMainWorktreePath(main) = %q, want %q", got, wantMain)
	}

	// From a linked worktree → still main (the core issue #168 fix).
	if got := resolve(linked); got != wantMain {
		t.Errorf("GetMainWorktreePath(linked) = %q, want %q", got, wantMain)
	}
}

func TestGetMainWorktreePathNonRepo(t *testing.T) {
	// A path that isn't a git repo returns unchanged (safe fallback).
	dir := t.TempDir()
	if got := GetMainWorktreePath(dir); got != dir {
		t.Errorf("GetMainWorktreePath(non-repo) = %q, want %q", got, dir)
	}
}
