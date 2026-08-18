package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExcludeFilePathUsesGitPath is a source guard. The natural implementation
// — `rev-parse --git-dir` joined with "info/exclude" — writes a file git never
// reads when called from a linked worktree, and every "did we write it?" test
// still passes. Only `--git-path` resolves what git actually opens.
func TestExcludeFilePathUsesGitPath(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exclude.go", nil, 0)
	if err != nil {
		t.Fatalf("parse exclude.go: %v", err)
	}

	var body string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ExcludeFilePath" {
			return true
		}
		var b strings.Builder
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				b.WriteString(lit.Value)
			}
			return true
		})
		body = b.String()
		return false
	})
	if body == "" {
		t.Fatal("ExcludeFilePath not found — rename? this guard is now vacuous")
	}
	if !strings.Contains(body, "--git-path") {
		t.Error("ExcludeFilePath must use `rev-parse --git-path info/exclude`")
	}
	if strings.Contains(body, "--git-dir") {
		t.Error("ExcludeFilePath must NOT use --git-dir: `info` is on git's shared-path " +
			"list, so a linked worktree's --git-dir + info/exclude is a file git never reads")
	}
}

// TestAddFleetExcludeFromLinkedWorktree is the real thing: it asserts git
// itself honours the entry, from inside a worktree, which is the only check
// that catches the --git-dir mistake.
func TestAddFleetExcludeFromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	main := filepath.Join(root, "repo")
	if err := os.MkdirAll(main, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run(main, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(main, "seed"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	run(main, "add", "seed")
	run(main, "commit", "-qm", "seed")

	wt := filepath.Join(root, "repo-wt")
	run(main, "worktree", "add", "-q", "-b", "feature", wt)

	// Twice: idempotence across the shared file is the whole point.
	if err := AddFleetExclude(wt); err != nil {
		t.Fatalf("AddFleetExclude: %v", err)
	}
	if err := AddFleetExclude(main); err != nil {
		t.Fatalf("AddFleetExclude (main): %v", err)
	}

	common := filepath.Join(main, ".git", "info", "exclude")
	data, err := os.ReadFile(common)
	if err != nil {
		t.Fatalf("the entry did not land in the common exclude file (%s): %v", common, err)
	}
	if got := strings.Count(string(data), FleetExcludeEntry); got != 1 {
		t.Errorf("entry appears %d times in %s, want exactly 1:\n%s", got, common, data)
	}

	// The private worktree git dir must NOT have grown an info/exclude — if it
	// did, someone resolved with --git-dir.
	priv := filepath.Join(main, ".git", "worktrees", filepath.Base(wt), "info", "exclude")
	if _, err := os.Stat(priv); err == nil {
		t.Errorf("wrote %s, which git never reads", priv)
	}

	// The verdict that matters: git agrees.
	if err := os.MkdirAll(filepath.Join(wt, ".fleet", "ticket", "BRZ-1"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(wt, ".fleet", "ticket", "BRZ-1", "ticket.md")
	if err := os.WriteFile(target, []byte("#"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "check-ignore", "-q", ".fleet/ticket/BRZ-1/ticket.md")
	cmd.Dir = wt
	if err := cmd.Run(); err != nil {
		t.Errorf("git does not ignore .fleet/ in the worktree (check-ignore exit %v)", err)
	}
}
