package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorktreePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	repo := "/code/fleet"
	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{"empty is the old sibling layout", "", "/code/fleet-feat-x"},
		{"explicit default", DefaultWorktreePathTemplate, "/code/fleet-feat-x"},
		{"directory beside the repo", "../{{repo}}.worktrees", "/code/fleet.worktrees/feat-x"},
		{"directory with trailing slash", "../{{repo}}.worktrees/", "/code/fleet.worktrees/feat-x"},
		{"full template", "../wt/{{repo}}/{{name}}", "/code/wt/fleet/feat-x"},
		{"shared dir keeps the repo in the folder name", "~/worktrees", filepath.Join(home, "worktrees", "fleet-feat-x")},
		{"home dir per repo", "~/worktrees/{{repo}}", filepath.Join(home, "worktrees", "fleet", "feat-x")},
		{"absolute", "/tmp/wt/{{name}}", "/tmp/wt/feat-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorktreePath(repo, tt.tmpl, "feat-x")
			if err != nil {
				t.Fatalf("ResolveWorktreePath(%q) error: %v", tt.tmpl, err)
			}
			if got != tt.want {
				t.Errorf("ResolveWorktreePath(%q) = %q, want %q", tt.tmpl, got, tt.want)
			}
		})
	}
}

// A worktree inside the main checkout would show as untracked in its git status
// and be walked by every recursive search run from there.
func TestResolveWorktreePathRefusesInsideRepo(t *testing.T) {
	for _, tmpl := range []string{"{{name}}", ".worktrees", "./wt/{{name}}", "../fleet/x/{{name}}", "."} {
		if p, err := ResolveWorktreePath("/code/fleet", tmpl, "feat-x"); err == nil {
			t.Errorf("template %q resolved to %q inside the repo; want an error", tmpl, p)
		}
	}
}

func TestWorktreePathPreview(t *testing.T) {
	if got := WorktreePathPreview("/code/fleet", "", "feat-x"); got != "../fleet-feat-x" {
		t.Errorf("default preview = %q, want ../fleet-feat-x", got)
	}
	if got := WorktreePathPreview("/code/fleet", "../{{repo}}.worktrees", "feat-x"); got != "../fleet.worktrees/feat-x" {
		t.Errorf("sibling-dir preview = %q, want ../fleet.worktrees/feat-x", got)
	}
	if got := WorktreePathPreview("/code/fleet", "{{name}}", "feat-x"); !strings.HasPrefix(got, "✕ ") {
		t.Errorf("unusable template preview = %q, want an ✕ error line", got)
	}
}

// A repo's own config beats config.json, and .fleet.local.json beats .fleet.json.
func TestWorktreePathTemplatePrecedence(t *testing.T) {
	t.Cleanup(func() { SetGlobalWorktreePath("") })
	repo := t.TempDir()

	if got := WorktreePathTemplate(repo); got != DefaultWorktreePathTemplate {
		t.Errorf("nothing set: got %q, want default", got)
	}
	SetGlobalWorktreePath("~/worktrees")
	if got := WorktreePathTemplate(repo); got != "~/worktrees" {
		t.Errorf("global only: got %q", got)
	}
	writeFile(t, repo, ".fleet.json", `{"worktree_path":"../{{repo}}.wt"}`)
	if got := WorktreePathTemplate(repo); got != "../{{repo}}.wt" {
		t.Errorf("repo config: got %q", got)
	}
	writeFile(t, repo, ".fleet.local.json", `{"worktree_path":"../mine/{{name}}"}`)
	if got := WorktreePathTemplate(repo); got != "../mine/{{name}}" {
		t.Errorf("local config: got %q", got)
	}
	if p, ok := ResolveProvider(repo).(*GitWorktreeProvider); !ok || p.PathTemplate != "../mine/{{name}}" {
		t.Errorf("ResolveProvider did not carry the template: %#v", ResolveProvider(repo))
	}
}

// The source names the file that actually won, so the dialogs' "(from …)" hint
// points at the line to edit — including a local file that exists but doesn't
// set the key, which must fall through to .fleet.json rather than claim it.
func TestWorktreePathSettingSource(t *testing.T) {
	t.Cleanup(func() { SetGlobalWorktreePath("") })
	repo := t.TempDir()

	if _, src := WorktreePathSetting(repo); src != "" {
		t.Errorf("nothing set: source = %q, want empty", src)
	}
	SetGlobalWorktreePath("~/worktrees")
	if _, src := WorktreePathSetting(repo); src != WorktreePathSourceGlobal {
		t.Errorf("global only: source = %q", src)
	}
	writeFile(t, repo, ".bc.json", `{"worktree_path":"../legacy"}`)
	if _, src := WorktreePathSetting(repo); src != ".bc.json" {
		t.Errorf("legacy repo config: source = %q", src)
	}
	writeFile(t, repo, ".fleet.json", `{"worktree_path":"../{{repo}}.wt"}`)
	writeFile(t, repo, ".fleet.local.json", `{"copy_files":{"paths":[".env"]}}`)
	if tmpl, src := WorktreePathSetting(repo); src != ".fleet.json" || tmpl != "../{{repo}}.wt" {
		t.Errorf("local without the key: got (%q, %q), want .fleet.json's", tmpl, src)
	}
	writeFile(t, repo, ".fleet.local.json", `{"worktree_path":"../mine/{{name}}"}`)
	p, ok := ResolveProvider(repo).(*GitWorktreeProvider)
	if !ok || p.PathSource != ".fleet.local.json" || ProviderPathSource(p) != ".fleet.local.json" {
		t.Errorf("ResolveProvider did not carry the source: %#v", ResolveProvider(repo))
	}
	if src := ProviderPathSource(&ShellProvider{}); src != "" {
		t.Errorf("shell provider source = %q, want empty", src)
	}
}

