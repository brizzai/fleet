package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestSnoozeStatePrecedence locks in the rule the whole feature rests on: the
// group umbrella outranks a session's own snooze for *display*, but the two
// deadlines run independently — so a session whose own snooze outlives its
// group's stays muted after the group wakes.
func TestSnoozeStatePrecedence(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	const origin, repo = "github.com/acme/x", "/tmp/x"
	originKey := OriginExpandKey(origin)

	cases := []struct {
		name         string
		own          time.Time
		groups       map[string]time.Time
		wantMuted    bool
		wantOwnTimer bool
	}{
		{
			name: "nothing snoozed",
		},
		{
			name: "own snooze only — session owns the clock",
			own:  now.Add(time.Hour), wantMuted: true, wantOwnTimer: true,
		},
		{
			name:   "checkout umbrella — child shows no clock of its own",
			groups: map[string]time.Time{repo: now.Add(time.Hour)},
			// OwnTimer stays false: the header renders the countdown, so N
			// children don't repeat the same number N times.
			wantMuted: true,
		},
		{
			name:      "origin umbrella reaches down to the session",
			groups:    map[string]time.Time{originKey: now.Add(time.Hour)},
			wantMuted: true,
		},
		{
			name: "group wins display while both are live",
			own:  now.Add(30 * time.Minute),
			// Same session, own snooze live too — the group still owns the clock.
			groups:    map[string]time.Time{repo: now.Add(time.Hour)},
			wantMuted: true,
		},
		{
			name: "group expired, own snooze survives it",
			own:  now.Add(time.Hour),
			// This is the independence guarantee: the umbrella lapsing does not
			// take the session's own snooze with it.
			groups:    map[string]time.Time{repo: now.Add(-time.Minute)},
			wantMuted: true, wantOwnTimer: true,
		},
		{
			name:   "expired group mutes nothing",
			groups: map[string]time.Time{repo: now.Add(-time.Hour)},
		},
		{
			name: "expired own snooze mutes nothing",
			own:  now.Add(-time.Second),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := session.NewSession("t", repo)
			s.SetSnoozedUntil(tc.own)

			got := snoozeState(s, origin, repo, tc.groups, now)
			if got.Muted != tc.wantMuted {
				t.Errorf("Muted = %v, want %v", got.Muted, tc.wantMuted)
			}
			if got.OwnTimer != tc.wantOwnTimer {
				t.Errorf("OwnTimer = %v, want %v", got.OwnTimer, tc.wantOwnTimer)
			}
			if tc.wantMuted && got.Until.IsZero() {
				t.Error("a muted result must carry the deadline responsible for it")
			}
		})
	}
}

// TestGroupSnoozeAtIgnoresParent: only the group holding a snooze renders a
// countdown. A checkout under a snoozed origin stays silent, for the same
// reason its sessions do — one clock per snooze.
func TestGroupSnoozeAtIgnoresParent(t *testing.T) {
	now := time.Now()
	groups := map[string]time.Time{OriginExpandKey("o"): now.Add(time.Hour)}

	if got := groupSnoozeAt(groups, "/tmp/repo", now); !got.IsZero() {
		t.Errorf("checkout under a snoozed origin must render no clock, got %v", got)
	}
	if got := groupSnoozeAt(groups, OriginExpandKey("o"), now); got.IsZero() {
		t.Error("the origin holding the snooze must render its own clock")
	}
}

// TestSnoozeTomorrowResolves: "Tomorrow" is a wall-clock target, not +24h —
// snoozing at 11pm must not wake you at 11pm the next night.
func TestSnoozeTomorrowResolves(t *testing.T) {
	for _, hour := range []int{0, 9, 14, 23} {
		now := time.Date(2026, 7, 20, hour, 30, 0, 0, time.Local)
		got := snoozeTomorrow(now)
		if got.Day() != 21 || got.Hour() != snoozeTomorrowHour || got.Minute() != 0 {
			t.Errorf("from %v: got %v, want Jul 21 at %02d:00", now, got, snoozeTomorrowHour)
		}
		if !got.After(now) {
			t.Errorf("from %v: wake time %v is not in the future", now, got)
		}
	}
}

