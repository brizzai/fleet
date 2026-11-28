package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// newParkTestHome is a persisted Home whose status worker counts as having
// completed a pass, since parking waits for one.
func newParkTestHome(t *testing.T) (*Home, *session.StateDB) {
	t.Helper()
	h := newPersistTestHome(t)
	h.lastWorkerCycleNano.Store(1)
	return h, h.storage
}

// snoozeGroup persists an "until I wake it" group snooze stamped at, and
// mirrors it in memory, as applySnoozeUntil would. The park sweep re-reads the
// row, so a memory-only group would read as woken elsewhere.
func snoozeGroup(t *testing.T, h *Home, key string, at time.Time) {
	t.Helper()
	at = at.Truncate(time.Second)
	if err := h.storage.SetGroupSnoozeAt(key, session.SnoozeForever, at); err != nil {
		t.Fatalf("SetGroupSnoozeAt: %v", err)
	}
	h.groupSnooze[key] = session.SnoozeForever
	h.groupSnoozedAt[key] = at
}

// saveRows persists sessions: the park sweep re-reads each row before it
// suspends, and treats a missing row as a session being deleted.
func saveRows(t *testing.T, storage *session.StateDB, ss ...*session.Session) {
	t.Helper()
	for _, s := range ss {
		if err := storage.SaveSession(s.ToRow()); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}
}

// parkable builds a session that is at rest and resumable, so the only thing
// deciding whether it parks is the snooze under test.
func parkable(title, repo string, status session.Status) *session.Session {
	s := session.NewSession(title, repo)
	s.SetStatus(status)
	s.ClaudeSessionID = "conv-" + title
	return s
}

// TestSnoozeDialogOffersIndefinite: the open-ended option is a preset like the
// others — the last one, right above the typed input — and says what it costs.
func TestSnoozeDialogOffersIndefinite(t *testing.T) {
	d := NewSnoozeDialog()
	d.SetSize(120, 40)
	d.Show("Snooze x")
	view := d.View()
	for _, want := range []string{"Until I wake", "frees RAM"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}

	d.setFocus(snoozeInputRow() - 1)
	_, cmd := d.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on the indefinite row produced no command")
	}
	msg := cmd().(snoozeSelectedMsg)
	if !session.IsSnoozeForever(msg.until) {
		t.Errorf("until = %v, want SnoozeForever", msg.until)
	}
	if msg.durationID != snoozeIndefiniteID {
		t.Errorf("durationID = %q, want %q", msg.durationID, snoozeIndefiniteID)
	}
}

