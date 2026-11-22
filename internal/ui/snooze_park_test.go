package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func newParkTestHome(t *testing.T) (*Home, *session.StateDB) {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "park.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{}), storage
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

// TestParkCandidates pins who an "until I wake it" snooze suspends. At rest
// and resumable only — a running or waiting session is never killed silently,
// and one with no conversation id would lose its conversation on resume. A
// group parks only the sessions that were already there, untouched, when it
// was snoozed: creating or opening one since is the user working there.
func TestParkCandidates(t *testing.T) {
	now := time.Now()
	const repo, origin = "/tmp/park", "github.com/acme/park"
	at := now.Add(-time.Hour)
	before := at.Add(-time.Hour)
	originOf := func(string) string { return origin }

	ownIdle := parkable("own-idle", "/tmp/own", session.StatusIdle)
	ownIdle.SetSnoozedUntil(session.SnoozeForever)
	ownFinished := parkable("own-finished", "/tmp/own", session.StatusFinished)
	ownFinished.SetSnoozedUntil(session.SnoozeForever)
	ownRunning := parkable("own-running", "/tmp/own", session.StatusRunning)
	ownRunning.SetSnoozedUntil(session.SnoozeForever)
	ownWaiting := parkable("own-waiting", "/tmp/own", session.StatusWaiting)
	ownWaiting.SetSnoozedUntil(session.SnoozeForever)
	ownSuspended := parkable("own-suspended", "/tmp/own", session.StatusSuspended)
	ownSuspended.SetSnoozedUntil(session.SnoozeForever)
	noID := parkable("no-id", "/tmp/own", session.StatusIdle)
	noID.ClaudeSessionID = ""
	noID.SetSnoozedUntil(session.SnoozeForever)
	deadline := parkable("deadline", "/tmp/own", session.StatusIdle)
	deadline.SetSnoozedUntil(now.Add(time.Hour))
	unsnoozed := parkable("unsnoozed", "/tmp/own", session.StatusIdle)
	attached := parkable("attached", "/tmp/own", session.StatusIdle)
	attached.SetSnoozedUntil(session.SnoozeForever)

	groupOld := parkable("group-old", repo, session.StatusIdle)
	groupOld.CreatedAt, groupOld.LastAccessedAt = before, before
	groupNew := parkable("group-new", repo, session.StatusIdle)
	groupNew.CreatedAt, groupNew.LastAccessedAt = now, now
	groupTouched := parkable("group-touched", repo, session.StatusIdle)
	groupTouched.CreatedAt, groupTouched.LastAccessedAt = before, now

	sessions := []*session.Session{
		ownIdle, ownFinished, ownRunning, ownWaiting, ownSuspended, noID,
		deadline, unsnoozed, attached, groupOld, groupNew, groupTouched,
	}
	groups := map[string]time.Time{repo: session.SnoozeForever, "/tmp/own": now.Add(time.Hour)}
	groupAt := map[string]time.Time{repo: at}

	got := parkCandidates(sessions, originOf, groups, groupAt, map[string]bool{attached.ID: true})
	gotIDs := map[string]bool{}
	for _, s := range got {
		gotIDs[s.Title] = true
	}
	want := map[string]bool{"own-idle": true, "own-finished": true, "group-old": true}
	for title := range want {
		if !gotIDs[title] {
			t.Errorf("%s was not parked", title)
		}
	}
	for title := range gotIDs {
		if !want[title] {
			t.Errorf("%s was parked, must not be", title)
		}
	}

	// Through the origin umbrella too, not only the checkout.
	byOrigin := parkCandidates([]*session.Session{groupOld}, originOf,
		map[string]time.Time{OriginExpandKey(origin): session.SnoozeForever},
		map[string]time.Time{OriginExpandKey(origin): at}, nil)
	if len(byOrigin) != 1 {
		t.Errorf("origin-snoozed session parked %d times, want 1", len(byOrigin))
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
	for _, s := range []*session.Session{idle, running} {
		if err := storage.SaveSession(s.ToRow()); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}
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
	for _, s := range []*session.Session{parked, timed} {
		if err := storage.SaveSession(s.ToRow()); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}
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
	if !strings.Contains(h.infoMsg, "until you wake it") || !strings.Contains(h.infoMsg, "suspending 1, the rest once idle") {
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
	for _, s := range []*session.Session{own, member} {
		if err := storage.SaveSession(s.ToRow()); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}
	h.sessions = []*session.Session{own, member}
	h.groupSnooze[repo] = session.SnoozeForever
	h.groupSnoozedAt[repo] = past.Add(time.Minute)

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
	h, _ := newParkTestHome(t)
	const repo = "/tmp/park-ext-group"
	past := time.Now().Add(-time.Hour)
	s := parkable("member", repo, session.StatusSuspended)
	s.CreatedAt, s.LastAccessedAt = past, past
	h.sessions = []*session.Session{s}
	h.sessionByID = map[string]*session.Session{s.ID: s}
	h.groupSnooze[repo] = session.SnoozeForever
	h.groupSnoozedAt[repo] = past.Add(time.Minute)

	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{{
		id: s.ID, fromTmux: s.TmuxSessionName, tmuxSession: "fleet_elsewhere",
	}}})
	s.SetStatus(session.StatusFinished)
	if cmd := h.parkSnoozedNow(nil); cmd != nil {
		t.Error("the group re-parked a session the CLI just woke")
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
	if !strings.HasSuffix(h.infoMsg, " · suspending to free memory") {
		t.Errorf("toast = %q, want it to describe only the snoozed row", h.infoMsg)
	}
	if h.parkSnoozedNow(h.inSnoozeScope(sc)) == nil {
		t.Fatal("scoped park did nothing")
	}
	if other.GetStatus() != session.StatusIdle {
		t.Error("a scoped park suspended a session outside the scope")
	}
}
