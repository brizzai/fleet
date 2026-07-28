package session

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
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

	// writeTranscript writes a JSONL transcript under a temp HOME and returns the
	// (claudeSessionID, projectPath) to read it back.
	writeTranscript := func(t *testing.T, name, content string) (string, string) {
		t.Helper()
		tmpHome := t.TempDir()
		t.Setenv("HOME", tmpHome)
		projectPath := "/test/" + name
		claudeSessionID := "sid-" + name
		projectDir := filepath.Join(tmpHome, ".claude", "projects", ClaudeProjectDirName(projectPath))
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(projectDir, claudeSessionID+".jsonl"), []byte(content), 0o644); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		return claudeSessionID, projectPath
	}

	t.Run("ai-title only returns last aiTitle", func(t *testing.T) {
		id, path := writeTranscript(t, "ai-only", `{"type":"message","content":"hi"}
{"type":"ai-title","aiTitle":"First Topic"}
{"type":"message","content":"more"}
{"type":"ai-title","aiTitle":"Second Topic"}
`)
		if got := ReadClaudeSessionName(id, path); got != "Second Topic" {
			t.Errorf("got %q, want %q", got, "Second Topic")
		}
	})

	t.Run("custom-title wins over ai-title regardless of order", func(t *testing.T) {
		id, path := writeTranscript(t, "custom-wins", `{"type":"ai-title","aiTitle":"Auto Title"}
{"type":"custom-title","customTitle":"My Name"}
{"type":"ai-title","aiTitle":"Auto Title Drifted"}
`)
		if got := ReadClaudeSessionName(id, path); got != "My Name" {
			t.Errorf("got %q, want %q", got, "My Name")
		}
	})

	t.Run("evolving ai-title returns the latest", func(t *testing.T) {
		id, path := writeTranscript(t, "evolving", `{"type":"ai-title","aiTitle":"Boot Splash Work"}
{"type":"ai-title","aiTitle":"Mutex Refactor"}
{"type":"ai-title","aiTitle":"Title Swap"}
`)
		if got := ReadClaudeSessionName(id, path); got != "Title Swap" {
			t.Errorf("got %q, want %q", got, "Title Swap")
		}
	})

	t.Run("empty aiTitle is skipped", func(t *testing.T) {
		id, path := writeTranscript(t, "empty-ai", `{"type":"ai-title","aiTitle":""}
{"type":"message","content":"hi"}
`)
		if got := ReadClaudeSessionName(id, path); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("kebab ai-title is de-slugified, case preserved", func(t *testing.T) {
		id, path := writeTranscript(t, "kebab-ai", `{"type":"ai-title","aiTitle":"native-ai-title-integration"}
`)
		if got := ReadClaudeSessionName(id, path); got != "native ai title integration" {
			t.Errorf("got %q, want %q", got, "native ai title integration")
		}
	})

	t.Run("de-slugify keeps existing uppercase acronyms", func(t *testing.T) {
		id, path := writeTranscript(t, "acronym-ai", `{"type":"ai-title","aiTitle":"fix-API-client"}
`)
		if got := ReadClaudeSessionName(id, path); got != "fix API client" {
			t.Errorf("got %q, want %q", got, "fix API client")
		}
	})

	t.Run("sentence-case ai-title is left unchanged", func(t *testing.T) {
		id, path := writeTranscript(t, "sentence-ai", `{"type":"ai-title","aiTitle":"Improve onboarding for Brizz users"}
`)
		if got := ReadClaudeSessionName(id, path); got != "Improve onboarding for Brizz users" {
			t.Errorf("got %q, want unchanged", got)
		}
	})

	t.Run("custom-title slug is NOT de-slugified (explicit rename)", func(t *testing.T) {
		id, path := writeTranscript(t, "custom-slug", `{"type":"ai-title","aiTitle":"some-auto-title"}
{"type":"custom-title","customTitle":"my-pinned-name"}
`)
		if got := ReadClaudeSessionName(id, path); got != "my-pinned-name" {
			t.Errorf("got %q, want %q (custom-title verbatim)", got, "my-pinned-name")
		}
	})
}

func TestDeslugify(t *testing.T) {
	cases := map[string]string{
		"native-ai-title-integration":  "native ai title integration",
		"fix-API-client":               "fix API client",
		"snake_case_title":             "snake case title",
		"Improve onboarding for Brizz": "Improve onboarding for Brizz", // has spaces → unchanged
		"single":                       "single",                       // no separator → unchanged
		"":                             "",
		"well-known issue":             "well-known issue", // contains a space → natural language, unchanged
	}
	for in, want := range cases {
		if got := deslugify(in); got != want {
			t.Errorf("deslugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastLeadTranscriptTimestamp(t *testing.T) {
	t.Run("returns last lead entry, skipping sidechain", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "t.jsonl")
		content := strings.Join([]string{
			`{"type":"user","timestamp":"2026-06-16T11:25:00.000Z"}`,
			`{"type":"assistant","timestamp":"2026-06-16T11:26:00.000Z"}`,
			// A sub-agent (sidechain) entry AFTER the last lead entry must be ignored.
			`{"type":"assistant","isSidechain":true,"timestamp":"2026-06-16T11:30:00.000Z"}`,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got := lastLeadTranscriptTimestamp(path)
		want, _ := time.Parse(time.RFC3339Nano, "2026-06-16T11:26:00.000Z")
		if !got.Equal(want) {
			t.Errorf("lastLeadTranscriptTimestamp = %v, want %v (sidechain entry should be skipped)", got, want)
		}
	})

	t.Run("missing file returns zero", func(t *testing.T) {
		if got := lastLeadTranscriptTimestamp(filepath.Join(t.TempDir(), "nope.jsonl")); !got.IsZero() {
			t.Errorf("expected zero time for missing file, got %v", got)
		}
	})

	t.Run("empty path returns zero", func(t *testing.T) {
		if got := lastLeadTranscriptTimestamp(""); !got.IsZero() {
			t.Errorf("expected zero time for empty path, got %v", got)
		}
	})

	t.Run("only sidechain entries returns zero", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "t.jsonl")
		content := `{"type":"assistant","isSidechain":true,"timestamp":"2026-06-16T11:30:00.000Z"}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := lastLeadTranscriptTimestamp(path); !got.IsZero() {
			t.Errorf("expected zero time when all entries are sidechain, got %v", got)
		}
	})
}

// TestTranscriptScanSurvivesOversizedLine covers the bug where a single JSONL line
// larger than the reader's buffer silently ended every transcript walk.
//
// bufio.Scanner reports an over-long line by returning false from Scan() and
// setting ErrTooLong — indistinguishable from a clean EOF unless Err() is checked,
// which none of these callers did. The scan therefore answered from whatever it had
// read *before* the long line, and answered confidently: on a real 12,826-line
// transcript whose first oversized entry sat at line 4,341, lastLeadTranscriptTimestamp
// returned a timestamp sixteen days stale. That silently disabled the
// conversationActivePastHook tiebreaker, so one between-bursts frame flipped a
// mid-turn session to finished.
//
// An entry carrying a pasted image or a large tool result is a single JSON line, so
// this is ordinary transcript content, not corruption.
func TestTranscriptScanSurvivesOversizedLine(t *testing.T) {
	early := time.Date(2026, 7, 12, 14, 26, 47, 0, time.UTC)
	late := time.Date(2026, 7, 28, 12, 31, 53, 0, time.UTC)
	lateUUID := "11111111-2222-3333-4444-555555555555"

	entry := func(ts time.Time, pad int, uuid string) string {
		var b strings.Builder
		b.WriteString(`{"type":"user","uuid":"` + uuid + `","timestamp":"`)
		b.WriteString(ts.UTC().Format(time.RFC3339Nano))
		b.WriteString(`","data":"` + strings.Repeat("x", pad) + `"}` + "\n")
		return b.String()
	}

	// Oversized but under transcriptLineCap: its own timestamp must still be read.
	// Ordering matters — everything the bug hid lives after the long line.
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := entry(early, 10, "aaaaaaaa-0000-0000-0000-000000000000") +
		entry(early.Add(time.Minute), 2<<20, "bbbbbbbb-0000-0000-0000-000000000000") +
		entry(late, 10, lateUUID)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := lastLeadTranscriptTimestamp(path); !got.Equal(late) {
		t.Errorf("lastLeadTranscriptTimestamp = %v, want %v (scan stopped at the oversized line)", got, late)
	}
	if got := lastTranscriptTimestamp(path); !got.Equal(late) {
		t.Errorf("lastTranscriptTimestamp = %v, want %v", got, late)
	}
	// sessionRotationVerdict's parent-link signal reads the owner transcript this
	// way; a uuid past the long line must not read as absent.
	if !transcriptContainsUUID(path, lateUUID) {
		t.Error("transcriptContainsUUID missed a uuid located after the oversized line")
	}

	// Past the cap the line itself is dropped, but the walk must continue: the entry
	// after it is still found. Losing one entry is recoverable; losing the tail is not.
	huge := filepath.Join(t.TempDir(), "huge.jsonl")
	body = entry(early, 10, "aaaaaaaa-0000-0000-0000-000000000000") +
		entry(early.Add(time.Minute), transcriptLineCap+1, "bbbbbbbb-0000-0000-0000-000000000000") +
		entry(late, 10, lateUUID)
	if err := os.WriteFile(huge, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lastLeadTranscriptTimestamp(huge); !got.Equal(late) {
		t.Errorf("over-cap line aborted the walk: got %v, want %v", got, late)
	}
	if !transcriptContainsUUID(huge, lateUUID) {
		t.Error("over-cap line aborted the walk before a later uuid")
	}
}

// TestForEachTranscriptLineReportsReadErrors guards the property whose absence caused
// the bug: a truncated walk must be distinguishable from a complete one.
func TestForEachTranscriptLineReportsReadErrors(t *testing.T) {
	if err := forEachTranscriptLine(filepath.Join(t.TempDir(), "missing.jsonl"), func(string) bool {
		return true
	}); err == nil {
		t.Error("expected an error for a missing transcript, got nil")
	}

	// A final line with no trailing newline must still be delivered.
	path := filepath.Join(t.TempDir(), "noeol.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"b\":2}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := forEachTranscriptLine(path, func(line string) bool {
		got = append(got, line)
		return true
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[1] != `{"b":2}` {
		t.Errorf("lines = %q, want both entries including the unterminated last one", got)
	}
}

// TestWarnTranscriptThrottles guards the property that makes these warnings
// affordable. They sit on per-cycle paths — ReadClaudeSessionName re-reads a
// transcript on every worker cycle while a session is unnamed — and report sticky
// conditions (EACCES does not clear itself; an over-cap entry recurs on every
// scan). Unthrottled they write a line per session per cycle forever, and since
// debug.log's last 100 lines are what the bug-report flow publishes, that drip
// evicts the diagnostics the warnings exist to preserve.
func TestWarnTranscriptThrottles(t *testing.T) {
	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	t.Cleanup(func() { debuglog.Logger = prev })

	const reason = "transcript: test reason"
	warnTranscript(reason, "/tmp/a.jsonl")
	warnTranscript(reason, "/tmp/a.jsonl") // same pair — suppressed
	warnTranscript(reason, "/tmp/a.jsonl") // still suppressed

	if got := strings.Count(buf.String(), reason); got != 1 {
		t.Errorf("same (reason, path) logged %d times, want 1", got)
	}

	// A different path is a different condition and must still be reported —
	// throttling per-pair rather than globally keeps one noisy transcript from
	// masking a second one.
	warnTranscript(reason, "/tmp/b.jsonl")
	if got := strings.Count(buf.String(), reason); got != 2 {
		t.Errorf("second path logged %d total, want 2 (a distinct path must not be throttled)", got)
	}

	// The path is always attached, so a report names the transcript at fault.
	if !strings.Contains(buf.String(), "/tmp/b.jsonl") {
		t.Error("warning omitted the transcript path")
	}
}
