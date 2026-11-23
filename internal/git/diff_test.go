package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// diffRepo is a temp repo on master with one commit, plus a git runner.
func diffRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "master")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	write(t, dir, "keep.txt", "one\ntwo\nthree\n")
	write(t, dir, "gone.txt", "bye\n")
	write(t, dir, "moved.txt", strings.Repeat("a stable line of text\n", 20))
	write(t, dir, ".gitignore", "*.log\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir, run
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byPath(files []FileDiff) map[string]FileDiff {
	m := make(map[string]FileDiff, len(files))
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

// The snapshot must hold every kind of change — committed, staged, unstaged,
// untracked — and nothing .gitignore excludes, and DiffFiles must report each
// with the right letter, counts and hunks.
func TestSnapshotAndDiffCoverEveryKindOfChange(t *testing.T) {
	dir, run := diffRepo(t)
	base := run("rev-parse", "HEAD")

	run("checkout", "-q", "-b", "feature")
	// Committed: a modification and a rename.
	write(t, dir, "keep.txt", "one\nTWO\nthree\nfour\n")
	run("mv", "moved.txt", "renamed.txt")
	run("commit", "-q", "-am", "work")
	// Staged delete, unstaged binary, untracked file, ignored file.
	run("rm", "-q", "gone.txt")
	if err := os.WriteFile(filepath.Join(dir, "img.bin"), []byte{0, 1, 2, 0, 255, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "new/untracked.go", "package x\n")
	write(t, dir, "debug.log", "noise\n")

	tree, err := SnapshotTree(dir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	if len(tree) < 40 {
		t.Fatalf("tree sha = %q", tree)
	}
	files, err := DiffFiles(dir, base, tree)
	if err != nil {
		t.Fatalf("DiffFiles: %v", err)
	}
	got := byPath(files)
	if len(files) != 5 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}

	if f := got["keep.txt"]; f.Status != "M" || f.Additions != 2 || f.Deletions != 1 ||
		!strings.HasPrefix(f.Patch, "@@ ") || !strings.Contains(f.Patch, "+TWO\n") {
		t.Errorf("keep.txt = %+v", f)
	}
	if f := got["renamed.txt"]; f.Status != "R" || f.OldPath != "moved.txt" || f.Patch != "" {
		t.Errorf("renamed.txt = %+v", f)
	}
	if f := got["gone.txt"]; f.Status != "D" || f.Deletions != 1 || !strings.Contains(f.Patch, "-bye") {
		t.Errorf("gone.txt = %+v", f)
	}
	if f := got["img.bin"]; f.Status != "A" || !f.Binary || f.Patch != "" {
		t.Errorf("img.bin = %+v", f)
	}
	if f := got["new/untracked.go"]; f.Status != "A" || f.Additions != 1 {
		t.Errorf("new/untracked.go = %+v", f)
	}
	if _, ok := got["debug.log"]; ok {
		t.Error("ignored debug.log leaked into the snapshot")
	}
	for _, f := range files {
		if strings.Contains(f.Patch, "diff --git") || strings.Contains(f.Patch, "+++ ") {
			t.Errorf("%s patch carries its preamble:\n%s", f.Path, f.Patch)
		}
	}
}

// The snapshot must leave the real index byte-for-byte as it was: a review
// that silently staged the user's half-finished work would be a disaster.
func TestSnapshotNeverTouchesTheRealIndex(t *testing.T) {
	dir, run := diffRepo(t)
	write(t, dir, "keep.txt", "staged\n")
	run("add", "keep.txt")
	write(t, dir, "keep.txt", "staged then edited\n")
	write(t, dir, "untracked.txt", "u\n")

	idx := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := run("status", "--porcelain")

	tree, err := SnapshotTree(dir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	after, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("real index changed")
	}
	if got := run("status", "--porcelain"); got != statusBefore {
		t.Errorf("status changed:\nbefore %q\nafter  %q", statusBefore, got)
	}
	// The tree holds the working-tree bytes, not the staged ones.
	if got := run("cat-file", "-p", tree+":keep.txt"); got != "staged then edited" {
		t.Errorf("keep.txt in tree = %q", got)
	}
	if got := run("cat-file", "-p", tree+":untracked.txt"); got != "u" {
		t.Errorf("untracked.txt in tree = %q", got)
	}
}

// Same bytes on disk → same tree, which is what makes the SHA a cache key.
func TestSnapshotIsStable(t *testing.T) {
	dir, _ := diffRepo(t)
	write(t, dir, "x.txt", "x\n")
	a, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("snapshots differ: %s vs %s", a, b)
	}
	write(t, dir, "x.txt", "y\n")
	c, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Error("snapshot did not change with the worktree")
	}
}

// A clean worktree snapshots to HEAD's own tree.
func TestSnapshotOfCleanTreeIsHeadTree(t *testing.T) {
	dir, run := diffRepo(t)
	tree, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := run("rev-parse", "HEAD^{tree}"); tree != want {
		t.Errorf("tree = %s, want HEAD^{tree} %s", tree, want)
	}
	files, err := DiffFiles(dir, "HEAD", tree)
	if err != nil || len(files) != 0 {
		t.Errorf("DiffFiles(HEAD, clean) = %v, %v", files, err)
	}
}

// Linked worktrees keep their index under .git/worktrees/<name>/, and an
// unborn branch has no HEAD to seed from — both still snapshot.
func TestSnapshotLinkedWorktreeAndUnbornBranch(t *testing.T) {
	_, linked := initRepoWithWorktree(t)
	write(t, linked, "f.txt", "f\n")
	tree, err := SnapshotTree(linked)
	if err != nil {
		t.Fatalf("linked: %v", err)
	}
	if out, err := exec.Command("git", "-C", linked, "cat-file", "-e", tree+":f.txt").CombinedOutput(); err != nil {
		t.Errorf("f.txt missing from linked snapshot: %s", out)
	}

	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("init: %s", out)
	}
	write(t, dir, "a.txt", "a\n")
	if _, err := SnapshotTree(dir); err != nil {
		t.Errorf("unborn: %v", err)
	}
}

