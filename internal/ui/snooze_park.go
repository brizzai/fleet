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

// parkSweepInterval throttles the tick-driven park sweep, matching the wake
// sweep. A session that finishes is parked within this window; a fresh
// snooze doesn't wait for it (the snoozeSelectedMsg handler parks at once).
const parkSweepInterval = snoozeSweepInterval

// parkIntent reports whether an "until I wake it" snooze asks for this session
// to be parked — its own, or one on its checkout or origin group. A group parks
// only the sessions that were already in it and untouched when it was snoozed:
// one created or opened since is the user working there on purpose, and
// re-suspending it every time it went idle would fight them. The group's
// snooze still mutes it — parking and muting are separate.
func parkIntent(s *session.Session, originKey, repo string, groups, groupAt map[string]time.Time) bool {
	if session.IsSnoozeForever(s.SnoozedUntil()) {
		return true
	}
	for _, key := range []string{OriginExpandKey(originKey), repo} {
		if !session.IsSnoozeForever(groups[key]) {
			continue
		}
		at := groupAt[key]
		if !s.CreatedAt.After(at) && !s.LastAccessed().After(at) {
			return true
		}
	}
	return false
}

// parkableNow reports whether a session can be suspended without losing anything:
// at rest, and resumable — with no conversation id the resume would start a
// fresh agent and the conversation would be gone.
func parkableNow(s *session.Session) bool {
	switch s.GetStatus() {
	case session.StatusIdle, session.StatusFinished:
	default:
		return false
	}
	return s.GetClaudeSessionID() != "" && !s.Relaunching()
}

// parkCandidates returns the sessions an "until I wake it" snooze should
// suspend right now. skip holds sessions the user is looking at (attached, or
// focused in the split), which are never pulled out from under them.
func parkCandidates(sessions []*session.Session, originOf func(string) string, groups, groupAt map[string]time.Time, skip map[string]bool) []*session.Session {
	var out []*session.Session
	for _, s := range sessions {
		if skip[s.ID] || !parkableNow(s) {
			continue
		}
		repo := session.GetRepoRoot(s.ProjectPath)
		if parkIntent(s, originOf(repo), repo, groups, groupAt) {
			out = append(out, s)
		}
	}
	return out
}

// parkSkipSet is the sessions parking must leave alone: the attached one (its
// id is written by the attach path under workerMu) and, in split focus mode,
// the one being typed into.
func (h *Home) parkSkipSet() map[string]bool {
	skip := map[string]bool{}
	h.workerMu.Lock()
	if h.attachedSessionID != "" {
		skip[h.attachedSessionID] = true
	}
	h.workerMu.Unlock()
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
func (h *Home) parkSnoozedNow(scope func(*session.Session) bool) tea.Cmd {
	originOf, _ := h.originResolvers()
	var targets []*session.Session
	for _, s := range parkCandidates(h.sessions, originOf, h.groupSnooze, h.groupSnoozedAt, h.parkSkipSet()) {
		if scope != nil && !scope(s) {
			continue
		}
		repo := session.GetRepoRoot(s.ProjectPath)
		if !h.stillWantsPark(s, originOf(repo), repo) {
			continue
		}
		targets = append(targets, s)
	}
	if len(targets) == 0 {
		return nil
	}
	debuglog.Logger.Info("parking snoozed sessions", "n", len(targets))
	return h.suspendSessions(targets, true)
}

// stillWantsPark re-reads the session's row before it is suspended and adopts
// what another fleet process wrote there: `fleet restart` respawns in place
// (no tmux rename, so relaunchedElsewhere never sees it) and clears the snooze
// in the row, and every CLI relaunch stamps last_accessed. Suspending is the
// one step here that can't be quietly undone, so it compares before it acts,
// like updateAndPersistStatus does before clobbering a status. A failed read
// errs on the side of not suspending.
func (h *Home) stillWantsPark(s *session.Session, originKey, repo string) bool {
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
	return parkIntent(s, originKey, repo, h.groupSnooze, h.groupSnoozedAt)
}

// maybeParkSnoozed is parkSnoozedNow on the tick, throttled.
func (h *Home) maybeParkSnoozed() tea.Cmd {
	now := time.Now()
	if !h.lastParkSweepAt.IsZero() && now.Sub(h.lastParkSweepAt) < parkSweepInterval {
		return nil
	}
	h.lastParkSweepAt = now
	return h.parkSnoozedNow(nil)
}

// endIndefiniteSnooze drops a session's own "until I wake it" snooze when the
// user picks the session back up (attach, resume, or a relaunch from the CLI).
// Leaving it would have the sweep suspend the session again the moment it next
// went idle. A timed snooze is a reminder and is left to run.
func (h *Home) endIndefiniteSnooze(s *session.Session) {
	if !session.IsSnoozeForever(s.SnoozedUntil()) {
		return
	}
	s.SetSnoozedUntil(time.Time{})
	if err := h.storage.SetSessionSnooze(s.ID, time.Time{}); err != nil {
		debuglog.Logger.Error("failed to clear indefinite snooze", "id", s.ID, "err", err)
	}
	h.rebuildFlatItems()
}

// parkNote is the tail of the "Snoozed …" toast, saying what the snooze will
// do to the process. Computed from the same rules the sweep applies, so it
// can't promise a suspend the sweep won't make. Empty for a timed snooze or
// when there is nothing to park.
func (h *Home) parkNote(sc snoozeScope) string {
	originOf, _ := h.originResolvers()
	inScope := h.inSnoozeScope(sc)
	skip := h.parkSkipSet()
	// now: suspended right away. later: busy, parked once it settles.
	// unresumable: no conversation id yet, so suspending would lose it — it
	// stays running until it has one, and saying "once idle" about an idle
	// session would be a promise the sweep doesn't keep.
	now, later, unresumable := 0, 0, 0
	for _, s := range h.sessions {
		if !inScope(s) || s.GetStatus() == session.StatusSuspended {
			continue
		}
		repo := session.GetRepoRoot(s.ProjectPath)
		if !parkIntent(s, originOf(repo), repo, h.groupSnooze, h.groupSnoozedAt) {
			continue
		}
		switch {
		case s.GetClaudeSessionID() == "":
			unresumable++
		case !skip[s.ID] && parkableNow(s):
			now++
		default:
			later++
		}
	}
	switch {
	case now > 0 && later+unresumable > 0:
		return fmt.Sprintf(" · suspending %d, the rest once idle", now)
	case now > 0:
		return " · suspending to free memory"
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
	if sc.session != nil {
		if sc.session.GetStatus() == session.StatusSuspended {
			return " · enter to resume"
		}
		return ""
	}
	inScope := h.inSnoozeScope(sc)
	n := 0
	for _, s := range h.sessions {
		if inScope(s) && s.GetStatus() == session.StatusSuspended {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
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

// snoozedRowHint is the selected row's own-snooze suffix:
// "☾ snoozed 3h · z to wake", or "☾ snoozed · z to wake" with no deadline.
func snoozedRowHint(until, now time.Time) string {
	if session.IsSnoozeForever(until) {
		return SnoozeGlyph + " snoozed · z to wake"
	}
	return SnoozeGlyph + " snoozed " + formatSnoozeRemaining(until, now) + " · z to wake"
}
