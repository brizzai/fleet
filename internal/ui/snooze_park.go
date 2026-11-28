package ui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// Parking: an "until I wake it" snooze also suspends the agent process, so a
// session you've set aside stops holding ~400MB. It is the only snooze that
// does — a timed snooze is a reminder, and its expiry (idle → finished) needs
// a live session to flip.
//
// Parking never kills work in progress. A session is suspended only once it is
// at rest (idle or finished, the same rule as the manual suspend command), so
// a running one is parked when it finishes and a waiting one stays muted and
// live until someone answers it. Waking (`z`) only ends the mute; the process
// comes back lazily on Enter, like any suspended row.
//
// A finished session is parked too, and suspending it replaces Finished, so
// it comes back from a wake as a plain suspended row rather than an unread
// one. That trade is deliberate: the user set it aside for good, and keeping
// it running until someone looked at it would hold the memory this is for.

// parkIntent reports whether an "until I wake it" snooze asks for this session
// to be parked — its own, or one on its checkout or origin group. A group parks
// only the sessions that were already in it and untouched when it was snoozed:
// one created or opened since is the user working there on purpose, and
// re-suspending it every time it went idle would fight them. The group's
// snooze still mutes it — parking and muting are separate.
//
// groupAt is whole seconds (it is stored that way), and so are a reloaded
// session's timestamps, so "before" is strict: a session created or opened in
// the same second as the snooze is not known to predate it, and stays running.
func parkIntent(s *session.Session, originKey, repo string, groups, groupAt map[string]time.Time) bool {
	if session.IsSnoozeForever(s.SnoozedUntil()) {
		return true
	}
	for _, key := range []string{OriginExpandKey(originKey), repo} {
		if !session.IsSnoozeForever(groups[key]) {
			continue
		}
		at := groupAt[key]
		if s.CreatedAt.Before(at) && s.LastAccessed().Before(at) {
			return true
		}
	}
	return false
}

// wantsPark is parkIntent against this Home's group snoozes.
func (h *Home) wantsPark(s *session.Session) bool {
	originOf, _ := h.originResolvers()
	repo := session.GetRepoRoot(s.ProjectPath)
	return parkIntent(s, originOf(repo), repo, h.groupSnooze, h.groupSnoozedAt)
}

// parkSkipSet is the sessions parking must leave alone: the attached one and,
// in split focus mode, the one being typed into.
func (h *Home) parkSkipSet() map[string]bool {
	skip := map[string]bool{}
	if id := h.attachedID(); id != "" {
		skip[id] = true
	}
	if h.focusMode {
		if s := h.selectedSession(); s != nil {
			skip[s.ID] = true
		}
	}
	return skip
}

// parkSnoozedNow suspends the sessions an "until I wake it" snooze is waiting
// to park, returning the teardown command (nil when there is none). scope
// limits it to some sessions (nil = all): picking a snooze parks only what it
// covers, so the toast that follows counts only that.
//
// It waits for the status worker's first completed pass: until then a status
// may still be the one loaded from the row, and a session that row calls
// finished may already be running a turn a CLI started while fleet was closed.
func (h *Home) parkSnoozedNow(scope func(*session.Session) bool) tea.Cmd {
	if h.lastWorkerCycleNano.Load() == 0 {
		return nil
	}
	if err := h.refreshIndefiniteSnoozes(); err != nil {
		debuglog.Logger.Error("park: re-read snoozes", "err", err)
		return nil // stale state is no basis for a kill; the next sweep retries
	}
	skip := h.parkSkipSet()
	var targets []*session.Session
	for _, s := range h.sessions {
		if scope != nil && !scope(s) {
			continue
		}
		if skip[s.ID] || !s.CanSuspend() || !h.wantsPark(s) {
			continue
		}
		if !h.adoptSessionRow(s) || !h.wantsPark(s) {
			continue
		}
		targets = append(targets, s)
	}
	if len(targets) == 0 {
		return nil
	}
	debuglog.Logger.Info("parking snoozed sessions", "n", len(targets))
	return h.suspendSessions(targets, suspendSnoozed)
}

// refreshIndefiniteSnoozes adopts what other fleet processes wrote about the
// "until I wake it" snoozes this one holds: a group woken elsewhere, or a
// session whose snooze `fleet restart` cleared while respawning in place (no
// tmux rename, so relaunchedElsewhere never sees it). Suspending is the one
// step here that can't be quietly undone, so it compares before it acts, like
// updateAndPersistStatus does before clobbering a status. It also keeps the
// sidebar honest between parks: a session picked up from the CLI stops
// rendering as snoozed within one sweep.
func (h *Home) refreshIndefiniteSnoozes() error {
	changed := false
	for key, until := range h.groupSnooze {
		if !session.IsSnoozeForever(until) {
			continue
		}
		stored, at, ok, err := h.storage.LoadGroupSnooze(key)
		if err != nil {
			return fmt.Errorf("group %s: %w", key, err)
		}
		switch {
		case !ok:
			h.dropGroupSnooze(key)
			changed = true
		case !session.IsSnoozeForever(stored):
			h.groupSnooze[key] = stored
			changed = true
		default:
			h.groupSnoozedAt[key] = at
		}
	}
	for _, s := range h.sessions {
		if !session.IsSnoozeForever(s.SnoozedUntil()) {
			continue
		}
		if !h.adoptSessionRow(s) {
			continue // a missing row is a session being deleted; nothing to show
		}
		if !session.IsSnoozeForever(s.SnoozedUntil()) {
			changed = true
		}
	}
	if changed {
		h.rebuildFlatItems()
	}
	return nil
}

