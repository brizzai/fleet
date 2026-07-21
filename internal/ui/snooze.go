package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// Snooze mutes a session — or a whole repo/worktree group — from the attention
// surfaces (the Space jump rotation and the status pills) until a deadline.
//
// It is deliberately NOT a Status. A snoozed session that is waiting is still
// waiting; you just don't want to be told about it right now. Overwriting the
// status would destroy that information and force us to reconstruct it on wake.
// It is also not Suspend: snooze never touches the process, which is what makes
// it safe to snooze a session that is actively running.

// SnoozeGlyph marks a muted row. U+263E, not the more obvious U+23FE "power
// sleep" symbol: Menlo (macOS Terminal's default) has no U+23FE glyph and would
// render it as a fallback box, exactly the failure that got U+2B21 rejected for
// the agent sigils. U+263E is covered by the same Menlo faces as `✻`, and
// ansi.StringWidth reports 1 so it costs one cell like every other marker.
const SnoozeGlyph = "☾"

// SnoozeDuration is one entry in the duration picker. Fixed set, no custom
// input — the whole point is to decide in one keystroke.
type SnoozeDuration struct {
	// ID is the analytics/label key ("30m", "1h", "4h", "tomorrow").
	ID string
	// Label is what the picker row reads.
	Label string
	// resolve computes the wake time from now. A function rather than a
	// time.Duration because "tomorrow" is a wall-clock target, not an offset.
	resolve func(now time.Time) time.Time
}

// snoozeTomorrowHour is when "Tomorrow" wakes: the start of the next workday
// rather than a literal +24h, which at 11pm would wake you at 11pm.
const snoozeTomorrowHour = 9

// SnoozeDurations is the picker's fixed menu, in display order.
var SnoozeDurations = []SnoozeDuration{
	{ID: "30m", Label: "30 minutes", resolve: func(now time.Time) time.Time { return now.Add(30 * time.Minute) }},
	{ID: "1h", Label: "1 hour", resolve: func(now time.Time) time.Time { return now.Add(time.Hour) }},
	{ID: "4h", Label: "4 hours", resolve: func(now time.Time) time.Time { return now.Add(4 * time.Hour) }},
	{ID: "tomorrow", Label: "Tomorrow", resolve: snoozeTomorrow},
}

// Resolve returns the absolute wake time for this duration.
func (d SnoozeDuration) Resolve(now time.Time) time.Time { return d.resolve(now) }

// snoozeTomorrow resolves to the next calendar day at snoozeTomorrowHour, in
// the machine's local zone. Uses the date parts rather than a 24h offset so a
// DST boundary still lands on 09:00 wall-clock.
func snoozeTomorrow(now time.Time) time.Time {
	d := now.AddDate(0, 0, 1)
	return time.Date(d.Year(), d.Month(), d.Day(), snoozeTomorrowHour, 0, 0, 0, d.Location())
}

// snoozeResult is what the sidebar needs to know about one session's mute.
type snoozeResult struct {
	// Muted is true when the session should be dropped from the attention
	// surfaces and dimmed.
	Muted bool
	// Until is the deadline responsible for the mute.
	Until time.Time
	// OwnTimer is true only when the mute comes from the session's *own*
	// snooze. A session muted by its group shows a bare marker and no clock —
	// the countdown lives on the group header, so N children don't render N
	// copies of the same number.
	OwnTimer bool
}

// snoozeState resolves a session's mute. The group umbrella wins over the
// session's own deadline: origin first (the widest scope), then checkout, then
// the session itself. A session may hold its own snooze inside a snoozed group;
// that deadline keeps ticking independently and takes effect once the group wakes.
//
// This is the single place the precedence rule lives. Callers consult the
// result — they must not re-derive it.
func snoozeState(s *session.Session, originKey, repoPath string, groups map[string]time.Time, now time.Time) snoozeResult {
	if until, ok := activeGroupSnooze(groups, OriginExpandKey(originKey), now); ok {
		return snoozeResult{Muted: true, Until: until}
	}
	if until, ok := activeGroupSnooze(groups, repoPath, now); ok {
		return snoozeResult{Muted: true, Until: until}
	}
	if s != nil {
		if until := s.SnoozedUntil(); !until.IsZero() && until.After(now) {
			return snoozeResult{Muted: true, Until: until, OwnTimer: true}
		}
	}
	return snoozeResult{}
}

// activeGroupSnooze looks up a group key, treating a lapsed deadline as absent
// so an un-swept row never mutes anything.
func activeGroupSnooze(groups map[string]time.Time, key string, now time.Time) (time.Time, bool) {
	if len(groups) == 0 || key == "" {
		return time.Time{}, false
	}
	until, ok := groups[key]
	if !ok || until.IsZero() || !until.After(now) {
		return time.Time{}, false
	}
	return until, true
}

