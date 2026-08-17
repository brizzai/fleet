package ui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// colorHex renders a color.Color as a "#rrggbb" string for the consumers that
// still need a hex string rather than a color.Color: the tmux status bar (which
// takes string colors) and the splash gradient math. Lip Gloss v2 colors are
// color.Color values, so we derive the hex from their RGBA components.
func colorHex(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// Initial colors match the default Fleet Pink palette. ApplyPalette reassigns
// these when a theme is loaded, so a fresh fleet without a configured theme
// still renders in the flagship pink.
var (
	ColorBg      = lipgloss.Color("#16121f")
	ColorSurface = lipgloss.Color("#231a2e")
	ColorBorder  = lipgloss.Color("#5e4d6e")
	ColorText    = lipgloss.Color("#f3e9f0")
	ColorTextDim = lipgloss.Color("#807888")
	ColorAccent  = lipgloss.Color("#dc88c0")
	ColorGreen   = lipgloss.Color("#8ad698")
	ColorYellow  = lipgloss.Color("#e8c590")
	ColorBlue    = lipgloss.Color("#9aa8e0")
	ColorRed     = lipgloss.Color("#e07685")
	ColorGray    = lipgloss.Color("#807888")
	ColorWhite   = lipgloss.Color("#f3e9f0")
	ColorBrand   = lipgloss.Color("#dc88c0") // fleet pink — theme-independent brand mark
	ColorOrange  = lipgloss.Color("#e8a880")
	ColorPurple  = lipgloss.Color("#a08dd6")
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

	// AgentGlyphStyle is the muted tone for the per-session agent sigil (✻/⬡):
	// quiet, monochrome, theme-safe — identity is carried by shape, not color,
	// so the status dot keeps sole ownership of the status color.
	AgentGlyphStyle = lipgloss.NewStyle().
			Foreground(ColorTextDim)

	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder)

	DialogStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(1, 2)

	StatusRunningStyle   = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	StatusWaitingStyle   = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	StatusFinishedStyle  = lipgloss.NewStyle().Foreground(ColorBlue).Bold(true)
	StatusIdleStyle      = lipgloss.NewStyle().Foreground(ColorGray)
	StatusErrorStyle     = lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	StatusStartingStyle  = lipgloss.NewStyle().Foreground(ColorAccent)
	StatusSuspendedStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

	// Tool badge style.
	ToolClaudeStyle = lipgloss.NewStyle().Foreground(ColorOrange)

	// Selection styles (inverted).
	SessionSelectionPrefix = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	SessionTitleSelStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	SessionStatusSelStyle  = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	TreeConnectorSelStyle  = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	ToolBadgeSelStyle      = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	// Dimmed selection (used when the sidebar doesn't own the keyboard — e.g.
	// the terminal drawer is focused). A muted bar instead of the bright accent
	// pill, so the row still reads as "selected" but clearly inactive.
	SessionTitleSelDimStyle  = lipgloss.NewStyle().Bold(true).Foreground(ColorText).Background(ColorBorder)
	SessionStatusSelDimStyle = lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder)

	// selectionDimmed makes the sidebar's selected-row pill render muted instead
	// of accent — set by RenderSidebar when the sidebar doesn't own the keyboard
	// (terminal drawer focused). Render-thread only, so a plain package var is safe.
	selectionDimmed bool

	// Panel title style (cyan/blue like agent-deck).
	PanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBlue)

	// Header bar style — no background fill so the top bar reads as part of
	// the canvas, not a separate ribbon.
	HeaderBarStyle = lipgloss.NewStyle().Padding(0, 1)

	// Help bar key style — accent-color text, bold. No background fill;
	// reads as a Posting-style "colored key + plain description" pair.
	HelpKeyStyle = lipgloss.NewStyle().
			Foreground(ColorAccent).
			Bold(true)

	HelpDescStyle = lipgloss.NewStyle().Foreground(ColorText)

	HelpSepStyle = lipgloss.NewStyle().Foreground(ColorBorder)

	// Git info styles.
	BranchStyle    = lipgloss.NewStyle().Foreground(ColorBlue)
	DirtyStyle     = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	PROpenStyle    = lipgloss.NewStyle().Foreground(ColorGreen)
	PRFailStyle    = lipgloss.NewStyle().Foreground(ColorRed)
	PRPendingStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	PRMergedStyle  = lipgloss.NewStyle().Foreground(ColorPurple)
	PRDraftStyle   = lipgloss.NewStyle().Foreground(ColorTextDim)

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
	AgentGlyphStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
	PanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorBorder)
	DialogStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorAccent).Padding(1, 2)

	StatusRunningStyle = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	StatusWaitingStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	StatusFinishedStyle = lipgloss.NewStyle().Foreground(ColorBlue).Bold(true)
	StatusIdleStyle = lipgloss.NewStyle().Foreground(ColorGray)
	StatusErrorStyle = lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	StatusStartingStyle = lipgloss.NewStyle().Foreground(ColorAccent)
	StatusSuspendedStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

	ToolClaudeStyle = lipgloss.NewStyle().Foreground(ColorOrange)

	SessionSelectionPrefix = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	SessionTitleSelStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	SessionStatusSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	TreeConnectorSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	ToolBadgeSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	SessionTitleSelDimStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorText).Background(ColorBorder)
	SessionStatusSelDimStyle = lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder)

	PanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBlue)
	HeaderBarStyle = lipgloss.NewStyle().Padding(0, 1)

	HelpKeyStyle = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	HelpDescStyle = lipgloss.NewStyle().Foreground(ColorText)
	HelpSepStyle = lipgloss.NewStyle().Foreground(ColorBorder)

	BranchStyle = lipgloss.NewStyle().Foreground(ColorBlue)
	DirtyStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	PROpenStyle = lipgloss.NewStyle().Foreground(ColorGreen)
	PRFailStyle = lipgloss.NewStyle().Foreground(ColorRed)
	PRPendingStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	PRMergedStyle = lipgloss.NewStyle().Foreground(ColorPurple)
	PRDraftStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

	SlotBadgeStyle = lipgloss.NewStyle().Foreground(ColorOrange).Bold(true)
	SlotBadgeDimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
}

