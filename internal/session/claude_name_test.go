package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeProjectDirName(t *testing.T) {
	t.Run("plain absolute path", func(t *testing.T) {
		got := ClaudeProjectDirName("/Users/a/code/foo")
		want := "-Users-a-code-foo"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("dots and underscores collapse to dashes", func(t *testing.T) {
		got := ClaudeProjectDirName("/Users/a/code/foo.bar_baz")
		want := "-Users-a-code-foo-bar-baz"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("nonexistent path falls back to Clean", func(t *testing.T) {
		got := ClaudeProjectDirName("/this/does/not/exist/foo.bar")
		want := "-this-does-not-exist-foo-bar"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("symlinked path resolves to realpath", func(t *testing.T) {
		// Create real dir + symlink pointing at it; both should encode to the
		// same dirname (the realpath one).
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		gotReal := ClaudeProjectDirName(real)
		gotLink := ClaudeProjectDirName(link)
		if gotReal != gotLink {
			t.Errorf("symlink and realpath should encode identically: real=%q link=%q", gotReal, gotLink)
		}
	})
}

func TestCopyClaudeForkTranscript(t *testing.T) {
	// Use t.TempDir() for both src and dst project paths, and override the
	// claude projects root by pointing HOME at a temp dir.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srcProject := t.TempDir()
	dstProject := t.TempDir()
	sessionID := "abc-123"

	srcDir := filepath.Join(tmpHome, ".claude", "projects", ClaudeProjectDirName(srcProject))
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	srcFile := filepath.Join(srcDir, sessionID+".jsonl")
	body := []byte(`{"type":"message","content":"hi"}` + "\n")
	if err := os.WriteFile(srcFile, body, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	t.Run("happy path copies into dest project dir", func(t *testing.T) {
		if err := CopyClaudeForkTranscript(sessionID, srcProject, dstProject); err != nil {
			t.Fatalf("copy: %v", err)
		}
		dstFile := filepath.Join(tmpHome, ".claude", "projects", ClaudeProjectDirName(dstProject), sessionID+".jsonl")
		got, err := os.ReadFile(dstFile)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(got) != string(body) {
			t.Errorf("dst content = %q, want %q", got, body)
		}
	})

	t.Run("overwrites existing dest file", func(t *testing.T) {
		// Pre-populate dst with stale content.
		dstDir := filepath.Join(tmpHome, ".claude", "projects", ClaudeProjectDirName(dstProject))
		dstFile := filepath.Join(dstDir, sessionID+".jsonl")
		if err := os.WriteFile(dstFile, []byte("stale\n"), 0o644); err != nil {
			t.Fatalf("seed dst: %v", err)
		}
		if err := CopyClaudeForkTranscript(sessionID, srcProject, dstProject); err != nil {
			t.Fatalf("copy: %v", err)
		}
		got, err := os.ReadFile(dstFile)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(got) != string(body) {
			t.Errorf("dst not overwritten: got %q, want %q", got, body)
		}
	})

	t.Run("missing parent transcript returns ErrParentTranscriptMissing", func(t *testing.T) {
		err := CopyClaudeForkTranscript("does-not-exist", srcProject, dstProject)
		if !errors.Is(err, ErrParentTranscriptMissing) {
			t.Errorf("expected ErrParentTranscriptMissing, got %v", err)
		}
	})

	t.Run("does not leave stray temp file on failure", func(t *testing.T) {
		// Trigger failure via empty session id.
		if err := CopyClaudeForkTranscript("", srcProject, dstProject); err == nil {
			t.Fatal("expected error for empty session id")
		}
		entries, err := os.ReadDir(filepath.Join(tmpHome, ".claude", "projects", ClaudeProjectDirName(dstProject)))
		if err != nil {
			t.Fatalf("read dst dir: %v", err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("stray temp file: %s", e.Name())
			}
		}
	})
}

func TestReadClaudeSessionName(t *testing.T) {
	t.Run("empty claudeSessionID returns empty", func(t *testing.T) {
		got := ReadClaudeSessionName("", "/some/path")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("empty projectPath returns empty", func(t *testing.T) {
		got := ReadClaudeSessionName("some-id", "")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("both empty returns empty", func(t *testing.T) {
		got := ReadClaudeSessionName("", "")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("missing file returns empty", func(t *testing.T) {
		got := ReadClaudeSessionName("nonexistent-id", "/nonexistent/path")
		if got != "" {
			t.Errorf("expected empty for missing file, got %q", got)
		}
	})

	t.Run("valid JSONL with custom-title returns last title", func(t *testing.T) {
		// Set up a temp dir structure mimicking ~/.claude/projects/<dir>/<id>.jsonl
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}

		projectPath := "/test/my-project"
		claudeSessionID := "test-session-abc123"
		dirName := "-test-my-project"

		projectDir := filepath.Join(homeDir, ".claude", "projects", dirName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("failed to create project dir: %v", err)
		}
		t.Cleanup(func() {
			os.RemoveAll(projectDir)
		})

		jsonlPath := filepath.Join(projectDir, claudeSessionID+".jsonl")
		content := `{"type":"message","content":"hello"}
{"type":"custom-title","customTitle":"First Title"}
{"type":"message","content":"world"}
{"type":"custom-title","customTitle":"Second Title"}
{"type":"message","content":"done"}
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write JSONL: %v", err)
		}

		got := ReadClaudeSessionName(claudeSessionID, projectPath)
		if got != "Second Title" {
			t.Errorf("expected %q, got %q", "Second Title", got)
		}
	})

	t.Run("JSONL with no custom-title entries returns empty", func(t *testing.T) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}

		projectPath := "/test/no-titles"
		claudeSessionID := "test-session-notitles"
		dirName := "-test-no-titles"

		projectDir := filepath.Join(homeDir, ".claude", "projects", dirName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("failed to create project dir: %v", err)
		}
		t.Cleanup(func() {
			os.RemoveAll(projectDir)
		})

		jsonlPath := filepath.Join(projectDir, claudeSessionID+".jsonl")
		content := `{"type":"message","content":"hello"}
{"type":"message","content":"world"}
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write JSONL: %v", err)
		}

		got := ReadClaudeSessionName(claudeSessionID, projectPath)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("JSONL with custom-title but empty customTitle is skipped", func(t *testing.T) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}

		projectPath := "/test/empty-title"
		claudeSessionID := "test-session-emptytitle"
		dirName := "-test-empty-title"

		projectDir := filepath.Join(homeDir, ".claude", "projects", dirName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("failed to create project dir: %v", err)
		}
		t.Cleanup(func() {
			os.RemoveAll(projectDir)
		})

		jsonlPath := filepath.Join(projectDir, claudeSessionID+".jsonl")
		content := `{"type":"custom-title","customTitle":""}
{"type":"message","content":"hello"}
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write JSONL: %v", err)
		}

		got := ReadClaudeSessionName(claudeSessionID, projectPath)
		if got != "" {
			t.Errorf("expected empty for empty customTitle, got %q", got)
		}
	})

	t.Run("JSONL with malformed JSON lines are skipped", func(t *testing.T) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}

		projectPath := "/test/malformed"
		claudeSessionID := "test-session-malformed"
		dirName := "-test-malformed"

		projectDir := filepath.Join(homeDir, ".claude", "projects", dirName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("failed to create project dir: %v", err)
		}
		t.Cleanup(func() {
			os.RemoveAll(projectDir)
		})

		jsonlPath := filepath.Join(projectDir, claudeSessionID+".jsonl")
		content := `not valid json custom-title
{"type":"custom-title","customTitle":"Valid Title"}
{broken json custom-title
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write JSONL: %v", err)
		}

		got := ReadClaudeSessionName(claudeSessionID, projectPath)
		if got != "Valid Title" {
			t.Errorf("expected %q, got %q", "Valid Title", got)
		}
	})
}
