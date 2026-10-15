package session

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func titlePtr(s string) *string { return &s }

// writeOpenCodeDB creates an OpenCode-shaped session store under
// xdgRoot/opencode/opencode.db (the layout OpenCode builds under
// $XDG_DATA_HOME). tables maps a table name ("session_v2", "session") to its
// (id, title) rows; a nil title writes NULL, which only session_v2 allows —
// the legacy table's title column is NOT NULL, like the real schema.
func writeOpenCodeDB(t *testing.T, xdgRoot string, tables map[string]map[string]*string) string {
	t.Helper()

	dataDir := filepath.Join(xdgRoot, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return writeOpenCodeDBFile(t, filepath.Join(dataDir, "opencode.db"), tables)
}

// writeOpenCodeDBFile seeds an OpenCode-shaped store at an explicit path, for
// the cases that exercise the database-name rules rather than the data dir.
// The connection goes through the same escaped URI the reader uses: modernc's
// driver splits a plain DSN at ? (so a filename like "a?b.db" would create
// "a" instead) and only the file: URI form percent-decodes back to the
// literal path.
func writeOpenCodeDBFile(t *testing.T, path string, tables map[string]map[string]*string) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+openCodeURIReplacer.Replace(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for table, rows := range tables {
		ddl := "CREATE TABLE " + table + " (id text PRIMARY KEY, title text"
		if table != "session_v2" {
			ddl += " NOT NULL"
		}
		ddl += ")"
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		for id, title := range rows {
			var err error
			if title == nil {
				if table != "session_v2" {
					t.Fatalf("nil title needs session_v2, got %s", table)
				}
				_, err = db.Exec("INSERT INTO "+table+" (id, title) VALUES (?, NULL)", id)
			} else {
				_, err = db.Exec("INSERT INTO "+table+" (id, title) VALUES (?, ?)", id, *title)
			}
			if err != nil {
				t.Fatalf("insert %s into %s: %v", id, table, err)
			}
		}
	}
	return path
}

func TestReadOpenCodeSessionName(t *testing.T) {
	xdg := t.TempDir()
	writeOpenCodeDB(t, xdg, map[string]map[string]*string{
		"session_v2": {
			"titled":          titlePtr("Ship the fleet status plugin"),
			"untitled":        nil, // no ai-title yet; the legacy copy must not answer
			"fallback":        titlePtr("New session - 2026-09-16T15:48:20.071Z"),
			"child-fallback":  titlePtr("Child session - 2026-09-16T15:48:20.071Z"),
			"overlong":        titlePtr(strings.Repeat("x", openCodeTitleMaxRunes+1)),
			"at-the-cap":      titlePtr(strings.Repeat("y", openCodeTitleMaxRunes)),
			"blank":           titlePtr(""),
			"whitespace":      titlePtr("   "),
			"v2-wins":         titlePtr("live title"),
			"nearly-fallback": titlePtr("New session - not-a-timestamp"),
		},
		"session": {
			"untitled":    titlePtr("stale pre-migration title"),
			"legacy-only": titlePtr("pre-migration title"),
			"v2-wins":     titlePtr("stale title"),
		},
	})
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{"titled session returns its title", "titled", "Ship the fleet status plugin"},
		{"null title in the live table wins over a legacy copy", "untitled", ""},
		{"placeholder title returns empty", "fallback", ""},
		{"child placeholder title returns empty", "child-fallback", ""},
		{"placeholder-shaped text that is not a placeholder is kept", "nearly-fallback", "New session - not-a-timestamp"},
		{"title at the cap is kept", "at-the-cap", strings.Repeat("y", openCodeTitleMaxRunes)},
		{"overlong title returns empty", "overlong", ""},
		{"blank title returns empty", "blank", ""},
		{"whitespace-only title returns empty", "whitespace", ""},
		{"session only in the legacy table is adopted", "legacy-only", "pre-migration title"},
		{"live table wins when both hold the row", "v2-wins", "live title"},
		{"unknown session returns empty", "unknown", ""},
		{"empty session id returns empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadOpenCodeSessionName(tt.sessionID); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// An install old enough to have no session_v2 at all (OpenCode 1.x, early
// 2.x) must still hand over its titles from the legacy table.
func TestReadOpenCodeSessionNameLegacyDB(t *testing.T) {
	xdg := t.TempDir()
	writeOpenCodeDB(t, xdg, map[string]map[string]*string{
		"session": {"ses_old": titlePtr("Legacy install title")},
	})
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_old"); got != "Legacy install title" {
		t.Errorf("got %q, want %q", got, "Legacy install title")
	}
}

// The data dir also resolves through $HOME when XDG_DATA_HOME is unset — the
// shape every default Linux install has.
func TestReadOpenCodeSessionNameHomeFallback(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	writeOpenCodeDB(t, xdg, map[string]map[string]*string{
		"session_v2": {"ses_home": titlePtr("home-dir title")},
	})
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_home"); got != "home-dir title" {
		t.Errorf("got %q, want %q", got, "home-dir title")
	}
}

func TestReadOpenCodeSessionNameNoDB(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // exists, but holds no opencode store
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_any"); got != "" {
		t.Errorf("got %q, want %q", got, "")
	}
}

