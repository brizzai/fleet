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

// TestListBranchesReportsTheLaterOfTheTwoRefs is the half the flags test misses.
//
// The dedupe keeps the LOCAL ref, but a branch you have not pulled has a local
// tip older than origin/<name>. Reporting the survivor's own date makes the row
// describe a different commit than the ref a caller asking for the remote form
// resolves — and since the date is the sort key, an unpulled branch sinks below
// fresher ones and drops out of any top-N list. `lagging` is exactly that shape:
// local at 2020, origin/lagging at 2024.
func TestListBranchesReportsTheLaterOfTheTwoRefs(t *testing.T) {
	repo := initBranchRepo(t)

	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}

	pos := make(map[string]int, len(branches))
	date := make(map[string]string, len(branches))
	for i, b := range branches {
		pos[b.Name] = i
		date[b.Name] = b.CommitDate.UTC().Format("2006-01-02")
	}

	if got := date["lagging"]; got != "2024-01-01" {
		t.Errorf("lagging date = %s, want 2024-01-01 — origin/lagging is the later ref, and it is "+
			"the one baseRefFor hands to git when the field carries the prefix", got)
	}
	if got := date["level"]; got != "2020-01-01" {
		t.Errorf("level date = %s, want 2020-01-01 — its refs are level, so there is nothing to take the max of", got)
	}
	if got := date["solo"]; got != "2020-01-01" {
		t.Errorf("solo date = %s, want 2020-01-01 — no remote counterpart to consider", got)
	}

	// Ordering follows the revised dates, or the fix would be cosmetic: `lagging`
	// must sit with the 2024 refs, not below `ghost` where its local tip put it.
	if pos["lagging"] > pos["level"] || pos["lagging"] > pos["solo"] {
		t.Errorf("lagging at %d sorts below level (%d) / solo (%d) — the list must be ordered by the "+
			"date actually reported", pos["lagging"], pos["level"], pos["solo"])
	}
}

// initCloneWithUpstream builds a bare "remote" with one commit, a work clone
// that pushes to it (a teammate), and a second clone (the user's). Returns
// (userClone, teammateWork, run).
func initCloneWithUpstream(t *testing.T) (string, string, func(dir string, args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(root, "init", "-q", "--bare", "-b", "master", "remote.git")
	bare := filepath.Join(root, "remote.git")

	run(root, "clone", "-q", bare, "work")
	work := filepath.Join(root, "work")
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	run(work, "commit", "-q", "--allow-empty", "-m", "one")
	run(work, "push", "-q", "origin", "master")

	run(root, "clone", "-q", bare, "clone")
	clone := filepath.Join(root, "clone")
	return clone, work, run
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// A worktree branched from origin/master must start at the tip the remote
// actually has, not at whatever this clone last fetched.
func TestFetchBaseRefAdvancesTrackingRef(t *testing.T) {
	clone, work, run := initCloneWithUpstream(t)

	stale := revParse(t, clone, "origin/master")
	headBefore := revParse(t, clone, "HEAD")

	run(work, "commit", "-q", "--allow-empty", "-m", "two")
	run(work, "push", "-q", "origin", "master")
	fresh := revParse(t, work, "HEAD")

	if revParse(t, clone, "origin/master") != stale {
		t.Fatal("precondition: clone's origin/master moved without a fetch")
	}

	if err := FetchBaseRef(clone, "origin/master"); err != nil {
		t.Fatalf("FetchBaseRef: %v", err)
	}
	if got := revParse(t, clone, "origin/master"); got != fresh {
		t.Errorf("origin/master = %s, want the pushed tip %s", got, fresh)
	}
	// The fetch must be invisible to the checkout it runs in: it refreshes a
	// remote-tracking ref, it does not move the user's HEAD.
	if got := revParse(t, clone, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved to %s, want %s — fetch must not touch the checkout", got, headBefore)
	}
}

// A base that names no remote must not be split on "/": reading a local branch
// "feature/foo" as remote "feature" would spend the whole timeout on a fetch
// that cannot work. The broken origin URL is what makes the assertion real —
// without the positive control below, a nil error would prove nothing.
func TestFetchBaseRefSkipsLocalBase(t *testing.T) {
	clone, _, run := initCloneWithUpstream(t)
	run(clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	for _, base := range []string{"master", "feature/foo", ""} {
		if err := FetchBaseRef(clone, base); err != nil {
			t.Errorf("FetchBaseRef(%q) = %v, want no fetch attempted", base, err)
		}
	}
	if err := FetchBaseRef(clone, "origin/master"); err == nil {
		t.Fatal("positive control: fetch against a missing remote should fail, so the nils above are evidence of not fetching")
	}
}
