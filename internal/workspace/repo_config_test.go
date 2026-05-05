package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProvider(t *testing.T) {
	t.Run("no config files returns GitWorktreeProvider", func(t *testing.T) {
		repo := t.TempDir()
		got := ResolveProvider(repo)
		if _, ok := got.(*GitWorktreeProvider); !ok {
			t.Errorf("got %T, want *GitWorktreeProvider", got)
		}
	})

	t.Run(".fleet.json with shell commands wins", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"workspace":{"create":"new-fleet","destroy":"destroy-fleet"}}`)
		got, ok := ResolveProvider(repo).(*ShellProvider)
		if !ok {
			t.Fatalf("got %T, want *ShellProvider", got)
		}
		if got.CreateCmd != "new-fleet" {
			t.Errorf("CreateCmd: got %q, want %q", got.CreateCmd, "new-fleet")
		}
	})

	t.Run("legacy .bc.json is used when .fleet.json absent", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".bc.json", `{"workspace":{"create":"new-bc"}}`)
		got, ok := ResolveProvider(repo).(*ShellProvider)
		if !ok {
			t.Fatalf("got %T, want *ShellProvider", got)
		}
		if got.CreateCmd != "new-bc" {
			t.Errorf("CreateCmd: got %q, want %q", got.CreateCmd, "new-bc")
		}
	})

	t.Run("empty .fleet.json suppresses .bc.json (presence wins)", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{}`)
		writeFile(t, repo, ".bc.json", `{"workspace":{"create":"new-bc"}}`)
		got := ResolveProvider(repo)
		if _, ok := got.(*GitWorktreeProvider); !ok {
			t.Errorf("got %T, want *GitWorktreeProvider — empty .fleet.json should suppress .bc.json", got)
		}
	})

	t.Run("malformed .fleet.json still suppresses .bc.json", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{not valid json`)
		writeFile(t, repo, ".bc.json", `{"workspace":{"create":"new-bc"}}`)
		got := ResolveProvider(repo)
		if _, ok := got.(*GitWorktreeProvider); !ok {
			t.Errorf("got %T, want *GitWorktreeProvider — malformed .fleet.json should still suppress .bc.json", got)
		}
	})

	t.Run("unreadable .fleet.json still suppresses .bc.json (presence-wins)", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"workspace":{"create":"new-fleet"}}`)
		writeFile(t, repo, ".bc.json", `{"workspace":{"create":"new-bc"}}`)
		// Strip read perms so loadRepoConfig sees a non-ENOENT error.
		fleetPath := filepath.Join(repo, ".fleet.json")
		if err := os.Chmod(fleetPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fleetPath, 0o644) })

		got := ResolveProvider(repo)
		if _, ok := got.(*GitWorktreeProvider); !ok {
			t.Errorf("got %T, want *GitWorktreeProvider — unreadable .fleet.json should still suppress .bc.json", got)
		}
	})

	t.Run("local override is field-by-field", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"workspace":{"create":"base-create","destroy":"base-destroy"}}`)
		writeFile(t, repo, ".fleet.local.json", `{"workspace":{"create":"local-create"}}`)
		got, ok := ResolveProvider(repo).(*ShellProvider)
		if !ok {
			t.Fatalf("got %T, want *ShellProvider", got)
		}
		if got.CreateCmd != "local-create" {
			t.Errorf("CreateCmd: got %q, want %q (local should override)", got.CreateCmd, "local-create")
		}
		if got.DestroyCmd != "base-destroy" {
			t.Errorf("DestroyCmd: got %q, want %q (base should be preserved)", got.DestroyCmd, "base-destroy")
		}
	})
}

func TestIgnorePatterns(t *testing.T) {
	t.Run("no config returns nil", func(t *testing.T) {
		repo := t.TempDir()
		if got := IgnorePatterns(repo); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("reads from .fleet.json", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"pr_checks":{"ignore":["minimum-review/*","gitStream.cm"]}}`)
		got := IgnorePatterns(repo)
		want := []string{"minimum-review/*", "gitStream.cm"}
		if !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("base + local merge additively with dedupe", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"pr_checks":{"ignore":["a","b"]}}`)
		writeFile(t, repo, ".fleet.local.json", `{"pr_checks":{"ignore":["b","c"]}}`)
		got := IgnorePatterns(repo)
		want := []string{"a", "b", "c"}
		if !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("legacy .bc.json honored", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".bc.json", `{"pr_checks":{"ignore":["legacy/*"]}}`)
		got := IgnorePatterns(repo)
		want := []string{"legacy/*"}
		if !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("invalid globs dropped at load time", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"pr_checks":{"ignore":["[","good","[bad"]}}`)
		got := IgnorePatterns(repo)
		want := []string{"good"}
		if !equalStrings(got, want) {
			t.Errorf("got %v, want %v (invalid globs should be filtered)", got, want)
		}
	})

	t.Run("workspace and pr_checks coexist", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".fleet.json", `{"workspace":{"create":"mk"},"pr_checks":{"ignore":["x"]}}`)
		if got := IgnorePatterns(repo); !equalStrings(got, []string{"x"}) {
			t.Errorf("ignore: got %v, want [x]", got)
		}
		sp, ok := ResolveProvider(repo).(*ShellProvider)
		if !ok {
			t.Fatalf("got %T, want *ShellProvider", sp)
		}
		if sp.CreateCmd != "mk" {
			t.Errorf("CreateCmd: got %q, want mk", sp.CreateCmd)
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
