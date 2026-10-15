package session

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
)

// openCodeTitleMaxRunes bounds what we'll accept as an OpenCode title.
// OpenCode truncates the titles it generates at 100 characters, and nothing a
// person types into a rename is longer than the Codex/Copilot cap either; a
// longer value means the column stopped holding titles, not a longer title.
const openCodeTitleMaxRunes = 120

// openCodeQueryErrOnce keeps a broken-schema warning to one line per launch.
var openCodeQueryErrOnce sync.Once

// openCodeFallbackTitleRe matches the placeholder OpenCode seeds a session
// with until its generated title lands: "New session - 2026-09-16T15:48:20.071Z"
// (or "Child session - …" for a sub-agent). Mirrors isFallbackTitle in
// OpenCode's packages/util/src/session-title-fallback.ts.
var openCodeFallbackTitleRe = regexp.MustCompile(`^(New session|Child session) - \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// openCodeURIReplacer escapes the characters SQLite's URI parser reserves, so
// a database path is always read as the literal filename: a literal % would
// start a percent-escape (custom%20titles.db would open as "custom
// titles.db"), and ? or # would end the path and mangle the mode=ro parameter
// riding behind them. See sqlite.org/uri.html, "URI filenames".
var openCodeURIReplacer = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// openCodeDBPath returns OpenCode's SQLite session database, or "" if there
// isn't one. $OPENCODE_DB overrides the name (it may be relative to the data
// dir, and ":memory:" means there is no file to read); a missing override is
// no database, not an error. The default name is opencode.db; a non-stable
// release channel suffixes it (opencode-<channel>.db — databasePath in
// packages/cli/src/database-path.ts), and fleet can't know the channel, so
// when more than one candidate exists the most recently active wins.
//
// "Active", not "newest file": these are WAL databases, and a live writer
// checkpoints the main file only occasionally — its mtime lags — while its
// -wal/-shm siblings move on every write. Freshness is therefore the newest
// mtime among the three, and a leftover database from a channel the user
// stopped running loses to the one their sessions land in today. It is still
// one database per read: an install running two channels at once has its
// other channel's sessions read as untitled, the same single-store choice the
// Codex reader makes against state_<N>.sqlite.
func openCodeDBPath() string {
	dir := hooks.GetOpenCodeDataDir()
	if dir == "" {
		return ""
	}
	if env := os.Getenv("OPENCODE_DB"); env != "" {
		if env == ":memory:" {
			return ""
		}
		path := env
		if !filepath.IsAbs(env) {
			path = filepath.Join(dir, env)
		}
		if _, err := os.Stat(path); err != nil {
			return ""
		}
		return path
	}

	candidates := []string{filepath.Join(dir, "opencode.db")}
	// The data dir is enumerated, not globbed: a directory containing glob
	// metacharacters (…/data/[account]/) would have the directory itself read
	// as a pattern, silently omitting its stores or matching a sibling's.
	// Only the filenames are matched, against a fixed pattern whose single
	// metacharacter is the intended *.
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if ok, matchErr := filepath.Match("opencode-*.db", entry.Name()); matchErr == nil && ok {
				candidates = append(candidates, filepath.Join(dir, entry.Name()))
			}
		}
	}

	best, bestAt := "", int64(-1)
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue // candidate doesn't exist; -wal/-shm alone are not a store
		}
		if at := openCodeActivityTime(path); at > bestAt {
			best, bestAt = path, at
		}
	}
	return best
}

// openCodeActivityTime is the newest mtime of a WAL database's main file and
// its -wal/-shm siblings — the closest thing to "when was this store last
// written" that the filesystem offers without opening it. A missing file
// returns 0, so it never wins the contest.
func openCodeActivityTime(path string) int64 {
	var newest int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if st, err := os.Stat(p); err == nil && st.ModTime().UnixNano() > newest {
			newest = st.ModTime().UnixNano()
		}
	}
	return newest
}

// ReadOpenCodeSessionName reads the title OpenCode keeps for a session: the
// one its model generates after the first turn (like Claude's ai-title) or a
// later rename made inside OpenCode. Every OpenCode install — 1.x and 2.x,
// every server, tabs or not — keeps its sessions in one SQLite file under the
// data dir, so that's what we read, read-only, keyed by the session id fleet
// already stores as ClaudeSessionID.
//
// OpenCode 2.0.29+ keeps live sessions in `session_v2`; older 2.x and 1.19
// wrote `session` (an upgraded install can hold a row in both, and the newer
// table is the live one). A session with no title yet holds NULL or the
// "New session - <timestamp>" placeholder, which returns "" so fleet's prompt
// heuristic keeps naming it until the real title lands.
//
// Returns "" whenever there's no title to adopt — a missing database, an
// unknown session, or a schema that has moved on. Like a missing Claude
// transcript, that's the normal case, not an error.
func ReadOpenCodeSessionName(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	path := openCodeDBPath()
	if path == "" {
		return ""
	}

	db, err := sql.Open("sqlite", "file:"+openCodeURIReplacer.Replace(path)+"?mode=ro")
	if err != nil {
		debuglog.Logger.Debug("opencode name: open db", "path", path, "err", err)
		return ""
	}
	defer db.Close()

	warnQuery := func(err error) {
		openCodeQueryErrOnce.Do(func() {
			debuglog.Logger.Warn("opencode name: query failed — OpenCode titles won't update (schema change?)",
				"db", path, "err", err)
		})
	}

	// Table presence is asked of sqlite_master rather than inferred from a
	// failed query, so an install old enough to lack session_v2 reads as a
	// fallback, not as a schema break.
	tables, err := openCodeSessionTables(db)
	if err != nil {
		warnQuery(err)
		return ""
	}
	if len(tables) == 0 {
		// A real OpenCode store always holds at least the legacy table, so
		// this is a foreign file or a schema that moved on — worth one line,
		// since the alternative is titles silently stopping.
		warnQuery(errors.New("no session or session_v2 table"))
		return ""
	}

	for _, table := range tables {
		var title sql.NullString
		err := db.QueryRow("SELECT title FROM "+table+" WHERE id = ?", sessionID).Scan(&title)
		if err == sql.ErrNoRows {
			continue // not in this table; an older one may still hold it
		}
		if err != nil {
			warnQuery(err)
			return ""
		}
		// Found the row: this table is the session's home, and its verdict
		// stands even when the title is not usable yet — a stale copy in an
		// older table must not answer for the live one.
		return openCodeUsableTitle(title)
	}
	return ""
}

// openCodeSessionTables returns the session tables present in db, newest
// schema first. The names come from the fixed slice below, never from the
// database, so interpolating them into the query is safe.
func openCodeSessionTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	present := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var tables []string
	for _, name := range []string{"session_v2", "session"} {
		if present[name] {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

// openCodeUsableTitle filters what ReadOpenCodeSessionName will adopt: no
// title, a placeholder, or something too long to be one all return "".
func openCodeUsableTitle(title sql.NullString) string {
	if !title.Valid {
		return ""
	}
	name := strings.TrimSpace(title.String)
	if name == "" || openCodeFallbackTitleRe.MatchString(name) {
		return ""
	}
	if utf8.RuneCountInString(name) > openCodeTitleMaxRunes {
		return ""
	}
	return name
}