// A database with neither known session table is a foreign file, not an
// OpenCode store: no title, and nothing may panic or hang on it.
func TestReadOpenCodeSessionNameForeignDB(t *testing.T) {
	xdg := t.TempDir()
	dataDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "opencode.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE something_else (id text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_any"); got != "" {
		t.Errorf("got %q, want %q", got, "")
	}
}

// $OPENCODE_DB is OpenCode's own override of the database name: absolute wins,
// relative joins the data dir, ":memory:" means there is no file to read, and
// a missing override is no database rather than an error.
func TestReadOpenCodeSessionNameDBOverride(t *testing.T) {
	t.Run("absolute override wins", func(t *testing.T) {
		defaults := t.TempDir()
		writeOpenCodeDB(t, defaults, map[string]map[string]*string{
			"session_v2": {"ses_default": titlePtr("default db title")},
		})
		other := t.TempDir()
		abs := writeOpenCodeDB(t, other, map[string]map[string]*string{
			"session_v2": {"ses_custom": titlePtr("custom db title")},
		})

		t.Setenv("XDG_DATA_HOME", defaults)
		t.Setenv("OPENCODE_DB", abs)
		if got := ReadOpenCodeSessionName("ses_custom"); got != "custom db title" {
			t.Errorf("got %q, want %q", got, "custom db title")
		}
		if got := ReadOpenCodeSessionName("ses_default"); got != "" {
			t.Errorf("default db read despite override, got %q", got)
		}
	})
	t.Run("relative override joins the data dir", func(t *testing.T) {
		xdg := t.TempDir()
		path := writeOpenCodeDB(t, xdg, map[string]map[string]*string{
			"session_v2": {"ses_relative": titlePtr("relative db title")},
		})
		// writeOpenCodeDB always names the file opencode.db; rename it to the
		// shape a relative override would point at.
		if err := os.Rename(path, filepath.Join(xdg, "opencode", "custom.db")); err != nil {
			t.Fatalf("rename: %v", err)
		}
		t.Setenv("XDG_DATA_HOME", xdg)
		t.Setenv("OPENCODE_DB", "custom.db")
		if got := ReadOpenCodeSessionName("ses_relative"); got != "relative db title" {
			t.Errorf("got %q, want %q", got, "relative db title")
		}
	})
	t.Run("memory override reads nothing", func(t *testing.T) {
		xdg := t.TempDir()
		writeOpenCodeDB(t, xdg, map[string]map[string]*string{
			"session_v2": {"ses_custom": titlePtr("custom db title")},
		})
		t.Setenv("XDG_DATA_HOME", xdg)
		t.Setenv("OPENCODE_DB", ":memory:")
		if got := ReadOpenCodeSessionName("ses_custom"); got != "" {
			t.Errorf("got %q, want %q", got, "")
		}
	})
	t.Run("missing override reads nothing", func(t *testing.T) {
		xdg := t.TempDir()
		writeOpenCodeDB(t, xdg, map[string]map[string]*string{
			"session_v2": {"ses_custom": titlePtr("custom db title")},
		})
		t.Setenv("XDG_DATA_HOME", xdg)
		t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "not-there.db"))
		if got := ReadOpenCodeSessionName("ses_custom"); got != "" {
			t.Errorf("got %q, want %q", got, "")
		}
	})
}

