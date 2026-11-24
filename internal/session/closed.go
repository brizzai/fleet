package session

import (
	"encoding/json"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// ClosedKeep is how many closed sessions are remembered for "Reopen last
// closed session". Older ones fall off; a closed session is cheap to forget
// because the conversation itself lives in the agent's own transcript.
const ClosedKeep = 20

// ClosedSession is a session the user closed, kept so it can be brought back.
type ClosedSession struct {
	Row      SessionRow
	ClosedAt time.Time
}

// RecordClosed remembers a closed session (replacing an earlier entry with the
// same id) and trims the list to the newest ClosedKeep.
func (s *StateDB) RecordClosed(row *SessionRow, at time.Time) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO closed_sessions (id, closed_at, row_json) VALUES (?, ?, ?)`,
		row.ID, at.UnixNano(), string(b)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM closed_sessions WHERE id NOT IN
		(SELECT id FROM closed_sessions ORDER BY closed_at DESC, id DESC LIMIT ?)`, ClosedKeep); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadClosed returns the remembered closed sessions, newest first.
func (s *StateDB) LoadClosed() ([]ClosedSession, error) {
	rows, err := s.db.Query(`SELECT closed_at, row_json FROM closed_sessions ORDER BY closed_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClosedSession
	for rows.Next() {
		var at int64
		var js string
		if err := rows.Scan(&at, &js); err != nil {
			return nil, err
		}
		var row SessionRow
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			debuglog.Logger.Warn("skipping unreadable closed-session row", "err", err)
			continue
		}
		out = append(out, ClosedSession{Row: row, ClosedAt: time.Unix(0, at)})
	}
	return out, rows.Err()
}

// DeleteClosed forgets one closed session (it was reopened, undone, or is
// beyond saving).
func (s *StateDB) DeleteClosed(id string) error {
	_, err := s.db.Exec(`DELETE FROM closed_sessions WHERE id = ?`, id)
	return err
}
