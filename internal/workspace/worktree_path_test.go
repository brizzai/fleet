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
