package ui

import (
	"math"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/stats"
)

// statsCopyMsg carries a recap card as plain text for the wiring layer to put
// on the clipboard through fleet's existing copy path. Redacted says repo
// names were left out, so the toast can say how to include them.
type statsCopyMsg struct {
	Text     string
	Redacted bool
}

// statsRecapReposMsg reports the reel's `r` toggle: whether copies now include
// repo names.
type statsRecapReposMsg struct{ On bool }

// statsBadgeText is the header badge announcing an unseen weekly recap.
const statsBadgeText = "◷ Your week"

// renderStatsBadge renders the static "◷ Your week" header badge. Foreground
// only — a badge is not a cursor, so it spends no fill — and no animation loop:
// it is the one push surface Stats has, and it should not twitch for attention.
func renderStatsBadge() string {
	return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◷") +
		lipgloss.NewStyle().Foreground(ColorText).Bold(true).Render(strings.TrimPrefix(statsBadgeText, "◷"))
}

// RecapView is the weekly "your week in fleet" reel: a centred overlay of
// cards paged by hand (never auto-advancing), each copyable as text.
//
// View returns the bare, fixed-size box; the caller centres it with overlayAt
// over dimBackdrop(base), like the command palette.
type RecapView struct {
	visible       bool
	width, height int
	recap         stats.Recap
	page          int
	// withRepos puts repo names on copied cards. Off by default and never
	// persisted: repo and worktree names often carry client or ticket names,
	// and a card is copied to be shared.
	withRepos bool
}

func NewRecapView() *RecapView { return &RecapView{} }

// Show opens the reel on its first card.
func (v *RecapView) Show(r stats.Recap) {
	v.recap = r
	v.page = 0
	v.visible = true
}

func (v *RecapView) Hide()            { v.visible = false }
func (v *RecapView) IsVisible() bool  { return v.visible }
func (v *RecapView) SetSize(w, h int) { v.width, v.height = w, h }

// Page is the index of the card on screen.
func (v *RecapView) Page() int { return v.page }

// Update pages (←→, h/l, tab), copies (c), toggles repo names on copies (r)
// and closes (esc/q). Enter advances, and closes from the last card.
func (v *RecapView) Update(msg tea.Msg) (*RecapView, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}
	n := len(v.recap.Cards)
	switch key.String() {
	case "esc", "q":
		v.Hide()
	case "left", "h", "shift+tab":
		v.page = max(v.page-1, 0)
	case "right", "l", "tab", "space":
		v.page = min(v.page+1, max(n-1, 0))
	case "enter":
		if v.page >= n-1 {
			v.Hide()
		} else {
			v.page++
		}
	case "c":
		if v.page < n {
			c := v.recap.Cards[v.page]
			text := recapCardText(c, v.withRepos)
			redacted := !v.withRepos && len(c.RepoLines) > 0
			return v, func() tea.Msg { return statsCopyMsg{Text: text, Redacted: redacted} }
		}
	case "r":
		v.withRepos = !v.withRepos
		on := v.withRepos
		return v, func() tea.Msg { return statsRecapReposMsg{On: on} }
	}
	return v, nil
}

// recapBoxSize is the reel's fixed frame: paging must never resize the box.
func (v *RecapView) recapBoxSize() (int, int) {
	w := clampInt(60, 24, max(v.width-4, 24))
	h := clampInt(18, 10, max(v.height-2, 10))
	return w, h
}

// View renders the reel's box (bare — the caller composites it).
func (v *RecapView) View() string {
	bw, bh := v.recapBoxSize()
	innerW, innerH := bw-2, bh-2
	n := len(v.recap.Cards)

	var body []string
	if n == 0 {
		body = []string{"", DimStyle.Render("Not enough happened last week"), DimStyle.Render("for a recap.")}
	} else {
		body = recapCardLines(v.recap.Cards[v.page], innerW-4)
	}
	// Cards differ in height; each sits at the box's vertical middle, which
	// never moves because the box itself is fixed.
	if len(body) > innerH {
		body = body[:innerH]
	}
	lines := make([]string, 0, innerH)
	for range (innerH - len(body)) / 2 {
		lines = append(lines, "")
	}
	for _, l := range body {
		lines = append(lines, recapCenter(l, innerW))
	}

	right := ""
	if n > 0 {
		right = strconv.Itoa(v.page+1) + " / " + strconv.Itoa(n)
	}
	hints := "←→ page · c copy · esc close"
	if n > 0 && len(v.recap.Cards[v.page].RepoLines) > 0 {
		hints = "←→ page · c copy · r repos · esc close"
	}
	week := ""
	if !v.recap.WeekStart.IsZero() {
		ws := v.recap.WeekStart.Local()
		we := ws.AddDate(0, 0, 6)
		week = ws.Format("Jan 2") + " – " + we.Format("Jan 2")
		if ws.Month() == we.Month() {
			week = ws.Format("Jan 2") + "–" + we.Format("2")
		}
	}
	if lipgloss.Width(hints)+lipgloss.Width(week)+8 > bw {
		week = ""
	}
	return RenderBorderedPanelInsets(strings.Join(lines, "\n"), "Your week", right, hints, week, bw, bh, true)
}

// recapCardLines lays out one card: the figure (three rows tall in the big
// accent digits when it fits, units dim), its caption, supporting lines, and
// a block sparkline in the chart ramp. The figure is the card: a one-cell
// number above a longer caption let the caption outweigh it.
func recapCardLines(c stats.RecapCard, w int) []string {
	out := []string{""}
	if big, ok := statsBigFigure(c.Big); ok && lipgloss.Width(big[0]) <= w {
		out = append(out, big...)
		out = append(out, "")
	} else {
		out = append(out, ansi.Truncate(statsFigure(c.Big), w, "…"))
	}
	if c.Caption != "" {
		out = append(out, lipgloss.NewStyle().Foreground(ColorText).Render(ansi.Truncate(c.Caption, w, "…")))
	}
	if len(c.Lines) > 0 {
		out = append(out, "")
		for _, l := range c.Lines {
			out = append(out, DimStyle.Render(ansi.Truncate(l, w, "…")))
		}
	}
	if len(c.Spark) > 0 {
		maxV := 0.0
		for _, s := range c.Spark {
			maxV = max(maxV, s)
		}
		if maxV > 0 {
			// Each sample gets an equal run of columns, so a 7-day spark reads
			// as seven days rather than one bar per stretched pixel.
			sw := min(w, 32) / len(c.Spark) * len(c.Spark)
			if sw == 0 {
				sw = min(w, 32)
			}
			out = append(out, "")
			out = append(out, statsBlockChart(c.Spark, sw, 3, maxV, nil, func(v float64) int {
				return 1 + int(math.Round(v/maxV*float64(statsRampN-2)))
			})...)
		}
	}
	return out
}

// recapCenter centres a (styled) line in w columns.
func recapCenter(s string, w int) string {
	s = ansi.Truncate(s, w, "")
	return strings.Repeat(" ", max((w-lipgloss.Width(s))/2, 0)) + s
}

// recapCardText is a card as plain text, for the clipboard. Lines naming repos
// are left out unless withRepos.
func recapCardText(c stats.RecapCard, withRepos bool) string {
	var lines []string
	if head := strings.TrimSpace(c.Big + " " + c.Caption); head != "" {
		lines = append(lines, head)
	}
	for i, l := range c.Lines {
		if !withRepos && slices.Contains(c.RepoLines, i) {
			continue
		}
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	lines = append(lines, "— my week in fleet")
	return strings.Join(lines, "\n")
}