// adoptSessionRow re-reads a session's row and adopts a cleared "until I wake
// it" snooze and a newer last access (every CLI relaunch stamps one). It
// reports false when the row can't be read or is gone, which means: don't
// suspend.
func (h *Home) adoptSessionRow(s *session.Session) bool {
	until, lastAccessed, ok, err := h.storage.LoadSessionSnooze(s.ID)
	if err != nil {
		debuglog.Logger.Error("park: re-read session row", "id", s.ID, "err", err)
		return false
	}
	if !ok {
		return false // row gone: the session is being deleted
	}
	if session.IsSnoozeForever(s.SnoozedUntil()) && !session.IsSnoozeForever(until) {
		s.SetSnoozedUntil(until)
	}
	s.AdoptLastAccessed(lastAccessed)
	return true
}

// endIndefiniteSnooze drops a session's own "until I wake it" snooze when the
// user picks the session back up (attach, resume, restart, split focus).
// Leaving it would have the sweep suspend the session again the moment it next
// went idle. A timed snooze is a reminder and is left to run; the row write is
// conditional for the same reason, in case another process set one meanwhile.
func (h *Home) endIndefiniteSnooze(s *session.Session) {
	if !session.IsSnoozeForever(s.SnoozedUntil()) {
		return
	}
	s.SetSnoozedUntil(time.Time{})
	if err := h.storage.ClearIndefiniteSessionSnooze(s.ID); err != nil {
		debuglog.Logger.Error("failed to clear indefinite snooze", "id", s.ID, "err", err)
	}
	h.rebuildFlatItems()
}

// parkNote is the tail of the "Snoozed …" toast, saying what the snooze will
// do to the processes it doesn't suspend right away. The ones it does suspend
// get their own toast once the teardown finishes (sessionsSuspendedMsg), which
// counts what really happened, so this one doesn't count them. Empty when no
// "until I wake it" snooze (the session's own or its group's) covers the scope.
func (h *Home) parkNote(sc snoozeScope) string {
	inScope := h.inSnoozeScope(sc)
	skip := h.parkSkipSet()
	// now: suspended right away. later: busy, parked once it settles.
	// unresumable: no conversation id yet, so suspending would lose it — it
	// stays running until it has one, and saying "once idle" about an idle
	// session would be a promise the sweep doesn't keep.
	now, later, unresumable := 0, 0, 0
	for _, s := range h.sessions {
		if !inScope(s) || s.GetStatus() == session.StatusSuspended || !h.wantsPark(s) {
			continue
		}
		switch {
		case s.GetClaudeSessionID() == "":
			unresumable++
		case !skip[s.ID] && s.CanSuspend():
			now++
		default:
			later++
		}
	}
	switch {
	case now > 0 && later+unresumable > 0:
		return " · the rest suspend once idle"
	case now > 0:
		return ""
	case later > 0:
		return " · suspends once idle"
	case unresumable > 0:
		return " · stays running: nothing to resume yet"
	}
	return ""
}

// wakeNote is the tail of the "Woke …" toast: resume is lazy, so a wake that
// leaves suspended sessions behind says how to bring them back.
func (h *Home) wakeNote(sc snoozeScope) string {
	inScope := h.inSnoozeScope(sc)
	n := 0
	for _, s := range h.sessions {
		if inScope(s) && s.GetStatus() == session.StatusSuspended {
			n++
		}
	}
	switch {
	case n == 0:
		return ""
	case sc.session != nil:
		return " · enter to resume"
	case n == 1:
		return " · 1 suspended, enter to resume it"
	}
	return fmt.Sprintf(" · %d suspended, enter to resume each", n)
}

// inSnoozeScope reports which sessions a snooze scope covers: the session
// itself, or every session in the checkout or origin group.
func (h *Home) inSnoozeScope(sc snoozeScope) func(*session.Session) bool {
	if sc.session != nil {
		id := sc.session.ID
		return func(s *session.Session) bool { return s.ID == id }
	}
	originOf, _ := h.originResolvers()
	return func(s *session.Session) bool {
		repo := session.GetRepoRoot(s.ProjectPath)
		return repo == sc.groupKey || OriginExpandKey(originOf(repo)) == sc.groupKey
	}
}