// TestFormatSnoozeRemainingWidth: the timer rides in the sidebar's title budget,
// so every bucket must stay within the 3 cells renderSessionItem reserves for it.
func TestFormatSnoozeRemainingWidth(t *testing.T) {
	now := time.Now()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "<1m"},
		{90 * time.Second, "2m"},
		{45 * time.Minute, "45m"},
		{2 * time.Hour, "2h"},
		{23 * time.Hour, "23h"},
		// Custom durations can exceed a day, so hours give way to days.
		{24 * time.Hour, "1d"},
		{30 * time.Hour, "2d"},
		{snoozeMaxDuration, "30d"}, // the widest the input allows
	}
	for _, tc := range cases {
		got := formatSnoozeRemaining(now.Add(tc.d), now)
		if got != tc.want {
			t.Errorf("formatSnoozeRemaining(+%v) = %q, want %q", tc.d, got, tc.want)
		}
		if len(got) > 3 {
			t.Errorf("formatSnoozeRemaining(+%v) = %q exceeds the 3-cell budget", tc.d, got)
		}
	}
}

// TestSnoozedSessionsExcludedFromPills: a muted session must drop out of BOTH
// the checkout and the origin tally. If only one skipped, the two headers would
// disagree on screen.
func TestSnoozedSessionsExcludedFromPills(t *testing.T) {
	now := time.Now()
	repo := "/tmp/pill-repo"
	origin := "github.com/acme/pill"

	quiet := session.NewSession("quiet", repo)
	quiet.SetStatus(session.StatusWaiting)
	quiet.SetSnoozedUntil(now.Add(time.Hour))
	loud := session.NewSession("loud", repo)
	loud.SetStatus(session.StatusWaiting)

	originOf := func(string) string { return origin }
	items := BuildFlatItems([]*session.Session{quiet, loud}, nil, map[string]bool{}, "",
		nil, nil, nil, now, originOf, nil)

	for _, it := range items {
		if !it.IsRepoHeader {
			continue
		}
		if got := it.StatusCounts[session.StatusWaiting]; got != 1 {
			kind := "checkout"
			if it.IsOriginHeader {
				kind = "origin"
			}
			t.Errorf("%s header counts %d waiting, want 1 (the snoozed one must not count)", kind, got)
		}
	}
}

// TestGroupSnoozeMutesFutureSessions: the umbrella is stored on the group, never
// fanned out onto sessions — so a session created *after* the snooze is muted too.
// That's the difference between a group snooze and a bulk edit.
func TestGroupSnoozeMutesFutureSessions(t *testing.T) {
	now := time.Now()
	repo := "/tmp/umbrella"
	origin := "github.com/acme/umbrella"

	// Created after the snooze was set; it carries no snooze of its own.
	newcomer := session.NewSession("created later", repo)
	newcomer.SetStatus(session.StatusWaiting)

	groups := map[string]time.Time{repo: now.Add(time.Hour)}
	originOf := func(string) string { return origin }
	items := BuildFlatItems([]*session.Session{newcomer}, nil, map[string]bool{}, "",
		nil, nil, groups, now, originOf, nil)

	var found bool
	for _, it := range items {
		if it.Session == nil {
			continue
		}
		found = true
		if !it.Snooze.Muted {
			t.Error("a session created under a snoozed group must be muted")
		}
		if it.Snooze.OwnTimer {
			t.Error("a group-muted child must not render its own countdown")
		}
	}
	if !found {
		t.Fatal("session row missing from the tree")
	}
}

