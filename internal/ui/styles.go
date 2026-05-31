package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// Tokyo Night dark theme colors.
var (
	ColorBg      = lipgloss.Color("#1a1b26")
	ColorSurface = lipgloss.Color("#24283b")
	ColorBorder  = lipgloss.Color("#414868")
	ColorText    = lipgloss.Color("#c0caf5")
	ColorTextDim = lipgloss.Color("#565f89")
	ColorAccent  = lipgloss.Color("#7aa2f7")
	ColorGreen   = lipgloss.Color("#9ece6a")
	ColorYellow  = lipgloss.Color("#e0af68")
	ColorBlue    = lipgloss.Color("#7dcfff")
	ColorRed     = lipgloss.Color("#f7768e")
	ColorGray    = lipgloss.Color("#565f89")
	ColorWhite   = lipgloss.Color("#c0caf5")
	ColorBrand   = lipgloss.Color("#F48FB1") // fleet pink — theme-independent
	ColorOrange  = lipgloss.Color("#ff9e64")
	ColorPurple  = lipgloss.Color("#bb9af7")
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
	BranchStyle    = lipgloss.NewStyle().Foreground(ColorBlue)
	DirtyStyle     = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	PROpenStyle    = lipgloss.NewStyle().Foreground(ColorGreen)
	PRFailStyle    = lipgloss.NewStyle().Foreground(ColorRed)
	PRPendingStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	PRMergedStyle  = lipgloss.NewStyle().Foreground(ColorPurple)

	// Slot badge style (RTS-style quick-access hotkey).
	SlotBadgeStyle = lipgloss.NewStyle().Foreground(ColorOrange).Bold(true)

	// Dim variant of the slot badge — used in the clean-tree sidebar where
	// the bright orange would fight the calm row layout.
	SlotBadgeDimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

	// Single-glyph "│" guide line drawn down the left edge of each
	// checkout's sessions in the clean-tree sidebar.
	BorderGuideStyle = lipgloss.NewStyle().Foreground(ColorBorder)
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
	BorderGuideStyle = lipgloss.NewStyle().Foreground(ColorBorder)
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

// StatusSymbol returns a styled status indicator.
func StatusSymbol(status session.Status) string {
	switch status {
	case session.StatusRunning:
		return StatusRunningStyle.Render("●")
	case session.StatusWaiting:
		return StatusWaitingStyle.Render("◐")
	case session.StatusFinished:
		return StatusFinishedStyle.Render("●")
	case session.StatusIdle:
		return StatusIdleStyle.Render("○")
	case session.StatusError:
		return StatusErrorStyle.Render("✕")
	case session.StatusStarting:
		return StatusStartingStyle.Render("○")
	default:
		return StatusIdleStyle.Render("○")
	}
}

// StatusSymbolRaw returns the raw icon character for a status (no styling).
func StatusSymbolRaw(status session.Status) string {
	switch status {
	case session.StatusRunning:
		return "●"
	case session.StatusWaiting:
		return "◐"
	case session.StatusFinished:
		return "●"
	case session.StatusIdle:
		return "○"
	case session.StatusError:
		return "✕"
	case session.StatusStarting:
		return "○"
	default:
		return "○"
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
