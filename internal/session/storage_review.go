package session

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Review run statuses (review_runs.status).
const (
	ReviewPreparing = "preparing"
	ReviewReady     = "ready"
	ReviewFailed    = "failed"
	ReviewSent      = "sent"
	ReviewApproved  = "approved"
)

// ReviewRunRow is one self-review run over one snapshot of a checkout. JSON is
// the enforced selfreview.Run; this package stores it as text and never
// decodes it (session must not import the reviewer).
//
// A run belongs to its CHECKOUT — the repo or worktree root the sidebar groups
// sessions under — not to a session: several sessions can work one worktree,
// and the change under review is the checkout's, whoever typed it. SessionID is
// only the run's send target (the session S last delivered to, or was pointed
// at); "" = none chosen yet.
type ReviewRunRow struct {
	ID        string
	Checkout  string // checkout root (session.GetRepoRoot), the sidebar's key
	SessionID string // send target / last author; "" = none
	Worktree  string
	Round     int
	BaseRef   string // the ref the diff is measured against ("origin/master")
	BaseSHA   string // merge-base(BaseRef, HEAD) — the diff's left side
	TreeSHA   string // SnapshotTree — the diff's right side and the run's anchor
	HeadSHA   string
	CacheKey  string
	Status    string // ReviewPreparing … ReviewApproved
	Model     string
	Account   string // Claude account email the reviewer ran as; "" = ambient
	JSON      string // enforced selfreview.Run; "" until ready
	Error     string // why it failed
	ElapsedMS int64
	CreatedAt time.Time
	SeenAt    time.Time // zero = not opened yet
}

// ReviewAnswerRow is the human's answer to one stop of one run.
type ReviewAnswerRow struct {
	RunID     string
	StopID    string
	Answer    string // fine | change | ask | later
	Note      string
	UpdatedAt time.Time
	SentAt    time.Time // zero = not delivered yet
}