// groupSnoozeAt returns a group's own live snooze deadline, or the zero time.
// Unlike snoozeState it does not consult the parent origin: a checkout inside a
// snoozed origin renders no clock of its own, for the same reason its sessions
// don't — one countdown per snooze, owned by whoever holds it.
func groupSnoozeAt(groups map[string]time.Time, key string, now time.Time) time.Time {
	until, _ := activeGroupSnooze(groups, key, now)
	return until
}

// snoozeMenuItem builds the one context-menu row snooze needs. It's a toggle,
// exactly like the `z` key: snoozed rows offer Wake, everything else offers
// Snooze. Both states are always reachable, so there is no guard to fail and
// therefore no dim note — per the project rule, a row that can never light up
// is worse than no row.
//
// Note a session muted only by its group still reads "Snooze…", which is
// accurate: it has no snooze of its own to wake, and setting one is meaningful
// (it outlives the group's).
func (h *Home) snoozeMenuItem() ContextMenuItem {
	sc, ok := h.snoozeScopeAtCursor()
	if !ok {
		return ContextMenuItem{}
	}
	if h.snoozed(sc) {
		return ContextMenuItem{ID: "unsnooze", Label: "Wake Now", Shortcut: "z", Key: "z", Enabled: true}
	}
	return ContextMenuItem{ID: "snooze", Label: "Snooze…", Shortcut: "z", Key: "z", Enabled: true}
}

// snoozeSweepInterval throttles the wake sweep. The shortest duration on the
// menu is 30m, so sub-minute precision buys nothing; 15s keeps a wake feeling
// immediate without waking the tick handler up for no reason.
const snoozeSweepInterval = 15 * time.Second

// snoozeScope describes what the row under the cursor snoozes.
type snoozeScope struct {
	// session is set for a session row; groupKey for a header row. Exactly one
	// is non-empty on a valid scope.
	session  *session.Session
	groupKey string
	// label names the target in toasts ("Fix the drawer bug", "main", "fleet").
	label string
	// kind is the analytics dimension: session | checkout | origin.
	kind string
}

// snoozeScopeAtCursor classifies the row under the cursor. Returns ok=false on
// a spacer, a pending row, or an out-of-range cursor.
func (h *Home) snoozeScopeAtCursor() (snoozeScope, bool) {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return snoozeScope{}, false
	}
	item := h.flatItems[h.cursor]
	switch {
	case item.IsOriginHeader:
		return snoozeScope{groupKey: OriginExpandKey(item.OriginKey), label: labelForOrigin(item.OriginKey), kind: "origin"}, true
	case item.IsCheckoutHeader:
		return snoozeScope{groupKey: item.RepoPath, label: filepath.Base(item.RepoPath), kind: "checkout"}, true
	case item.Session != nil:
		return snoozeScope{session: item.Session, label: item.Session.Title, kind: "session"}, true
	}
	return snoozeScope{}, false
}

// snoozed reports whether this scope currently holds a snooze of its own. For a
// session that means its own deadline, not an umbrella from its group — waking
// a session muted only by its repo would change nothing visible.
func (h *Home) snoozed(sc snoozeScope) bool {
	now := time.Now()
	if sc.session != nil {
		return sc.session.IsSnoozed(now)
	}
	return !groupSnoozeAt(h.groupSnooze, sc.groupKey, now).IsZero()
}

// applySnooze sets one of the preset durations on the cursor's scope.
func (h *Home) applySnooze(sc snoozeScope, d SnoozeDuration) {
	h.applySnoozeUntil(sc, d.Resolve(time.Now()), d.ID)
}

// applySnoozeUntil sets an already-resolved deadline. durationID is the
// analytics dimension — a preset id, or "custom" for a typed duration.
//
// A group snooze also collapses the group: the point of muting a repo is to get
// it out of your way, and a fold is the strongest way to say "not today". The
// user can still re-expand it.
func (h *Home) applySnoozeUntil(sc snoozeScope, until time.Time, durationID string) {
	if sc.session != nil {
		sc.session.SetSnoozedUntil(until)
		if err := h.storage.SetSessionSnooze(sc.session.ID, until); err != nil {
			debuglog.Logger.Error("failed to persist session snooze", "id", sc.session.ID, "err", err)
		}
	} else {
		h.groupSnooze[sc.groupKey] = until
		if err := h.storage.SetGroupSnooze(sc.groupKey, until); err != nil {
			debuglog.Logger.Error("failed to persist group snooze", "key", sc.groupKey, "err", err)
		}
		h.setExpanded(sc.groupKey, false)
	}
	h.rebuildFlatItems()
	h.actionLog.Add("snooze "+sc.kind, sc.label, true)
	analytics.Track(analytics.EventSnoozeSet, map[string]interface{}{
		"scope":    sc.kind,
		"duration": durationID,
	})
	h.setInfo(fmt.Sprintf("Snoozed %s until %s", sc.label, formatSnoozeWake(until, time.Now())))
}

