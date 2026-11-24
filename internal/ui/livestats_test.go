package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/stats"
)

// liveCostHome returns a Home holding one Claude session ("conv-1") in a real
// folder, with HOME pointed at a temp dir so transcripts land there.
func liveCostHome(t *testing.T) (h *Home, id, dir string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h = adoptTestHome(t)
	dir = filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := session.NewSession("my task", dir)
	s.ClaudeSessionID = "conv-1"
	s.CreatedAt = time.Now().Add(-48 * time.Hour) // older than the transcript's turns, or Stats drops them as inherited history
	row := s.ToRow()
	if err := h.storage.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	h.handleAdoptSessions(adoptSessionsMsg{
		sessions:  []*session.Session{session.FromRow(row)},
		repoRoots: map[string]string{row.ID: dir},
	})
	if err := os.MkdirAll(filepath.Dir(session.ClaudeTranscriptPath("conv-1", dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	return h, row.ID, dir
}

func liveCursorOn(t *testing.T, h *Home, id string) {
	t.Helper()
	for i, it := range h.flatItems {
		if !it.IsRepoHeader && it.Session != nil && it.Session.ID == id {
			h.cursor = i
			return
		}
	}
	t.Fatal("session row not in the sidebar")
}

func TestFormatLiveCost(t *testing.T) {
	for _, tc := range []struct {
		c    stats.LiveCost
		ok   bool
		want string
	}{
		{stats.LiveCost{}, false, "—"},
		{stats.LiveCost{Turns: 0}, true, "—"},
		{stats.LiveCost{Turns: 2, USD: 0.001}, true, "<$0.01"},
		{stats.LiveCost{Turns: 2, USD: 1.234}, true, "$1.23"},
		{stats.LiveCost{Turns: 2, USD: 412.6}, true, "$413"},
		{stats.LiveCost{Turns: 3, USD: 2, Unpriced: 1}, true, "$2.00+"},
		{stats.LiveCost{Turns: 1, USD: 0, Unpriced: 1}, true, "$0.00+"},
	} {
		if got := formatLiveCost(tc.c, tc.ok); got != tc.want {
			t.Errorf("formatLiveCost(%+v, %v) = %q, want %q", tc.c, tc.ok, got, tc.want)
		}
	}
}

func TestFormatIdle(t *testing.T) {
	for _, tc := range []struct {
		st   session.Status
		d    time.Duration
		want string
	}{
		{session.StatusIdle, 30 * time.Second, ""},
		{session.StatusIdle, 12 * time.Minute, "idle 12m"},
		{session.StatusFinished, 3 * time.Hour, "idle 3h"},
		{session.StatusWaiting, 90 * time.Minute, "waiting 1h"},
		{session.StatusIdle, 72 * time.Hour, "idle 3d"},
		{session.StatusRunning, 12 * time.Minute, ""},
		{session.StatusSuspended, 5 * time.Hour, ""},
		{session.StatusError, 5 * time.Hour, ""},
	} {
		if got := formatIdle(tc.st, tc.d); got != tc.want {
			t.Errorf("formatIdle(%s, %v) = %q, want %q", tc.st, tc.d, got, tc.want)
		}
	}
}

func TestFormatQuota(t *testing.T) {
	now := time.Now()
	if got := formatQuota(claudeaccount.Usage{FiveHourPct: 41, SevenDayPct: 18, FetchedAt: now}); got != "5h 41% · 7d 18%" {
		t.Errorf("quota = %q", got)
	}
	if got := formatQuota(claudeaccount.Usage{}); got != "" {
		t.Errorf("no reading must say nothing, got %q", got)
	}
	if got := formatQuota(claudeaccount.Usage{FiveHourPct: 41, FetchedAt: now, LoggedOut: true}); got != "" {
		t.Errorf("logged-out account must say nothing, got %q", got)
	}
}

// A narrow panel keeps path, clock and account and sheds the live chips from the
// end, never the other way round; a wide one shows them all.
func TestPreviewFooterShedsLiveChipsFirst(t *testing.T) {
	s := session.NewSession("t", "/tmp/proj")
	s.LastAccessedAt = time.Now().Add(-5 * time.Minute)
	live := []string{"$1.23", "idle 12m", "5h 41% · 7d 18%"}

	wide := ansi.Strip(BuildPreviewFooter(s, "me@x.com", live, 200))
	for _, want := range []string{"/tmp/proj", "me@x.com", "$1.23", "idle 12m", "5h 41%"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide footer missing %q: %q", want, wide)
		}
	}
	base := ansi.Strip(BuildPreviewFooter(s, "me@x.com", nil, 200))
	for w := ansi.StringWidth(base); w <= ansi.StringWidth(wide); w++ {
		got := ansi.Strip(BuildPreviewFooter(s, "me@x.com", live, w))
		if ansi.StringWidth(got) > w {
			t.Fatalf("footer is %d cells at width %d: %q", ansi.StringWidth(got), w, got)
		}
		if !strings.HasPrefix(got, base) {
			t.Fatalf("at width %d the live chips displaced the fixed ones: %q (base %q)", w, got, base)
		}
	}
	// Order of shedding: quota first, then idle, then cost.
	mid := ansi.Strip(BuildPreviewFooter(s, "me@x.com", live, ansi.StringWidth(base)+len(" · $1.23")))
	if !strings.Contains(mid, "$1.23") || strings.Contains(mid, "idle") {
		t.Errorf("expected only the cost to survive: %q", mid)
	}
}

func writeCostTranscript(t *testing.T, path string, turns int) {
	t.Helper()
	var b strings.Builder
	base := time.Now().Add(-time.Hour).UTC()
	f := func(at time.Time) string { return at.Format("2006-01-02T15:04:05.000Z") }
	for i := 0; i < turns; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":"x"},"timestamp":%q,"origin":{"kind":"human"},"isSidechain":false}`+"\n", f(at))
		fmt.Fprintf(&b, `{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"id":"m%d","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000000,"output_tokens":100000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`+"\n", f(at.Add(5*time.Second)), i)
		fmt.Fprintf(&b, `{"type":"system","subtype":"turn_duration","durationMs":10000,"timestamp":%q,"isSidechain":false}`+"\n", f(at.Add(10*time.Second)))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// End to end: the selected Claude session's transcript is read, priced like the
// Stats screen, and shows in the footer; the same figure is not re-read until it
// is due, and turning the flag off removes it.
func TestLiveCostRefreshesForSelectedSession(t *testing.T) {
	h, id, dir := liveCostHome(t) // Claude session "conv-1" with a transcript dir on disk
	st, err := stats.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.statsStore = st

	s := h.sessionByID[id]
	s.SetStatus(session.StatusIdle)
	liveCursorOn(t, h, id)
	writeCostTranscript(t, session.ClaudeTranscriptPath("conv-1", dir), 2)

	if chips := h.previewLiveStats(s); len(chips) == 0 || chips[0] != "—" {
		t.Fatalf("before the first reading the cost must read —, got %v", chips)
	}
	now := time.Now()
	cmd := h.maybeRefreshLiveCost(now)
	if cmd == nil {
		t.Fatal("selected Claude session with a transcript: a read was due")
	}
	if h.maybeRefreshLiveCost(now) != nil {
		t.Fatal("a second read started while one is in flight")
	}
	msg, ok := cmd().(liveCostMsg)
	if !ok {
		t.Fatal("read returned no liveCostMsg")
	}
	h.handleLiveCost(msg)

	// 2 turns × (1M in × $5 + 0.1M out × $25) = $15.00
	if chips := h.previewLiveStats(s); chips[0] != "$15.00" {
		t.Fatalf("cost chip = %v, want $15.00", chips)
	}
	if h.maybeRefreshLiveCost(now.Add(time.Second)) != nil {
		t.Fatal("re-read a figure that was only a second old")
	}
	if h.maybeRefreshLiveCost(now.Add(liveCostRestingEvery+time.Second)) == nil {
		t.Fatal("a resting session's figure must come due again")
	}

	off := false
	h.cfg.SessionLiveStats = &off
	if chips := h.previewLiveStats(s); chips != nil {
		t.Fatalf("session_live_stats=false must show nothing, got %v", chips)
	}
}

// No conversation yet (never prompted): no read is attempted and the footer says —.
func TestLiveCostSkipsSessionWithoutConversation(t *testing.T) {
	h, id, _ := liveCostHome(t)
	st, err := stats.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.statsStore = st
	s := h.sessionByID[id]
	s.ClaudeSessionID = ""
	liveCursorOn(t, h, id)
	if h.maybeRefreshLiveCost(time.Now()) != nil {
		t.Fatal("no conversation id: nothing to read")
	}
	if chips := h.previewLiveStats(s); len(chips) == 0 || chips[0] != "—" {
		t.Fatalf("want — for a session with no cost data, got %v", chips)
	}
}