// TestParkIntent pins who an "until I wake it" snooze asks to park, and
// CanSuspend who can be parked now. At rest and resumable only — a running or
// waiting session is never killed silently, and one with no conversation id
// would lose its conversation on resume. A group parks only the sessions that
// were already there, untouched, when it was snoozed: creating or opening one
// since is the user working there. "Before" is strict, because after a reload
// every one of these timestamps is whole seconds.
func TestParkIntent(t *testing.T) {
	now := time.Now()
	const repo, origin = "/tmp/park", "github.com/acme/park"
	at := now.Add(-time.Hour).Truncate(time.Second)
	before := at.Add(-time.Hour)
	groups := map[string]time.Time{repo: session.SnoozeForever, "/tmp/own": now.Add(time.Hour)}
	groupAt := map[string]time.Time{repo: at}

	cases := []struct {
		name          string
		repo          string
		status        session.Status
		noID          bool
		own           time.Time
		created, used time.Time
		wantIntent    bool
		wantNow       bool
	}{
		{name: "own idle", repo: "/tmp/own", status: session.StatusIdle, own: session.SnoozeForever, wantIntent: true, wantNow: true},
		{name: "own finished", repo: "/tmp/own", status: session.StatusFinished, own: session.SnoozeForever, wantIntent: true, wantNow: true},
		{name: "own running", repo: "/tmp/own", status: session.StatusRunning, own: session.SnoozeForever, wantIntent: true},
		{name: "own waiting", repo: "/tmp/own", status: session.StatusWaiting, own: session.SnoozeForever, wantIntent: true},
		{name: "own suspended", repo: "/tmp/own", status: session.StatusSuspended, own: session.SnoozeForever, wantIntent: true},
		{name: "no conversation id", repo: "/tmp/own", status: session.StatusIdle, noID: true, own: session.SnoozeForever, wantIntent: true},
		{name: "deadline snooze", repo: "/tmp/own", status: session.StatusIdle, own: now.Add(time.Hour), wantNow: true},
		{name: "not snoozed", repo: "/tmp/own", status: session.StatusIdle, wantNow: true},
		{name: "group, untouched", repo: repo, status: session.StatusIdle, created: before, used: before, wantIntent: true, wantNow: true},
		{name: "group, created since", repo: repo, status: session.StatusIdle, created: now, used: now, wantNow: true},
		{name: "group, opened since", repo: repo, status: session.StatusIdle, created: before, used: now, wantNow: true},
		{name: "group, same second", repo: repo, status: session.StatusIdle, created: at, used: at, wantNow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parkable(tc.name, tc.repo, tc.status)
			if tc.noID {
				s.ClaudeSessionID = ""
			}
			s.SetSnoozedUntil(tc.own)
			s.CreatedAt, s.LastAccessedAt = tc.created, tc.used
			if got := parkIntent(s, origin, tc.repo, groups, groupAt); got != tc.wantIntent {
				t.Errorf("parkIntent = %v, want %v", got, tc.wantIntent)
			}
			if got := s.CanSuspend(); got != tc.wantNow {
				t.Errorf("CanSuspend = %v, want %v", got, tc.wantNow)
			}
		})
	}

	// Through the origin umbrella too, not only the checkout.
	s := parkable("by-origin", "/tmp/elsewhere", session.StatusIdle)
	s.CreatedAt, s.LastAccessedAt = before, before
	if !parkIntent(s, origin, "/tmp/elsewhere",
		map[string]time.Time{OriginExpandKey(origin): session.SnoozeForever},
		map[string]time.Time{OriginExpandKey(origin): at}) {
		t.Error("an origin-snoozed session is not asked to park")
	}
}

// TestParkSkipsWhatTheUserIsUsing: the attached session and the one focused in
// the split are never pulled out from under the user.
func TestParkSkipsWhatTheUserIsUsing(t *testing.T) {
	h, storage := newParkTestHome(t)
	attached := parkable("attached", "/tmp/park-skip", session.StatusIdle)
	attached.SetSnoozedUntil(session.SnoozeForever)
	focused := parkable("focused", "/tmp/park-skip", session.StatusIdle)
	focused.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{attached, focused}
	saveRows(t, storage, attached, focused)
	h.rebuildFlatItems()
	h.attachedSessionID = attached.ID
	for i, it := range h.flatItems {
		if it.Session == focused {
			h.cursor = i
		}
	}
	h.focusMode = true

	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Errorf("parked a session in use: attached=%s focused=%s", attached.GetStatus(), focused.GetStatus())
	}
}

// TestParkWaitsForTheFirstWorkerPass: before the status worker has run, a
// status is still the row's, and a row saying finished may hide a turn a CLI
// started while fleet was closed.
func TestParkWaitsForTheFirstWorkerPass(t *testing.T) {
	h, storage := newParkTestHome(t)
	h.lastWorkerCycleNano.Store(0)
	s := parkable("own", "/tmp/park-boot", session.StatusFinished)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	saveRows(t, storage, s)
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("parked before the status worker's first pass")
	}
	h.lastWorkerCycleNano.Store(1)
	if cmd := h.parkSnoozedNow(nil); cmd == nil {
		t.Error("did not park once the worker had run")
	}
}

// TestParkSkipsAMissingRow: a session whose row is gone is mid-delete (inside
// the undo window); suspending it would kill what undo is meant to restore.
func TestParkSkipsAMissingRow(t *testing.T) {
	h, _ := newParkTestHome(t)
	s := parkable("own", "/tmp/park-gone", session.StatusIdle)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("parked a session with no row")
	}
}

// TestFocusEndsIndefiniteSnooze: in split Enter mode, focusing a session and
// typing into it is picking it back up, as attaching is. Otherwise the sweep
// suspends it as soon as focus ends and its turn settles.
func TestFocusEndsIndefiniteSnooze(t *testing.T) {
	h, storage := newParkTestHome(t)
	s := parkable("own", "/tmp/park-focus", session.StatusWaiting)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	saveRows(t, storage, s)
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if it.Session == s {
			h.cursor = i
		}
	}
	_ = h.enterFocusMode()
	h.focusMode = false // focus ended
	s.SetStatus(session.StatusFinished)
	if !s.SnoozedUntil().IsZero() {
		t.Error("focus kept the indefinite snooze")
	}
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("a session the user focused was parked once its turn ended")
	}
}