// TestParseSnoozeDuration: the custom input takes a single unit — one positive
// integer plus m, h or d. Deliberately not time.ParseDuration, which would
// accept combos and seconds we don't offer and knows nothing about days.
func TestParseSnoozeDuration(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"3h", 3 * time.Hour},
		{"30h", 30 * time.Hour},
		{"2d", 48 * time.Hour},
		{"30d", snoozeMaxDuration},
		{"  2D  ", 48 * time.Hour}, // trimmed + case-insensitive
	}
	for _, tc := range ok {
		got, err := parseSnoozeDuration(tc.in)
		if err != nil {
			t.Errorf("parse(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	bad := []string{
		"",      // empty
		"15",    // bare number — no unit
		"m",     // unit with no number
		"15s",   // seconds aren't offered
		"1h30m", // combos aren't offered
		"-5m",   // negative
		"0m",    // zero
		"31d",   // past the 30d cap
		"999d",  // fat-fingered
		// Overflow guard: CharLimit admits 8 digits, and time.Duration(n)*mult
		// wraps int64 well below that. Before the fix these returned a NEGATIVE
		// duration with err == nil — not > the cap, so it sailed through as a
		// wake time in the past.
		"9999999d",
		"9999999h",
		"999999d",
		"99999999m",
		"abc",    // nonsense
		"1.5h",   // fractional
		"15 m",   // internal space
		"1m2d3h", // multi-unit
	}
	for _, in := range bad {
		got, err := parseSnoozeDuration(in)
		if err == nil {
			t.Errorf("parse(%q) = %v, want an error", in, got)
		} else if err.Error() == "" {
			t.Errorf("parse(%q): error message is empty — it renders in the dialog", in)
		}
		// Belt and braces for the overflow class: a rejected input must never
		// hand back a usable duration, and never a negative one.
		if got < 0 {
			t.Errorf("parse(%q) returned a negative duration %v — overflow slipped through", in, got)
		}
	}
}

// TestSnoozeDialogDownReachesInput: the input is the row below the last preset,
// so ↓ walks into it and ↑ walks back out. Focus must move the text input's own
// focus with it, or the caret blinks somewhere the highlight isn't.
func TestSnoozeDialogDownReachesInput(t *testing.T) {
	d := NewSnoozeDialog()
	d.Show("Snooze x")

	if d.inputFocused() {
		t.Fatal("dialog should open on the first preset, not the input")
	}
	for i := 0; i < len(SnoozeDurations); i++ {
		d.Update(key("down"))
	}
	if !d.inputFocused() {
		t.Fatalf("↓ past the last preset should focus the input, focus = %d", d.focus)
	}
	if !d.input.Focused() {
		t.Error("the text input must actually take focus, or typing goes nowhere")
	}

	// One more ↓ must clamp, not wrap around to the top.
	d.Update(key("down"))
	if !d.inputFocused() {
		t.Error("↓ at the input should clamp, not wrap")
	}

	d.Update(key("up"))
	if d.inputFocused() {
		t.Error("↑ from the input should go back to the last preset")
	}
	if d.input.Focused() {
		t.Error("leaving the input must blur it, or the caret outlives the highlight")
	}
	if d.focus != len(SnoozeDurations)-1 {
		t.Errorf("↑ landed on focus %d, want the last preset (%d)", d.focus, len(SnoozeDurations)-1)
	}
}

// TestSnoozeDialogTypingJumpsToInput: typing from a preset row moves focus to
// the input and keeps the keystroke, so the fast path never needs the arrows.
func TestSnoozeDialogTypingJumpsToInput(t *testing.T) {
	d := NewSnoozeDialog()
	d.Show("Snooze x")

	for _, k := range []string{"2", "d"} {
		d.Update(key(k))
	}
	if !d.inputFocused() {
		t.Fatal("typing from a preset should jump to the input")
	}
	if got := d.input.Value(); got != "2d" {
		t.Errorf("input = %q, want %q — the jumping keystroke must not be swallowed", got, "2d")
	}
}

// TestSnoozeDialogFocusDecidesEnter: Enter acts on whatever is highlighted. The
// highlight is the promise — with focus on the input a typed value wins, and
// with focus on a preset the preset wins even if the box holds text.
func TestSnoozeDialogFocusDecidesEnter(t *testing.T) {
	d := NewSnoozeDialog()
	d.Show("Snooze x")
	d.input.SetValue("2d")
	d.setFocus(0) // back onto "30 minutes" with text still in the box

	_, cmd := d.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on a preset produced no command")
	}
	if got := cmd().(snoozeSelectedMsg).durationID; got != SnoozeDurations[0].ID {
		t.Errorf("durationID = %q, want the highlighted preset %q", got, SnoozeDurations[0].ID)
	}

	// Now with the input focused, the same text wins.
	d.Show("Snooze x")
	d.setFocus(snoozeInputRow())
	d.input.SetValue("2d")

	_, cmd = d.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter with a valid custom duration produced no command")
	}
	msg, ok := cmd().(snoozeSelectedMsg)
	if !ok {
		t.Fatalf("got %T, want snoozeSelectedMsg", cmd())
	}
	if msg.durationID != "custom" {
		t.Errorf("durationID = %q, want %q", msg.durationID, "custom")
	}
	if got := time.Until(msg.until); got < 47*time.Hour || got > 49*time.Hour {
		t.Errorf("custom 2d resolved to %v away, want ~48h", got)
	}
	if d.IsVisible() {
		t.Error("picking a duration should close the dialog")
	}
}

