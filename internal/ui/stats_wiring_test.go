package ui

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/stats"
)

// statsTestHome builds a Home whose stats.db lives in a temp dir. withStore
// false leaves it nil, the state fleet runs in when stats.db can't be opened.
func statsTestHome(t *testing.T, withStore bool) *Home {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	if withStore {
		path := filepath.Join(t.TempDir(), "stats.db")
		prev := statsOpenStore
		statsOpenStore = func() (*stats.Store, error) { return stats.Open(path) }
		t.Cleanup(func() { statsOpenStore = prev })
	}
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	t.Cleanup(func() { _ = h.statsStore.Close() })
	h.width, h.height = 120, 40
	h.statsView.SetSize(120, 40)
	h.recapView.SetSize(120, 40)
	return h
}

func pressKey(t *testing.T, h *Home, key string) tea.Cmd {
	t.Helper()
	var msg tea.KeyPressMsg
	if key == "esc" {
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	} else {
		r := []rune(key)[0]
		msg = tea.KeyPressMsg{Code: r, Text: key}
	}
	_, cmd := h.Update(msg)
	return cmd
}

func TestStatsKeyOpensStatsAndAsksForAReport(t *testing.T) {
	h := statsTestHome(t, true)
	cmd := pressKey(t, h, "i")
	if !h.statsView.IsVisible() {
		t.Fatal("i did not open Stats")
	}
	if cmd == nil {
		t.Fatal("opening Stats must request a Report and a scan")
	}
	if !h.modalOpen() {
		t.Error("Stats is a full-screen view; modalOpen must say so")
	}
	pressKey(t, h, "i")
	if h.statsView.IsVisible() {
		t.Error("i inside Stats should close it")
	}
}

func TestStatsWithoutAStoreSaysSoInsteadOfOpeningEmpty(t *testing.T) {
	h := statsTestHome(t, false)
	pressKey(t, h, "i")
	if h.statsView.IsVisible() {
		t.Error("Stats opened with no store — it would read as 'nothing recorded' when the truth is 'could not record'")
	}
	if h.err == nil {
		t.Error("no error shown for a missing stats.db")
	}
}

func TestStatsReportFromAnOlderComputeIsDropped(t *testing.T) {
	h := statsTestHome(t, true)
	h.statsReportCmd(stats.Range7d) // gen 1
	h.statsReportCmd(stats.Range7d) // gen 2
	stale := stats.Report{Range: stats.Range7d, Hero: stats.Hero{Peak: 1}}
	h.handleStatsMsg(statsReportMsg{rng: stats.Range7d, gen: 1, report: stale})
	if _, ok := h.statsView.reports[stats.Range7d]; ok {
		t.Fatal("an older compute overwrote a newer one's slot")
	}
	fresh := stats.Report{Range: stats.Range7d, Hero: stats.Hero{Peak: 2}}
	h.handleStatsMsg(statsReportMsg{rng: stats.Range7d, gen: 2, report: fresh})
	if got := h.statsView.reports[stats.Range7d].Hero.Peak; got != 2 {
		t.Fatalf("current compute not applied: peak %d", got)
	}
}

func TestStatsRangeChangeRequestsThatRange(t *testing.T) {
	h := statsTestHome(t, true)
	cmd, ok := h.handleStatsMsg(statsRangeMsg{Range: stats.Range30d})
	if !ok || cmd == nil {
		t.Fatal("statsRangeMsg must start a compute")
	}
	if h.statsReportGen[stats.Range30d] != 1 {
		t.Errorf("gen for 30d = %d, want 1", h.statsReportGen[stats.Range30d])
	}
}

func TestRecapBadgeOpensWithStatsAndClearsOnceSeen(t *testing.T) {
	h := statsTestHome(t, true)
	h.booted = true
	week := stats.LastFullWeekStart(time.Now())
	recap := stats.Recap{WeekStart: week, Cards: []stats.RecapCard{{Big: "6", Caption: "agents at once"}}}
	h.handleStatsMsg(statsRecapMsg{recap: recap})
	if !h.statsRecapUnseen {
		t.Fatal("a recap with cards for an unseen week must raise the badge")
	}
	h.viewDirty = true
	if !strings.Contains(h.composeScreen(), "Your week") {
		t.Error("badge not drawn in the header")
	}

	pressKey(t, h, "i")
	if !h.statsView.IsVisible() || !h.recapView.IsVisible() {
		t.Fatalf("with the badge up, i opens Stats with the recap on top (stats=%v recap=%v)",
			h.statsView.IsVisible(), h.recapView.IsVisible())
	}
	if h.statsRecapUnseen {
		t.Error("opening the recap must clear the badge")
	}
	if got, want := h.cfg.GetStatsRecapSeenWeek(), statsWeekKey(week); got != want {
		t.Errorf("seen week = %q, want %q", got, want)
	}

	// The recap is topmost: esc closes it and leaves Stats underneath.
	pressKey(t, h, "esc")
	if h.recapView.IsVisible() || !h.statsView.IsVisible() {
		t.Fatalf("esc should close only the recap (stats=%v recap=%v)", h.statsView.IsVisible(), h.recapView.IsVisible())
	}

	// A later recap for the same week does not raise the badge again.
	h.handleStatsMsg(statsRecapMsg{recap: recap})
	if h.statsRecapUnseen {
		t.Error("badge came back for a week already seen")
	}
}