// A linked worktree has no .fleet.local.json (it's untracked); the main
// checkout's must govern where its sibling worktrees go.
func TestProviderPlacedFromMainCheckout(t *testing.T) {
	t.Cleanup(func() { SetGlobalWorktreePath("") })
	main, linked := t.TempDir(), t.TempDir()
	writeFile(t, main, ".fleet.local.json", `{"worktree_path":"../{{repo}}.worktrees"}`)

	p := ProviderPlacedFrom(ResolveProvider(linked), main)
	if got := ProviderPathTemplate(p); got != "../{{repo}}.worktrees" {
		t.Errorf("template = %q, want the main checkout's", got)
	}
	if got := ProviderPathSource(p); got != ".fleet.local.json" {
		t.Errorf("source = %q, want .fleet.local.json", got)
	}

	shell := &ShellProvider{}
	if got := ProviderPlacedFrom(shell, main); got != Provider(shell) {
		t.Errorf("custom provider was replaced: %#v", got)
	}
}

// End to end: git creates the missing parent directory, and Destroy finds the
// worktree by the same template.
func TestGitWorktreeProviderUsesTemplate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")

	p := &GitWorktreeProvider{PathTemplate: "../{{repo}}.worktrees"}
	info, err := p.Create(repo, "feat-x", "feat/x", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := filepath.Join(root, "proj.worktrees", "feat-x")
	gotReal, _ := filepath.EvalSymlinks(info.Path)
	wantReal, _ := filepath.EvalSymlinks(want)
	if gotReal != wantReal {
		t.Fatalf("worktree at %q, want %q", info.Path, want)
	}
	if err := p.Destroy(repo, "feat-x"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after Destroy (stat err %v)", err)
	}
}

func newTestRepo(t *testing.T, root string) (string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(root, "proj")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	return repo, git
}

// After worktree_path changes, a newer checkout of a same-named branch sits at
// the path the template now derives. Destroying the OLD checkout by its own
// path must remove that one, never the newer one.
func TestDestroyPrefersExactCheckoutOverDerivedPath(t *testing.T) {
	root := t.TempDir()
	repo, _ := newTestRepo(t, root)

	oldP := &GitWorktreeProvider{} // default sibling layout
	old, err := oldP.Create(repo, "feat-x", "feat/x", "")
	if err != nil {
		t.Fatalf("Create old: %v", err)
	}
	newP := &GitWorktreeProvider{PathTemplate: "../aaa/{{repo}}-{{name}}"}
	newer, err := newP.Create(repo, "feat-x", "feat-x", "")
	if err != nil {
		t.Fatalf("Create new: %v", err)
	}

	if err := newP.Destroy(old.Path, "feat-x"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(old.Path); !os.IsNotExist(err) {
		t.Errorf("requested checkout %s still exists (stat err %v)", old.Path, err)
	}
	if _, err := os.Stat(newer.Path); err != nil {
		t.Errorf("unrelated checkout %s was removed: %v", newer.Path, err)
	}
}

// A symlink on the way to the destination can put a worktree inside the repo
// while the template looks outside it lexically; Create must refuse.
func TestCreateRefusesSymlinkIntoRepo(t *testing.T) {
	root := t.TempDir()
	repo, _ := newTestRepo(t, root)
	inside := filepath.Join(repo, "worktrees")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "wt-link")); err != nil {
		t.Fatal(err)
	}

	p := &GitWorktreeProvider{PathTemplate: "../wt-link/{{name}}"}
	if _, err := p.Create(repo, "feat-x", "feat/x", ""); err == nil || !strings.Contains(err.Error(), "inside the repo") {
		t.Fatalf("Create through a symlink into the repo: err = %v, want inside-the-repo refusal", err)
	}
	if _, err := os.Stat(filepath.Join(inside, "feat-x")); !os.IsNotExist(err) {
		t.Errorf("worktree was created inside the repo (stat err %v)", err)
	}
}

func TestResolveExistingPrefix(t *testing.T) {
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ in, want string }{
		{filepath.Join(root, "link", "a", "b"), filepath.Join(realRoot, "target", "a", "b")},
		{filepath.Join(root, "missing", "x"), filepath.Join(realRoot, "missing", "x")},
		{filepath.Join(root, "target"), filepath.Join(realRoot, "target")},
	}
	for _, tt := range tests {
		got, err := resolveExistingPrefix(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("resolveExistingPrefix(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