// TestSnoozeDialogRefusesBadCustom: a non-empty but unparseable box is a typo,
// not a request for the highlighted preset. Enter must do nothing rather than
// silently snooze for a duration the user never asked for.
func TestSnoozeDialogRefusesBadCustom(t *testing.T) {
	d := NewSnoozeDialog()
	d.Show("Snooze x")
	d.setFocus(snoozeInputRow())
	d.input.SetValue("2x")

	if _, cmd := d.Update(key("enter")); cmd != nil {
		t.Errorf("enter on an invalid duration emitted %#v, want nothing", cmd())
	}
	if !d.IsVisible() {
		t.Error("dialog should stay open so the typo can be fixed")
	}
	// And the box must say why, since that text renders inline.
	if !strings.Contains(d.View(), "end with m, h or d") {
		t.Errorf("dialog gives no reason for the rejection:\n%s", d.View())
	}
}

// TestSnoozeDialogPresetPath: with an empty box, Enter takes the highlighted
// preset.
func TestSnoozeDialogPresetPath(t *testing.T) {
	d := NewSnoozeDialog()
	d.Show("Snooze x")
	d.Update(key("down")) // 30m -> 1h

	_, cmd := d.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on a preset produced no command")
	}
	msg := cmd().(snoozeSelectedMsg)
	if msg.durationID != SnoozeDurations[1].ID {
		t.Errorf("durationID = %q, want %q", msg.durationID, SnoozeDurations[1].ID)
	}
}

// TestSnoozeDialogPreviewsWakeTime: the resolved wake time is shown live, which
// is what keeps a typed "2d" from being a guess.
func TestSnoozeDialogPreviewsWakeTime(t *testing.T) {
	d := NewSnoozeDialog()
	d.SetSize(100, 40)
	d.Show("Snooze x")
	d.setFocus(snoozeInputRow())
	d.input.SetValue("3h")

	want := formatSnoozeWake(time.Now().Add(3*time.Hour), time.Now())
	if got := d.View(); !strings.Contains(got, want) {
		t.Errorf("dialog does not preview the wake time %q:\n%s", want, got)
	}
}

// TestSnoozeDialogHeightIsStable: the verdict line swaps between the key hint
// and a wake time as you type, and a wrapped hint would grow the box a row
// mid-keystroke. Pin both dimensions so a longer hint can't reintroduce that.
func TestSnoozeDialogHeightIsStable(t *testing.T) {
	// Every distinct verdict-line state, including input-focused-but-empty —
	// that one has its own hint string, and it wrapped the first time.
	states := []struct {
		name    string
		onInput bool
		typed   string
	}{
		{"preset focused", false, ""},
		{"input focused, empty", true, ""},
		{"input focused, valid", true, "2d"},
		{"input focused, bad unit", true, "2x"},
		{"input focused, over cap", true, "99d"},
		{"input focused, partial", true, "1"},
		{"input focused, widest", true, "30d"},
	}
	var w, h int
	for i, st := range states {
		d := NewSnoozeDialog()
		d.SetSize(120, 40)
		d.Show("Snooze Refactor status worker")
		if st.onInput {
			d.setFocus(snoozeInputRow())
		}
		d.input.SetValue(st.typed)
		v := d.View()
		gotW, gotH := lipgloss.Width(v), lipgloss.Height(v)
		if i == 0 {
			w, h = gotW, gotH
			continue
		}
		if gotW != w || gotH != h {
			t.Errorf("%s: box is %dx%d, want %dx%d — it must not resize as you move or type",
				st.name, gotW, gotH, w, h)
		}
	}
}

