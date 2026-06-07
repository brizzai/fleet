package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

// newTipHome builds a minimal Home with the tip maps initialized and one
// session per given status. It avoids NewHome (and its storage/tmux deps) since
// the tip logic only reads h.sessions, h.cfg, and the tip maps.
func newTipHome(cfg *config.Config, statuses ...session.Status) *Home {
	h := &Home{
		cfg:                 cfg,
		tipEpisodeDismissed: map[string]bool{},
		tipShownSince:       map[string]time.Time{},
	}
	for i, st := range statuses {
		s := session.NewSession(fmt.Sprintf("s%d", i), "/tmp/repo")
		s.SetStatus(st)
		h.sessions = append(h.sessions, s)
	}
	return h
}

func errStatuses(n int) []session.Status {
	out := make([]session.Status, n)
	for i := range out {
		out[i] = session.StatusError
	}
	return out
}

func TestRecurringTip_ThresholdAndEpisodeReset(t *testing.T) {
	// Pre-mark the command-palette (tipOnce) tip seen so it doesn't interfere —
	// it triggers at >= cmdPaletteMinSessions, which overlaps these counts.
	seenCmd := func() *config.Config { return &config.Config{SeenTips: []string{tipCmdPaletteID}} }

	h := newTipHome(seenCmd(), errStatuses(reloadFailedThreshold-1)...)
	h.refreshTips()
	if h.activeTipID != "" {
		t.Fatalf("below threshold: want no tip, got %q", h.activeTipID)
	}

	// Hit the threshold — the failed-sessions tip should show.
	h = newTipHome(seenCmd(), errStatuses(reloadFailedThreshold)...)
	h.refreshTips()
	if h.activeTipID != tipReloadFailedID {
		t.Fatalf("at threshold: want %q, got %q", tipReloadFailedID, h.activeTipID)
	}

	// Dismiss hides the current occurrence (in-memory, not persisted).
	h.dismissActiveTip()
	if h.activeTipID != "" {
		t.Fatalf("after dismiss: want no tip, got %q", h.activeTipID)
	}
	if h.cfg.IsTipSeen(tipReloadFailedID) {
		t.Fatalf("recurring dismiss must not persist the tip id; SeenTips=%v", h.cfg.SeenTips)
	}
	h.refreshTips()
	if h.activeTipID != "" {
		t.Fatalf("still-failing after dismiss: tip should stay hidden, got %q", h.activeTipID)
	}

	// Condition clears (errors resolved) then recurs — tip returns.
	for _, s := range h.sessions {
		s.SetStatus(session.StatusRunning)
	}
	h.refreshTips() // episode ends, dismissal flag reset
	for _, s := range h.sessions {
		s.SetStatus(session.StatusError)
	}
	h.refreshTips()
	if h.activeTipID != tipReloadFailedID {
		t.Fatalf("after recurrence: want %q, got %q", tipReloadFailedID, h.activeTipID)
	}
}

func TestOnceTip_RespectsSeenAndPersistsOnDismiss(t *testing.T) {
	// Isolate config writes to a temp HOME so markTipSeen doesn't touch the
	// real ~/.config/fleet/config.json.
	t.Setenv("HOME", t.TempDir())

	// Already seen → never shows.
	seen := &config.Config{SeenTips: []string{tipCmdPaletteID}}
	h := newTipHome(seen, session.StatusRunning, session.StatusRunning, session.StatusRunning)
	h.refreshTips()
	if h.activeTipID != "" {
		t.Fatalf("seen tipOnce should stay hidden, got %q", h.activeTipID)
	}

	// Fresh config, enough sessions → shows, then dismiss persists it.
	cfg := &config.Config{}
	h = newTipHome(cfg, session.StatusRunning, session.StatusRunning, session.StatusRunning)
	h.refreshTips()
	if h.activeTipID != tipCmdPaletteID {
		t.Fatalf("want %q, got %q", tipCmdPaletteID, h.activeTipID)
	}
	h.dismissActiveTip()
	if !cfg.IsTipSeen(tipCmdPaletteID) {
		t.Fatalf("dismiss must record tipOnce in SeenTips; got %v", cfg.SeenTips)
	}
	h.refreshTips()
	if h.activeTipID != "" {
		t.Fatalf("dismissed tipOnce should stay hidden, got %q", h.activeTipID)
	}
}

func TestTipPriority_FailedBeatsCmdPalette(t *testing.T) {
	// Both conditions hold: 4 error sessions (>= cmdPaletteMinSessions too).
	h := newTipHome(&config.Config{}, errStatuses(reloadFailedThreshold)...)
	h.refreshTips()
	if h.activeTipID != tipReloadFailedID {
		t.Fatalf("higher-priority tip should win, got %q", h.activeTipID)
	}
}

func TestConfigIsTipSeen(t *testing.T) {
	c := &config.Config{SeenTips: []string{"a", "b"}}
	if !c.IsTipSeen("a") || !c.IsTipSeen("b") {
		t.Fatal("IsTipSeen should report stored ids")
	}
	if c.IsTipSeen("c") {
		t.Fatal("IsTipSeen should be false for unstored id")
	}
}
