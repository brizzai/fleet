package ui

import (
	"fmt"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// tipPolicy controls how a tip is dismissed and whether it can return.
type tipPolicy int

const (
	// tipRecurring is condition-driven. Shift+X hides the current occurrence;
	// the tip returns the next time its condition goes inactive→active, and on
	// a fresh fleet launch. Dismissal is in-memory only (never persisted).
	tipRecurring tipPolicy = iota
	// tipOnce has no natural recurrence. It shows while relevant, auto-expires
	// after tipOnceBudget, and on dismiss-or-expiry is recorded in config so it
	// never shows again.
	tipOnce
)

// Tip is a single contextual hint shown in the bottom-right corner.
type Tip struct {
	ID       string
	Policy   tipPolicy
	Priority int // higher wins when several tips are active at once
	// active reports whether the tip's trigger condition currently holds.
	active func(h *Home) bool
	// text builds the rendered message body (may interpolate live counts).
	text func(h *Home) string
}

const (
	tipReloadFailedID = "reload_failed_sessions"
	tipCmdPaletteID   = "command_palette"
	tipDrawerID       = "terminal_drawer"
	tipTCCBlockedID   = "tcc_blocked_folder"

	reloadFailedThreshold = 4
	cmdPaletteMinSessions = 3
	// tipOnceBudget is how long a tipOnce tip stays visible (since first shown)
	// before it auto-dismisses itself and is marked seen.
	tipOnceBudget = 45 * time.Second
)

// tipRegistry is the catalog of contextual tips. Add new tips here.
var tipRegistry = []Tip{
	{
		// Highest priority: a TCC-blocked folder makes every session/shell there
		// unusable, so it outranks the reload-failed hint.
		ID:       tipTCCBlockedID,
		Policy:   tipRecurring,
		Priority: 120,
		active:   func(h *Home) bool { return h.anyTCCBlocked() },
		text: func(h *Home) string {
			return fmt.Sprintf("macOS blocks tmux from %s — sessions there fail with \"Operation not permitted\". "+
				"Move the project outside it, or grant Full Disk Access to your terminal + tmux and run `tmux kill-server`. "+
				"Press Ctrl+K → \"Open Full Disk Access Settings\".", tildeHome(h.firstTCCBlockedRoot()))
		},
	},
	{
		ID:       tipReloadFailedID,
		Policy:   tipRecurring,
		Priority: 100,
		active: func(h *Home) bool {
			return h.countSessionsByStatus(session.StatusError) >= reloadFailedThreshold
		},
		text: func(h *Home) string {
			return fmt.Sprintf("%d sessions stopped. Press Ctrl+K → \"Reload All Sessions\" to restart them.",
				h.countSessionsByStatus(session.StatusError))
		},
	},
	{
		ID:       tipCmdPaletteID,
		Policy:   tipOnce,
		Priority: 10,
		active:   func(h *Home) bool { return len(h.sessions) >= cmdPaletteMinSessions },
		text: func(h *Home) string {
			return "Tip: press Ctrl+K to open the command palette — jump to any session, repo, or command."
		},
	},
	{
		ID:       tipDrawerID,
		Policy:   tipOnce,
		Priority: 8, // below the command-palette tip so it doesn't fight on first launch
		active:   func(h *Home) bool { return len(h.sessions) >= 1 && len(h.shells) == 0 && h.drawerMode == drawerHidden },
		text: func(h *Home) string {
			return "Tip: press ` to open a terminal drawer — dev servers, logs, and scratch shells for the selected repo."
		},
	},
}

func findTip(id string) *Tip {
	for i := range tipRegistry {
		if tipRegistry[i].ID == id {
			return &tipRegistry[i]
		}
	}
	return nil
}

// countSessionsByStatus returns how many sessions are currently in st. Runs on
// the Update goroutine, so reading h.sessions is safe (same contract as
// rebuildFlatItems).
func (h *Home) countSessionsByStatus(st session.Status) int {
	n := 0
	for _, s := range h.sessions {
		if s.GetStatus() == st {
			n++
		}
	}
	return n
}

// refreshTips recomputes which tip (if any) should display, applying each tip's
// dismissal policy. Called on the ~2s tick and after a dismissal — never from
// the render path, so View stays side-effect free.
//
// tipVisible reports whether the tip area is actually on screen right now
// (false while a modal covers it), so a tipOnce's display budget only ages while
// the tip is genuinely visible — never off-screen behind a higher-priority tip
// or a dialog.
func (h *Home) refreshTips(tipVisible bool) {
	now := time.Now()
	var elapsed time.Duration
	if !h.lastTipTickAt.IsZero() {
		elapsed = now.Sub(h.lastTipTickAt)
	}
	h.lastTipTickAt = now

	// Phase 1: pick the highest-priority eligible tip, with no timer side effects.
	chosen := ""
	bestPri := -1
	for i := range tipRegistry {
		t := &tipRegistry[i]
		if !t.active(h) {
			// Condition cleared — reset per-tip state so the tip starts fresh the
			// next time it becomes relevant (recurrence for tipRecurring; a clean
			// display budget for tipOnce).
			delete(h.tipEpisodeDismissed, t.ID)
			delete(h.tipVisibleFor, t.ID)
			continue
		}
		switch t.Policy {
		case tipRecurring:
			if h.tipEpisodeDismissed[t.ID] {
				continue
			}
		case tipOnce:
			if h.cfg.IsTipSeen(t.ID) {
				continue
			}
		}
		if t.Priority > bestPri {
			bestPri = t.Priority
			chosen = t.ID
		}
	}

	// Phase 2: age the display budget only for the chosen tipOnce tip, and only
	// while it's actually visible. Every other tipOnce's timer resets, so a tip
	// that never wins the priority pick (or sits behind a modal) can't be retired
	// off-screen. elapsed is added only once the tip has already been showing for
	// a full tick, so pre-display time never counts.
	for i := range tipRegistry {
		t := &tipRegistry[i]
		if t.Policy != tipOnce {
			continue
		}
		if t.ID != chosen {
			delete(h.tipVisibleFor, t.ID)
			continue
		}
		if tipVisible {
			if h.activeTipID == t.ID {
				h.tipVisibleFor[t.ID] += elapsed
			}
			if h.tipVisibleFor[t.ID] >= tipOnceBudget {
				h.markTipSeen(t.ID)
				chosen = "" // retired this tick; the next tick surfaces any successor
			}
		}
	}
	h.activeTipID = chosen
}

// dismissActiveTip handles Shift+X: hides the visible tip per its policy and
// surfaces the next eligible tip, if any.
func (h *Home) dismissActiveTip() {
	id := h.activeTipID
	if id == "" {
		return
	}
	if t := findTip(id); t != nil {
		switch t.Policy {
		case tipRecurring:
			h.tipEpisodeDismissed[id] = true
		case tipOnce:
			h.markTipSeen(id)
		}
	}
	h.activeTipID = ""
	// Dismiss only fires from a keypress in normal (non-modal) context, so the
	// successor tip, if any, is visible.
	h.refreshTips(true)
}

// markTipSeen records a tipOnce tip as permanently dismissed and persists it.
func (h *Home) markTipSeen(id string) {
	if h.cfg.IsTipSeen(id) {
		return
	}
	h.cfg.SeenTips = append(h.cfg.SeenTips, id)
	if err := h.cfg.Save(); err != nil {
		debuglog.Logger.Error("config: save seen tip", "id", id, "err", err)
	}
}

// tipView renders the active tip box, or "" when none is showing.
func (h *Home) tipView() string {
	if h.activeTipID == "" {
		return ""
	}
	t := findTip(h.activeTipID)
	if t == nil {
		return ""
	}
	// Suppress immediately if the trigger no longer holds. activeTipID is only
	// recomputed on the ~2s tick, so without this re-check the box (and its live
	// count, e.g. "0 sessions stopped") would linger up to a full tick after the
	// condition cleared. Pure read — keeps View side-effect free.
	if !t.active(h) {
		return ""
	}
	return renderTip(t.text(h), h.width)
}

// renderTip draws the bottom-right hint box for the active tip: a width-1 accent
// sigil, the message, and a dim "shift+X to dismiss" footer. Shares renderHintBox
// and toastMaxWidth with the toast stack so the two boxes always align when
// stacked bottom-right.
func renderTip(body string, maxWidth int) string {
	width := toastMaxWidth
	if maxWidth > 0 && maxWidth < width+2 {
		width = maxWidth - 2
	}
	if width < 12 {
		return ""
	}
	return renderHintBox("✦", ColorAccent, body, "shift+X to dismiss", width)
}