// migrateReview creates the self-review tables.
//
// review_runs was first keyed by session; it is keyed by checkout now. Nothing
// had shipped, so the move is an added column plus a one-time backfill of rows
// that predate it (migrateReviewCheckouts), not a table rebuild — session_id
// stays NOT NULL with ” meaning "no target".
//
// Deliberately no FOREIGN KEY to sessions: SaveSession is INSERT OR REPLACE,
// and SQLite's REPLACE deletes the old row before inserting the new one — with
// ON DELETE CASCADE that wipes every child row (measured: slot_bindings do not
// survive a SaveSession of their session). Review history must survive it, so
// DeleteSession removes these rows explicitly instead.
func (s *StateDB) migrateReview() error {
	stmts := []struct{ what, sql string }{
		{"create review_runs table", `
		CREATE TABLE IF NOT EXISTS review_runs (
			id         TEXT PRIMARY KEY,
			checkout   TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			worktree   TEXT NOT NULL DEFAULT '',
			round      INTEGER NOT NULL DEFAULT 1,
			base_ref   TEXT NOT NULL DEFAULT '',
			base_sha   TEXT NOT NULL DEFAULT '',
			tree_sha   TEXT NOT NULL DEFAULT '',
			head_sha   TEXT NOT NULL DEFAULT '',
			cache_key  TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL,
			model      TEXT NOT NULL DEFAULT '',
			account    TEXT NOT NULL DEFAULT '',
			json       TEXT NOT NULL DEFAULT '',
			error      TEXT NOT NULL DEFAULT '',
			elapsed_ms INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			seen_at    INTEGER NOT NULL DEFAULT 0
		)`},
		{"create review_answers table", `
		CREATE TABLE IF NOT EXISTS review_answers (
			run_id     TEXT NOT NULL,
			stop_id    TEXT NOT NULL,
			answer     TEXT NOT NULL,
			note       TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			sent_at    INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (run_id, stop_id)
		)`},
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st.sql); err != nil {
			debuglog.Logger.Error("migration failed: "+st.what, "error", err)
			return err
		}
	}
	if !s.hasColumn("review_runs", "checkout") {
		if _, err := s.db.Exec(`ALTER TABLE review_runs ADD COLUMN checkout TEXT NOT NULL DEFAULT ''`); err != nil {
			debuglog.Logger.Error("migration failed: add review_runs.checkout", "error", err)
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_review_runs_checkout ON review_runs(checkout)`); err != nil {
		debuglog.Logger.Error("migration failed: create idx_review_runs_checkout", "error", err)
		return err
	}
	s.migrateReviewCheckouts()
	return nil
}

// migrateReviewCheckouts backfills the checkout of runs stored before runs were
// keyed by checkout. Such a row's worktree column is the session's ProjectPath
// at the time, so its checkout is that path's repo root — resolved with the
// same GetRepoRoot the sidebar groups on, and left as the raw path when git
// can't say (the directory is gone). Best effort: a row it can't place is
// simply never shown. Runs once per row: the next launch finds none empty.
func (s *StateDB) migrateReviewCheckouts() {
	rows, err := s.db.Query(`SELECT id, worktree FROM review_runs WHERE checkout = ''`)
	if err != nil {
		debuglog.Logger.Warn("review checkout backfill: query failed", "error", err)
		return
	}
	type pending struct{ id, wt string }
	var todo []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.wt) == nil {
			todo = append(todo, p)
		}
	}
	rows.Close()
	for _, p := range todo {
		root := p.wt
		if root != "" {
			root = GetRepoRoot(root)
		}
		if root == "" {
			root = "?" // no worktree recorded: unplaceable, never matched
		}
		if _, err := s.db.Exec(`UPDATE review_runs SET checkout = ? WHERE id = ?`, root, p.id); err != nil {
			debuglog.Logger.Warn("review checkout backfill: update failed", "id", p.id, "error", err)
		}
	}
}

// Review timestamps are unix milliseconds: two runs of one session can land in
// the same second (a failed run and its retry), and "latest" must not be a
// coin flip. Zero round-trips as the zero time.
func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func timeToMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

const reviewRunCols = `id, checkout, session_id, worktree, round, base_ref, base_sha, tree_sha, head_sha, cache_key,
	status, model, account, json, error, elapsed_ms, created_at, seen_at`

func scanReviewRun(sc interface{ Scan(...any) error }) (*ReviewRunRow, error) {
	var r ReviewRunRow
	var created, seen int64
	if err := sc.Scan(&r.ID, &r.Checkout, &r.SessionID, &r.Worktree, &r.Round, &r.BaseRef, &r.BaseSHA, &r.TreeSHA, &r.HeadSHA,
		&r.CacheKey, &r.Status, &r.Model, &r.Account, &r.JSON, &r.Error, &r.ElapsedMS, &created, &seen); err != nil {
		return nil, err
	}
	r.CreatedAt, r.SeenAt = msToTime(created), msToTime(seen)
	return &r, nil
}

// InsertReviewRun stores a new run. CreatedAt defaults to now.
func (s *StateDB) InsertReviewRun(r *ReviewRunRow) error {
	if r.ID == "" || r.Checkout == "" {
		return fmt.Errorf("review run needs an id and a checkout")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO review_runs (`+reviewRunCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Checkout, r.SessionID, r.Worktree, r.Round, r.BaseRef, r.BaseSHA, r.TreeSHA, r.HeadSHA, r.CacheKey,
		r.Status, r.Model, r.Account, r.JSON, r.Error, r.ElapsedMS, timeToMS(r.CreatedAt), timeToMS(r.SeenAt))
	if err != nil {
		debuglog.Logger.Error("failed to insert review run", "id", r.ID, "error", err)
	}
	return err
}

// UpdateReviewRun rewrites a run's mutable columns: status, model, account,
// json, error, elapsed and seen. The snapshot columns are the run's identity
// and never change.
func (s *StateDB) UpdateReviewRun(r *ReviewRunRow) error {
	res, err := s.db.Exec(`UPDATE review_runs SET status = ?, model = ?, account = ?, json = ?, error = ?,
		elapsed_ms = ?, seen_at = ? WHERE id = ?`,
		r.Status, r.Model, r.Account, r.JSON, r.Error, r.ElapsedMS, timeToMS(r.SeenAt), r.ID)
	if err != nil {
		debuglog.Logger.Error("failed to update review run", "id", r.ID, "error", err)
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("review run %s: %w", r.ID, sql.ErrNoRows)
	}
	return nil
}

