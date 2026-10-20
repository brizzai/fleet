package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ReadHead must agree with `git rev-parse --abbrev-ref HEAD` — it replaces that
// fork in `fleet status` — including in a linked worktree, whose HEAD is its own.
func TestReadHead(t *testing.T) {
	main, linked := initRepoWithWorktree(t)

	t.Run("main checkout", func(t *testing.T) {
		root, branch := ReadHead(main)
		if root != main || branch != GetBranchName(main) || branch == "" {
			t.Errorf("ReadHead = (%q, %q), want (%q, %q)", root, branch, main, GetBranchName(main))
		}
	})

	t.Run("linked worktree reads its own HEAD", func(t *testing.T) {
		root, branch := ReadHead(linked)
		if root != linked || branch != "feature" {
			t.Errorf("ReadHead = (%q, %q), want (%q, feature)", root, branch, linked)
		}
	})

	t.Run("a subdirectory resolves to its checkout", func(t *testing.T) {
		sub := filepath.Join(linked, "pkg", "deep")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if root, branch := ReadHead(sub); root != linked || branch != "feature" {
			t.Errorf("ReadHead = (%q, %q)", root, branch)
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		cmd := exec.Command("git", "checkout", "-q", "--detach")
		cmd.Dir = linked
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("detach: %v\n%s", err, out)
		}
		if _, branch := ReadHead(linked); branch != "HEAD" || GetBranchName(linked) != "HEAD" {
			t.Errorf("ReadHead branch = %q, git says %q", branch, GetBranchName(linked))
		}
	})

	t.Run("not a checkout", func(t *testing.T) {
		dir := t.TempDir()
		if out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-dir").CombinedOutput(); err == nil {
			t.Skipf("the temp dir is inside a checkout (%s)", out)
		}
		if root, branch := ReadHead(dir); root != "" || branch != "" {
			t.Errorf("ReadHead = (%q, %q), want empty", root, branch)
		}
	})
}
