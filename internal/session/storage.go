package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/github"
	_ "modernc.org/sqlite"
)

// PRCacheRow is the persisted form of a repo's PR-refresh state. It carries
// just enough metadata to reconstitute the in-memory cache without re-firing
// `gh` on restart: the PR data itself, the branch the PR was fetched against
// (so a branch switch invalidates the cache), the origin key (so the sidebar
// paints with the right grouping on frame 0), and the two timestamps that
// drive the TTL + rate-limit back-off gates.
type PRCacheRow struct {
	RepoPath        string
	Branch          string
	OriginKey       string
	PR              *github.PR // nil when there is no PR for this branch
	LastPRRefresh   time.Time
	PRRateLimitedAt time.Time // zero = clean (not rate-limited)
}

// StateDB wraps a SQLite database for session persistence.
type StateDB struct {
	db *sql.DB
}

// SessionRow represents a session row in the database.
type SessionRow struct {
	ID              string
	Title           string
	ProjectPath     string
	Agent           string
	Status          string
	TmuxSession     string
	CreatedAt       time.Time
	LastAccessed    time.Time
	Acknowledged    bool
	ClaudeSessionID string
	WorkspaceName   string
	ManuallyRenamed bool
	FirstPrompt     string
	TitleGenerated  bool
	PromptCount     int
}

// DefaultDBPath returns the default database path.
func DefaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "state.db")
}

// Open opens or creates the SQLite database with WAL mode.
func Open(dbPath string) (*StateDB, error) {
	debuglog.Logger.Info("opening database", "path", dbPath)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		debuglog.Logger.Error("failed to create db directory", "path", dbPath, "error", err)
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		debuglog.Logger.Error("failed to open database", "path", dbPath, "error", err)
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Configure for concurrent access.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			debuglog.Logger.Error("failed to set pragma", "pragma", p, "error", err)
			db.Close()
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}

	s := &StateDB{db: db}
	if err := s.migrate(); err != nil {
		debuglog.Logger.Error("database migration failed", "error", err)
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	debuglog.Logger.Info("database opened successfully", "path", dbPath)
	return s, nil
}

// Close checkpoints the WAL and closes the database.
func (s *StateDB) Close() error {
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

func (s *StateDB) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id            TEXT PRIMARY KEY,
			title         TEXT NOT NULL,
			project_path  TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'idle',
			tmux_session  TEXT NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL,
			last_accessed INTEGER NOT NULL DEFAULT 0,
			acknowledged  INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		debuglog.Logger.Error("migration failed: create sessions table", "error", err)
		return err
	}

	// Add claude_session_id column if missing.
	if !s.hasColumn("sessions", "claude_session_id") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN claude_session_id TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add claude_session_id column", "error", err)
			return err
		}
	}

	// Add workspace_name column if missing.
	if !s.hasColumn("sessions", "workspace_name") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN workspace_name TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add workspace_name column", "error", err)
			return err
		}
	}

	// Add auto-naming columns if missing.
	if !s.hasColumn("sessions", "manually_renamed") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN manually_renamed INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add manually_renamed column", "error", err)
			return err
		}
	}
	if !s.hasColumn("sessions", "first_prompt") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN first_prompt TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add first_prompt column", "error", err)
			return err
		}
	}
	if !s.hasColumn("sessions", "title_generated") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN title_generated INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add title_generated column", "error", err)
			return err
		}
	}
	if !s.hasColumn("sessions", "prompt_count") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN prompt_count INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add prompt_count column", "error", err)
			return err
		}
	}
	// Add agent column if missing (which coding agent the session runs).
	if !s.hasColumn("sessions", "agent") {
		_, err = s.db.Exec(`ALTER TABLE sessions ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude'`)
		if err != nil {
			debuglog.Logger.Error("migration failed: add agent column", "error", err)
			return err
		}
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS slot_bindings (
			slot_number INTEGER PRIMARY KEY CHECK (slot_number BETWEEN 0 AND 9),
			session_id  TEXT NOT NULL UNIQUE,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		debuglog.Logger.Error("migration failed: create slot_bindings table", "error", err)
		return err
	}

	// Pinned repos table for sticky repo headers.
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS pinned_repos (repo_path TEXT PRIMARY KEY)`)
	if err != nil {
		debuglog.Logger.Error("migration failed: create pinned_repos table", "error", err)
		return err
	}

	// Collapsed sidebar groups: presence of a row means that header (origin or
	// checkout) is collapsed. Absence = expanded, preserving the default.
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS collapsed_groups (group_key TEXT PRIMARY KEY)`)
	if err != nil {
		debuglog.Logger.Error("migration failed: create collapsed_groups table", "error", err)
		return err
	}

	// Per-repo PR cache: survives fleet restarts so we don't re-fire `gh` for
	// every repo on launch. The worker carries pr_json forward (subject to a
	// branch-match check) until the TTL gate decides to refresh.
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS repo_pr_cache (
			repo_path          TEXT PRIMARY KEY,
			branch             TEXT NOT NULL,
			origin_key         TEXT NOT NULL,
			pr_json            TEXT NOT NULL,
			last_pr_refresh    INTEGER NOT NULL,
			pr_rate_limited_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		debuglog.Logger.Error("migration failed: create repo_pr_cache table", "error", err)
		return err
	}

	return nil
}