// LoadReviewRun returns one run by id, or nil when there is none.
func (s *StateDB) LoadReviewRun(id string) (*ReviewRunRow, error) {
	r, err := scanReviewRun(s.db.QueryRow(`SELECT `+reviewRunCols+` FROM review_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// LatestReviewRun returns the checkout's newest run, or nil when it has none.
func (s *StateDB) LatestReviewRun(checkout string) (*ReviewRunRow, error) {
	r, err := scanReviewRun(s.db.QueryRow(`SELECT `+reviewRunCols+` FROM review_runs WHERE checkout = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, checkout))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ListReviewRuns returns every run of a checkout, newest first.
func (s *StateDB) ListReviewRuns(checkout string) ([]*ReviewRunRow, error) {
	rows, err := s.db.Query(`SELECT `+reviewRunCols+` FROM review_runs WHERE checkout = ?
		ORDER BY created_at DESC, rowid DESC`, checkout)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReviewRunRow
	for rows.Next() {
		r, err := scanReviewRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReviewRunsForCheckouts returns each listed checkout's newest run in one
// query — what the sidebar paints its review rows from. Checkouts with no run
// are absent. An empty list returns every checkout's newest run.
func (s *StateDB) ReviewRunsForCheckouts(checkouts []string) (map[string]*ReviewRunRow, error) {
	q := `SELECT ` + reviewRunCols + ` FROM review_runs r WHERE rowid = (
		SELECT rowid FROM review_runs WHERE checkout = r.checkout
		ORDER BY created_at DESC, rowid DESC LIMIT 1)`
	args := make([]any, 0, len(checkouts))
	if len(checkouts) > 0 {
		q += ` AND checkout IN (?` + strings.Repeat(", ?", len(checkouts)-1) + `)`
		for _, c := range checkouts {
			args = append(args, c)
		}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*ReviewRunRow)
	for rows.Next() {
		r, err := scanReviewRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.Checkout] = r
	}
	return out, rows.Err()
}

// SetReviewTarget records the session a run's answers go to (or went to).
func (s *StateDB) SetReviewTarget(runID, sessionID string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET session_id = ? WHERE id = ?`, sessionID, runID)
	return err
}

// SaveReviewAnswer records (or replaces) the answer to one stop. An empty
// Answer clears it. A changed answer is unsent again: sent_at resets.
func (s *StateDB) SaveReviewAnswer(a ReviewAnswerRow) error {
	if a.Answer == "" {
		_, err := s.db.Exec(`DELETE FROM review_answers WHERE run_id = ? AND stop_id = ?`, a.RunID, a.StopID)
		return err
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO review_answers (run_id, stop_id, answer, note, updated_at, sent_at)
		VALUES (?, ?, ?, ?, ?, 0)
		ON CONFLICT(run_id, stop_id) DO UPDATE SET answer = excluded.answer, note = excluded.note,
			updated_at = excluded.updated_at, sent_at = 0`,
		a.RunID, a.StopID, a.Answer, a.Note, timeToMS(a.UpdatedAt))
	if err != nil {
		debuglog.Logger.Error("failed to save review answer", "run", a.RunID, "stop", a.StopID, "error", err)
	}
	return err
}

// LoadReviewAnswers returns a run's answers in the order they were first given.
func (s *StateDB) LoadReviewAnswers(runID string) ([]ReviewAnswerRow, error) {
	rows, err := s.db.Query(`SELECT run_id, stop_id, answer, note, updated_at, sent_at FROM review_answers
		WHERE run_id = ? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewAnswerRow
	for rows.Next() {
		var a ReviewAnswerRow
		var upd, sent int64
		if err := rows.Scan(&a.RunID, &a.StopID, &a.Answer, &a.Note, &upd, &sent); err != nil {
			return nil, err
		}
		a.UpdatedAt, a.SentAt = msToTime(upd), msToTime(sent)
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkReviewSeen records that the human opened the run (the sidebar marker
// turns dim). Idempotent: the first opening is the one kept.
func (s *StateDB) MarkReviewSeen(runID string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE review_runs SET seen_at = ? WHERE id = ? AND seen_at = 0`, timeToMS(at), runID)
	return err
}

// MarkReviewSent records that the run's answers were delivered to the session:
// the run becomes sent, and every answer it holds is stamped.
func (s *StateDB) MarkReviewSent(runID string, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE review_runs SET status = ? WHERE id = ?`, ReviewSent, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE review_answers SET sent_at = ? WHERE run_id = ?`, timeToMS(at), runID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkReviewApproved records the human's approval of the run's snapshot.
func (s *StateDB) MarkReviewApproved(runID string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status = ? WHERE id = ?`, ReviewApproved, runID)
	return err
}

// DeleteReviewRunsForCheckout removes every run of a checkout and their
// answers — the review row's discard.
func (s *StateDB) DeleteReviewRunsForCheckout(checkout string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM review_answers WHERE run_id IN (SELECT id FROM review_runs WHERE checkout = ?)`, checkout); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM review_runs WHERE checkout = ?`, checkout); err != nil {
		return err
	}
	return tx.Commit()
}

// clearReviewTarget forgets a deleted session as any run's send target. The
// runs themselves stay: they belong to the checkout, which outlives any one of
// its sessions.
func (s *StateDB) clearReviewTarget(sessionID string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET session_id = '' WHERE session_id = ?`, sessionID)
	return err
}