// TestRefreshAdoptsAGroupWokenElsewhere: another fleet process woke the group.
// This one must stop parking its members, and drop the snooze it still holds.
func TestRefreshAdoptsAGroupWokenElsewhere(t *testing.T) {
	h, storage := newParkTestHome(t)
	const repo = "/tmp/park-woken"
	past := time.Now().Add(-time.Hour)
	member := parkable("member", repo, session.StatusIdle)
	member.CreatedAt, member.LastAccessedAt = past, past
	h.sessions = []*session.Session{member}
	saveRows(t, storage, member)
	snoozeGroup(t, h, repo, past.Add(time.Minute))

	if err := storage.SetGroupSnooze(repo, time.Time{}); err != nil { // the other process's wake
		t.Fatal(err)
	}
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("parked a member of a group woken elsewhere")
	}
	if _, still := h.groupSnooze[repo]; still {
		t.Error("the woken group's snooze stayed in memory")
	}
	if _, still := h.groupSnoozedAt[repo]; still {
		t.Error("the woken group's snoozed_at stayed in memory")
	}
}

// TestRefreshShowsACLIRestartWithoutParking: an in-place `fleet restart` cleared
// the snooze in the row. The sidebar must stop showing it as snoozed on the
// next sweep, even though the session is running and so never a park candidate.
func TestRefreshShowsACLIRestartWithoutParking(t *testing.T) {
	h, storage := newParkTestHome(t)
	s := parkable("own", "/tmp/park-cli-restart", session.StatusRunning)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	saveRows(t, storage, s)
	if err := storage.ClearIndefiniteSessionSnooze(s.ID); err != nil {
		t.Fatal(err)
	}
	_ = h.parkSnoozedNow(nil)
	if !s.SnoozedUntil().IsZero() {
		t.Error("a snooze the CLI cleared still shows in the TUI")
	}
	if h.snoozeMuted[s.ID] {
		t.Error("the session is still muted")
	}
}

// TestIndefiniteSnoozeParksAtOnce: picking "until I wake it" on a session at
// rest suspends it straight away (marked before the teardown, like manual
// suspend, so the worker can't read the dying tmux as a crash) and persists
// both the snooze and the status. A running session is muted but left alone.
func TestIndefiniteSnoozeParksAtOnce(t *testing.T) {
	h, storage := newParkTestHome(t)
	idle := parkable("idle", "/tmp/park-now", session.StatusIdle)
	running := parkable("running", "/tmp/park-now", session.StatusRunning)
	saveRows(t, storage, idle, running)
	h.sessions = []*session.Session{idle, running}

	h.applySnoozeUntil(snoozeScope{session: idle, label: "idle", kind: "session"}, session.SnoozeForever, snoozeIndefiniteID)
	h.applySnoozeUntil(snoozeScope{session: running, label: "running", kind: "session"}, session.SnoozeForever, snoozeIndefiniteID)
	if cmd := h.parkSnoozedNow(nil); cmd == nil {
		t.Fatal("parking an idle session returned no teardown command")
	}

	if got := idle.GetStatus(); got != session.StatusSuspended {
		t.Errorf("idle session after indefinite snooze = %s, want suspended", got)
	}
	if got := running.GetStatus(); got != session.StatusRunning {
		t.Errorf("running session after indefinite snooze = %s, want running untouched", got)
	}
	if !h.snoozeMuted[running.ID] {
		t.Error("running session must still be muted while it waits to be parked")
	}

	rows, _ := storage.LoadSessions()
	for _, r := range rows {
		if !session.IsSnoozeForever(r.SnoozedUntil) {
			t.Errorf("%s persisted snooze = %v, want SnoozeForever", r.Title, r.SnoozedUntil)
		}
		if r.ID == idle.ID && r.Status != string(session.StatusSuspended) {
			t.Errorf("parked session persisted status %q, want suspended", r.Status)
		}
	}

	// Once the running one settles, the sweep parks it too.
	running.SetStatus(session.StatusFinished)
	if cmd := h.parkSnoozedNow(nil); cmd == nil {
		t.Fatal("a settled session was not parked")
	}
	if got := running.GetStatus(); got != session.StatusSuspended {
		t.Errorf("settled session = %s, want suspended", got)
	}

	// Nothing left to park: no command, so the tick pays nothing.
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("sweep returned a command with nothing to park")
	}
}

