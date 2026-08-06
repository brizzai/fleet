package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
)

// The readout alternates between the two windows rather than picking one. The
// 5-hour bucket gates your next message; the weekly one gates your week, and
// both are worth a glance. Showing them side by side would double the width in
// a corner already shared with the What's New badge, so each takes a turn and
// carries a label saying which it is.
//
// The phase comes from the wall clock, not a ticker. The header re-renders on
// the existing UI tick regardless, so deriving it from `now` costs no new
// state, no goroutine, and no timer that could outlive what it animates.
const (
	// accountBarCells is the width of the mini gauge. Short on purpose: this
	// shares the header row with the What's New badge and must never crowd it.
	accountBarCells = 6
	// accountHeaderMinWidth is the terminal width below which the readout is
	// dropped entirely rather than shortened into noise.
	accountHeaderMinWidth = 72
	// accountHeaderCompactWidth is where labels are dropped and only the gauges
	// and percentages survive.
	accountHeaderCompactWidth = 100
	// accountWindowRotateSecs is how long each window holds the corner: long
	// enough to read, short enough that you don't wait for the other one.
	accountWindowRotateSecs = 6
)

// quotaWindow is which bucket the readout is currently showing.
type quotaWindow int

const (
	windowSevenDay quotaWindow = iota
	windowFiveHour
)

func (w quotaWindow) label() string {
	if w == windowFiveHour {
		return "5h"
	}
	return "7d"
}

func (w quotaWindow) pct(u claudeaccount.Usage) int {
	if w == windowFiveHour {
		return u.FiveHourPct
	}
	return u.SevenDayPct
}

// windowAt derives the displayed window from the clock, so every surface that
// renders in the same frame agrees without sharing state.
func windowAt(now time.Time) quotaWindow {
	if (now.Unix()/accountWindowRotateSecs)%2 == 1 {
		return windowFiveHour
	}
	return windowSevenDay
}

// renderAccountUsageHeader draws a compact per-account quota readout, or "" when
// there is nothing worth showing.
//
// Returns empty for a single account: with one subscription there is no choice
// being made, so the number is trivia rather than information, and the header
// row is better spent on nothing.
func renderAccountUsageHeader(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, width int, now time.Time) string {
	if len(accounts) < 2 || width < accountHeaderMinWidth {
		return ""
	}

	win := windowAt(now)
	chips := make([]string, 0, len(accounts))
	for _, a := range accounts {
		u := usage[a.Email]
		// A rejected account shows even with no reading to its name — it is the
		// one state here that is not trivia. Skipping it (which the Known check
		// below would do, since a refused token never returns numbers) leaves the
		// corner looking like a healthy single-account setup while half the
		// rotation is dead.
		if !u.Rejected && !u.Known() {
			continue
		}
		chips = append(chips, accountChip(a, u, win, width, now))
	}
	if len(chips) == 0 {
		return ""
	}

	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	sep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" │ ")
	// One label for the whole readout rather than one per chip: both chips show
	// the same window, so repeating it would be noise that costs real columns.
	return strings.Join(chips, sep) + dim.Render(" "+win.label())
}

// accountChip renders one account as "label ▰▰▱▱▱▱ 34%", or as a reset
// countdown when its 5-hour window is spent.
func accountChip(a claudeaccount.Account, u claudeaccount.Usage, win quotaWindow, width int, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)

	label := ""
	if width >= accountHeaderCompactWidth {
		label = dim.Render(accountShortLabel(a)) + " "
	}

	// Rejected outranks spent, which outranks the percentage. Both displace the
	// number in either window rather than waiting their turn — a percentage is
	// something to note, these are something to do.
	//
	// The wording distinguishes them because the actions differ: "spent" is a
	// wait and resolves itself, "rejected" needs a new token and never will.
	if u.Rejected {
		return label + lipgloss.NewStyle().Foreground(ColorRed).Render("✕ rejected")
	}
	if u.Exhausted(now) {
		return label + lipgloss.NewStyle().Foreground(ColorRed).Render("spent "+resetIn(u, now))
	}

	pct := win.pct(u)
	return label + renderQuotaBar(pct) + " " + quotaStyle(pct).Render(fmt.Sprintf("%d%%", pct))
}

// renderQuotaBar draws a filled/empty gauge coloured by headroom. The half-block
// glyphs are from the same Geometric Shapes range as fleet's status dots, so
// they stay aligned in the base monospace fonts the sidebar already targets.
func renderQuotaBar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * accountBarCells / 100
	// Any nonzero usage should show at least one cell, or a busy account reads
	// as untouched.
	if filled == 0 && pct > 0 {
		filled = 1
	}
	full := quotaStyle(pct).Render(strings.Repeat("▰", filled))
	rest := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("▱", accountBarCells-filled))
	return full + rest
}

// resetIn renders how long until the account is usable again, as "in 42m" or
// "in 3h". Relative rather than a clock time: the question being asked is "how
// long do I wait", and an absolute time makes the reader do the subtraction.
func resetIn(u claudeaccount.Usage, now time.Time) string {
	if u.FiveHourReset.IsZero() {
		return ""
	}
	d := u.FiveHourReset.Sub(now)
	if d <= 0 {
		return ""
	}
	if d < time.Hour {
		return fmt.Sprintf("in %dm", int(d.Minutes())+1)
	}
	return fmt.Sprintf("in %.0fh", d.Hours())
}

// accountShortLabel trims an account to something that fits a header chip: a
// user-set label wins, otherwise the local part of the email ("yuval" from
// "yuval@example.com"), which is what distinguishes two personal accounts.
func accountShortLabel(a claudeaccount.Account) string {
	if a.Label != "" {
		return truncateLabel(a.Label)
	}
	local, _, found := strings.Cut(a.Email, "@")
	if found && local != "" {
		return truncateLabel(local)
	}
	return truncateLabel(a.Email)
}

const accountLabelMax = 10

func truncateLabel(s string) string {
	if lipgloss.Width(s) <= accountLabelMax {
		return s
	}
	r := []rune(s)
	return string(r[:accountLabelMax-1]) + "…"
}