func (s *StateDB) hasColumn(table, column string) bool {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}

// SaveSession inserts or replaces a session row.
func (s *StateDB) SaveSession(row *SessionRow) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO sessions (id, title, project_path, agent, status, tmux_session, created_at, last_accessed, acknowledged, claude_session_id, workspace_name, manually_renamed, first_prompt, title_generated, prompt_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.ID, row.Title, row.ProjectPath, string(agent.Parse(row.Agent)), row.Status, row.TmuxSession,
		row.CreatedAt.Unix(), row.LastAccessed.Unix(), boolToInt(row.Acknowledged),
		row.ClaudeSessionID, row.WorkspaceName,
		boolToInt(row.ManuallyRenamed), row.FirstPrompt, boolToInt(row.TitleGenerated),
		row.PromptCount,
	)
	if err != nil {
		debuglog.Logger.Error("failed to save session", "id", row.ID, "error", err)
	}
	return err
}

// LoadSessions returns all sessions ordered by creation time.
func (s *StateDB) LoadSessions() ([]*SessionRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, project_path, agent, status, tmux_session, created_at, last_accessed, acknowledged, claude_session_id, workspace_name, manually_renamed, first_prompt, title_generated, prompt_count
		FROM sessions ORDER BY created_at
	`)
	if err != nil {
		debuglog.Logger.Error("failed to query sessions", "error", err)
		return nil, err
	}
	defer rows.Close()

	var sessions []*SessionRow
	for rows.Next() {
		var r SessionRow
		var createdAt, lastAccessed int64
		var ack, manuallyRenamed, titleGenerated int
		if err := rows.Scan(&r.ID, &r.Title, &r.ProjectPath, &r.Agent, &r.Status, &r.TmuxSession, &createdAt, &lastAccessed, &ack, &r.ClaudeSessionID, &r.WorkspaceName, &manuallyRenamed, &r.FirstPrompt, &titleGenerated, &r.PromptCount); err != nil {
			debuglog.Logger.Error("failed to scan session row", "error", err)
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0)
		r.LastAccessed = time.Unix(lastAccessed, 0)
		r.Acknowledged = ack != 0
		r.ManuallyRenamed = manuallyRenamed != 0
		r.TitleGenerated = titleGenerated != 0
		sessions = append(sessions, &r)
	}
	if err := rows.Err(); err != nil {
		debuglog.Logger.Error("error iterating session rows", "error", err)
		return sessions, err
	}
	debuglog.Logger.Debug("loaded sessions from database", "count", len(sessions))
	return sessions, nil
}

// DeleteSession removes a session by ID.
func (s *StateDB) DeleteSession(id string) error {
	debuglog.Logger.Info("deleting session from storage", "id", id)
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		debuglog.Logger.Error("failed to delete session", "id", id, "error", err)
	}
	return err
}

// UpdateStatus updates the status field and auto-clears acknowledged on "running".
func (s *StateDB) UpdateStatus(id, status string) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET status = ?,
			acknowledged = CASE WHEN ? = 'running' THEN 0 ELSE acknowledged END
		WHERE id = ?
	`, status, status, id)
	if err != nil {
		debuglog.Logger.Error("failed to update session status", "id", id, "status", status, "error", err)
	}
	return err
}

// SetAcknowledged updates the acknowledged flag.
func (s *StateDB) SetAcknowledged(id string, ack bool) error {
	_, err := s.db.Exec("UPDATE sessions SET acknowledged = ? WHERE id = ?", boolToInt(ack), id)
	return err
}

