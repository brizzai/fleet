package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyConfiguredFiles(t *testing.T) {
	t.Run("copies files, glob matches, and directory trees", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		writeFile(t, src, ".env", "ENV=1")
		mustMkdir(t, filepath.Join(src, "config"))
		writeFile(t, filepath.Join(src, "config"), "a.local.json", "A")
		writeFile(t, filepath.Join(src, "config"), "b.local.json", "B")
		writeFile(t, filepath.Join(src, "config"), "shared.json", "SHARED") // should NOT copy
		mustMkdir(t, filepath.Join(src, ".vscode"))
		writeFile(t, filepath.Join(src, ".vscode"), "settings.json", "VS")
		writeFile(t, src, "README.md", "READ") // should NOT copy

		writeFile(t, src, ".fleet.json", `{"copy_files":{"paths":[".env","config/*.local.json",".vscode"]}}`)

		CopyConfiguredFiles(src, dst)

		assertFile(t, filepath.Join(dst, ".env"), "ENV=1")
		assertFile(t, filepath.Join(dst, "config", "a.local.json"), "A")
		assertFile(t, filepath.Join(dst, "config", "b.local.json"), "B")
		assertFile(t, filepath.Join(dst, ".vscode", "settings.json"), "VS")
		assertMissing(t, filepath.Join(dst, "config", "shared.json"))
		assertMissing(t, filepath.Join(dst, "README.md"))
	})

	t.Run("merges .fleet.json and .fleet.local.json additively", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		writeFile(t, src, "shared.env", "S")
		writeFile(t, src, "personal.env", "P")
		writeFile(t, src, ".fleet.json", `{"copy_files":{"paths":["shared.env"]}}`)
		writeFile(t, src, ".fleet.local.json", `{"copy_files":{"paths":["personal.env"]}}`)

		CopyConfiguredFiles(src, dst)

		assertFile(t, filepath.Join(dst, "shared.env"), "S")
		assertFile(t, filepath.Join(dst, "personal.env"), "P")
	})

	t.Run("rejects patterns escaping repo root", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		writeFile(t, src, ".fleet.json", `{"copy_files":{"paths":["../escape.txt","/abs/path"]}}`)

		// Must not panic and must not create anything in dst.
		CopyConfiguredFiles(src, dst)

		entries, err := os.ReadDir(dst)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty dst, got %d entries", len(entries))
		}
	})

	t.Run("no config is a no-op", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		writeFile(t, src, ".env", "X")
		CopyConfiguredFiles(src, dst)
		assertMissing(t, filepath.Join(dst, ".env"))
	})

	t.Run("missing source paths are silently skipped", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		writeFile(t, src, ".fleet.json", `{"copy_files":{"paths":["does-not-exist.txt"]}}`)
		CopyConfiguredFiles(src, dst) // must not panic
	})
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s: got %q, want %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists but should not (err=%v)", path, err)
	}
}