// TestSnoozeKeyOpensDurationPicker: `z` opens the picker; picking applies to the
// row it named.
func TestSnoozeKeyOpensDurationPicker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snz.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40

	s := session.NewSession("snoozable", "/tmp/snz-e2e")
	h.sessions = []*session.Session{s}
	h.setGitInfo(map[string]*git.RepoInfo{"/tmp/snz-e2e": {OriginKey: "github.com/acme/snz"}})
	// Drive the real tree rather than a hand-built flatItems: applySnooze calls
	// rebuildFlatItems, so a synthetic row would be discarded mid-test and the
	// cursor would end up on a header.
	focusSession := func() {
		h.rebuildFlatItems()
		for i, it := range h.flatItems {
			if it.Session != nil && it.Session.ID == s.ID {
				h.cursor = i
				return
			}
		}
		t.Fatal("session row missing from the tree")
	}
	focusSession()

	if _, cmd := h.handleKey(key("z")); cmd != nil {
		t.Errorf("opening the picker emitted %#v, want nothing", cmd())
	}
	if !h.snoozeDialog.IsVisible() {
		t.Fatal("`z` did not open the duration picker")
	}
	// Every preset must be offered, each with its resolved wake time on screen.
	view := h.snoozeDialog.View()
	for _, dur := range SnoozeDurations {
		if !strings.Contains(view, dur.Label) {
			t.Errorf("picker is missing the %q preset:\n%s", dur.Label, view)
		}
	}
	if !strings.Contains(view, "or type") {
		t.Errorf("picker offers no custom input:\n%s", view)
	}

	// Picking a duration snoozes the session and closes the picker.
	_, cmd := h.snoozeDialog.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter in the picker produced no command")
	}
	if _, c := h.Update(cmd()); c != nil {
		_ = c()
	}
	if !s.IsSnoozed(time.Now()) {
		t.Fatal("picking a duration did not snooze the session")
	}

	// `z` on an already-snoozed row wakes it rather than re-opening the picker.
	focusSession()
	if _, cmd := h.handleKey(key("z")); cmd != nil {
		_ = cmd()
	}
	if h.snoozeDialog.IsVisible() {
		t.Error("`z` on a snoozed row should wake it, not re-open the picker")
	}
	if s.IsSnoozed(time.Now()) {
		t.Error("`z` on a snoozed row did not wake it")
	}
}

// TestGroupSnoozeCollapsesAndWakeExpands: snoozing a repo folds it away (the
// point is to get it out of your way) and waking restores it, symmetrically.
func TestGroupSnoozeCollapsesAndWakeExpands(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snzg.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40

	const repo = "/tmp/snz-group"
	h.flatItems = []SidebarItem{{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo, Expanded: true}}
	h.cursor = 0

	sc, ok := h.snoozeScopeAtCursor()
	if !ok || sc.groupKey != repo {
		t.Fatalf("scope = %+v, want the checkout key %q", sc, repo)
	}

	h.applySnooze(sc, SnoozeDurations[0])
	if IsExpanded(h.repoExpanded, repo) {
		t.Error("snoozing a group must collapse it")
	}
	if got := groupSnoozeAt(h.groupSnooze, repo, time.Now()); got.IsZero() {
		t.Error("group snooze was not recorded")
	}
	// It must survive a restart, so it has to reach storage.
	persisted, err := storage.LoadSnoozedGroups()
	if err != nil {
		t.Fatalf("LoadSnoozedGroups: %v", err)
	}
	if _, ok := persisted[repo]; !ok {
		t.Error("group snooze was not persisted")
	}

	h.clearSnooze(sc)
	if !IsExpanded(h.repoExpanded, repo) {
		t.Error("waking a group must re-expand it")
	}
	persisted, _ = storage.LoadSnoozedGroups()
	if _, still := persisted[repo]; still {
		t.Error("waking a group must delete its persisted row")
	}
}

