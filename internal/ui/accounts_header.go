package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
)

// The header readout shows the **weekly** window rather than the 5-hour one.
// The 5-hour bucket is what actually gates the next message, but it swings all
// day and would make the header twitch; the weekly figure is the one worth
// glancing at. The 5-hour window still gets its say — an account whose 5-hour
// bucket is spent flips its chip to a reset countdown, so the readout is quiet
// until the number you can't act on is the one that matters.
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
)

// renderAccountUsageHeader draws a compact per-account weekly-quota readout, or
// "" when there is nothing worth showing.
//
// Returns empty for a single account: with one subscription there is no choice
// being made, so the number is trivia rather than information, and the header
// row is better spent on nothing.
func renderAccountUsageHeader(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, width int, now time.Time) string {
	if len(accounts) < 2 || width < accountHeaderMinWidth {
		return ""
	}

	chips := make([]string, 0, len(accounts))
	for _, a := range accounts {
		u, ok := usage[a.Email]
		if !ok || !u.Known() {
			continue
		}
		chips = append(chips, accountChip(a, u, width, now))
	}
	if len(chips) == 0 {
		return ""
	}

	sep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" │ ")
	return strings.Join(chips, sep)
}

// accountChip renders one account as "label ▰▰▱▱▱▱ 34%", or as a reset
// countdown when its 5-hour window is spent.
func accountChip(a claudeaccount.Account, u claudeaccount.Usage, width int, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)

	label := ""
	if width >= accountHeaderCompactWidth {
		label = dim.Render(accountShortLabel(a)) + " "
	}

	// A spent 5-hour bucket is the one thing here you can act on, so it
	// displaces the weekly figure entirely rather than sitting beside it.
	if u.Exhausted(now) {
		return label + lipgloss.NewStyle().Foreground(ColorRed).Render("spent "+resetIn(u, now))
	}

	pct := u.SevenDayPct
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
