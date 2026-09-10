package ui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
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

// fleetWordmarkWidth is the wordmark's column count. Every row is exactly this
// many single-width runes, so a rune index IS a terminal column — the property
// the shine's vertical bar is built on. TestFleetWordmarkColumnsAlign pins it.
var fleetWordmarkWidth = len([]rune(fleetWordmark[0]))

var splashSpinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

const (
	splashBarCells   = 32
	splashFilledChar = "▰"
	splashEmptyChar  = "▱"
	splashLabelDwell = 48 // frames each label sticks for (~1s at 20ms)
	splashSpinnerDiv = 4  // frames per spinner glyph — holds the spinner at its
	//                       original 80ms cadence now that the tick runs at 20ms
	//                       for the shine's benefit. Keep it an exact divisor of
	//                       the tick's ratio to 80ms, or the spinner drifts.
)

// Shine sweep — a narrow vertical glint travels left→right across the wordmark,
// lifting the glyphs in the columns it passes toward white, then parks
// off-screen for splashShineRest frames before the next pass.
//
// splashShineSpeed and splashShineWidth are tuned together, not independently:
// their ratio sets how many frames each column spends ramping up and back down,
// and below ~3 the sweep starts to read as a stepping bar rather than a moving
// light. At 1.7/2.85 each column is lit across ~3.4 frames.
//
// Going faster is what drove the tick from 80ms to 20ms (see splashTick): at a
// fixed frame rate the only way to speed the sweep up is to widen the glint —
// the width is the motion blur — until it stops reading as a line and starts
// reading as a glow. Raising the rate buys speed and narrowness at once, which
// is why the glint is 5.7 columns wide here and was 8 at a third of this speed.
//
// splashShineRest is long enough that the sweep reads as a punctuated glint
// rather than a near-continuous scan — the wordmark is still for roughly as
// long as the glint takes to cross it. It is also picked to keep the cycle
// off-harmonic with the other two animations on this screen: 55.8 frames
// against the spinner's 40 and the label's 48, so ratios of 1.40 and 1.16.
// Landing near 1.00 (or 0.50) would make the sweep fire at the same spinner
// glyph or the same label every pass, which reads as a mechanism rather than a
// flourish.
//
// splashShineMax sits well under the badge's 0.55 because these are solid
// blocks: a full-cell glyph shows a white blend far harder than a thin letter,
// and there are six stacked rows of it.
//
// 0.28 is also the floor, and it is a hard one rather than a matter of taste:
// below it the accent end of the gradient downsamples to the SAME xterm-256
// index as its own base (#ff77c6 and its 0.26 lift both land on 212), so the
// shine disappears outright for anyone not on a truecolor terminal while
// looking fine to whoever tuned it. Measured against colorprofile.ANSI256 —
// re-measure before lowering this, and note the threshold moves with the
// palette's accent.
const (
	splashShineWidth = 2.85 // half-width of the glint, in columns
	splashShineMax   = 0.28 // peak blend toward white (floor — see above)
	splashShineSpeed = 1.7  // columns per frame (~0.6s per sweep at 20ms)
	splashShineRest  = 26   // frames parked between sweeps (~0.52s)
)

// splashShineCrest returns the column the glint's center sits on for frame, or
// +Inf while the sweep rests between passes (crest off-screen → every column
// renders at its base gradient color). Frame 0 parks the crest exactly
// splashShineWidth left of column 0, where the falloff contributes zero — so a
// FLEET_FREEZE_ANIM capture, which pins frame at 0, is byte-identical to the
// un-shined wordmark. Don't change the frame-0 phase without checking that.
func splashShineCrest(frame int) float64 {
	travel := (float64(fleetWordmarkWidth) + 2*splashShineWidth) / splashShineSpeed
	t := math.Mod(float64(frame), travel+splashShineRest)
	if t >= travel {
		return math.Inf(1)
	}
	return -splashShineWidth + t*splashShineSpeed
}

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
//   - frame advances every ~20ms, driving the wordmark shine; the spinner and
//     label divide it down so they keep their slower original cadence.
//
// Returns "" when width/height are non-positive so the caller can no-op
// before WindowSizeMsg lands.
func RenderSplash(width, height int, progress float64, frame int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// One crest for every row: that is what makes the glint a single vertical
	// bar rather than six independently-lit rows.
	wordmarkWidth := lipgloss.Width(fleetWordmark[0])
	crest := splashShineCrest(frame)
	gradient := make([]string, len(fleetWordmark))
	for i, row := range fleetWordmark {
		gradient[i] = gradientLine(row, crest)
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

	spinner := renderSpinnerGlyph(frame/splashSpinnerDiv, ColorPurple)
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

// renderSpinnerGlyph renders the current Braille spinner frame in the given
// color (bold). Shared by the boot splash and the shutdown overlay so the two
// spinners stay glyph- and cadence-identical.
func renderSpinnerGlyph(frame int, c color.Color) string {
	return lipgloss.NewStyle().Foreground(c).Bold(true).
		Render(splashSpinnerFrames[frame%len(splashSpinnerFrames)])
}

// renderShutdownBox builds the small "Shutting down…" box floated over the
// dimmed UI while fleet tears down. Spinner + label in the shared DialogStyle so
// it reads as part of fleet's dialog vocabulary. `frame` advances the spinner.
func renderShutdownBox(frame int) string {
	spinner := renderSpinnerGlyph(frame, ColorAccent)
	label := lipgloss.NewStyle().Foreground(ColorText).Render("Shutting down…")
	return DialogStyle.Render(spinner + "  " + label)
}

// gradientLine colors each rune of s by lerping ColorAccent → ColorPurple
// across the row, then lifts the columns near crest toward white — the
// travelling shine. Spaces pass through uncolored to keep the gap glyphs from
// burning extra escape sequences and, for free, keep the gaps between letters
// dark as the glint crosses them: the light lands on the letterforms, not as a
// scanline through the whole row. Pass math.Inf(1) for no shine.
func gradientLine(s string, crest float64) string {
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return ""
	}
	c1 := hexToRGB(colorHex(ColorAccent))
	c2 := hexToRGB(colorHex(ColorPurple))
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
		// Raised-cosine falloff, not the badge's linear one: it reaches zero
		// with zero slope, so the glint's outer columns melt into the gradient.
		// Linear would end on a visible step across six stacked rows of solid
		// blocks, and its spiked peak would make brightness jitter as the
		// fractional crest slid between cells.
		if d := math.Abs(float64(i) - crest); d <= splashShineWidth {
			lift := splashShineMax * 0.5 * (1 + math.Cos(math.Pi*d/splashShineWidth))
			col = lerpRGB(col, rgbColor{255, 255, 255}, lift)
		}
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