// A non-stable release channel suffixes the database (opencode-<channel>.db),
// and fleet can't know the channel. With only suffixed stores present, the
// most recently active one wins — a stale one must not freeze titles at an
// upgrade, like Codex's state_<N>.
func TestReadOpenCodeSessionNameChannelDB(t *testing.T) {
	xdg := t.TempDir()
	dataDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	newer := writeOpenCodeDBFile(t, filepath.Join(dataDir, "opencode-nightly.db"), map[string]map[string]*string{
		"session_v2": {"ses_ch": titlePtr("newest channel title")},
	})
	older := writeOpenCodeDBFile(t, filepath.Join(dataDir, "opencode-preview.db"), map[string]map[string]*string{
		"session_v2": {"ses_ch": titlePtr("older channel title")},
	})

	// mtimes a minute apart, so filesystem granularity can't reorder them.
	now := time.Now()
	setMtime := func(path string, at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	setMtime(older, now.Add(-time.Minute))
	setMtime(newer, now)

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_ch"); got != "newest channel title" {
		t.Errorf("got %q, want %q", got, "newest channel title")
	}
}

// The mixed case: a leftover opencode.db from a stable install beside the
// suffixed store a channel build now writes. The active store must win even
// though its main file's mtime lags — these are WAL databases, and the write
// activity shows in the -wal sibling, so that is what the contest reads.
func TestReadOpenCodeSessionNameChannelDBBeatsStaleDefault(t *testing.T) {
	xdg := t.TempDir()
	dataDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	stale := writeOpenCodeDBFile(t, filepath.Join(dataDir, "opencode.db"), map[string]map[string]*string{
		"session_v2": {"ses_mix": titlePtr("stale stable title")},
	})
	channel := writeOpenCodeDBFile(t, filepath.Join(dataDir, "opencode-nightly.db"), map[string]map[string]*string{
		"session_v2": {"ses_mix": titlePtr("live channel title")},
	})
	// SQLite drops -wal/-shm on clean close, so the siblings are staged by
	// hand: empty files carrying only the mtimes the contest reads.
	for _, p := range []string{stale + "-wal", channel + "-wal"} {
		if err := os.WriteFile(p, nil, 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// The stable store was busier once, but stopped: everything it touches is
	// older than the channel store's latest write.
	now := time.Now()
	setMtime := func(path string, at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	setMtime(stale, now.Add(-time.Hour))
	setMtime(stale+"-wal", now.Add(-time.Hour))
	setMtime(channel, now.Add(-2*time.Hour)) // checkpoints lag behind writes
	setMtime(channel+"-wal", now)

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_mix"); got != "live channel title" {
		t.Errorf("got %q, want %q", got, "live channel title")
	}
}

// Paths with characters SQLite's URI parser reserves (%, ?, #) must open the
// literal file, not a percent-decoded or truncated URI: a database named
// custom%20titles.db must not open as "custom titles.db".
func TestReadOpenCodeSessionNameReservedPathChars(t *testing.T) {
	xdg := t.TempDir()
	dataDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dataDir, "custom%20titles?v2#.db")
	writeOpenCodeDBFile(t, path, map[string]map[string]*string{
		"session_v2": {"ses_pct": titlePtr("reserved char title")},
	})

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", path)

	if got := ReadOpenCodeSessionName("ses_pct"); got != "reserved char title" {
		t.Errorf("got %q, want %q", got, "reserved char title")
	}
}

// A data directory containing glob metacharacters must not be read as a glob
// pattern itself: its stores still count as candidates (a bracketed directory
// name would otherwise have the directory matched as a character class and
// its channel-suffixed stores silently omitted).
func TestReadOpenCodeSessionNameGlobMetacharDataDir(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "xdg[42]")
	path := writeOpenCodeDB(t, xdg, map[string]map[string]*string{
		"session_v2": {"ses_ch": titlePtr("bracketed dir title")},
	})
	if err := os.Rename(path, filepath.Join(xdg, "opencode", "opencode-nightly.db")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_ch"); got != "bracketed dir title" {
		t.Errorf("got %q, want %q", got, "bracketed dir title")
	}
}

// The live database is a WAL store held open by a running OpenCode server —
// the state fleet reads it in. A read-only connection must still see rows
// committed after it opened.
func TestReadOpenCodeSessionNameWALDB(t *testing.T) {
	xdg := t.TempDir()
	dataDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dataDir, "opencode.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("wal: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE session_v2 (id text PRIMARY KEY, title text)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec("INSERT INTO session_v2 (id, title) VALUES (?, ?)", "ses_wal", "title behind an open writer"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("OPENCODE_DB", "")

	if got := ReadOpenCodeSessionName("ses_wal"); got != "title behind an open writer" {
		t.Errorf("got %q, want %q", got, "title behind an open writer")
	}
}