// TestSnoozeMutedCoversCollapsedGroups: statusCountsLine is fleet-wide, so the
// muted map must cover sessions hidden inside a collapsed group. Reading the
// answer off flatItems instead would silently drop them and make the global
// pill disagree with the sidebar underneath it.
func TestSnoozeMutedCoversCollapsedGroups(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snzm.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	const repo = "/tmp/snz-collapsed"
	const origin = "github.com/acme/collapsed"
	h.setGitInfo(map[string]*git.RepoInfo{repo: {OriginKey: origin}})

	hidden := session.NewSession("hidden under a fold", repo)
	hidden.SetStatus(session.StatusWaiting)
	hidden.SetSnoozedUntil(time.Now().Add(time.Hour))
	h.sessions = []*session.Session{hidden}

	// Fold the origin so the session is absent from flatItems entirely.
	h.repoExpanded[OriginExpandKey(origin)] = false
	h.rebuildFlatItems()

	for _, it := range h.flatItems {
		if it.Session != nil && it.Session.ID == hidden.ID {
			t.Fatal("precondition failed: session should be hidden by the collapsed origin")
		}
	}
	if !h.snoozeMuted[hidden.ID] {
		t.Error("a snoozed session inside a collapsed group must still be marked muted")
	}
}

// TestForgetSnoozeClearsGroupState: snoozed_groups shares collapsed_groups'
// keyspace, so it has to share its lifecycle. Without cleanup on delete, a
// worktree created later at the same path is born muted — dimmed, skipped by
// Space, with a countdown nobody set — until the original deadline lapses.
func TestForgetSnoozeClearsGroupState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snzf.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	const repo = "/tmp/snz-forget"
	const origin = "github.com/acme/forget"
	h.setGitInfo(map[string]*git.RepoInfo{repo: {OriginKey: origin}})

	h.flatItems = []SidebarItem{{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo, Expanded: true}}
	h.cursor = 0
	sc, ok := h.snoozeScopeAtCursor()
	if !ok {
		t.Fatal("expected a checkout scope at the cursor")
	}
	h.applySnooze(sc, SnoozeDurations[2]) // 4h

	if groupSnoozeAt(h.groupSnooze, repo, time.Now()).IsZero() {
		t.Fatal("precondition failed: group snooze was not set")
	}

	h.forgetSnooze(repo)

	if got := groupSnoozeAt(h.groupSnooze, repo, time.Now()); !got.IsZero() {
		t.Errorf("in-memory group snooze survived the delete: %v", got)
	}
	persisted, err := storage.LoadSnoozedGroups()
	if err != nil {
		t.Fatalf("LoadSnoozedGroups: %v", err)
	}
	if _, still := persisted[repo]; still {
		t.Error("persisted group snooze survived the delete — it would resurrect on re-add")
	}
	// The origin key goes too when the last checkout under it is gone, matching
	// forgetCollapse's rule.
	if _, still := persisted[OriginExpandKey(origin)]; still {
		t.Error("orphaned origin snooze survived the last checkout's removal")
	}

	// The real symptom: a session created at that path afterwards must be awake.
	reborn := session.NewSession("created after the delete", repo)
	reborn.SetStatus(session.StatusWaiting)
	h.sessions = []*session.Session{reborn}
	h.rebuildFlatItems()
	for _, it := range h.flatItems {
		if it.Session != nil && it.Session.ID == reborn.ID && it.Snooze.Muted {
			t.Error("a session created after the delete was born muted")
		}
	}
}

