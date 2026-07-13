package session

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
)

// writeCodexStateDB creates a Codex-shaped state DB at dir/state_<version>.sqlite
// and seeds it with the given threads. Only the columns fleet reads are modeled.
func writeCodexStateDB(t *testing.T, dir string, version int, threads map[string][2]string) string {
	t.Helper()

	path := filepath.Join(dir, "state_"+strconv.Itoa(version)+".sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		first_user_message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for id, tf := range threads {
		if _, err := db.Exec("INSERT INTO threads (id, title, first_user_message) VALUES (?, ?, ?)",
			id, tf[0], tf[1]); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	return path
}

func TestReadCodexSessionName(t *testing.T) {
	longPrompt := "please go read every file in the repo and " +
		"tell me what you think about the architecture, in detail"

	dir := t.TempDir()
	writeCodexStateDB(t, dir, 5, map[string][2]string{
		// Codex seeds title with the raw first user message — not a rename.
		"untouched": {longPrompt, longPrompt},
		// The user ran /rename.
		"renamed": {"my new title", longPrompt},
		// Defensive: a blank title is nothing to adopt.
		"blank": {"", longPrompt},
	})
	t.Setenv("CODEX_HOME", dir)

	tests := []struct {
		name     string
		threadID string
		want     string
	}{
		{"title still equals first prompt is not a rename", "untouched", ""},
		{"renamed thread returns the new title", "renamed", "my new title"},
		{"blank title returns empty", "blank", ""},
		{"unknown thread returns empty", "nope", ""},
		{"empty thread id returns empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadCodexSessionName(tt.threadID); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadCodexSessionNameNoStateDB(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir()) // exists, but holds no state_*.sqlite
	if got := ReadCodexSessionName("renamed"); got != "" {
		t.Errorf("got %q, want %q", got, "")
	}
}

// Codex bumps the state DB's version suffix with its schema. The newest one is
// the live one — reading a stale state_5 after an upgrade would serve a title
// frozen at the upgrade, or none at all for threads created since.
func TestReadCodexSessionNamePicksHighestStateVersion(t *testing.T) {
	dir := t.TempDir()
	writeCodexStateDB(t, dir, 5, map[string][2]string{"t": {"stale title", "prompt"}})
	writeCodexStateDB(t, dir, 12, map[string][2]string{"t": {"live title", "prompt"}})
	t.Setenv("CODEX_HOME", dir)

	if got := ReadCodexSessionName("t"); got != "live title" {
		t.Errorf("got %q, want %q", got, "live title")
	}
}

func TestReadAgentSessionName(t *testing.T) {
	dir := t.TempDir()
	writeCodexStateDB(t, dir, 5, map[string][2]string{"thread": {"codex title", "prompt"}})
	t.Setenv("CODEX_HOME", dir)
	// Point Claude's transcript lookup at a dir with no JSONL, so a Claude
	// session reads as untitled rather than picking up a real transcript.
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name  string
		agent agent.Type
		want  string
	}{
		{"codex reads the state db", agent.Codex, "codex title"},
		{"claude reads the transcript (none here)", agent.Claude, ""},
		{"legacy session with no agent is treated as claude", agent.Type(""), ""},
		{"opencode has no reader yet", agent.OpenCode, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadAgentSessionName(tt.agent, "thread", "/tmp/some-repo"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
