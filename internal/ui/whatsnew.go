package ui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// whatsNewText is the label shown in the animated top-right badge.
const whatsNewText = "✦ What's New"

// rainbowStops is the ROYGBIV gradient for the badge — the same 7-stop treatment
// Claude Code gives the "ultrathink" keyword, but with softened, low-chroma hues
// so it reads gentle rather than neon.
var rainbowStops = []color.Color{
	lipgloss.Color("#d97a7a"), // red
	lipgloss.Color("#d99e6a"), // orange
	lipgloss.Color("#ccbf6e"), // yellow
	lipgloss.Color("#79c088"), // green
	lipgloss.Color("#6ea3d1"), // blue
	lipgloss.Color("#9089d1"), // indigo
	lipgloss.Color("#b487cc"), // violet
}

// renderWhatsNewBadge renders the animated top-right "✦ What's New" badge like
// Claude Code's "ultrathink" shimmer: a STATIC ROYGBIV rainbow across the
// letters (Lab-blended so the midpoints stay clean) with a narrow glint that
// sweeps left→right through it once, then rests before the next pass. The
// rainbow itself never moves; only the glint does. `frame` advances ~every 60ms
// while the badge is visible. Spaces pass through uncolored (matching
// gradientLine).
func renderWhatsNewBadge(frame int) string {
	runes := []rune(whatsNewText)
	n := len(runes)
	if n == 0 {
		return ""
	}
	// Static rainbow: one blended color per character position.
	cols := lipgloss.Blend1D(n, rainbowStops...)

	// Shimmer: a narrow, subtle glint sweeps across the letters, then the crest
	// parks off-screen for restFrames — the pause between loops.
	const (
		shineWidth = 1.3  // half-width of the glint in characters (narrow)
		shineMax   = 0.55 // how far toward white the glint's center pushes (subtle)
		speed      = 0.7  // characters per frame
		restFrames = 12   // pause between sweeps (~0.7s at 60ms)
	)
	travelFrames := (float64(n) + 2*shineWidth) / speed
	t := math.Mod(float64(frame), travelFrames+restFrames)
	crest := math.Inf(1) // resting → glint off-screen, badge is just the rainbow
	if t < travelFrames {
		crest = -shineWidth + t*speed
	}

	var b strings.Builder
	for i, r := range runes {
		if r == ' ' {
			b.WriteByte(' ')
			continue
		}
		col := hexToRGB(colorHex(cols[i]))
		if d := math.Abs(float64(i) - crest); d <= shineWidth {
			col = lerpRGB(col, rgbColor{255, 255, 255}, shineMax*(1-d/shineWidth))
		}
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(rgbToHex(col))).
			Bold(true).
			Render(string(r)))
	}
	// Trailing "press W" hint: "press" dimmed, the key itself styled like the
	// footer bar's keybindings (accent + bold) so it's unmistakably a keybind.
	b.WriteString(DimStyle.Render(" · press ") + HelpKeyStyle.Render("W"))
	return b.String()
}