func TestMergeBase(t *testing.T) {
	dir, run := diffRepo(t)
	fork := run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "feature")
	run("commit", "-q", "--allow-empty", "-m", "feature work")
	run("checkout", "-q", "master")
	run("commit", "-q", "--allow-empty", "-m", "master moved on")
	run("checkout", "-q", "feature")

	got, err := MergeBase(dir, "master")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if got != fork {
		t.Errorf("MergeBase = %s, want fork point %s", got, fork)
	}
	if _, err := MergeBase(dir, "no-such-branch"); err == nil {
		t.Error("MergeBase on a missing ref succeeded")
	}
}

// A revision that reads as an option must be refused before git sees it.
func TestDiffRefusesOptionLookingRevisions(t *testing.T) {
	dir, _ := diffRepo(t)
	out := filepath.Join(t.TempDir(), "pwned")
	if _, err := DiffFiles(dir, "--output="+out, "HEAD"); err == nil {
		t.Error("DiffFiles accepted an option-looking revision")
	}
	if _, err := MergeBase(dir, "--output="+out); err == nil {
		t.Error("MergeBase accepted an option-looking revision")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("git wrote the --output file")
	}
}

func TestParseNumstatZ(t *testing.T) {
	in := "3\t1\ta.go\x00-\t-\tlogo.png\x000\t0\t\x00old.txt\x00new.txt\x00"
	got, err := parseNumstatZ([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if c := got["a.go"]; c.add != 3 || c.del != 1 || c.binary {
		t.Errorf("a.go = %+v", c)
	}
	if c := got["logo.png"]; !c.binary {
		t.Errorf("logo.png = %+v", c)
	}
	if _, ok := got["new.txt"]; !ok {
		t.Errorf("rename keyed wrong: %v", got)
	}
}

func TestHunksOf(t *testing.T) {
	sec := "diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	if got := hunksOf(sec); got != "@@ -1 +1 @@\n-a\n+b\n" {
		t.Errorf("hunksOf = %q", got)
	}
	if got := hunksOf("diff --git a/x b/y\nsimilarity index 100%\n"); got != "" {
		t.Errorf("pure rename hunks = %q", got)
	}
}

// ResolveBase: stored → origin/<prBase> when it verifies → GetDefaultBranch.
func TestResolveBaseFallbackOrder(t *testing.T) {
	dir, run := diffRepo(t)
	head := run("rev-parse", "HEAD")
	run("branch", "develop")
	run("update-ref", "refs/remotes/origin/release", head)
	run("update-ref", "refs/remotes/origin/master", head)

	cases := []struct {
		name, stored, prBase, want string
	}{
		{"stored wins", "develop", "release", "develop"},
		{"stored that no longer resolves falls through", "deleted-branch", "release", "origin/release"},
		{"PR base when its remote ref exists", "", "release", "origin/release"},
		{"PR base with no remote ref falls through", "", "nope", "origin/master"},
		{"nothing → default branch", "", "", "origin/master"},
		{"option-looking stored is ignored", "--output=/tmp/x", "", "origin/master"},
	}
	for _, c := range cases {
		if got := ResolveBase(dir, c.stored, c.prBase); got != c.want {
			t.Errorf("%s: ResolveBase(%q, %q) = %q, want %q", c.name, c.stored, c.prBase, got, c.want)
		}
	}
}
