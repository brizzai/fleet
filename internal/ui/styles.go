package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// Initial colors match the default Fleet Pink palette. ApplyPalette reassigns
// these when a theme is loaded, so a fresh fleet without a configured theme
// still renders in the flagship pink.
var (
	ColorBg      = lipgloss.Color("#16121f")
	ColorSurface = lipgloss.Color("#231a2e")
	ColorBorder  = lipgloss.Color("#5e4d6e")
	ColorText    = lipgloss.Color("#f3e9f0")
	ColorTextDim = lipgloss.Color("#7a6a80")
	ColorAccent  = lipgloss.Color("#e670b6")
	ColorGreen   = lipgloss.Color("#88e090")
	ColorYellow  = lipgloss.Color("#f3c98b")
	ColorBlue    = lipgloss.Color("#7ab8f5")
	ColorRed     = lipgloss.Color("#ff7088")
	ColorGray    = lipgloss.Color("#7a6a80")
	ColorWhite   = lipgloss.Color("#f3e9f0")
	ColorBrand   = lipgloss.Color("#e670b6") // fleet pink — theme-independent brand mark
	ColorOrange  = lipgloss.Color("#ff9d6e")
	ColorPurple  = lipgloss.Color("#c293f5")
)

// Pre-allocated styles.
var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorAccent)

	RepoHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorAccent)

	SessionItemStyle = lipgloss.NewStyle().
				Foreground(ColorText)

	SessionSelectedStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	PreviewHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorText)

	PreviewContentStyle = lipgloss.NewStyle().
				Foreground(ColorTextDim)

	HelpBarStyle = lipgloss.NewStyle().
			Foreground(ColorTextDim)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorRed)

	DimStyle = lipgloss.NewStyle().
			Foreground(ColorTextDim)

	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder)

	DialogStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(1, 2)

	StatusRunningStyle  = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	StatusWaitingStyle  = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	StatusFinishedStyle = lipgloss.NewStyle().Foreground(ColorBlue).Bold(true)
	StatusIdleStyle     = lipgloss.NewStyle().Foreground(ColorGray)
	StatusErrorStyle    = lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	StatusStartingStyle = lipgloss.NewStyle().Foreground(ColorAccent)

	// Tool badge style.
	ToolClaudeStyle = lipgloss.NewStyle().Foreground(ColorOrange)

	// Selection styles (inverted).
	SessionSelectionPrefix = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	SessionTitleSelStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	SessionStatusSelStyle  = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	TreeConnectorSelStyle  = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	ToolBadgeSelStyle      = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	// Panel title style (cyan/blue like agent-deck).
	PanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBlue)

	// Header bar style.
	HeaderBarStyle = lipgloss.NewStyle().Background(ColorSurface).Padding(0, 1)

	// Help bar key pill style (inverted accent).
	HelpKeyStyle = lipgloss.NewStyle().
			Background(ColorAccent).
			Foreground(ColorBg).
			Bold(true).
			Padding(0, 1)

	HelpDescStyle = lipgloss.NewStyle().Foreground(ColorText)

	HelpSepStyle = lipgloss.NewStyle().Foreground(ColorBorder)

	// Git info styles.
	BranchStyle      = lipgloss.NewStyle().Foreground(ColorBlue)
	DirtyStyle       = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	PROpenStyle    = lipgloss.NewStyle().Foreground(ColorGreen)
	PRFailStyle    = lipgloss.NewStyle().Foreground(ColorRed)
	PRPendingStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	PRMergedStyle  = lipgloss.NewStyle().Foreground(ColorPurple)

	// Slot badge style (RTS-style quick-access hotkey).
	SlotBadgeStyle = lipgloss.NewStyle().Foreground(ColorOrange).Bold(true)

	// Dim variant of the slot badge — used in the clean-tree sidebar where
	// the bright orange would fight the calm row layout.
	SlotBadgeDimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
)