// RenderBorderedPanel wraps content in a rounded border with a title inset
// into the top border. Output is exactly width × height.
//
//	╭─ Sessions ──────── 2 RUN · 51 idle ─╮
//	│ ...content...                       │
//	╰─────────────────────────────────────╯
//
// When accent is true, the border is drawn in the accent color (used for the
// focused panel in dual mode); otherwise it uses the muted border color.
// Use RenderBorderedPanelTopRight to embed a right-aligned status pill into
// the top border, or RenderBorderedPanelInsets to also embed footer insets in
// the bottom border.
func RenderBorderedPanel(content, title string, width, height int, accent bool) string {
	return renderBorderedPanel(content, title, "", "", "", width, height, accent)
}

// RenderBorderedPanelTopRight is like RenderBorderedPanel but also embeds a
// right-aligned status pill into the TOP border (after the title).
func RenderBorderedPanelTopRight(content, title, titleRight string, width, height int, accent bool) string {
	return renderBorderedPanel(content, title, titleRight, "", "", width, height, accent)
}

// RenderBorderedPanelInsets embeds all four optional insets: title + titleRight
// in the top border, footerLeft + footerRight in the bottom border. Used to ride
// the collapsed-shell chips on a panel's bottom-left while keeping its existing
// footer (cwd / status). Any inset may be "".
func RenderBorderedPanelInsets(content, title, titleRight, footerLeft, footerRight string, width, height int, accent bool) string {
	return renderBorderedPanel(content, title, titleRight, footerLeft, footerRight, width, height, accent)
}

// selTitle returns the selected-row title style — muted when the sidebar is
// unfocused (terminal drawer focused), accent otherwise.
func selTitle() lipgloss.Style {
	if selectionDimmed {
		return SessionTitleSelDimStyle
	}
	return SessionTitleSelStyle
}

// selStatus returns the selected-row status/count style (muted when unfocused).
func selStatus() lipgloss.Style {
	if selectionDimmed {
		return SessionStatusSelDimStyle
	}
	return SessionStatusSelStyle
}

// RenderBorderedPanelFull embeds a right-aligned inset into BOTH the top border
// (after the title) and the bottom border. Used by the terminal drawer for a
// top-border mode label + a bottom-border cwd/hint.
func RenderBorderedPanelFull(content, title, titleRight, footerRight string, width, height int, accent bool) string {
	return renderBorderedPanel(content, title, titleRight, "", footerRight, width, height, accent)
}