func TestRecapWithNoCardsNeverRaisesTheBadge(t *testing.T) {
	h := statsTestHome(t, true)
	h.handleStatsMsg(statsRecapMsg{recap: stats.Recap{WeekStart: stats.LastFullWeekStart(time.Now())}})
	if h.statsRecapUnseen {
		t.Error("an empty week raised the badge")
	}
}

func TestStatsViewWOpensTheRecap(t *testing.T) {
	h := statsTestHome(t, true)
	pressKey(t, h, "i")
	cmd := pressKey(t, h, "w")
	if cmd == nil {
		t.Fatal("w inside Stats should ask for the recap")
	}
	if _, ok := cmd().(statsOpenRecapMsg); !ok {
		t.Fatal("w did not emit statsOpenRecapMsg")
	}
}

var statsEnumRE = regexp.MustCompile(`^[a-z_]+$`)

// The tee's table is the whole privacy contract on this side: every output is a
// stats enum, and attaches (their own table) never appear, or they'd count twice.
func TestStatsActionTableEmitsOnlyEnums(t *testing.T) {
	for _, action := range []string{
		"fork session", "fork to worktree", "undo delete", "restart session",
		"resume session", "open terminal drawer", "snooze session",
		"snooze checkout", "snooze origin", "bind slot",
	} {
		got := statsActionFor(action)
		if got == "" || !statsEnumRE.MatchString(got) {
			t.Errorf("statsActionFor(%q) = %q, want a snake_case enum", action, got)
		}
	}
	// Attaches, creates and deletes count from their own tables; the rest are
	// logged before their guard runs and record where they succeed instead.
	for _, action := range []string{"attach session", "attach via slot", "create session", "delete session", "command: stats", "open bug report",
		"quick approve", "mark unread", "open editor", "open PR", "copy PR link"} {
		if got := statsActionFor(action); got != "" {
			t.Errorf("statsActionFor(%q) = %q, want nothing", action, got)
		}
	}
}

func TestStatsTeeLandsInTheLocalStore(t *testing.T) {
	h := statsTestHome(t, true)
	h.logAction("fork session", "a session title that must never be stored", true)
	h.logAction("fork session", "", false) // failures don't count
	h.statsStore.Flush(2 * time.Second)
	r, err := h.statsStore.Report(stats.Range7d, time.Now())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	n := 0
	for _, a := range r.Activity {
		if a.Action == stats.ActionFork {
			n = a.N
		}
	}
	if n != 1 {
		t.Errorf("forks recorded = %d, want 1 (activity: %+v)", n, r.Activity)
	}
}

// Actions logged before their own guard runs count only where they succeed:
// a Y on a session that isn't waiting, or a p on a branch with no PR, used to
// count as an approve / a PR opened.
func TestStatsCountsOnlyActionsThatHappened(t *testing.T) {
	h := statsTestHome(t, true)
	// Refused presses: nothing selected to approve / mark, no PR to open.
	pressKey(t, h, "Y")
	pressKey(t, h, "m")
	pressKey(t, h, "p")
	// Async results: failures don't count, successes do.
	h.Update(quickApproveMsg{err: errTest})
	h.Update(openPRMsg{err: errTest})
	h.Update(copyPRLinkMsg{err: errTest})
	h.Update(openEditorMsg{err: errTest})
	h.Update(quickApproveMsg{})
	h.Update(openPRMsg{})
	h.Update(copyPRLinkMsg{number: 1})
	h.Update(openEditorMsg{})
	h.statsStore.Flush(2 * time.Second)
	r, err := h.statsStore.Report(stats.Range7d, time.Now())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	got := map[string]int{}
	for _, a := range r.Activity {
		got[a.Action] = a.N
	}
	for _, a := range []string{stats.ActionQuickApprove, stats.ActionPROpen, stats.ActionCopyPRLink, stats.ActionEditorOpen} {
		if got[a] != 1 {
			t.Errorf("%s = %d, want 1 (activity: %+v)", a, got[a], r.Activity)
		}
	}
	if got[stats.ActionMarkUnread] != 0 {
		t.Errorf("a refused m counted as marked unread: %+v", r.Activity)
	}
}

