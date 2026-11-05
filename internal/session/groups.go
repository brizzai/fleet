package session

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Group is a cross-repo section of the sidebar: the sessions working on one
// thing — a ticket that touches five services, an investigation that fanned
// out. Membership lives on the session row (sessions.group_id); this is the
// group's own identity and name.
//
// Groups are flat. A session spawned by a group member joins the same group
// rather than starting a sub-group, so one piece of work is one section.
type Group struct {
	ID string
	// Name is the user-chosen label ("" = derive it from the lead session's
	// live title, so it follows auto-naming).
	Name string
	// LeadSessionID is the session that started the group — the one whose
	// pane ran `fleet wt` for the first child. Cleared when the lead is
	// deleted, at which point its label is frozen into Name.
	LeadSessionID string
	CreatedAt     time.Time
}

// NewGroupID mints a group id in the session-id shape (<8hex>-<unix>).
func NewGroupID() string { return generateID() }

// LoadGroups returns every group, oldest first. Creation order is the sidebar
// order, so a section never reshuffles when an auto-title lands.
func (s *StateDB) LoadGroups() ([]*Group, error) {
	rows, err := s.db.Query(`SELECT id, name, lead_session_id, created_at FROM session_groups ORDER BY created_at, id`)
	if err != nil {
		debuglog.Logger.Error("failed to load session groups", "error", err)
		return nil, err
	}
	defer rows.Close()
	var out []*Group
	for rows.Next() {
		var g Group
		var created int64
		if err := rows.Scan(&g.ID, &g.Name, &g.LeadSessionID, &created); err != nil {
			debuglog.Logger.Error("failed to scan session group row", "error", err)
			return nil, err
		}
		g.CreatedAt = time.Unix(created, 0)
		out = append(out, &g)
	}
	return out, rows.Err()
}

// CreateGroup inserts a new group row.
func (s *StateDB) CreateGroup(g *Group) error {
	_, err := s.db.Exec(`INSERT INTO session_groups (id, name, lead_session_id, created_at) VALUES (?, ?, ?, ?)`,
		g.ID, g.Name, g.LeadSessionID, g.CreatedAt.Unix())
	if err != nil {
		debuglog.Logger.Error("failed to create session group", "id", g.ID, "error", err)
	}
	return err
}

// GroupExists reports whether a group row with this id exists.
func (s *StateDB) GroupExists(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_groups WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetSessionGroup moves a session into a group ("" = ungrouped).
func (s *StateDB) SetSessionGroup(sessionID, groupID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET group_id = ? WHERE id = ?`, groupID, sessionID)
	if err != nil {
		debuglog.Logger.Error("failed to set session group", "id", sessionID, "group", groupID, "error", err)
	}
	return err
}

// RenameGroup sets a group's explicit name ("" returns it to the lead-derived
// label).
func (s *StateDB) RenameGroup(id, name string) error {
	_, err := s.db.Exec(`UPDATE session_groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		debuglog.Logger.Error("failed to rename session group", "id", id, "error", err)
	}
	return err
}

// RetireGroupLead records that a group's lead is gone. An unnamed group keeps
// the label it was showing (label), so it doesn't suddenly rename itself after
// whichever member happens to survive; a named group keeps its name.
func (s *StateDB) RetireGroupLead(id, label string) error {
	_, err := s.db.Exec(`
		UPDATE session_groups
		SET name = CASE WHEN name = '' THEN ? ELSE name END,
		    lead_session_id = ''
		WHERE id = ?`, label, id)
	if err != nil {
		debuglog.Logger.Error("failed to retire session group lead", "id", id, "error", err)
	}
	return err
}

