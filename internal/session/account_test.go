package session

import (
	"bytes"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
)

// withDirs installs an account config-dir resolver backed by real directories,
// so Provision does its work instead of failing on a path that cannot exist.
// Returns the email -> dir mapping the resolver will answer with.
func withDirs(t *testing.T, emails ...string) map[string]string {
	t.Helper()
	root := t.TempDir()
	dirs := make(map[string]string, len(emails))
	for i, e := range emails {
		dirs[e] = filepath.Join(root, fmt.Sprintf("acct%d", i))
	}
	SetAccountConfigDirFunc(func(email string) string { return dirs[email] })
	t.Cleanup(func() { SetAccountConfigDirFunc(nil) })
	return dirs
}

// configDirIn returns the config dir sessionEnv chose, if any.
func configDirIn(env []string) (string, bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, claudeaccount.ConfigDirEnvVar+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestSessionEnvSetsConfigDirForClaudeAccount(t *testing.T) {
	dirs := withDirs(t, "work@x.com")
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	got, ok := configDirIn(s.sessionEnv())
	if !ok {
		t.Fatal("no config dir in env — the session would run on the ambient login")
	}
	if got != dirs["work@x.com"] {
		t.Fatalf("config dir = %q, want the work account's %q", got, dirs["work@x.com"])
	}
}

// The whole feature rests on this one variable, and both rejected candidates
// fail silently — sessions launch, work correctly, and bill the wrong account.
// CLAUDE_CODE_OAUTH_TOKEN is ignored outright when a Keychain login exists
// (measured against 2.1.221 with a token the API answers 401 to, which still
// produced a normal reply). ANTHROPIC_AUTH_TOKEN is honoured, but sits at auth
// precedence 2 and displaces the claude.ai login, taking connectors and Remote
// Control with it. Only a config dir gives the session a login of its own.
func TestSessionEnvUsesAConfigDirNotACredential(t *testing.T) {
	dirs := withDirs(t, "work@x.com")
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	env := s.sessionEnv()
	for _, e := range env {
		for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
			if v, ok := strings.CutPrefix(e, name+"="); ok && v != "" {
				t.Fatalf("session sets %s to a value — that is either ignored outright "+
					"or displaces the claude.ai login and loses connectors", name)
			}
		}
	}
	if !slices.Contains(env, claudeaccount.ConfigDirEnvVar+"="+dirs["work@x.com"]) {
		t.Fatalf("env = %v, want CLAUDE_CONFIG_DIR set to the account's config dir", env)
	}
}

// tmux -e can only add variables, never remove one the server already inherited.
// So the credentials that outrank a config dir's login must be set to empty
// rather than left alone — Claude Code tests them for truthiness, and an empty
// value reads as unset. Without this a session inherits whatever credential
// fleet itself was launched with and bills that instead, which showed up as a
// brand-new login pane announcing "API Usage Billing".
func TestSessionEnvBlanksInheritedCredentials(t *testing.T) {
	withDirs(t, "work@x.com")
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	env := s.sessionEnv()
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if !slices.Contains(env, name+"=") {
			t.Errorf("env does not blank %s: %v", name, env)
		}
	}
}

func TestSessionEnvOmitsConfigDirWithoutAccount(t *testing.T) {
	// The regression that matters most: with no accounts configured the env
	// must be byte-identical to what fleet produced before this feature.
	withDirs(t, "work@x.com")
	s := &Session{ID: "abc", Agent: agent.Claude}

	env := s.sessionEnv()
	if _, ok := configDirIn(env); ok {
		t.Fatal("config dir set for a session with no account")
	}
	want := []string{"FLEET_INSTANCE_ID=abc", "ZSH_DOTENV_PROMPT=false"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestSessionEnvOmitsConfigDirForNonClaudeAgents(t *testing.T) {
	// The config dir carries a claude.ai login; Codex and OpenCode neither read
	// nor need it, and pointing them at one would relocate nothing they use.
	withDirs(t, "work@x.com")
	for _, ag := range []agent.Type{agent.Codex, agent.OpenCode} {
		s := &Session{ID: "abc", Agent: ag, Account: "work@x.com"}
		if _, ok := configDirIn(s.sessionEnv()); ok {
			t.Errorf("config dir set for %s session", ag)
		}
	}
}

func TestSessionEnvTreatsLegacyEmptyAgentAsClaude(t *testing.T) {
	// Rows written before the agent column default to "", which agent.Parse
	// resolves to Claude — the config dir must follow that same resolution.
	withDirs(t, "work@x.com")
	s := &Session{ID: "abc", Account: "work@x.com"}

	if _, ok := configDirIn(s.sessionEnv()); !ok {
		t.Fatal("legacy empty agent should resolve to Claude and get a config dir")
	}
}

func TestSessionEnvFallsBackWhenAccountUnknown(t *testing.T) {
	// An account removed from the store while sessions still reference it must
	// degrade to the ambient login, not fail to launch.
	withDirs(t)
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "deleted@x.com"}

	if _, ok := configDirIn(s.sessionEnv()); ok {
		t.Fatal("config dir set for an account the store no longer knows")
	}
}

func TestSessionEnvWithNoResolverInstalled(t *testing.T) {
	// fleet runs this way whenever no accounts are configured.
	SetAccountConfigDirFunc(nil)
	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}

	if _, ok := configDirIn(s.sessionEnv()); ok {
		t.Fatal("config dir set with no resolver installed")
	}
}

// sessionEnv logs on every launch, and debug.log is pasted into public issues by
// the bug-report flow. It must name the account and the dir it chose — "which
// account did this session launch as" is where every multi-account report starts
// — and must never carry a credential. There is no longer a credential anywhere
// near this path; the assertion stays as a tripwire against reintroducing one.
func TestSessionEnvLogsTheAccountAndNeverACredential(t *testing.T) {
	dirs := withDirs(t, "work@x.com")

	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { debuglog.Logger = prev })

	s := &Session{ID: "abc", Agent: agent.Claude, Account: "work@x.com"}
	if _, ok := configDirIn(s.sessionEnv()); !ok {
		t.Fatal("expected the config dir to be applied")
	}

	out := buf.String()
	if strings.Contains(out, "sk-ant-") {
		t.Fatalf("a credential reached the debug log:\n%s", out)
	}
	if !strings.Contains(out, "work@x.com") {
		t.Errorf("launch log should name the account:\n%s", out)
	}
	if !strings.Contains(out, dirs["work@x.com"]) {
		t.Errorf("launch log should name the config dir:\n%s", out)
	}
}

func TestSessionEnvLogsTheAmbientFallback(t *testing.T) {
	// A session pointing at a removed account silently falls back; without a
	// log line that is invisible and reads as "multi-account just doesn't work".
	withDirs(t)

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
