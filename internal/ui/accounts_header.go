package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/charmbracelet/x/ansi"
)

// The readout shows both quota windows, every frame, in a shape the user picks
// once in Settings.
//
// It used to show one window at a time and swap every six seconds, and to pick
// one of six densities by measuring what the breadcrumb left over — so the strip
// changed on a timer *and* reshaped whenever the cursor moved to a longer
// session title. A corner of the screen that animates on its own steals
// attention it hasn't earned, and a number you have to wait for is worse than a
// smaller number that is always there.
//
// So nothing here reads the clock except the countdowns, and the only thing the
// available width still decides is whether the account names fit.
const (
	// accountReadoutGap is the clear space kept between the header's breadcrumb
	// and the readout, so the two never read as one run of text.
	accountReadoutGap = 2
)

// The window type and its percentage live in claudeaccount rather than here:
// several surfaces name a window (this strip, the accounts dialog, the heal
// toast) and they have to agree.
const (
	windowSevenDay = claudeaccount.WindowSevenDay
	windowFiveHour = claudeaccount.WindowFiveHour
)

// renderAccountUsageHeader draws the per-account quota readout, or "" when there
// is nothing worth showing.
//
// Returns empty for a single account: with one subscription there is no choice
// being made, so the number is trivia rather than information, and the header
// row is better spent on nothing.
//
// style is one of config.AccountUsageSplit / AccountUsageGrouped. budget is the
// columns available before the What's New badge; nothing wider than it is ever
// returned.
func renderAccountUsageHeader(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, style string, budget int, now time.Time) string {
	if len(accounts) < 2 {
		return ""
	}

	shown := make([]claudeaccount.Account, 0, len(accounts))
	for _, a := range accounts {
		u := usage[a.Email]
		// A logged-out account shows even with no reading to its name — it is
		// the one state here that is not trivia. Skipping it (which the Known
		// check would do, since an account with no login never returns numbers)
		// leaves the corner looking like a healthy setup while half of it is
		// dead.
		if !u.LoggedOut && !u.Known() {
			continue
		}
		shown = append(shown, a)
	}
	if len(shown) == 0 {
		return ""
	}

	// Exactly one thing is given up before the strip disappears, and it is the
	// account names — position identifies two accounts, and the numbers are the
	// whole point. Deliberately never falls back to the *other* style: that
	// would hand the shape back to the breadcrumb, which is what this change
	// exists to stop.
	for _, labels := range []bool{true, false} {
		if out := renderReadout(shown, usage, style, labels, now); lipgloss.Width(out) <= budget {
			return out
		}
	}
	// Even the nameless form overflows: better nothing than a strip that pushes
	// the What's New badge off its corner.
	return ""
}

// renderReadout lays the whole strip out.
func renderReadout(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, style string, labels bool, now time.Time) string {
	sep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" │ ")

	chips := make([]string, 0, len(accounts))
	for _, a := range accounts {
		chips = append(chips, accountChip(a, usage[a.Email], style, labels, now))
	}
	return strings.Join(chips, sep)
}

// accountChip renders one account: both windows, 5-hour first.
//
// Nothing names the windows any more — the strip used to lead with the word
// "weekly" or "5-hour" because only one was on screen at a time. Now both are,
// and **order** is what tells them apart, so the 5-hour figure comes first in
// every style and every state. That ordering is a correctness property, not
// formatting taste: the countdowns alone can't do it (a weekly window about to
// roll over reads "3h" exactly like a 5-hour one).
func accountChip(a claudeaccount.Account, u claudeaccount.Usage, style string, labels bool, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	red := lipgloss.NewStyle().Foreground(ColorRed)

	label := ""
	if labels {
		label = dim.Render(accountShortLabel(a)) + " "
	}

	// The one state with no numbers to show, so it takes the whole chip. It
	// outranks everything below: a percentage is something to note, a dead login
	// is something to do, and unlike a spent window it does not resolve itself.
	if u.LoggedOut {
		return label + red.Render("✕ logged out")
	}

	fiveHour := u.Pct(windowFiveHour)
	sevenDay := u.Pct(windowSevenDay)
	fiveReset := resetIn(u, windowFiveHour, now)
	sevenReset := resetIn(u, windowSevenDay, now)

	// A spent window is deliberately *not* special-cased into "spent 2h" the way
	// it used to be. quotaStyle already paints anything at ExhaustedPct red — the
	// same threshold Select stops handing the account out at — and the countdown
	// is right there beside it, so "100%(2h)" carries everything the old wording
	// did without the chip changing shape when the state changes. Shape that
	// moves on its own is the thing being removed.
	if style == config.AccountUsageGrouped {
		out := label + quotaStyle(fiveHour).Render(fmt.Sprintf("%d%%", fiveHour)) +
			dim.Render("/") + quotaStyle(sevenDay).Render(fmt.Sprintf("%d%%", sevenDay))
		// All or nothing: pooled countdowns make a half group ("12%/34% (4d)")
		// unreadable — nothing says which window the 4d belongs to. The split
		// style has no such problem, since each parenthetical is attached to its
		// own figure, so there they drop independently.
		if fiveReset != "" && sevenReset != "" {
			out += dim.Render(" (") + fiveReset + dim.Render("/") + sevenReset + dim.Render(")")
		}
		return out
	}

	return label +
		windowFigure(fiveHour, fiveReset, dim) + " " +
		windowFigure(sevenDay, sevenReset, dim)
}

// windowFigure renders one window as "34%(4d)", or bare when the reset is
// unknown.
func windowFigure(pct int, reset string, dim lipgloss.Style) string {
	out := quotaStyle(pct).Render(fmt.Sprintf("%d%%", pct))
	if reset != "" {
		out += dim.Render("(") + reset + dim.Render(")")
	}
	return out
}

// resetIn renders how long until the given window refills, as "42m", "3h" or
// "5d". Relative rather than a clock time: the question is "how long do I
// wait", and an absolute time makes the reader do the subtraction.
//
// Per window, because they differ — and because a weekly figure without its
// horizon is the thing that prompted all of this. 54% of a week means nothing
// until you know whether it resets tomorrow or in six days.
func resetIn(u claudeaccount.Usage, win claudeaccount.Window, now time.Time) string {
	at := u.Reset(win)
	if at.IsZero() {
		return ""
	}
	d := at.Sub(now)
	if d <= 0 {
		return ""
	}
	switch {
	case d < time.Hour:
		return quotaResetStyle.Render(fmt.Sprintf("%dm", int(d.Minutes())+1))
	case d < 24*time.Hour:
		return quotaResetStyle.Render(fmt.Sprintf("%.0fh", d.Hours()))
	default:
		return quotaResetStyle.Render(fmt.Sprintf("%.0fd", d.Hours()/24))
	}
}

// quotaResetStyle keeps the countdown quieter than the percentage: the number
// is the headline, the horizon is the qualifier.
var quotaResetStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

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

// truncateLabel clips a label to accountLabelMax display columns.
//
// ansi.Truncate, not a rune slice: the test above measures display width, so a
// label of six double-width runes (CJK, emoji) reads as 12 columns and triggers
// truncation, while the slice holds only six elements — r[:9] then panicked and
// took the whole render loop down with it. Reachable from the account rename
// box, where a user can type anything.
func truncateLabel(s string) string {
	if lipgloss.Width(s) <= accountLabelMax {
		return s
	}
	return ansi.Truncate(s, accountLabelMax, "…")
}
