package stats

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"

	_ "modernc.org/sqlite"
)

// recorderQueueSize bounds the in-memory backlog. A full queue drops the
// event rather than stalling the caller (the Update goroutine or the status
// worker): losing one attach from a personal chart is cheaper than a frame.
const recorderQueueSize = 1024

// recorderBatchMax is how many queued events one write transaction takes.
const recorderBatchMax = 256

// enumRE is the shape every recorded enum must have: action names, statuses,
// agent names. Anything else — a path, a prompt, a title — is refused here,
// which is what keeps stats.db free of free text by construction.
var enumRE = regexp.MustCompile(`^[a-z_]+$`)

// idRE bounds session ids to the characters fleet and Claude ids use.
var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// SessionMeta is what the store remembers about a fleet session, so a session
// deleted from fleet keeps counting in history. Repo is a base name only.
type SessionMeta struct {
	ID, ClaudeSessionID, Agent, Repo string
	CreatedAt                        time.Time
}

// Store is stats.db plus its asynchronous recorder. A nil *Store is valid for
// every Record*/Note* call (they no-op), so callers need no guard when the
// database failed to open.
type Store struct {
	db *sql.DB

	mu     sync.RWMutex // guards closed + sends on ch
	closed bool
	ch     chan rec
	done   chan struct{}

	scanMu sync.Mutex

	// now and roots are seams for tests.
	now   func() time.Time
	roots func() []string
}

type recKind int

const (
	recStatus recKind = iota
	recAttach
	recAction
	recSession
	recFlush
)

type rec struct {
	kind     recKind
	session  string
	from, to string
	at       time.Time
	dur      time.Duration
	action   string
	meta     SessionMeta
	flushed  chan struct{}
}

// DefaultPath is ~/.config/fleet/stats.db — a separate file from state.db so
// its migrations stay isolated and "reset my stats" is deleting one file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "fleet-stats.db")
	}
	return filepath.Join(home, ".config", "fleet", "stats.db")
}

// Open opens (creating if needed) stats.db, migrates it, and starts the
// recorder's writer goroutine.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create stats dir: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open stats db: %w", err)
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate stats db: %w", err)
	}
	s := &Store{
		db:    db,
		ch:    make(chan rec, recorderQueueSize),
		done:  make(chan struct{}),
		now:   time.Now,
		roots: defaultRoots,
	}
	go s.writer()
	return s, nil
}

// Close drains the recorder queue, then closes the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()
	<-s.done
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

// Flush blocks until every event queued before it has been written, or the
// timeout passes. Useful before a Report that should include the last
// seconds of activity; never call it from the Update goroutine.
func (s *Store) Flush(timeout time.Duration) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return
	}
	// A flush marker may block briefly on a full queue — that is the point.
	select {
	case s.ch <- rec{kind: recFlush, flushed: done}:
	case <-time.After(timeout):
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// enqueue is the non-blocking send every recorder method funnels through.
func (s *Store) enqueue(r rec) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- r:
	default: // full: drop rather than block the caller
	}
}

// RecordStatus records a session's status transition.
func (s *Store) RecordStatus(sessionID, from, to string, at time.Time) {
	if s == nil || from == to || !idRE.MatchString(sessionID) || !enumRE.MatchString(to) {
		return
	}
	if from != "" && !enumRE.MatchString(from) {
		return
	}
	s.enqueue(rec{kind: recStatus, session: sessionID, from: from, to: to, at: at})
}

// RecordAttach records one attach to a session, stamped with when it began.
func (s *Store) RecordAttach(sessionID string, start time.Time, dur time.Duration) {
	if s == nil || dur < 0 || !idRE.MatchString(sessionID) {
		return
	}
	s.enqueue(rec{kind: recAttach, session: sessionID, at: start, dur: dur})
}

// RecordAction counts one fleet action for today. The action must be a
// snake_case enum (see actions.go); anything else is dropped.
func (s *Store) RecordAction(action string) {
	if s == nil || len(action) > 48 || !enumRE.MatchString(action) {
		return
	}
	s.enqueue(rec{kind: recAction, action: action, at: s.now()})
}

// NoteSession upserts a session's metadata. Every Claude session id a fleet
// session has ever had is kept, so a /clear rotation doesn't orphan the
// transcripts written under the old one. A Claude id's first owner keeps it:
// a fork's first SessionStart reports its PARENT's id, and letting that note
// win would hand the parent's transcript to the fork.
func (s *Store) NoteSession(m SessionMeta) {
	if s == nil || !idRE.MatchString(m.ID) {
		return
	}
	if m.ClaudeSessionID != "" && !idRE.MatchString(m.ClaudeSessionID) {
		m.ClaudeSessionID = ""
	}
	if m.Agent != "" && !enumRE.MatchString(m.Agent) {
		m.Agent = ""
	}
	m.Repo = repoBase(m.Repo)
	s.enqueue(rec{kind: recSession, meta: m})
}

// repoBase reduces anything path-shaped to its last element: stats.db never
// holds a full path, whatever the caller passed.
func repoBase(p string) string {
	if p == "" {
		return ""
	}
	b := filepath.Base(filepath.Clean(p))
	if b == "." || b == string(filepath.Separator) {
		return ""
	}
	if len(b) > 80 {
		b = b[:80]
	}
	return b
}

func (s *Store) writer() {
	defer close(s.done)
	for first := range s.ch {
		batch := []rec{first}
	drain:
		for len(batch) < recorderBatchMax {
			select {
			case r, ok := <-s.ch:
				if !ok {
					break drain
				}
				batch = append(batch, r)
			default:
				break drain
			}
		}
		if err := s.writeBatch(batch); err != nil {
			debuglog.Logger.Warn("stats: write batch failed", "events", len(batch), "error", err)
		}
		for _, r := range batch {
			if r.kind == recFlush {
				close(r.flushed)
			}
		}
	}
}