// ApplyPalette reassigns all color vars and rebuilds all style vars from the given palette.
// Must be called on the main goroutine (Bubble Tea Update/View).
func ApplyPalette(p Palette) {
	// 1. Reassign color vars.
	ColorBg = p.Bg
	ColorSurface = p.Surface
	ColorBorder = p.Border
	ColorText = p.Text
	ColorTextDim = p.TextDim
	ColorAccent = p.Accent
	ColorGreen = p.Green
	ColorYellow = p.Yellow
	ColorBlue = p.Blue
	ColorRed = p.Red
	ColorGray = p.Gray
	ColorWhite = p.Text
	ColorOrange = p.Orange
	ColorPurple = p.Purple

	// 2. Rebuild all styles (lipgloss copies colors by value at construction).
	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	RepoHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	SessionItemStyle = lipgloss.NewStyle().Foreground(ColorText)
	SessionSelectedStyle = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	PreviewHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	PreviewContentStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
	HelpBarStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
	ErrorStyle = lipgloss.NewStyle().Foreground(ColorRed)
	DimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
	PanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorBorder)
	DialogStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorAccent).Padding(1, 2)

	StatusRunningStyle = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	StatusWaitingStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	StatusFinishedStyle = lipgloss.NewStyle().Foreground(ColorBlue).Bold(true)
	StatusIdleStyle = lipgloss.NewStyle().Foreground(ColorGray)
	StatusErrorStyle = lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	StatusStartingStyle = lipgloss.NewStyle().Foreground(ColorAccent)

	ToolClaudeStyle = lipgloss.NewStyle().Foreground(ColorOrange)

	SessionSelectionPrefix = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	SessionTitleSelStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	SessionStatusSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	TreeConnectorSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	ToolBadgeSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	PanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBlue)
	HeaderBarStyle = lipgloss.NewStyle().Background(ColorSurface).Padding(0, 1)

	HelpKeyStyle = lipgloss.NewStyle().Background(ColorAccent).Foreground(ColorBg).Bold(true).Padding(0, 1)
	HelpDescStyle = lipgloss.NewStyle().Foreground(ColorText)
	HelpSepStyle = lipgloss.NewStyle().Foreground(ColorBorder)

	BranchStyle = lipgloss.NewStyle().Foreground(ColorBlue)
	DirtyStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	PROpenStyle = lipgloss.NewStyle().Foreground(ColorGreen)
	PRFailStyle = lipgloss.NewStyle().Foreground(ColorRed)
	PRPendingStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	PRMergedStyle = lipgloss.NewStyle().Foreground(ColorPurple)

	SlotBadgeStyle = lipgloss.NewStyle().Foreground(ColorOrange).Bold(true)
	SlotBadgeDimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
}

// RenderBorderedPanel wraps content in a rounded border with a title inset
// into the top border. Output is exactly width × height.
//
//	╭─ Sessions ────────╮
//	│ ...content...     │
//	╰──── 2 RUN · 1 WAIT ╯  (bottomRight optional)
//
// When accent is true, the border is drawn in the accent color (used for the
// focused panel in dual mode); otherwise it uses the muted border color.
// Pass bottomRight via RenderBorderedPanelFooter for the variant with a
// status pill embedded in the bottom border.
func RenderBorderedPanel(content, title string, width, height int, accent bool) string {
	return RenderBorderedPanelFooter(content, title, "", width, height, accent)
}

// RenderBorderedPanelFooter is like RenderBorderedPanel but also embeds a
// right-aligned status pill into the bottom border. Empty footer renders an
// unbroken bottom border.
func RenderBorderedPanelFooter(content, title, footerRight string, width, height int, accent bool) string {
	if width < 6 || height < 3 {
		return content
	}

	borderColor := ColorBorder
	if accent {
		borderColor = ColorAccent
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	// Top border with title inset: ╭─ title ─...─╮
	titleText := titleStyle.Render(title)
	titleWidth := lipgloss.Width(titleText)
	// Layout: ╭ ─ ' ' title ' ' ─*K ╮ — leading dash count is 1, K fills the rest.
	fillRight := width - 2 /*corners*/ - 1 /*leading dash*/ - 1 /*space*/ - titleWidth - 1 /*space*/
	if fillRight < 1 {
		// Title too wide; truncate it so the border still closes.
		maxTitle := max(width-6, 1)
		titleText = titleStyle.Render(ansiTruncate(title, maxTitle))
		titleWidth = lipgloss.Width(titleText)
		fillRight = max(width-5-titleWidth, 1)
	}
	top := borderStyle.Render("╭─ ") + titleText + borderStyle.Render(" "+strings.Repeat("─", fillRight)+"╮")

	// Bottom border, optionally with a right-aligned status pill inset.
	var bottom string
	if footerRight != "" {
		footerStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		footerText := footerStyle.Render(footerRight)
		footerW := lipgloss.Width(footerText)
		// Layout: ╰─*K ' ' footer ' '─╯ — trailing dash count is 1.
		fillLeft := width - 2 /*corners*/ - 1 /*space*/ - footerW - 1 /*space*/ - 1 /*trailing dash*/
		if fillLeft < 1 {
			// Footer too wide; truncate.
			maxFooter := max(width-6, 1)
			footerText = footerStyle.Render(ansiTruncate(footerRight, maxFooter))
			footerW = lipgloss.Width(footerText)
			fillLeft = max(width-5-footerW, 1)
		}
		bottom = borderStyle.Render("╰"+strings.Repeat("─", fillLeft)+" ") + footerText + borderStyle.Render(" ─╯")
	} else {
		bottom = borderStyle.Render("╰" + strings.Repeat("─", width-2) + "╯")
	}

	innerWidth := width - 2
	innerHeight := height - 2
	contentLines := strings.Split(content, "\n")

	side := borderStyle.Render("│")
	middle := make([]string, innerHeight)
	for i := range innerHeight {
		line := ""
		if i < len(contentLines) {
			line = contentLines[i]
		}
		lineW := lipgloss.Width(line)
		if lineW > innerWidth {
			line = ansiTruncate(line, innerWidth)
		} else if lineW < innerWidth {
			line += strings.Repeat(" ", innerWidth-lineW)
		}
		middle[i] = side + line + side
	}

	return top + "\n" + strings.Join(middle, "\n") + "\n" + bottom
}

// dimBackdrop dims rendered content by wrapping each line in the SGR Faint
// escape (CSI 2 m) so an overlay can sit above it with visible elevation.
// Inner ANSI sequences keep their hues; the faint layer only reduces overall
// intensity — most terminals render this as a subtle wash that's perfect as
// a modal backdrop without the cost of a full re-render.
func dimBackdrop(s string) string {
	const (
		faint = "\x1b[2m"
		reset = "\x1b[22m"
	)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = faint + line + reset
	}
	return strings.Join(lines, "\n")
}

