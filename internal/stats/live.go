package stats

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// LiveCost is the API-list-price cost of one conversation so far.
type LiveCost struct {
	USD float64
	// Turns is how many priced-or-not turns the figure sums; zero means the
	// transcript holds no completed turn yet.
	Turns int
	// Unpriced counts turns on a model the price table doesn't know: their
	// tokens are in the transcript but their dollars are not in USD, so a
	// non-zero value means the figure is a floor.
	Unpriced int
}

// ErrNotNoted is returned by ScanConversation for a Claude id the store has
// not been told about (NoteSession + Flush first): without the owning fleet
// session there is no way to attribute the turns, and guessing one could take
// a fork's rows from its parent.
var ErrNotNoted = errors.New("stats: conversation not noted")

// ScanConversation reads one conversation's transcript from where the last
// scan stopped and returns what the conversation has cost so far.
//
// It is the one-transcript counterpart of Scan and shares everything that
// makes the Stats screen's dollar figure what it is: the same incremental
// offsets (so a Stats scan right after finds nothing left to read), the same
// per-turn price table, and the same "own turns" rule that drops a fork's
// copied parent history. That is why the live figure and the Stats hero agree
// by construction rather than by tolerance.
//
// path is the transcript file (symlinks resolved here: an account dir links
// projects/ back to ~/.claude, and the file key is the realpath's hash).
// Cheap when nothing changed (a stat), cost proportional to the new bytes
// otherwise. Safe to call concurrently with Scan; the two serialize.
func (s *Store) ScanConversation(ctx context.Context, claudeID, path string) (LiveCost, error) {
	if s == nil {
		return LiveCost{}, nil
	}
	var owner string
	err := s.db.QueryRow(`SELECT session_id FROM claude_ids WHERE claude_session_id = ?`, claudeID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return LiveCost{}, ErrNotNoted
	}
	if err != nil {
		return LiveCost{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return LiveCost{}, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return LiveCost{}, err
	}
	if !st.Mode().IsRegular() {
		return LiveCost{}, errors.New("stats: transcript is not a regular file")
	}
	f := scanFile{path: real, key: fileKey(real), sessionID: owner, size: st.Size(), mtime: st.ModTime()}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if _, _, err := s.scanFile(ctx, f); err != nil {
		return LiveCost{}, err
	}
	return s.conversationCost(f.key)
}

// conversationCost sums the stored turns of one transcript file, applying the
// ownTurns rule so a fork's staged copy of its parent's history isn't charged
// to the fork.
func (s *Store) conversationCost(fileKey string) (LiveCost, error) {
	rows, err := s.db.Query(`SELECT t.model, t.input_tokens, t.output_tokens, t.cache_read, t.cache_write, t.cache_write_1h
		FROM turns t `+ownTurns+` AND t.file_key = ?`, fileKey)
	if err != nil {
		return LiveCost{}, err
	}
	defer rows.Close()
	var c LiveCost
	for rows.Next() {
		var model string
		var in, out, cr, cw5, cw1 int64
		if err := rows.Scan(&model, &in, &out, &cr, &cw5, &cw1); err != nil {
			return LiveCost{}, err
		}
		c.Turns++
		if usd, ok := turnCost(model, in, out, cr, cw5, cw1); ok {
			c.USD += usd
		} else {
			c.Unpriced++
		}
	}
	return c, rows.Err()
}