func (s *Store) writeBatch(batch []rec) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var earliest time.Time
	for _, r := range batch {
		switch r.kind {
		case recStatus:
			if _, err := tx.Exec(`INSERT INTO status_events(session_id, from_status, to_status, at_ms) VALUES(?,?,?,?)`,
				r.session, r.from, r.to, r.at.UnixMilli()); err != nil {
				return err
			}
		case recAttach:
			if _, err := tx.Exec(`INSERT INTO attaches(session_id, started_at_ms, dur_ms) VALUES(?,?,?)`,
				r.session, r.at.UnixMilli(), r.dur.Milliseconds()); err != nil {
				return err
			}
		case recAction:
			if _, err := tx.Exec(`INSERT INTO actions(day, action, n) VALUES(?,?,1)
				ON CONFLICT(day, action) DO UPDATE SET n = n + 1`,
				dayKey(r.at), r.action); err != nil {
				return err
			}
		case recSession:
			m := r.meta
			var created int64
			if !m.CreatedAt.IsZero() {
				created = m.CreatedAt.UnixMilli()
			}
			if _, err := tx.Exec(`INSERT INTO sessions(id, claude_session_id, agent, repo, created_at_ms) VALUES(?,?,?,?,?)
				ON CONFLICT(id) DO UPDATE SET
					claude_session_id = CASE WHEN excluded.claude_session_id != '' THEN excluded.claude_session_id ELSE sessions.claude_session_id END,
					agent = CASE WHEN excluded.agent != '' THEN excluded.agent ELSE sessions.agent END,
					repo = CASE WHEN excluded.repo != '' THEN excluded.repo ELSE sessions.repo END,
					created_at_ms = CASE WHEN sessions.created_at_ms = 0 THEN excluded.created_at_ms ELSE sessions.created_at_ms END`,
				m.ID, m.ClaudeSessionID, m.Agent, m.Repo, created); err != nil {
				return err
			}
			if m.ClaudeSessionID != "" {
				if _, err := tx.Exec(`INSERT INTO claude_ids(claude_session_id, session_id) VALUES(?,?)
					ON CONFLICT(claude_session_id) DO NOTHING`,
					m.ClaudeSessionID, m.ID); err != nil {
					return err
				}
			}
			continue // session metadata is not "activity"
		case recFlush:
			continue
		}
		if earliest.IsZero() || r.at.Before(earliest) {
			earliest = r.at
		}
	}
	if !earliest.IsZero() {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO meta(key, value) VALUES('recording_since', ?)`,
			strconv.FormatInt(earliest.UnixMilli(), 10)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dayKey is the local calendar day of t, as stored in actions.day.
func dayKey(t time.Time) string { return t.In(time.Local).Format("2006-01-02") }

// migrations are applied in order; the index+1 is the schema version.
var migrations = []string{
	`CREATE TABLE sessions (
		id                TEXT PRIMARY KEY,
		claude_session_id TEXT NOT NULL DEFAULT '',
		agent             TEXT NOT NULL DEFAULT '',
		repo              TEXT NOT NULL DEFAULT '',
		created_at_ms     INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE claude_ids (
		claude_session_id TEXT PRIMARY KEY,
		session_id        TEXT NOT NULL
	);
	CREATE TABLE status_events (
		session_id  TEXT NOT NULL,
		from_status TEXT NOT NULL,
		to_status   TEXT NOT NULL,
		at_ms       INTEGER NOT NULL
	);
	CREATE INDEX status_events_at ON status_events(at_ms);
	CREATE TABLE attaches (
		session_id    TEXT NOT NULL,
		started_at_ms INTEGER NOT NULL,
		dur_ms        INTEGER NOT NULL
	);
	CREATE INDEX attaches_at ON attaches(started_at_ms);
	CREATE TABLE actions (
		day    TEXT NOT NULL,
		action TEXT NOT NULL,
		n      INTEGER NOT NULL,
		PRIMARY KEY(day, action)
	);
	CREATE TABLE turns (
		file_key       TEXT NOT NULL,
		line_offset    INTEGER NOT NULL,
		session_id     TEXT NOT NULL,
		start_ms       INTEGER NOT NULL,
		dur_ms         INTEGER NOT NULL,
		model          TEXT NOT NULL DEFAULT '',
		input_tokens   INTEGER NOT NULL DEFAULT 0,
		output_tokens  INTEGER NOT NULL DEFAULT 0,
		cache_read     INTEGER NOT NULL DEFAULT 0,
		cache_write    INTEGER NOT NULL DEFAULT 0,
		cache_write_1h INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(file_key, line_offset)
	);
	CREATE INDEX turns_start ON turns(start_ms);
	CREATE TABLE scan_state (
		path     TEXT PRIMARY KEY,
		size     INTEGER NOT NULL,
		mtime_ms INTEGER NOT NULL,
		offset   INTEGER NOT NULL
	);
	CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
	// turns.file_key and scan_state.path held a transcript's realpath, which
	// spells out the home dir and the project's cwd; they now hold its hash
	// (fileKey). Dropping the old rows makes the next scan rebuild them. Opening
	// Stats is no longer counted as fleet activity either.
	`DELETE FROM turns; DELETE FROM scan_state; DELETE FROM actions WHERE action = 'stats_open';`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback() //nolint:errcheck
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