// ansiTruncate is a thin wrapper around lipgloss/ANSI-aware truncation that
// works on strings with embedded escape sequences (status colors, accents).
func ansiTruncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	// lipgloss exposes a helper via the underlying rendering — we re-use
	// a simple width-aware loop here to avoid a new dependency.
	if lipgloss.Width(s) <= w {
		return s
	}
	// Fallback: drop runes from the end until we're within budget. Loses
	// trailing styling but is correct for visible width.
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > w {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String()
}

// RenderPanelTitle renders a panel title with a divider underline.
func RenderPanelTitle(title string, width int) string {
	titleLine := PanelTitleStyle.Render(title)
	if width < 1 {
		width = 1
	}
	underline := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", width))
	return titleLine + "\n" + underline
}

// RenderFocusedPanelTitle renders a panel title with accent-colored underline (for focus mode).
func RenderFocusedPanelTitle(title string, width int) string {
	titleLine := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(title)
	if width < 1 {
		width = 1
	}
	underline := lipgloss.NewStyle().Foreground(ColorAccent).Render(strings.Repeat("─", width))
	return titleLine + "\n" + underline
}

// StatusIndicatorMode controls how StatusSymbol renders session state in the
// sidebar. "icon" (default) uses semantic circles (●◐✕); "bar" uses a single
// colored vertical bar in the leftmost column, VS Code gutter style. Idle is
// always blank in either mode — only non-idle states get a glyph.
var StatusIndicatorMode = "icon"

const StatusBarChar = "┃"

// StatusSymbol returns a styled status indicator.
func StatusSymbol(status session.Status) string {
	raw := StatusSymbolRaw(status)
	if raw == " " {
		return " "
	}
	return StatusStyle(status).Render(raw)
}

// StatusSymbolRaw returns the raw character for a status, respecting the
// configured indicator mode. Icon mode renders a dim `·` for idle so every
// row has a leftmost anchor for the eye; bar mode keeps idle blank because
// the gutter is the signal there.
func StatusSymbolRaw(status session.Status) string {
	if StatusIndicatorMode == "bar" {
		switch status {
		case session.StatusIdle, session.StatusStarting:
			return " "
		default:
			return StatusBarChar
		}
	}
	switch status {
	case session.StatusRunning, session.StatusFinished:
		return "●"
	case session.StatusWaiting:
		return "◐"
	case session.StatusError:
		return "✕"
	case session.StatusIdle, session.StatusStarting:
		return "·"
	default:
		return " "
	}
}

// StatusStyle returns the lipgloss style for a given status.
func StatusStyle(status session.Status) lipgloss.Style {
	switch status {
	case session.StatusRunning:
		return StatusRunningStyle
	case session.StatusWaiting:
		return StatusWaitingStyle
	case session.StatusFinished:
		return StatusFinishedStyle
	case session.StatusIdle:
		return StatusIdleStyle
	case session.StatusError:
		return StatusErrorStyle
	case session.StatusStarting:
		return StatusStartingStyle
	default:
		return StatusIdleStyle
	}
}

// TitleStyleForStatus returns the appropriate title style based on session status.
func TitleStyleForStatus(status session.Status) lipgloss.Style {
	switch status {
	case session.StatusRunning, session.StatusWaiting:
		return lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	case session.StatusError:
		return lipgloss.NewStyle().Foreground(ColorText).Underline(true)
	default:
		return lipgloss.NewStyle().Foreground(ColorText)
	}
}

// StatusLabel returns a styled status text.
func StatusLabel(status session.Status) string {
	switch status {
	case session.StatusRunning:
		return StatusRunningStyle.Render("running")
	case session.StatusWaiting:
		return StatusWaitingStyle.Render("waiting")
	case session.StatusFinished:
		return StatusFinishedStyle.Render("finished")
	case session.StatusIdle:
		return StatusIdleStyle.Render("idle")
	case session.StatusError:
		return StatusErrorStyle.Render("error")
	case session.StatusStarting:
		return StatusStartingStyle.Render("starting")
	default:
		return string(status)
	}
}