// TestDeadlineSnoozeNeverParks: a timed snooze is a reminder, and a reminder
// needs a live session (its expiry flips idle → finished). Only the
// open-ended option suspends.
func TestDeadlineSnoozeNeverParks(t *testing.T) {
	h, _ := newParkTestHome(t)
	s := parkable("idle", "/tmp/park-dl", session.StatusIdle)
	h.sessions = []*session.Session{s}
	h.applySnoozeUntil(snoozeScope{session: s, label: "idle", kind: "session"}, time.Now().Add(4*time.Hour), "4h")
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("a deadline snooze produced a suspend")
	}
	if got := s.GetStatus(); got != session.StatusIdle {
		t.Errorf("status = %s, want idle", got)
	}
}

// TestWakeLeavesParkedSessionSuspended: `z` on a parked session ends the
// snooze and nothing else — the agent comes back when you press Enter on the
// row, like any suspended session, so a wake never costs memory by itself.
func TestWakeLeavesParkedSessionSuspended(t *testing.T) {
	h, _ := newParkTestHome(t)
	s := parkable("parked", "/tmp/park-wake", session.StatusSuspended)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}

	h.clearSnooze(snoozeScope{session: s, label: "parked", kind: "session"})
	if !s.SnoozedUntil().IsZero() {
		t.Error("wake did not clear the snooze")
	}
	if got := s.GetStatus(); got != session.StatusSuspended {
		t.Errorf("status after wake = %s, want suspended (resume is lazy)", got)
	}
	if !strings.Contains(h.infoMsg, "enter to resume") {
		t.Errorf("wake toast = %q, want it to say how to resume", h.infoMsg)
	}
}

// TestResumeEndsIndefiniteSnooze: pressing Enter on a parked row means you
// picked it back up. Leaving the snooze in place would have the sweep suspend
// it again the moment it next went idle. A deadline snooze is a reminder and
// is left to run.
func TestResumeEndsIndefiniteSnooze(t *testing.T) {
	h, storage := newParkTestHome(t)
	parked := parkable("parked", "/tmp/park-resume", session.StatusSuspended)
	parked.SetSnoozedUntil(session.SnoozeForever)
	timed := parkable("timed", "/tmp/park-resume", session.StatusSuspended)
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	timed.SetSnoozedUntil(until)
	saveRows(t, storage, parked, timed)
	h.sessions = []*session.Session{parked, timed}

	_ = h.resumeSelected(parked) // the returned Restart is not run
	_ = h.resumeSelected(timed)

	if !parked.SnoozedUntil().IsZero() {
		t.Errorf("resumed parked session kept snooze %v", parked.SnoozedUntil())
	}
	if !timed.SnoozedUntil().Equal(until) {
		t.Errorf("resume cleared a deadline snooze: %v", timed.SnoozedUntil())
	}
	rows, _ := storage.LoadSessions()
	for _, r := range rows {
		if r.ID == parked.ID && !r.SnoozedUntil.IsZero() {
			t.Errorf("persisted snooze after resume = %v, want cleared", r.SnoozedUntil)
		}
	}
}

// TestExternalRelaunchEndsIndefiniteSnooze: `fleet resume` from another shell
// wakes the session too; the TUI must drop its in-memory snooze when it adopts
// the relaunch, or its next sweep would suspend what the CLI just resumed.
func TestExternalRelaunchEndsIndefiniteSnooze(t *testing.T) {
	h, _ := newParkTestHome(t)
	s := parkable("parked", "/tmp/park-ext", session.StatusSuspended)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	h.sessionByID = map[string]*session.Session{s.ID: s}

	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{{
		id: s.ID, fromTmux: s.TmuxSessionName, tmuxSession: "fleet_elsewhere",
	}}})
	if !s.SnoozedUntil().IsZero() {
		t.Errorf("externally relaunched session kept snooze %v", s.SnoozedUntil())
	}
}

