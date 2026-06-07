package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
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

	reloadFailedThreshold = 4
	cmdPaletteMinSessions = 3
	// tipOnceBudget is how long a tipOnce tip stays visible (since first shown)
	// before it auto-dismisses itself and is marked seen.
	tipOnceBudget = 45 * time.Second
)

// tipRegistry is the catalog of contextual tips. Add new tips here.
var tipRegistry = []Tip{
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
func (h *Home) refreshTips() {
	now := time.Now()
	chosen := ""
	bestPri := -1
	for i := range tipRegistry {
		t := &tipRegistry[i]
		act := t.active(h)
		switch t.Policy {
		case tipRecurring:
			if !act {
				// Episode ended — clear dismissal so the tip can recur.
				delete(h.tipEpisodeDismissed, t.ID)
				continue
			}
			if h.tipEpisodeDismissed[t.ID] {
				continue
			}
		case tipOnce:
			if !act || h.cfg.IsTipSeen(t.ID) {
				continue
			}
			// Start the visibility timer on first display, then retire the tip
			// once it has had its time on screen.
			if _, ok := h.tipShownSince[t.ID]; !ok {
				h.tipShownSince[t.ID] = now
			}
			if now.Sub(h.tipShownSince[t.ID]) >= tipOnceBudget {
				h.markTipSeen(t.ID)
				continue
			}
		}
		if t.Priority > bestPri {
			bestPri = t.Priority
			chosen = t.ID
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
	h.refreshTips()
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
	return renderTip(t.text(h), h.width)
}

// tipMaxWidth bounds the tip column; matches the toast column for visual
// consistency when both stack in the bottom-right.
const tipMaxWidth = 44

// renderTip draws the bottom-right hint box: accent rounded border, a width-1
// accent sigil prefix, the body, and a dim "shift+X to dismiss" footer line.
// Mirrors renderToast (toast.go) so the two stack cleanly.
func renderTip(body string, maxWidth int) string {
	width := tipMaxWidth
	if maxWidth > 0 && maxWidth < width+2 {
		width = maxWidth - 2
	}
	if width < 12 {
		return ""
	}

	innerWidth := width - toastBorderCols - 2*toastInnerHPad
	if innerWidth < 6 {
		innerWidth = 6
	}
	bodyWidth := innerWidth - 2 // reserve 2 cells for the sigil/indent prefix

	sigilStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(ColorText)
	hintStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	lines := wrapToastLine(strings.ReplaceAll(body, "\n", " "), bodyWidth)

	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(sigilStyle.Render("✦") + " " + msgStyle.Render(line))
		} else {
			b.WriteString("\n  " + msgStyle.Render(line))
		}
	}
	b.WriteString("\n  " + hintStyle.Render("shift+X to dismiss"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, toastInnerHPad).
		Width(width - toastBorderCols).
		Render(b.String())
}
