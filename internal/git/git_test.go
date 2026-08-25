package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// initBranchRepo builds a repo whose branches cover every shape ListBranches
// has to tell apart: local-only, local with a remote counterpart that is level,
// local with a remote counterpart that is AHEAD, and remote-only.
func initBranchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	// The commit dates are pinned rather than left to the clock. Two
	// --allow-empty commits land in the same second, and --sort=-committerdate
	// then falls back to refname order, which always puts refs/heads before
	// refs/remotes — silently hiding the very ordering this fixture exists to
	// produce.
	at := func(date string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if date != "" {
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_DATE="+date,
				"GIT_COMMITTER_DATE="+date)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run := func(args ...string) string { return at("", args...) }

	const old, recent = "2020-01-01T00:00:00Z", "2024-01-01T00:00:00Z"

	run("init", "-q", ".")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	at(old, "commit", "-q", "--allow-empty", "-m", "one")
	first := run("rev-parse", "HEAD")

	run("branch", "level")
	run("branch", "lagging")
	run("branch", "solo")
	run("update-ref", "refs/remotes/origin/level", first)

	// A newer commit, so origin/lagging sorts strictly BEFORE the local branch
	// of the same name — the everyday state of a branch you have not pulled,
	// and the ordering that used to defeat the dedupe.
	at(recent, "commit", "-q", "--allow-empty", "-m", "two")
	second := run("rev-parse", "HEAD")
	run("update-ref", "refs/remotes/origin/lagging", second)
	run("update-ref", "refs/remotes/origin/ghost", second)

	return repo
}

// TestListBranchesRemoteCounterparts pins both halves of the two-pass form: the
// HasRemote flag the worktree dialog's origin/ rule reads, and the dedupe — a
// remote ref that sorts BEFORE its local counterpart (because the local branch
// has fallen behind) used to emit the branch twice.
func TestListBranchesRemoteCounterparts(t *testing.T) {
	repo := initBranchRepo(t)

	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}

	byName := make(map[string][]BranchInfo)
	for _, b := range branches {
		byName[b.Name] = append(byName[b.Name], b)
	}

	cases := []struct {
		name      string
		isRemote  bool
		hasRemote bool
		why       string
	}{
		{"solo", false, false, "local branch with no origin/ ref"},
		{"level", false, true, "local branch whose origin/ ref is level with it"},
		{"lagging", false, true, "local branch whose origin/ ref is AHEAD, so the remote line sorts first"},
		{"ghost", true, true, "origin/-only branch"},
	}

	for _, c := range cases {
		got := byName[c.name]
		if len(got) != 1 {
			t.Errorf("%s (%s): listed %d times, want exactly 1", c.name, c.why, len(got))
			continue
		}
		if got[0].IsRemote != c.isRemote {
			t.Errorf("%s: IsRemote = %v, want %v (%s)", c.name, got[0].IsRemote, c.isRemote, c.why)
		}
		if got[0].HasRemote != c.hasRemote {
			t.Errorf("%s: HasRemote = %v, want %v (%s)", c.name, got[0].HasRemote, c.hasRemote, c.why)
		}
	}
}
