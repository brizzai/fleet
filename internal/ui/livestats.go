package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/stats"
)

// Live session stats: what the selected session has cost, how long it has been
// still, and how much of its account's quota is gone — one dim line in the
// preview footer. Selected session only, on purpose: it is information you want
// for the one session you are looking at, not forty rows at once, and it keeps
// the cost of reading transcripts to a single file.

const (
	// liveCostWorkingEvery is how often a running session's transcript is
	// re-read for cost. The read is incremental (only the new bytes) and the
	// check is a stat when nothing changed.
	liveCostWorkingEvery = 20 * time.Second
	// liveCostRestingEvery paces everything else: a session that is not working
	// writes no transcript, so there is little to find.
	liveCostRestingEvery = 2 * time.Minute
)

type liveCostEntry struct {
	cost stats.LiveCost
	ok   bool // a reading landed (false: none yet, or the last one failed)
	at   time.Time
}

// liveCostMsg carries one conversation's cost back to the Update loop.
type liveCostMsg struct {
	id   string // fleet session id
	cost stats.LiveCost
	err  error
}

// maybeRefreshLiveCost starts a cost read for the selected session when its
// figure is due. Update goroutine; the read itself runs in the returned Cmd.
func (h *Home) maybeRefreshLiveCost(now time.Time) tea.Cmd {
	if h.statsStore == nil || h.liveCostBusy || h.isAttaching.Load() || !h.cfg.IsSessionLiveStats() {
		return nil
	}
	s := h.selectedSession()
	if s == nil || agentOf(s) != agent.Claude || s.ClaudeSessionID == "" {
		return nil
	}
	every := liveCostRestingEvery
	if st := s.GetStatus(); st == session.StatusRunning || st == session.StatusWaiting {
		every = liveCostWorkingEvery
	}
	if e, seen := h.liveCost[s.ID]; seen && now.Sub(e.at) < every {
		return nil
	}
	path := session.ClaudeTranscriptPath(s.ClaudeSessionID, s.ProjectPath)
	if path == "" {
		return nil
	}
	h.liveCostBusy = true
	id, claudeID := s.ID, s.ClaudeSessionID
	note := h.statsNotes([]*session.Session{s})
	store, ctx := h.statsStore, h.ctx
	return func() tea.Msg {
		if note != nil {
			note()
		}
		store.Flush(statsFlushTimeout) // the note must land before the owner lookup
		cost, err := store.ScanConversation(ctx, claudeID, path)
		return liveCostMsg{id: id, cost: cost, err: err}
	}
}

func (h *Home) handleLiveCost(msg liveCostMsg) {
	h.liveCostBusy = false
	e := liveCostEntry{at: time.Now()}
	switch {
	case msg.err == nil:
		e.cost, e.ok = msg.cost, true
	case errors.Is(msg.err, context.Canceled):
		return
	default:
		// "—" rather than a stale number: a figure we could not refresh is not
		// one to keep showing as current. Logged once per attempt, not per frame.
		debuglog.Logger.Debug("live cost unavailable", "id", msg.id, "err", msg.err)
	}
	if h.liveCost == nil {
		h.liveCost = map[string]liveCostEntry{}
	}
	h.liveCost[msg.id] = e
}

// previewLiveStats returns the footer chips for s, in priority order (the
// footer drops from the end when the panel is narrow), or nil when the feature
// is off or has nothing to say.
func (h *Home) previewLiveStats(s *session.Session) []string {
	if s == nil || !h.cfg.IsSessionLiveStats() {
		return nil
	}
	var chips []string
	if agentOf(s) == agent.Claude {
		e := h.liveCost[s.ID]
		chips = append(chips, formatLiveCost(e.cost, e.ok))
	}
	if c := formatIdle(s.GetStatus(), s.IdleFor()); c != "" {
		chips = append(chips, c)
	}
	if agentOf(s) == agent.Claude && s.Account != "" {
		if u, ok := h.accountUsageSnapshot()[s.Account]; ok {
			if c := formatQuota(u); c != "" {
				chips = append(chips, c)
			}
		}
	}
	return chips
}

// formatLiveCost renders the dollar figure: "—" with no reading, "$1.23"
// otherwise, "$1.23+" when some turns ran on a model the price table lacks (a
// floor, not the total), and "<$0.01" for a session that has barely started.
func formatLiveCost(c stats.LiveCost, ok bool) string {
	if !ok || c.Turns == 0 {
		return "—"
	}
	var out string
	switch {
	case c.USD < 0.005 && c.Unpriced == 0:
		return "<$0.01"
	case c.USD >= 100:
		out = fmt.Sprintf("$%.0f", c.USD)
	default:
		out = fmt.Sprintf("$%.2f", c.USD)
	}
	if c.Unpriced > 0 {
		out += "+"
	}
	return out
}

// formatIdle says how long a session that is not working has been still:
// "idle 12m", or "waiting 12m" when it is stopped on a prompt for you. Working,
// starting, suspended and errored sessions say nothing (they are not "idle"),
// and so does a minute or less — that is just the gap between two messages.
func formatIdle(st session.Status, d time.Duration) string {
	word := ""
	switch st {
	case session.StatusIdle, session.StatusFinished:
		word = "idle"
	case session.StatusWaiting:
		word = "waiting"
	default:
		return ""
	}
	switch {
	case d < time.Minute:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%s %dm", word, int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%s %dh", word, int(d.Hours()))
	default:
		return fmt.Sprintf("%s %dd", word, int(d.Hours()/24))
	}
}

// formatQuota renders the session's account usage, 5-hour first (the order is
// the label — the same convention the header readout keeps). Nothing for an
// account with no reading or no login.
func formatQuota(u claudeaccount.Usage) string {
	if u.LoggedOut || !u.Known() {
		return ""
	}
	return fmt.Sprintf("5h %d%% · 7d %d%%", u.FiveHourPct, u.SevenDayPct)
}
