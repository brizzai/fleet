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
// is named as a word so you always know which you are looking at.
//
// The phase comes from the wall clock, not a ticker. The header re-renders on
// the existing UI tick regardless, so deriving it from `now` costs no new
// state, no goroutine, and no timer that could outlive what it animates.
const (
	// accountBarCells is the width of the mini gauge. Short on purpose: this
	// shares the header row with the What's New badge and must never crowd it.
	accountBarCells = 6
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

// name is the window as a word. Deliberately not "5h"/"7d": those are bare
// durations, and they sat beside the per-account countdowns meaning something
// entirely different.
func (w quotaWindow) name() string {
	if w == windowFiveHour {
		return "5-hour"
	}
	return "weekly"
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

// density is one way of rendering the readout, from richest to tightest.
//
// Fixed width thresholds were the obvious approach and are the wrong one: the
// strip's width depends on how many accounts there are and how long their names
// happen to be, so any threshold is right for one setup and wrong for the next.
// Instead every density is rendered and the first that fits is used — the strip
// is always as informative as the space genuinely allows, and adding a third
// account degrades it gracefully instead of overflowing.
type density struct {
	labels    bool // account names
	bar       int  // gauge cells; 0 drops the gauge entirely
	resetWord bool // spell out "resets" rather than leaning on the separator
	gap       bool // space between the percentage and the countdown
}

// densities run widest to narrowest. Each step gives up the least useful thing
// left: the spelled-out word first (the window is already named, so a bare
// duration can only be a countdown), then gauge precision, then the gauge, then
// the names — which go last because they are the only thing position cannot
// convey once there are more than two accounts.
var densities = []density{
	{labels: true, bar: accountBarCells, resetWord: true, gap: true},
	{labels: true, bar: accountBarCells, gap: true},
	{labels: true, bar: 4, gap: true},
	{labels: true, bar: 4},
	{labels: true},
	{},
}

// renderAccountUsageHeader draws a compact per-account quota readout, or "" when
// there is nothing worth showing.
//
// Returns empty for a single account: with one subscription there is no choice
// being made, so the number is trivia rather than information, and the header
// row is better spent on nothing.
//
// budget is the columns available before the What's New badge; nothing wider
// than it is ever returned.
func renderAccountUsageHeader(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, budget int, now time.Time) string {
	if len(accounts) < 2 {
		return ""
	}

	win := windowAt(now)
	shown := make([]claudeaccount.Account, 0, len(accounts))
	for _, a := range accounts {
		u := usage[a.Email]
		// A logged-out account shows even with no reading to its name — it is
		// the one state here that is not trivia. Skipping it (which the Known
		// check would do, since an account with no login never returns numbers)
		// leaves the corner looking like a healthy single-account setup while
		// half the rotation is dead.
		if !u.LoggedOut && !u.Known() {
			continue
		}
		shown = append(shown, a)
	}
	if len(shown) == 0 {
		return ""
	}

	for _, d := range densities {
		if out := renderReadout(shown, usage, win, d, now); lipgloss.Width(out) <= budget {
			return out
		}
	}
	// Even the tightest form overflows: better nothing than a strip that pushes
	// the What's New badge off its corner.
	return ""
}

// renderReadout lays the whole strip out at one density.
func renderReadout(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, win quotaWindow, d density, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	sep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" │ ")

	chips := make([]string, 0, len(accounts))
	for _, a := range accounts {
		chips = append(chips, accountChip(a, usage[a.Email], win, d, now))
	}

	// The window is named once, in front, and as a word.
	//
	// It used to trail the strip as "7d", which put a bare duration next to the
	// per-account countdowns — identical shape, opposite meaning ("resets in 5
	// days" beside "these are the 7-day figures"). A word cannot be read as a
	// countdown, and leading it makes it a heading over both accounts rather
	// than something dangling off the last one.
	return dim.Render(win.name()) + sep + strings.Join(chips, sep)
}

// accountChip renders one account at the given density.
func accountChip(a claudeaccount.Account, u claudeaccount.Usage, win quotaWindow, d density, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	red := lipgloss.NewStyle().Foreground(ColorRed)

	label := ""
	if d.labels {
		label = dim.Render(accountShortLabel(a)) + " "
	}

	// Logged out outranks spent, which outranks the percentage. Both displace
	// the number rather than waiting their turn — a percentage is something to
	// note, these are something to do. The wording distinguishes them because
	// the actions differ: "spent" is a wait that resolves itself, "logged out"
	// needs you to sign in again.
	if u.LoggedOut {
		return label + red.Render("✕ logged out")
	}
	if u.Exhausted(now) {
		// Plain space, not the fused separator: there is no percentage here for
		// the countdown to be joined to, so a dot would just be noise.
		return label + red.Render("spent ") + resetIn(u, windowFiveHour, now)
	}

	pct := win.pct(u)
	out := label
	if d.bar > 0 {
		out += renderQuotaBar(pct, d.bar) + " "
	}
	out += quotaStyle(pct).Render(fmt.Sprintf("%d%%", pct))

	// The countdown is what makes the percentage actionable: 72% with an hour
	// left is fine, 72% with four hours left is not.
	if reset := resetIn(u, win, now); reset != "" {
		if d.resetWord {
			out += dim.Render(" resets ") + reset
		} else {
			out += dim.Render(d.join()) + reset
		}
	}
	return out
}

// join is the separator between a percentage and its countdown: fused at tight
// densities so the pair reads as one fact ("40% used, 2h left") rather than two
// competing numbers.
func (d density) join() string {
	if d.gap {
		return " ·"
	}
	return "·"
}

// renderQuotaBar draws a filled/empty gauge coloured by headroom. The half-block
// glyphs are from the same Geometric Shapes range as fleet's status dots, so
// they stay aligned in the base monospace fonts the sidebar already targets.
func renderQuotaBar(pct, cells int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * cells / 100
	// Any nonzero usage should show at least one cell, or a busy account reads
	// as untouched.
	if filled == 0 && pct > 0 {
		filled = 1
	}
	full := quotaStyle(pct).Render(strings.Repeat("▰", filled))
	rest := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("▱", cells-filled))
	return full + rest
}

// resetIn renders how long until the shown window refills, as "42m", "3h" or
// "5d". Relative rather than a clock time: the question is "how long do I
// wait", and an absolute time makes the reader do the subtraction.
//
// Per window, because they differ — and because a weekly figure without its
// horizon is the thing that prompted all of this. 54% of a week means nothing
// until you know whether it resets tomorrow or in six days.
func resetIn(u claudeaccount.Usage, win quotaWindow, now time.Time) string {
	at := u.FiveHourReset
	if win == windowSevenDay {
		at = u.SevenDayReset
	}
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

func truncateLabel(s string) string {
	if lipgloss.Width(s) <= accountLabelMax {
		return s
	}
	r := []rune(s)
	return string(r[:accountLabelMax-1]) + "…"
}