// TestRenderIndefiniteSnooze: no deadline, so no countdown — a bare ☾ on the
// row and on a group header, and the selected row says how to wake it.
func TestRenderIndefiniteSnooze(t *testing.T) {
	s := session.NewSession("t", "/tmp/w")
	s.SetStatus(session.StatusIdle)
	item := SidebarItem{Session: s, Snooze: snoozeResult{Muted: true, Until: session.SnoozeForever, OwnTimer: true}}

	row := renderSessionItem(item, 60, false, -1)
	if !strings.Contains(row, SnoozeGlyph) {
		t.Errorf("indefinitely snoozed row missing the marker: %q", row)
	}
	if strings.ContainsAny(ansi.Strip(row), "0123456789") {
		t.Errorf("indefinitely snoozed row renders a countdown: %q", row)
	}
	sel := ansi.Strip(renderSessionItem(item, 100, true, -1))
	if !strings.Contains(sel, "z to wake") {
		t.Errorf("selected row should say how to wake it: %q", sel)
	}

	header := ansi.Strip(renderGroupSnooze(session.SnoozeForever))
	if strings.TrimSpace(header) != SnoozeGlyph {
		t.Errorf("group header suffix = %q, want a bare %s", header, SnoozeGlyph)
	}

	if got := formatSnoozeWake(session.SnoozeForever, time.Now()); got != "you wake it" {
		t.Errorf("formatSnoozeWake(forever) = %q", got)
	}
}

// TestIndefiniteGroupSnoozeParksMembers: snoozing a checkout "until I wake it"
// suspends its sessions already at rest, says so in the toast (including the
// busy ones it will get to later), and leaves a session created afterwards
// alone. Waking the group leaves its parked sessions suspended and says how to
// bring them back.
func TestIndefiniteGroupSnoozeParksMembers(t *testing.T) {
	h, storage := newParkTestHome(t)
	const repo = "/tmp/park-group"
	past := time.Now().Add(-time.Hour)
	idle := parkable("idle", repo, session.StatusIdle)
	idle.CreatedAt, idle.LastAccessedAt = past, past
	running := parkable("running", repo, session.StatusRunning)
	running.CreatedAt, running.LastAccessedAt = past, past
	h.sessions = []*session.Session{idle, running}
	saveRows(t, storage, idle, running)

	sc := snoozeScope{groupKey: repo, label: "park-group", kind: "checkout"}
	h.applySnoozeUntil(sc, session.SnoozeForever, snoozeIndefiniteID)
	if !strings.Contains(h.infoMsg, "until you wake it") || !strings.Contains(h.infoMsg, "the rest suspend once idle") {
		t.Errorf("toast = %q", h.infoMsg)
	}
	if h.parkSnoozedNow(nil) == nil || idle.GetStatus() != session.StatusSuspended {
		t.Fatalf("idle member not parked: %s", idle.GetStatus())
	}

	later := parkable("later", repo, session.StatusIdle) // created now, after the snooze
	h.sessions = append(h.sessions, later)
	saveRows(t, storage, later)
	if h.parkSnoozedNow(nil) != nil {
		t.Error("a session created after the group snooze was parked")
	}

	h.clearSnooze(sc)
	if idle.GetStatus() != session.StatusSuspended {
		t.Errorf("group wake resumed a member: %s", idle.GetStatus())
	}
	if !strings.Contains(h.infoMsg, "1 suspended, enter to resume it") {
		t.Errorf("wake toast = %q", h.infoMsg)
	}
}

// TestParkRereadsTheRowBeforeKilling: another fleet process can end a snooze
// without this TUI noticing — `fleet restart` respawns in place (same tmux
// name, so relaunchedElsewhere never fires) and clears the row, or a CLI wake
// stamps last_accessed. Suspending is the one irreversible step here, so the
// sweep re-reads the row first and adopts what it says, the same
// compare-before-clobber rule updateAndPersistStatus follows.
func TestParkRereadsTheRowBeforeKilling(t *testing.T) {
	h, storage := newParkTestHome(t)
	own := parkable("own", "/tmp/park-reread", session.StatusIdle)
	own.SetSnoozedUntil(session.SnoozeForever)
	const repo = "/tmp/park-reread-group"
	past := time.Now().Add(-time.Hour)
	member := parkable("member", repo, session.StatusIdle)
	member.CreatedAt, member.LastAccessedAt = past, past
	saveRows(t, storage, own, member)
	h.sessions = []*session.Session{own, member}
	snoozeGroup(t, h, repo, past.Add(time.Minute))

	// What the CLI does behind the TUI's back.
	if err := storage.SetSessionSnooze(own.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := storage.UpdateLastAccessed(member.ID); err != nil {
		t.Fatal(err)
	}

	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("parked a session whose row says it was picked back up")
	}
	if !own.SnoozedUntil().IsZero() {
		t.Error("the cleared row's snooze was not adopted into memory")
	}
	if !member.LastAccessed().After(h.groupSnoozedAt[repo]) {
		t.Error("the row's newer last_accessed was not adopted into memory")
	}
}