// An instance left running across Monday must still light the badge for the
// week that just ended: the tick asks for a recap once per week rollover.
func TestRecapRecomputedWhenTheWeekRollsOver(t *testing.T) {
	h := statsTestHome(t, true)
	now := time.Now()
	if cmd := h.maybeRecapNewWeek(now); cmd != nil {
		t.Fatal("the startup scan already covers this week; the tick must not ask again")
	}
	next := now.AddDate(0, 0, 7)
	cmd := h.maybeRecapNewWeek(next)
	if cmd == nil {
		t.Fatal("a new week must trigger a recap")
	}
	if _, ok := cmd().(statsRecapMsg); !ok {
		t.Fatal("expected a statsRecapMsg")
	}
	if h.maybeRecapNewWeek(next) != nil {
		t.Fatal("the same week must be asked once")
	}
}

// The badge shrinks to the room the breadcrumb leaves rather than cutting it.
func TestRecapBadgeFitsTheRoomItHas(t *testing.T) {
	full, short := renderStatsRecapBadge(), renderStatsBadge()
	if got := fitStatsRecapBadge(lipgloss.Width(full)); got != full {
		t.Errorf("room for all of it: %q", got)
	}
	if got := fitStatsRecapBadge(lipgloss.Width(full) - 1); got != short {
		t.Errorf("one column short should drop the key hint: %q", got)
	}
	if got := fitStatsRecapBadge(lipgloss.Width(short) - 1); got != "" {
		t.Errorf("no room should render nothing: %q", got)
	}
}

// A held key is one jump: terminal auto-repeat fires every ~30–50ms, and
// counting each repeat once turned a held Space into 942 jumps in a day.
func TestStatsJumpGateCollapsesHeldKeys(t *testing.T) {
	var g statsJumpGate
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.Local)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	n := 0
	// Held for two seconds at a 40ms repeat rate, every press landing somewhere new.
	for ms := 0; ms <= 2000; ms += 40 {
		if g.count("space", true, at(ms)) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a 2s held key counted %d jumps, want 1", n)
	}
	if !g.count("space", true, at(2000+int(statsRepeatWindow/time.Millisecond))) {
		t.Error("a fresh press after the key was released must count")
	}
	if g.count("space", false, at(5000)) {
		t.Error("a press that landed nowhere new must not count")
	}
	if !g.count("slot_jump:2", true, at(5010)) {
		t.Error("a different key is its own burst")
	}
}

// Space counts only a landing on a different session, and a rapid run of
// presses counts once.
func TestSpaceJumpCountsOnlyRealMoves(t *testing.T) {
	h := statsTestHome(t, true)
	a := session.NewSession("a", "/tmp/sj-a")
	a.SetStatus(session.StatusWaiting)
	b := session.NewSession("b", "/tmp/sj-b")
	b.SetStatus(session.StatusWaiting)
	h.sessions = []*session.Session{a, b}
	h.rebuildFlatItems()
	h.cursor = 0

	for range 5 { // well inside statsRepeatWindow of each other
		h.jumpToNextAttentionSession()
	}
	// Released, then pressed again: a second jump.
	h.statsJumps.last[stats.ActionSpaceJump] = time.Now().Add(-time.Second)
	h.jumpToNextAttentionSession()
	// Nothing to jump to: no jump, however far apart.
	a.SetStatus(session.StatusIdle)
	b.SetStatus(session.StatusIdle)
	h.statsJumps.last[stats.ActionSpaceJump] = time.Now().Add(-time.Second)
	h.jumpToNextAttentionSession()
	// One target, cursor already on it: Space wraps back onto the same row.
	a.SetStatus(session.StatusWaiting)
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if it.Session == a {
			h.cursor = i
		}
	}
	h.statsJumps.last[stats.ActionSpaceJump] = time.Now().Add(-time.Second)
	h.jumpToNextAttentionSession()
	if cur := h.flatItems[h.cursor].Session; cur != a {
		t.Fatalf("wrap-around left the cursor on %v, want session a", cur)
	}

	h.statsStore.Flush(2 * time.Second)
	r, err := h.statsStore.Report(stats.Range7d, time.Now())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	n := 0
	for _, act := range r.Activity {
		if act.Action == stats.ActionSpaceJump {
			n = act.N
		}
	}
	if n != 2 {
		t.Errorf("space jumps recorded = %d, want 2", n)
	}
}