// UpdateLastAccessed updates the last_accessed timestamp.
func (s *StateDB) UpdateLastAccessed(id string) error {
	_, err := s.db.Exec("UPDATE sessions SET last_accessed = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// UpdateTmuxSession updates the tmux session name (used after restart).
func (s *StateDB) UpdateTmuxSession(id, tmuxSession string) error {
	_, err := s.db.Exec("UPDATE sessions SET tmux_session = ? WHERE id = ?", tmuxSession, id)
	return err
}

// UpdateClaudeSessionID updates the Claude conversation session ID.
func (s *StateDB) UpdateClaudeSessionID(id, claudeSessionID string) error {
	_, err := s.db.Exec("UPDATE sessions SET claude_session_id = ? WHERE id = ?", claudeSessionID, id)
	if err != nil {
		debuglog.Logger.Error("failed to update claude session ID", "id", id, "claude_session_id", claudeSessionID, "error", err)
	}
	return err
}

// UpdateTitle updates the session title.
func (s *StateDB) UpdateTitle(id, title string) error {
	_, err := s.db.Exec("UPDATE sessions SET title = ? WHERE id = ?", title, id)
	if err != nil {
		debuglog.Logger.Error("failed to update session title", "id", id, "title", title, "error", err)
	}
	return err
}

// UpdateWorkspaceName updates the workspace name for a session.
func (s *StateDB) UpdateWorkspaceName(id, name string) error {
	_, err := s.db.Exec("UPDATE sessions SET workspace_name = ? WHERE id = ?", name, id)
	return err
}

// MarkManuallyRenamed marks a session as manually renamed (prevents auto-rename).
func (s *StateDB) MarkManuallyRenamed(id string) error {
	_, err := s.db.Exec("UPDATE sessions SET manually_renamed = 1 WHERE id = ?", id)
	return err
}

// UpdateFirstPrompt stores the first user prompt for a session.
func (s *StateDB) UpdateFirstPrompt(id, prompt string) error {
	_, err := s.db.Exec("UPDATE sessions SET first_prompt = ? WHERE id = ?", prompt, id)
	return err
}

// MarkTitleGenerated marks a session's title as generated (prevents re-generation).
func (s *StateDB) MarkTitleGenerated(id string) error {
	_, err := s.db.Exec("UPDATE sessions SET title_generated = 1 WHERE id = ?", id)
	return err
}

// UpdatePromptCount updates the prompt count for a session.
func (s *StateDB) UpdatePromptCount(id string, count int) error {
	_, err := s.db.Exec("UPDATE sessions SET prompt_count = ? WHERE id = ?", count, id)
	return err
}

// BindSlot assigns a session to a slot. The slot and session are both unique:
// any existing binding for the slot or the session is removed first, so a
// session holds at most one slot at a time.
func (s *StateDB) BindSlot(slot int, sessionID string) error {
	if slot < 0 || slot > 9 {
		return fmt.Errorf("slot %d out of range 0-9", slot)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM slot_bindings WHERE slot_number = ? OR session_id = ?`, slot, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO slot_bindings (slot_number, session_id) VALUES (?, ?)`, slot, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// UnbindSlot removes the binding for a slot, if any.
func (s *StateDB) UnbindSlot(slot int) error {
	_, err := s.db.Exec(`DELETE FROM slot_bindings WHERE slot_number = ?`, slot)
	return err
}

// LoadSlotBindings returns the slot → session_id map.
func (s *StateDB) LoadSlotBindings() (map[int]string, error) {
	rows, err := s.db.Query(`SELECT slot_number, session_id FROM slot_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]string)
	for rows.Next() {
		var slot int
		var id string
		if err := rows.Scan(&slot, &id); err != nil {
			return nil, err
		}
		out[slot] = id
	}
	return out, rows.Err()
}

// DeleteSlotBindingForSession clears any slot pointing at the given session.
// FK cascade handles this when a session row is deleted, but callers that
// remove bindings without deleting the session use this directly.
func (s *StateDB) DeleteSlotBindingForSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM slot_bindings WHERE session_id = ?`, sessionID)
	return err
}

// PinRepo adds a repo to the pinned set (idempotent).
func (s *StateDB) PinRepo(repoPath string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO pinned_repos (repo_path) VALUES (?)", repoPath)
	if err != nil {
		debuglog.Logger.Error("failed to pin repo", "repo", repoPath, "error", err)
	}
	return err
}

// UnpinRepo removes a repo from the pinned set.
func (s *StateDB) UnpinRepo(repoPath string) error {
	_, err := s.db.Exec("DELETE FROM pinned_repos WHERE repo_path = ?", repoPath)
	if err != nil {
		debuglog.Logger.Error("failed to unpin repo", "repo", repoPath, "error", err)
	}
	return err
}

// LoadPinnedRepos returns all pinned repo paths.
func (s *StateDB) LoadPinnedRepos() ([]string, error) {
	rows, err := s.db.Query("SELECT repo_path FROM pinned_repos ORDER BY repo_path")
	if err != nil {
		debuglog.Logger.Error("failed to load pinned repos", "error", err)
		return nil, err
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			debuglog.Logger.Error("failed to scan pinned repo row", "error", err)
			return nil, err
		}
		repos = append(repos, path)
	}
	return repos, rows.Err()
}

// SetGroupCollapsed records (or clears) the collapsed state for a sidebar group
// key. The key is the same one the UI uses in repoExpanded: "origin:<key>" for
// origin headers, the repo path for checkout headers. Collapsed inserts a row;
// expanded deletes it (absence = expanded).
func (s *StateDB) SetGroupCollapsed(groupKey string, collapsed bool) error {
	var err error
	if collapsed {
		_, err = s.db.Exec("INSERT OR IGNORE INTO collapsed_groups (group_key) VALUES (?)", groupKey)
	} else {
		_, err = s.db.Exec("DELETE FROM collapsed_groups WHERE group_key = ?", groupKey)
	}
	if err != nil {
		debuglog.Logger.Error("failed to set group collapsed", "key", groupKey, "collapsed", collapsed, "error", err)
	}
	return err
}

// LoadCollapsedGroups returns the keys of all collapsed sidebar groups.
func (s *StateDB) LoadCollapsedGroups() ([]string, error) {
	rows, err := s.db.Query("SELECT group_key FROM collapsed_groups")
	if err != nil {
		debuglog.Logger.Error("failed to load collapsed groups", "error", err)
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			debuglog.Logger.Error("failed to scan collapsed group row", "error", err)
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// SavePRCacheRow upserts a single repo's PR-refresh state. Called per-repo
// after each successful refresh in the worker. JSON-encodes the PR; on
// nil PR we persist an empty string (distinct from row absence, which
// means "we've never refreshed this repo").
func (s *StateDB) SavePRCacheRow(row *PRCacheRow) error {
	if row == nil || row.RepoPath == "" {
		return nil
	}
	var prJSON string
	if row.PR != nil {
		b, err := json.Marshal(row.PR)
		if err != nil {
			debuglog.Logger.Error("failed to marshal PR for cache", "repo", row.RepoPath, "error", err)
			return err
		}
		prJSON = string(b)
	}
	_, err := s.db.Exec(`
		INSERT INTO repo_pr_cache (repo_path, branch, origin_key, pr_json, last_pr_refresh, pr_rate_limited_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_path) DO UPDATE SET
			branch             = excluded.branch,
			origin_key         = excluded.origin_key,
			pr_json            = excluded.pr_json,
			last_pr_refresh    = excluded.last_pr_refresh,
			pr_rate_limited_at = excluded.pr_rate_limited_at
	`,
		row.RepoPath,
		row.Branch,
		row.OriginKey,
		prJSON,
		row.LastPRRefresh.UnixNano(),
		row.PRRateLimitedAt.UnixNano(),
	)
	if err != nil {
		debuglog.Logger.Error("failed to save PR cache row", "repo", row.RepoPath, "error", err)
	}
	return err
}

// LoadPRCache returns all persisted PR-refresh rows keyed by repo_path.
// Callers apply their own freshness filters (e.g. drop rows older than 24h)
// — this method itself returns everything that's in the table.
func (s *StateDB) LoadPRCache() (map[string]*PRCacheRow, error) {
	rows, err := s.db.Query(`
		SELECT repo_path, branch, origin_key, pr_json, last_pr_refresh, pr_rate_limited_at
		FROM repo_pr_cache
	`)
	if err != nil {
		debuglog.Logger.Error("failed to load PR cache", "error", err)
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]*PRCacheRow)
	for rows.Next() {
		var (
			repoPath      string
			branch        string
			originKey     string
			prJSON        string
			lastRefreshNS int64
			rateLimitedNS int64
		)
		if err := rows.Scan(&repoPath, &branch, &originKey, &prJSON, &lastRefreshNS, &rateLimitedNS); err != nil {
			debuglog.Logger.Error("failed to scan PR cache row", "error", err)
			return nil, err
		}
		row := &PRCacheRow{
			RepoPath:      repoPath,
			Branch:        branch,
			OriginKey:     originKey,
			LastPRRefresh: time.Unix(0, lastRefreshNS),
		}
		if rateLimitedNS != 0 {
			row.PRRateLimitedAt = time.Unix(0, rateLimitedNS)
		}
		if prJSON != "" {
			var pr github.PR
			if err := json.Unmarshal([]byte(prJSON), &pr); err != nil {
				// Corrupt row: skip, log, keep going. Re-fetching on next
				// cycle is harmless.
				debuglog.Logger.Warn("PR cache row JSON parse failed; skipping", "repo", repoPath, "error", err)
				continue
			}
			row.PR = &pr
		}
		out[repoPath] = row
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