// TestRestartEndsIndefiniteSnooze: `r` is picking the session back up, the
// same as attach and resume.
func TestRestartEndsIndefiniteSnooze(t *testing.T) {
	h, _ := newParkTestHome(t)
	s := parkable("own", "/tmp/park-restart", session.StatusIdle)
	s.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{s}
	_ = h.restartSession(s) // the returned relaunch is not run
	if !s.SnoozedUntil().IsZero() {
		t.Errorf("restart kept the indefinite snooze")
	}
}

// TestExternalRelaunchExemptsGroupMember: a session woken from the CLI under a
// parked group is in use now; marking it accessed is what keeps the group's
// sweep from suspending it again once it finishes.
func TestExternalRelaunchExemptsGroupMember(t *testing.T) {
	h, storage := newParkTestHome(t)
	const repo = "/tmp/park-ext-group"
	past := time.Now().Add(-time.Hour)
	s := parkable("member", repo, session.StatusSuspended)
	s.CreatedAt, s.LastAccessedAt = past, past
	// The control: an untouched member the group still parks, so a pass here
	// can't come from the sweep doing nothing at all.
	untouched := parkable("untouched", repo, session.StatusIdle)
	untouched.CreatedAt, untouched.LastAccessedAt = past, past
	h.sessions = []*session.Session{s, untouched}
	h.sessionByID = map[string]*session.Session{s.ID: s, untouched.ID: untouched}
	saveRows(t, storage, s, untouched)
	snoozeGroup(t, h, repo, past.Add(time.Minute))

	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{{
		id: s.ID, fromTmux: s.TmuxSessionName, tmuxSession: "fleet_elsewhere",
	}}})
	if !s.LastAccessed().After(h.groupSnoozedAt[repo]) {
		t.Fatalf("relaunch did not mark the member accessed after the group's cutoff: %v", s.LastAccessed())
	}
	s.SetStatus(session.StatusFinished)
	if cmd := h.parkSnoozedNow(nil); cmd == nil {
		t.Fatal("the untouched member was not parked")
	}
	if got := s.GetStatus(); got != session.StatusFinished {
		t.Errorf("the group re-parked a session the CLI just woke: %s", got)
	}
}

// TestSnoozeToastAndParkStayInScope: picking "Until I wake" on one row talks
// about, and parks, that row's scope only — not a waiting session elsewhere
// under an older indefinite snooze, nor an idle one the tick has yet to reach.
func TestSnoozeToastAndParkStayInScope(t *testing.T) {
	h, storage := newParkTestHome(t)
	a := parkable("a", "/tmp/park-a", session.StatusIdle)
	waiting := parkable("b-waiting", "/tmp/park-b", session.StatusWaiting)
	waiting.SetSnoozedUntil(session.SnoozeForever)
	other := parkable("c-idle", "/tmp/park-c", session.StatusIdle)
	other.SetSnoozedUntil(session.SnoozeForever)
	h.sessions = []*session.Session{a, waiting, other}
	saveRows(t, storage, a, waiting, other)

	sc := snoozeScope{session: a, label: "a", kind: "session"}
	h.applySnoozeUntil(sc, session.SnoozeForever, snoozeIndefiniteID)
	if h.infoMsg != "Snoozed a until you wake it" {
		t.Errorf("toast = %q, want it to describe only the snoozed row", h.infoMsg)
	}
	if h.parkSnoozedNow(h.inSnoozeScope(sc)) == nil {
		t.Fatal("scoped park did nothing")
	}
	if other.GetStatus() != session.StatusIdle {
		t.Error("a scoped park suspended a session outside the scope")
	}
}
