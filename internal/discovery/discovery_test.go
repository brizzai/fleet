package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript creates a fake Claude project dir + transcript and a real
// repo dir on disk, then back-dates the transcript so recency is testable.
func writeTranscript(t *testing.T, home, projDir, repoPath string, lines []string, mod time.Time) {
	t.Helper()
	// Real repo dir with a .git dir so it passes the git check.
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", projDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestRecentRepos(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoOld := filepath.Join(home, "code", "old-proj")
	repoNew := filepath.Join(home, "code", "new-proj")

	// The cwd in the transcript is the source of truth (dash-safe).
	writeTranscript(t, home, "proj-old", repoOld, []string{
		`{"type":"user","isMeta":true,"cwd":"` + repoOld + `","gitBranch":"main","message":{"role":"user","content":"<local-command-caveat>/clear</local-command-caveat>"}}`,
		`{"type":"user","cwd":"` + repoOld + `","gitBranch":"main","message":{"role":"user","content":"please fix the old login bug"}}`,
	}, time.Now().Add(-48*time.Hour))

	writeTranscript(t, home, "proj-new", repoNew, []string{
		`{"type":"user","cwd":"` + repoNew + `","gitBranch":"feature/x","message":{"role":"user","content":[{"type":"text","text":"add the launchpad onboarding"}]}}`,
	}, time.Now().Add(-1*time.Hour))

	got := RecentRepos(10)
	if len(got) != 2 {
		t.Fatalf("want 2 repos, got %d: %+v", len(got), got)
	}

	// Most-recently-used first.
	if got[0].Path != repoNew {
		t.Errorf("want newest first (%s), got %s", repoNew, got[0].Path)
	}
	if got[0].Branch != "feature/x" {
		t.Errorf("branch: want feature/x, got %q", got[0].Branch)
	}
	if got[0].ClaudeSessionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("session id not recovered from filename: %q", got[0].ClaudeSessionID)
	}
	if got[0].Title == "" {
		t.Error("title should be derived from the array-form prompt")
	}

	// The /clear caveat must be skipped in favor of the real prompt.
	if got[1].Title == "" || got[1].Path != repoOld {
		t.Errorf("old repo: title=%q path=%q", got[1].Title, got[1].Path)
	}
}

func TestRecentReposSkipsNonGitAndMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A transcript whose cwd is a plain (non-git) dir → dropped.
	plain := filepath.Join(home, "scratch")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", "plain")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "aaa.jsonl"),
		[]byte(`{"type":"user","cwd":"`+plain+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)

	// A transcript whose cwd no longer exists → dropped.
	dir2 := filepath.Join(home, ".claude", "projects", "gone")
	_ = os.MkdirAll(dir2, 0o755)
	_ = os.WriteFile(filepath.Join(dir2, "bbb.jsonl"),
		[]byte(`{"type":"user","cwd":"/nope/does/not/exist","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)

	if got := RecentRepos(10); len(got) != 0 {
		t.Fatalf("want 0 repos (non-git + missing dropped), got %d: %+v", len(got), got)
	}
}

func TestRecentReposEmptyHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := RecentRepos(10); got != nil {
		t.Fatalf("want nil for missing ~/.claude/projects, got %+v", got)
	}
}
