package session

import (
	"bytes"
	"database/sql"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

// withTokens installs an account-token resolver for the duration of a test.
func withTokens(t *testing.T, tokens map[string]string) {
	t.Helper()
	SetAccountTokenFunc(func(email string) string { return tokens[email] })
	t.Cleanup(func() { SetAccountTokenFunc(nil) })
}

func tokenIn(env []string) (string, bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN="); ok {
			return v, true
		}
	}
	return "", false
}

func TestSessionEnvSetsTokenForClaudeAccount(t *testing.T) {
	withTokens(t, map[string]string{"work@x.com": "sk-ant-oat01-work"})
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	got, ok := tokenIn(s.sessionEnv())
	if !ok {
		t.Fatal("no CLAUDE_CODE_OAUTH_TOKEN in env — the session would run on the ambient login")
	}
	if got != "sk-ant-oat01-work" {
		t.Fatalf("token = %q, want the work account's", got)
	}
}

func TestSessionEnvOmitsTokenWithoutAccount(t *testing.T) {
	// The regression that matters most: with no accounts configured the env
	// must be byte-identical to what fleet produced before this feature.
	withTokens(t, map[string]string{"work@x.com": "sk-ant-oat01-work"})
	s := &Session{ID: "abc", Agent: agent.Claude}

	env := s.sessionEnv()
	if _, ok := tokenIn(env); ok {
		t.Fatal("token set for a session with no account")
	}
	want := []string{"FLEET_INSTANCE_ID=abc", "ZSH_DOTENV_PROMPT=false"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestSessionEnvOmitsTokenForNonClaudeAgents(t *testing.T) {
	// CLAUDE_CODE_OAUTH_TOKEN is a claude.ai subscription credential; Codex and
	// OpenCode neither read nor need it.
	withTokens(t, map[string]string{"work@x.com": "sk-ant-oat01-work"})
	for _, ag := range []agent.Type{agent.Codex, agent.OpenCode} {
		s := &Session{ID: "abc", Agent: ag, Account: "work@x.com"}
		if _, ok := tokenIn(s.sessionEnv()); ok {
			t.Errorf("token set for %s session", ag)
		}
	}
}

func TestSessionEnvTreatsLegacyEmptyAgentAsClaude(t *testing.T) {
	// Rows written before the agent column default to "", which agent.Parse
	// resolves to Claude — the token must follow that same resolution.
	withTokens(t, map[string]string{"work@x.com": "sk-ant-oat01-work"})
	s := &Session{ID: "abc", Account: "work@x.com"}

	if _, ok := tokenIn(s.sessionEnv()); !ok {
		t.Fatal("legacy empty agent should resolve to Claude and get a token")
	}
}

func TestSessionEnvFallsBackWhenAccountUnknown(t *testing.T) {
	// An account removed from the store while sessions still reference it must
	// degrade to the ambient login, not fail to launch.
	withTokens(t, map[string]string{})
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "deleted@x.com"}

	if _, ok := tokenIn(s.sessionEnv()); ok {
		t.Fatal("token set for an account the store no longer knows")
	}
}

func TestSessionEnvWithNoResolverInstalled(t *testing.T) {
	// fleet runs this way whenever no accounts are configured.
	SetAccountTokenFunc(nil)
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	if _, ok := tokenIn(s.sessionEnv()); ok {
		t.Fatal("token set with no resolver installed")
	}
}

// A pre-multi-account database must gain the column and keep every existing
// row loadable, with the empty account that means "ambient /login".
func TestAccountColumnMigratesExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Build a database at the schema that shipped before the account column.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, project_path TEXT NOT NULL,
			status TEXT NOT NULL, tmux_session TEXT NOT NULL,
			created_at INTEGER NOT NULL, last_accessed INTEGER NOT NULL,
			acknowledged INTEGER NOT NULL DEFAULT 0, claude_session_id TEXT NOT NULL DEFAULT '',
			workspace_name TEXT NOT NULL DEFAULT '', manually_renamed INTEGER NOT NULL DEFAULT 0,
			first_prompt TEXT NOT NULL DEFAULT '', title_generated INTEGER NOT NULL DEFAULT 0,
			prompt_count INTEGER NOT NULL DEFAULT 0, snoozed_until INTEGER NOT NULL DEFAULT 0,
			agent TEXT NOT NULL DEFAULT 'claude'
		)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO sessions (id, title, project_path, status, tmux_session, created_at, last_accessed)
		 VALUES ('old-1', 'legacy session', '/tmp/repo', 'idle', 'fleet_old', 1, 1)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migrate) failed: %v", err)
	}
	defer db.Close()

	rows, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions after migration failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("loaded %d rows, want 1", len(rows))
	}
	if rows[0].Account != "" {
		t.Errorf("migrated row account = %q, want empty (ambient login)", rows[0].Account)
	}

	// And the new column round-trips on the migrated database.
	rows[0].Account = "work@x.com"
	if err := db.SaveSession(rows[0]); err != nil {
		t.Fatalf("SaveSession after migration failed: %v", err)
	}
	again, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if again[0].Account != "work@x.com" {
		t.Errorf("account = %q after save, want work@x.com", again[0].Account)
	}
}

func TestUpdateAccount(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	row := &SessionRow{ID: "s1", Title: "t", ProjectPath: "/tmp", Agent: "claude", Account: "a@x.com", Status: "idle", TmuxSession: "fleet_s1"}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	// Unlike agent, account is mutable — a spent account can be moved off.
	if err := db.UpdateAccount("s1", "b@x.com"); err != nil {
		t.Fatalf("UpdateAccount failed: %v", err)
	}
	rows, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if rows[0].Account != "b@x.com" {
		t.Errorf("account = %q, want b@x.com", rows[0].Account)
	}
}

// sessionEnv logs on every launch, and debug.log is pasted into public issues
// by the bug-report flow. It must name the account but never the credential.
func TestSessionEnvLogsAccountButNeverTheToken(t *testing.T) {
	const token = "sk-ant-oat01-AbCdEf0123456789_-GhIjKlMnOpQrStUvWxYz0123456789AbCdEf"
	withTokens(t, map[string]string{"work@x.com": token})

	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { debuglog.Logger = prev })

	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}
	if _, ok := tokenIn(s.sessionEnv()); !ok {
		t.Fatal("expected the token to be applied")
	}

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("token leaked into the debug log:\n%s", out)
	}
	// The account is the whole point of the line — "which account did this
	// session launch as" is where every multi-account report starts.
	if !strings.Contains(out, "work@x.com") {
		t.Errorf("launch log should name the account:\n%s", out)
	}
}

func TestSessionEnvLogsTheAmbientFallback(t *testing.T) {
	// A session pointing at a removed account silently falls back; without a
	// log line that is invisible and reads as "multi-account just doesn't work".
	withTokens(t, map[string]string{})

	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { debuglog.Logger = prev })

	s := &Session{ID: "abc", Agent: agent.Claude, Account: "deleted@x.com"}
	s.sessionEnv()

	if !strings.Contains(buf.String(), "deleted@x.com") {
		t.Errorf("fallback should name the unresolvable account:\n%s", buf.String())
	}
}

func TestAccountSurvivesRowRoundTrip(t *testing.T) {
	s := &Session{ID: "abc", Title: "t", Agent: agent.Claude, Account: "work@x.com"}
	row := s.ToRow()
	if row.Account != "work@x.com" {
		t.Fatalf("ToRow account = %q, want work@x.com", row.Account)
	}
	back := FromRow(row)
	if back.Account != "work@x.com" {
		t.Fatalf("FromRow account = %q, want work@x.com", back.Account)
	}
}