// TestMaybeWakeSnoozedExpires: the sweep drops lapsed deadlines and leaves live
// ones alone, and self-throttles so it isn't doing SQLite writes every tick.
func TestMaybeWakeSnoozedExpires(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snzw.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	now := time.Now()

	lapsed := session.NewSession("lapsed", "/tmp/snz-w")
	lapsed.SetSnoozedUntil(now.Add(-time.Minute))
	live := session.NewSession("live", "/tmp/snz-w")
	live.SetSnoozedUntil(now.Add(time.Hour))
	h.sessions = []*session.Session{lapsed, live}
	h.groupSnooze = map[string]time.Time{
		"/tmp/snz-old": now.Add(-time.Second),
		"/tmp/snz-new": now.Add(time.Hour),
	}

	if !h.maybeWakeSnoozed() {
		t.Fatal("sweep reported no change despite an expired snooze")
	}
	if !lapsed.SnoozedUntil().IsZero() {
		t.Error("expired session snooze survived the sweep")
	}
	if live.SnoozedUntil().IsZero() {
		t.Error("sweep cleared a snooze that has not expired")
	}
	if _, still := h.groupSnooze["/tmp/snz-old"]; still {
		t.Error("expired group snooze survived the sweep")
	}
	if _, gone := h.groupSnooze["/tmp/snz-new"]; !gone {
		t.Error("sweep cleared a group snooze that has not expired")
	}

	// Throttled: an immediate second call must not re-run.
	if h.maybeWakeSnoozed() {
		t.Error("sweep ran again inside its throttle interval")
	}
}

// TestRenderSnoozedRowFitsWidth: the snooze suffix comes out of the title's
// budget, so a snoozed row must never render wider than an unsnoozed one. This
// is the regression test for the hardcoded `reserve` in renderSessionItem.
func TestRenderSnoozedRowFitsWidth(t *testing.T) {
	const width = 40
	longTitle := "a really quite long session title that will certainly need truncating"

	cases := []struct {
		name   string
		snooze snoozeResult
	}{
		{"not snoozed", snoozeResult{}},
		{"own snooze", snoozeResult{Muted: true, Until: time.Now().Add(3 * time.Hour), OwnTimer: true}},
		{"group muted", snoozeResult{Muted: true, Until: time.Now().Add(3 * time.Hour)}},
	}
	for _, tc := range cases {
		for _, selected := range []bool{false, true} {
			s := session.NewSession(longTitle, "/tmp/w")
			s.SetStatus(session.StatusWaiting)
			out := renderSessionItem(SidebarItem{Session: s, Snooze: tc.snooze}, width, selected, 3)
			// The selected row deliberately appends an affordance note beyond
			// the pill, so only the unselected row is width-bounded.
			if !selected && ansi.StringWidth(out) > width {
				t.Errorf("%s: row width %d exceeds %d: %q",
					tc.name, ansi.StringWidth(out), width, out)
			}
		}
	}
}

// TestSelectedRowKeepsFullTitleBudget: the selected branch never renders
// snoozeRaw (it draws its own affordance note outside the pill), so it must not
// be charged for it. Before the fix the cursor row lost ~6 cells of title to a
// suffix it didn't draw — and the title visibly grew when you moved the cursor off.
func TestSelectedRowKeepsFullTitleBudget(t *testing.T) {
	const width = 44
	longTitle := "a really quite long session title that needs truncating"

	mk := func(sn snoozeResult) string {
		s := session.NewSession(longTitle, "/tmp/w")
		s.SetStatus(session.StatusWaiting)
		return renderSessionItem(SidebarItem{Session: s, Snooze: sn}, width, true, -1)
	}

	plain := mk(snoozeResult{})
	snoozed := mk(snoozeResult{Muted: true, Until: time.Now().Add(4 * time.Hour), OwnTimer: true})

	// Compare the title as rendered inside the selection pill: strip styling and
	// cut at the affordance note, which is appended outside the budget.
	titleOf := func(row string) string {
		plainRow := ansi.Strip(row)
		if i := strings.Index(plainRow, SnoozeGlyph); i >= 0 {
			plainRow = plainRow[:i]
		}
		return strings.TrimSpace(plainRow)
	}

	if got, want := titleOf(snoozed), titleOf(plain); got != want {
		t.Errorf("selected snoozed row truncates differently from an unsnoozed one:\n"+
			" snoozed: %q\n   plain: %q", got, want)
	}
}