// DeleteGroup dissolves a group: its members return to their origins (no
// session is deleted) and the group row goes.
func (s *StateDB) DeleteGroup(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE sessions SET group_id = '' WHERE group_id = ?`, id); err != nil {
		debuglog.Logger.Error("failed to ungroup sessions", "group", id, "error", err)
		return err
	}
	if _, err := tx.Exec(`DELETE FROM session_groups WHERE id = ?`, id); err != nil {
		debuglog.Logger.Error("failed to delete session group", "group", id, "error", err)
		return err
	}
	return tx.Commit()
}

// DeleteEmptyGroups removes groups no session row points at. Two exemptions,
// both about timing rather than policy:
//   - groups created at or after createdBefore: a group created by the TUI
//     a moment ago may not have its first member yet;
//   - ids in keep: a deleted member's row is gone during its 5s undo window,
//     and `u` must restore it into a group that still exists.
//
// One transaction, and the DELETE re-checks emptiness itself: a join commits
// between this sweep's SELECT and its DELETE otherwise, and a session saved
// into the group in that gap would be left pointing at nothing.
func (s *StateDB) DeleteEmptyGroups(createdBefore time.Time, keep map[string]bool) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`
		SELECT id FROM session_groups
		WHERE created_at < ?
		  AND id NOT IN (SELECT group_id FROM sessions WHERE group_id != '')`, createdBefore.Unix())
	if err != nil {
		debuglog.Logger.Error("failed to find empty session groups", "error", err)
		return nil, err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !keep[id] {
			candidates = append(candidates, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var gone []string
	for _, id := range candidates {
		res, err := tx.Exec(`
			DELETE FROM session_groups
			WHERE id = ? AND NOT EXISTS (SELECT 1 FROM sessions WHERE group_id = ?)`, id, id)
		if err != nil {
			debuglog.Logger.Error("failed to delete empty session group", "group", id, "error", err)
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			gone = append(gone, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return gone, nil
}

// JoinLeadGroup puts child in the group a session spawned from lead belongs
// to: lead's own group when it has one, otherwise a fresh group led by lead,
// with lead moved into it too. child "" only resolves (and creates) the group.
//
// One write-locked transaction (state.db opens with _txlock=immediate), so:
//   - two children spawned at once by the same parent serialize instead of
//     racing — the second sees the group the first created;
//   - the parent is moved only together with a child that is already saved,
//     so a launch that failed can never leave it regrouped alone;
//   - an empty-group sweep can't delete the group between the read and the
//     child's write.
//
// ok is false when lead is not a session in this database (a stale or foreign
// FLEET_INSTANCE_ID) — nothing is written and the child stays ungrouped.
func (s *StateDB) JoinLeadGroup(leadID, childID string, now time.Time) (groupID string, created, ok bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var current string
	err = tx.QueryRow(`SELECT group_id FROM sessions WHERE id = ?`, leadID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	groupID = current
	if current != "" {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM session_groups WHERE id = ?`, current).Scan(&n); err != nil {
			return "", false, false, err
		}
		if n == 0 {
			// A dangling id (its group was dissolved) is treated as
			// ungrouped — the sidebar renders it that way too.
			groupID = ""
		}
	}
	if groupID == "" {
		groupID, created = NewGroupID(), true
		if _, err := tx.Exec(`INSERT INTO session_groups (id, name, lead_session_id, created_at) VALUES (?, '', ?, ?)`,
			groupID, leadID, now.Unix()); err != nil {
			return "", false, false, err
		}
		if _, err := tx.Exec(`UPDATE sessions SET group_id = ? WHERE id = ?`, groupID, leadID); err != nil {
			return "", false, false, err
		}
	}
	if childID != "" {
		if _, err := tx.Exec(`UPDATE sessions SET group_id = ? WHERE id = ?`, groupID, childID); err != nil {
			return "", false, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, false, err
	}
	return groupID, created, true, nil
}

// JoinNamedGroup puts child in the group whose explicit name is exactly name,
// creating it when there is none. One transaction, for the same reasons as
// JoinLeadGroup: two launches naming the same new group can't each create
// one, and an empty-group sweep can't collect the group between the lookup and
// the child's write.
func (s *StateDB) JoinNamedGroup(name, childID string, now time.Time) (groupID string, created bool, err error) {
	if name == "" {
		return "", false, errors.New("group name is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	err = tx.QueryRow(`SELECT id FROM session_groups WHERE name = ? ORDER BY created_at, id LIMIT 1`, name).Scan(&groupID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		groupID, created = NewGroupID(), true
		if _, err := tx.Exec(`INSERT INTO session_groups (id, name, lead_session_id, created_at) VALUES (?, ?, '', ?)`,
			groupID, name, now.Unix()); err != nil {
			return "", false, err
		}
	case err != nil:
		return "", false, err
	}
	if childID != "" {
		if _, err := tx.Exec(`UPDATE sessions SET group_id = ? WHERE id = ?`, groupID, childID); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return groupID, created, nil
}

// CreateGroupFor creates g and moves sessionID into it, in one transaction, so
// a failure can't leave an empty group for the sync sweep to show in the
// "Move to Group…" picker until the collector catches up.
func (s *StateDB) CreateGroupFor(g *Group, sessionID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO session_groups (id, name, lead_session_id, created_at) VALUES (?, ?, ?, ?)`,
		g.ID, g.Name, g.LeadSessionID, g.CreatedAt.Unix()); err != nil {
		debuglog.Logger.Error("failed to create session group", "id", g.ID, "error", err)
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET group_id = ? WHERE id = ?`, g.ID, sessionID); err != nil {
		debuglog.Logger.Error("failed to move session into new group", "id", sessionID, "group", g.ID, "error", err)
		return err
	}
	return tx.Commit()
}

// GroupLabelFromLead is the label an unnamed group shows: its lead's title,
// led by the ticket the lead's branch carries when there is one ("BRZ-12 ·
// Fix login"). One spelling for the TUI and `fleet remove`, which freezes it.
func GroupLabelFromLead(title, ticketID string) string {
	if ticketID != "" && !strings.HasPrefix(strings.ToUpper(title), ticketID) {
		return ticketID + " · " + title
	}
	return title
}

// SessionGroupIDs returns every session's group id, keyed by session id. The
// UI's sync sweep diffs this against memory: another process can move a
// session the UI already holds (a spawned child pulls its parent into a new
// group), which adoption — new rows only — would never see.
func SessionGroupIDs(rows []*SessionRow) map[string]string {
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.GroupID
	}
	return out
}