// clearSnooze wakes the cursor's scope now. A woken group re-expands, mirroring
// the auto-collapse; setExpanded is idempotent, so a group the user re-expanded
// by hand meanwhile is unaffected.
//
// Known limitation: a group that was ALREADY folded before the snooze does
// spring open on wake — snooze takes ownership of a collapse state that
// predated it. Accepted deliberately: knowing the difference means persisting
// "the snooze is what collapsed this" across restarts (the restart is the
// common wake path — see the startup reconciliation in loadSessions), i.e. a
// schema change on snoozed_groups for a fairly small nicety.
func (h *Home) clearSnooze(sc snoozeScope) {
	if sc.session != nil {
		sc.session.SetSnoozedUntil(time.Time{})
		if err := h.storage.SetSessionSnooze(sc.session.ID, time.Time{}); err != nil {
			debuglog.Logger.Error("failed to clear session snooze", "id", sc.session.ID, "err", err)
		}
	} else {
		delete(h.groupSnooze, sc.groupKey)
		if err := h.storage.SetGroupSnooze(sc.groupKey, time.Time{}); err != nil {
			debuglog.Logger.Error("failed to clear group snooze", "key", sc.groupKey, "err", err)
		}
		h.setExpanded(sc.groupKey, true)
	}
	h.rebuildFlatItems()
	h.actionLog.Add("wake "+sc.kind, sc.label, true)
	analytics.Track(analytics.EventSnoozeCleared, map[string]interface{}{
		"scope":  sc.kind,
		"reason": "manual",
	})
	h.setInfo(fmt.Sprintf("Woke %s", sc.label))
}

// maybeWakeSnoozed drops deadlines that have passed, returning true when
// anything changed. Runs on the ~2s Update tick rather than the worker: unlike
// the idle-suspend sweep (which probes sysctl and must not block Update), this
// is clock arithmetic plus a small SQLite write, and keeping it here means
// groupSnooze stays a plain single-threaded map.
func (h *Home) maybeWakeSnoozed() bool {
	now := time.Now()
	if !h.lastSnoozeSweepAt.IsZero() && now.Sub(h.lastSnoozeSweepAt) < snoozeSweepInterval {
		return false
	}
	h.lastSnoozeSweepAt = now

	changed := false
	for _, s := range h.sessions {
		if until := s.SnoozedUntil(); !until.IsZero() && !until.After(now) {
			s.SetSnoozedUntil(time.Time{})
			if err := h.storage.SetSessionSnooze(s.ID, time.Time{}); err != nil {
				debuglog.Logger.Error("failed to clear expired session snooze", "id", s.ID, "err", err)
			}
			changed = true
		}
	}
	for key, until := range h.groupSnooze {
		if until.IsZero() || !until.After(now) {
			delete(h.groupSnooze, key)
			if err := h.storage.SetGroupSnooze(key, time.Time{}); err != nil {
				debuglog.Logger.Error("failed to clear expired group snooze", "key", key, "err", err)
			}
			// Symmetric with the auto-collapse in applySnooze.
			h.setExpanded(key, true)
			changed = true
		}
	}
	if changed {
		analytics.Track(analytics.EventSnoozeCleared, map[string]interface{}{"reason": "expired"})
		h.rebuildFlatItems()
	}
	return changed
}

// formatSnoozeWake renders a wake time for confirmation copy: a clock time
// today, or a weekday-qualified one past midnight so "tomorrow" is unambiguous.
func formatSnoozeWake(until, now time.Time) string {
	if until.YearDay() == now.YearDay() && until.Year() == now.Year() {
		return until.Format("15:04")
	}
	return until.Format("Mon 15:04")
}

// formatSnoozeRemaining renders a deadline as compact time-left, capped at 3
// cells so it fits the budget renderSessionItem carves out of the title. The
// custom input allows up to 30d, so days is the widest unit — "30d" is 3 cells,
// which is what bounds the input rather than the display.
//
// Rounds UP: a snooze set for 4h has a few microseconds already gone by the
// time it renders, and truncating would show a stale "3h" for the first 59
// minutes of it.
func formatSnoozeRemaining(until, now time.Time) string {
	d := until.Sub(now)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(math.Ceil(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(math.Ceil(d.Hours())))
	default:
		return fmt.Sprintf("%dd", int(math.Ceil(d.Hours()/24)))
	}
}