func renderBorderedPanel(content, title, titleRight, footerLeft, footerRight string, width, height int, accent bool) string {
	if width < 6 || height < 3 {
		return content
	}

	borderColor := ColorBorder
	if accent {
		borderColor = ColorAccent
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	// Top border: ╭─ title ─...─ titleRight ─╮ (titleRight optional).
	// Pre-styled inputs (callers like BuildPreviewTitle / statusCountsLine that
	// already render per-segment colors) are passed through verbatim; only
	// plain strings get the default title/right wrap.
	titleText := styleIfPlain(title, titleStyle)
	titleWidth := lipgloss.Width(titleText)
	if titleRight != "" {
		rightText := styleIfPlain(titleRight, lipgloss.NewStyle().Foreground(ColorTextDim))
		rightW := lipgloss.Width(rightText)
		// Layout: ╭ ─ ' ' title ' ' ─*K ' ' titleRight ' ' ─ ╮
		// 8 fixed chars: 2 corners + 2 dashes + 4 spaces.
		dashes := width - 8 - titleWidth - rightW
		if dashes < 1 {
			// Top is too tight; drop the trailing right text rather than wrap.
			return renderBorderedPanel(content, title, "", footerLeft, footerRight, width, height, accent)
		}
		top := borderStyle.Render("╭─ ") + titleText + borderStyle.Render(" "+strings.Repeat("─", dashes)+" ") + rightText + borderStyle.Render(" ─╮")
		return finishBorderedPanel(top, content, footerLeft, footerRight, width, height, borderStyle)
	}
	// Layout: ╭ ─ ' ' title ' ' ─*K ╮ — leading dash count is 1, K fills the rest.
	fillRight := width - 2 /*corners*/ - 1 /*leading dash*/ - 1 /*space*/ - titleWidth - 1 /*space*/
	if fillRight < 1 {
		// Title too wide; truncate it so the border still closes. ansi.Truncate
		// is escape-sequence aware, so pre-styled titles stay intact.
		maxTitle := max(width-6, 1)
		titleText = styleIfPlain(ansi.Truncate(title, maxTitle, ""), titleStyle)
		titleWidth = lipgloss.Width(titleText)
		fillRight = max(width-5-titleWidth, 1)
	}
	top := borderStyle.Render("╭─ ") + titleText + borderStyle.Render(" "+strings.Repeat("─", fillRight)+"╮")
	return finishBorderedPanel(top, content, footerLeft, footerRight, width, height, borderStyle)
}

// styleIfPlain renders s with style only when s is plain text. If s already
// contains an ANSI escape, return it untouched so per-segment colors set by
// the caller (e.g. BuildPreviewTitle's status pill) survive the outer wrap.
func styleIfPlain(s string, style lipgloss.Style) string {
	if strings.Contains(s, "\x1b") {
		return s
	}
	return style.Render(s)
}

func finishBorderedPanel(top, content, footerLeft, footerRight string, width, height int, borderStyle lipgloss.Style) string {

	footerStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	// Bottom border, optionally with a left-aligned inset (footerLeft, e.g. the
	// collapsed-shell chips) and/or a right-aligned inset (footerRight, e.g. the
	// preview cwd). The left/right and both-insets layouts mirror the top border.
	var bottom string
	switch {
	case footerLeft != "" && footerRight != "":
		leftText := styleIfPlain(footerLeft, footerStyle)
		leftW := lipgloss.Width(leftText)
		rightText := styleIfPlain(footerRight, footerStyle)
		rightW := lipgloss.Width(rightText)
		// Layout: ╰ ─ ' ' left ' ' ─*K ' ' right ' ' ─ ╯ — 8 fixed chars.
		dashes := width - 8 - leftW - rightW
		if dashes < 1 {
			// Too tight for both — keep the right (cwd) and drop the left (chips).
			return finishBorderedPanel(top, content, "", footerRight, width, height, borderStyle)
		}
		bottom = borderStyle.Render("╰─ ") + leftText + borderStyle.Render(" "+strings.Repeat("─", dashes)+" ") + rightText + borderStyle.Render(" ─╯")
	case footerLeft != "":
		leftText := styleIfPlain(footerLeft, footerStyle)
		leftW := lipgloss.Width(leftText)
		// Layout: ╰ ─ ' ' left ' ' ─*K ╯ — leading dash count is 1.
		fillRight := width - 2 /*corners*/ - 1 /*leading dash*/ - 1 /*space*/ - leftW - 1 /*space*/
		if fillRight < 1 {
			maxLeft := max(width-6, 1)
			leftText = styleIfPlain(ansi.Truncate(footerLeft, maxLeft, ""), footerStyle)
			leftW = lipgloss.Width(leftText)
			fillRight = max(width-5-leftW, 1)
		}
		bottom = borderStyle.Render("╰─ ") + leftText + borderStyle.Render(" "+strings.Repeat("─", fillRight)+"╯")
	case footerRight != "":
		footerText := styleIfPlain(footerRight, footerStyle)
		footerW := lipgloss.Width(footerText)
		// Layout: ╰─*K ' ' footer ' '─╯ — trailing dash count is 1.
		fillLeft := width - 2 /*corners*/ - 1 /*space*/ - footerW - 1 /*space*/ - 1 /*trailing dash*/
		if fillLeft < 1 {
			// Footer too wide; truncate (ANSI-aware via ansi.Truncate).
			maxFooter := max(width-6, 1)
			footerText = styleIfPlain(ansi.Truncate(footerRight, maxFooter, ""), footerStyle)
			footerW = lipgloss.Width(footerText)
			fillLeft = max(width-5-footerW, 1)
		}
		bottom = borderStyle.Render("╰"+strings.Repeat("─", fillLeft)+" ") + footerText + borderStyle.Render(" ─╯")
	default:
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
			line = ansi.Truncate(line, innerWidth, "")
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
// sidebar. "icon" (default) uses semantic circles (●◐✕) and anchors idle/
// starting rows with a dim `·` so every row has a leftmost mark. "bar" uses
// a single colored vertical bar in the leftmost column (VS Code gutter style)
// and keeps idle/starting blank — the gutter bar carries the signal there.
var StatusIndicatorMode = "icon"

const StatusBarChar = "┃"

// Sidebar display flags. These mirror StatusIndicatorMode: package-level vars
// read by the sidebar render funcs, synced from config by ApplyDisplayConfig
// (called in NewHome at startup and after any Appearance settings change). They
// default to the "everything on" rendering so an unconfigured fleet is unchanged.
var (
	ShowAgentGlyphs    = true       // per-session ✻/◇ agent sigil
	ShowStatusPills    = true       // header "2● 1◐" status summary
	ShowPRBadges       = true       // "#123 ✓" PR badge on checkout headers
	ShowDirtyIndicator = true       // "*" dirty-worktree marker
	ShowSlotBadges     = true       // "[N]" hotkey slot badge
	ShowHeaderCounts   = true       // session count on origin/checkout headers
	ShowAccountUsage   = true       // top-right per-account weekly quota readout
	ChevronStyle       = "triangle" // "triangle" (▾▸) or "plusminus" (−+)
	SidebarDensity     = "normal"   // "normal" (gap between groups) or "compact"
)

// ApplyDisplayConfig syncs all sidebar display flags from config. Must be called
// on the main goroutine (Bubble Tea Update/View), like ApplyPalette.
func ApplyDisplayConfig(cfg *config.Config) {
	StatusIndicatorMode = cfg.GetStatusIndicator()
	ShowAgentGlyphs = cfg.IsShowAgentGlyphs()
	ShowStatusPills = cfg.IsShowStatusPills()
	ShowPRBadges = cfg.IsShowPRBadges()
	ShowDirtyIndicator = cfg.IsShowDirtyIndicator()
	ShowSlotBadges = cfg.IsShowSlotBadges()
	ShowHeaderCounts = cfg.IsShowHeaderCounts()
	ShowAccountUsage = cfg.IsShowAccountUsage()
	ChevronStyle = cfg.GetChevronStyle()
	SidebarDensity = cfg.GetSidebarDensity()
}

// chevronGlyph returns the expand/collapse marker for a header, honoring the
// configured ChevronStyle. Both glyphs in each style are width-1 so columns
// stay aligned regardless of choice.
func chevronGlyph(expanded bool) string {
	if ChevronStyle == "plusminus" {
		if expanded {
			return "−" // U+2212 minus sign (width-1, aligns with "+")
		}
		return "+"
	}
	if expanded {
		return "▾"
	}
	return "▸"
}

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
	case session.StatusSuspended:
		return "·" // same dot as idle; the dim style + "suspended" label carry the distinction
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
	case session.StatusSuspended:
		return StatusSuspendedStyle
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
	case session.StatusSuspended:
		return lipgloss.NewStyle().Foreground(ColorTextDim)
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
	case session.StatusSuspended:
		return StatusSuspendedStyle.Render("suspended")
	default:
		return string(status)
	}
}
