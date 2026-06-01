package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// 6-row block-letter wordmark "FLEET" (ANSI Shadow style).
// Letter widths vary; rows are pre-aligned so columns line up vertically.
var fleetWordmark = []string{
	"███████╗ ██╗      ███████╗ ███████╗ ████████╗",
	"██╔════╝ ██║      ██╔════╝ ██╔════╝ ╚══██╔══╝",
	"█████╗   ██║      █████╗   █████╗      ██║   ",
	"██╔══╝   ██║      ██╔══╝   ██╔══╝      ██║   ",
	"██║      ███████╗ ███████╗ ███████╗    ██║   ",
	"╚═╝      ╚══════╝ ╚══════╝ ╚══════╝    ╚═╝   ",
}

var splashSpinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

const (
	splashBarCells   = 32
	splashFilledChar = "▰"
	splashEmptyChar  = "▱"
	splashLabelDwell = 12 // spinner frames each label sticks for (~1s at 80ms)
)

// splashLabels rotate while the bootstrap runs. Ops humor — feels lived-in
// without being chatty. Order matters: first one greets the user, the rest
// roll in as time passes.
var splashLabels = []string{
	"negotiating with origin",
	"untangling git refs",
	"consulting the reflog oracle",
	"polishing worktrees",
	"alphabetizing branches",
	"sweeping stale tmux panes",
	"asking gh nicely",
	"sniffing for remotes",
	"befriending the dirty bit",
	"counting commits, slowly",
	"wrangling pinned repos",
	"rehydrating sessions",
	"warming up claude",
}

// RenderSplash paints the boot screen — gradient wordmark, spinner, progress
// bar, and a short label — centered in the given viewport.
//
//   - progress is clamped to [0,1] and drives the bar fill.
//   - frame advances every ~80ms to animate the spinner.
//
// Returns "" when width/height are non-positive so the caller can no-op
// before WindowSizeMsg lands.
func RenderSplash(width, height int, progress float64, frame int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	wordmarkWidth := lipgloss.Width(fleetWordmark[0])
	gradient := make([]string, len(fleetWordmark))
	for i, row := range fleetWordmark {
		gradient[i] = gradientLine(row)
	}

	// Progress bar — longer cell count for a more granular fill animation.
	// Rendered on its own line below the wordmark, with the spinner+label
	// centered on a third line so the bar gets the full visual width.
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	filled := min(int(math.Round(progress*float64(splashBarCells))), splashBarCells)
	barFilled := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(strings.Repeat(splashFilledChar, filled))
	barEmpty := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat(splashEmptyChar, splashBarCells-filled))
	bar := barFilled + barEmpty

	spinner := lipgloss.NewStyle().Foreground(ColorPurple).Bold(true).Render(splashSpinnerFrames[frame%len(splashSpinnerFrames)])
	label := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true).Render(splashLabels[(frame/splashLabelDwell)%len(splashLabels)])

	// Layout width = whichever is wider: the wordmark or the bar — so the
	// caller's center math wraps both on the same axis.
	blockWidth := max(wordmarkWidth, lipgloss.Width(bar))

	var lines []string
	lines = append(lines, gradient...)
	lines = append(lines, "")
	lines = append(lines, centerWithin(bar, blockWidth))
	lines = append(lines, centerWithin(spinner+"  "+label, blockWidth))

	topPad := max((height-len(lines))/2, 0)
	leftPad := max((width-blockWidth)/2, 0)

	var out strings.Builder
	for range topPad {
		out.WriteString("\n")
	}
	pad := strings.Repeat(" ", leftPad)
	for i, l := range lines {
		out.WriteString(pad)
		out.WriteString(l)
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// gradientLine colors each rune of s by lerping ColorAccent → ColorPurple
// across the row. Spaces pass through uncolored to keep the gap glyphs from
// burning extra escape sequences.
func gradientLine(s string) string {
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return ""
	}
	c1 := hexToRGB(string(ColorAccent))
	c2 := hexToRGB(string(ColorPurple))
	var b strings.Builder
	denom := float64(n - 1)
	if denom == 0 {
		denom = 1
	}
	for i, r := range runes {
		if r == ' ' {
			b.WriteRune(' ')
			continue
		}
		t := float64(i) / denom
		col := lerpRGB(c1, c2, t)
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(rgbToHex(col))).Render(string(r)))
	}
	return b.String()
}

type rgbColor struct{ r, g, b int }

func hexToRGB(s string) rgbColor {
	var rr, gg, bb int
	n, _ := fmt.Sscanf(s, "#%2x%2x%2x", &rr, &gg, &bb)
	if n != 3 {
		return rgbColor{255, 255, 255}
	}
	return rgbColor{rr, gg, bb}
}

func rgbToHex(c rgbColor) string {
	return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b)
}

func lerpRGB(a, b rgbColor, t float64) rgbColor {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return rgbColor{
		r: a.r + int(math.Round(float64(b.r-a.r)*t)),
		g: a.g + int(math.Round(float64(b.g-a.g)*t)),
		b: a.b + int(math.Round(float64(b.b-a.b)*t)),
	}
}

// centerWithin left-pads s with spaces so its visible width sits centered in
// a field of width w. Returns s unchanged if it's already wider.
func centerWithin(s string, w int) string {
	visW := lipgloss.Width(s)
	if visW >= w {
		return s
	}
	return strings.Repeat(" ", (w-visW)/2) + s
}