// TestRenderSnoozeMarkers: an own-snoozed row carries a countdown; a
// group-muted child carries the marker alone (the header owns the clock).
func TestRenderSnoozeMarkers(t *testing.T) {
	s := session.NewSession("t", "/tmp/w")
	s.SetStatus(session.StatusWaiting)
	until := time.Now().Add(3 * time.Hour)

	own := renderSessionItem(SidebarItem{
		Session: s, Snooze: snoozeResult{Muted: true, Until: until, OwnTimer: true},
	}, 60, false, -1)
	if !strings.Contains(own, SnoozeGlyph) {
		t.Errorf("own-snoozed row missing the marker: %q", own)
	}
	if !strings.Contains(own, "3h") {
		t.Errorf("own-snoozed row missing its countdown: %q", own)
	}

	grouped := renderSessionItem(SidebarItem{
		Session: s, Snooze: snoozeResult{Muted: true, Until: until},
	}, 60, false, -1)
	if !strings.Contains(grouped, SnoozeGlyph) {
		t.Errorf("group-muted row missing the marker: %q", grouped)
	}
	if strings.Contains(grouped, "3h") {
		t.Errorf("group-muted row must not repeat the header's countdown: %q", grouped)
	}
}

// TestJumpSkipsSnoozedSessions: Space must skip muted rows — that IS the
// feature. Covers both an individually-snoozed session and a whole snoozed
// group, since they mute through different paths.
func TestJumpSkipsSnoozedSessions(t *testing.T) {
	now := time.Now()

	quiet := session.NewSession("quiet", "/tmp/jz-a")
	quiet.SetStatus(session.StatusWaiting)
	quiet.SetSnoozedUntil(now.Add(time.Hour))
	grouped := session.NewSession("grouped", "/tmp/jz-b")
	grouped.SetStatus(session.StatusWaiting)
	loud := session.NewSession("loud", "/tmp/jz-c")
	loud.SetStatus(session.StatusWaiting)

	h := &Home{
		sessions:     []*session.Session{quiet, grouped, loud},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
		// /tmp/jz-b's whole checkout is snoozed.
		groupSnooze: map[string]time.Time{"/tmp/jz-b": now.Add(time.Hour)},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/jz-a": {OriginKey: "github.com/acme/a"},
		"/tmp/jz-b": {OriginKey: "github.com/acme/b"},
		"/tmp/jz-c": {OriginKey: "github.com/acme/c"},
	}
	h.gitInfoCache.Store(&gi)
	h.rebuildFlatItems()

	h.cursor = 0
	h.jumpToNextAttentionSession()

	landed := h.flatItems[h.cursor].Session
	if landed == nil || landed.ID != loud.ID {
		t.Fatalf("jump must land on the only unmuted waiting session, got %v", landed)
	}
}

// TestJumpReachesSessionAfterSnoozeExpiry: once the deadline passes the session
// rejoins the rotation with no further action.
func TestJumpReachesSessionAfterSnoozeExpiry(t *testing.T) {
	woken := session.NewSession("woken", "/tmp/jz-exp")
	woken.SetStatus(session.StatusWaiting)
	woken.SetSnoozedUntil(time.Now().Add(-time.Minute)) // already lapsed

	h := &Home{
		sessions:     []*session.Session{woken},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{"/tmp/jz-exp": {OriginKey: "github.com/acme/e"}}
	h.gitInfoCache.Store(&gi)
	h.rebuildFlatItems()

	h.cursor = 0
	h.jumpToNextAttentionSession()

	if landed := h.flatItems[h.cursor].Session; landed == nil || landed.ID != woken.ID {
		t.Errorf("an expired snooze must not keep a session out of the rotation, got %v", landed)
	}
}
