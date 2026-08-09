package claudeaccount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A fresh config dir is a fresh Claude Code install as far as Claude Code is
// concerned, so it opens on the theme picker and the welcome flow — inside a
// pane whose only job is a login. These are settled answers, not per-login
// state, so they travel.
func TestProvisionCarriesTheAnswersTheUserAlreadyGave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
		"theme": "dark",
		"hasCompletedOnboarding": true,
		"installMethod": "native",
		"mcpServers": {"context7": {"command": "npx"}},
		"oauthAccount": {"emailAddress": "someone@x.com"},
		"numStartups": 3991,
		"projects": {"/repo": {"hasTrustDialogAccepted": true, "history": ["secret"]}}
	}`), 0600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}

	dir := filepath.Join(home, "acct")
	if err := Provision(dir); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	var got map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read account .claude.json: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if got["theme"] != "dark" || got["hasCompletedOnboarding"] != true {
		t.Errorf("onboarding answers did not travel: %v", got)
	}
	if got["mcpServers"] == nil {
		t.Error("MCP servers did not travel; the session would lose them")
	}
	// Identity must not: claiming a login the dir hasn't authenticated as would
	// be lying to Claude Code about who is logged in.
	if _, ok := got["oauthAccount"]; ok {
		t.Error("oauthAccount was copied — the dir would claim a login it does not have")
	}
	// Installation-specific state is not a settled answer, it is someone else's
	// counter.
	if _, ok := got["numStartups"]; ok {
		t.Error("unrelated installation state was copied wholesale")
	}
	// Folder trust travels; the project's conversation history does not.
	projects, _ := got["projects"].(map[string]any)
	repo, _ := projects["/repo"].(map[string]any)
	if repo["hasTrustDialogAccepted"] != true {
		t.Error("folder trust did not travel; every session would re-prompt")
	}
	if _, ok := repo["history"]; ok {
		t.Error("project history was dragged into an account that never had it")
	}
}

// Provision runs on every launch, so it must not fight itself: a second pass
// leaves the symlinks and the mirrored keys exactly as they were.
func TestProvisionIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dir := filepath.Join(home, "acct")
	for i := range 2 {
		if err := Provision(dir); err != nil {
			t.Fatalf("Provision pass %d: %v", i+1, err)
		}
	}
	target, err := os.Readlink(filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatalf("projects is not a symlink after two passes: %v", err)
	}
	if target != filepath.Join(home, ".claude", "projects") {
		t.Errorf("projects -> %q, want the shared dir", target)
	}
}

// The account dir is fleet's to delete, and the path comes out of a JSON file.
// RemoveConfigDir must refuse anything that isn't one of ours.
func TestRemoveConfigDirRefusesPathsItDoesNotOwn(t *testing.T) {
	for _, bad := range []string{"", "relative/path", "/", "/Users/someone/.claude", filepath.Join(AccountsRoot(), "a", "b")} {
		if err := RemoveConfigDir(bad); err == nil {
			t.Errorf("RemoveConfigDir(%q) was accepted; it hands paths to RemoveAll", bad)
		}
	}
}
