package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// onboardingClosedMsg is emitted when the first-run onboarding screen closes
// (whether the user confirmed a theme or skipped).
type onboardingClosedMsg struct{}

// OnboardingDialog is the one-time first-run screen: a theme picker beside an
// annotated sample sidebar that teaches how to read the real one. It persists
// the chosen theme + DisplayOnboardingSeen, so it appears only once.
type OnboardingDialog struct {
	visible     bool
	width       int
	height      int
	cfg         *config.Config
	themeCursor int
	origTheme   string
}

// NewOnboardingDialog creates the onboarding dialog.
func NewOnboardingDialog(cfg *config.Config) *OnboardingDialog {
	return &OnboardingDialog{cfg: cfg}
}

func (d *OnboardingDialog) Show() {
	d.visible = true
	d.origTheme = d.cfg.Theme
	d.themeCursor = 0
	for i, p := range BuiltinPalettes {
		if p.Name == d.cfg.Theme {
			d.themeCursor = i
			break
		}
	}
}

func (d *OnboardingDialog) Hide()           { d.visible = false }
func (d *OnboardingDialog) IsVisible() bool { return d.visible }
func (d *OnboardingDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// Update handles key events. ←/→ preview a theme live; enter confirms; s/esc
// skips (reverting to the original theme). Both exits mark onboarding seen.
func (d *OnboardingDialog) Update(msg tea.Msg) (*OnboardingDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "left", "h":
		d.themeCursor = (d.themeCursor + len(BuiltinPalettes) - 1) % len(BuiltinPalettes)
		d.applyCursorTheme()
	case "right", "l":
		d.themeCursor = (d.themeCursor + 1) % len(BuiltinPalettes)
		d.applyCursorTheme()
	case "enter":
		return d.finish(true)
	case "s", "S", "esc":
		return d.finish(false)
	}
	return d, nil
}

// applyCursorTheme previews the cursor's theme live and records it on cfg.
func (d *OnboardingDialog) applyCursorTheme() {
	d.cfg.Theme = BuiltinPalettes[d.themeCursor].Name
	ApplyPalette(BuiltinPalettes[d.themeCursor])
}

// finish persists the result and closes. On skip, the original theme is restored.
func (d *OnboardingDialog) finish(confirmed bool) (*OnboardingDialog, tea.Cmd) {
	if confirmed {
		d.cfg.Theme = BuiltinPalettes[d.themeCursor].Name
		analytics.Track(analytics.EventThemeChanged, map[string]interface{}{"theme": d.cfg.Theme})
	} else {
		d.cfg.Theme = d.origTheme
		ApplyPalette(PaletteByName(d.origTheme))
	}
	d.cfg.DisplayOnboardingSeen = true
	_ = d.cfg.Save()
	d.Hide()
	return d, func() tea.Msg { return onboardingClosedMsg{} }
}

func (d *OnboardingDialog) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorBrand)
	b.WriteString(titleStyle.Render("⬡  Welcome to fleet"))
	b.WriteString("\n\n")

	sub := lipgloss.NewStyle().Foreground(ColorText)
	b.WriteString(sub.Render("Pick a theme — and here's how to read your sidebar."))
	b.WriteString("\n")
	b.WriteString(DimStyle.Render("Change everything later in Settings (S)."))
	b.WriteString("\n\n")

	// Theme cycler.
	name := PaletteDisplayName(BuiltinPalettes[d.themeCursor].Name)
	arrow := lipgloss.NewStyle().Foreground(ColorAccent)
	val := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	b.WriteString("Theme:  " + arrow.Render("‹") + " " + val.Render(name) + " " + arrow.Render("›"))
	b.WriteString("   ")
	b.WriteString(DimStyle.Render(themeDots(d.themeCursor, len(BuiltinPalettes))))
	b.WriteString("\n\n")

	// Annotated sample sidebar.
	b.WriteString(renderAnnotatedSample(34))
	b.WriteString("\n\n")

	// Footer.
	footer := lipgloss.NewStyle().Foreground(ColorTextDim).
		Render("←→ theme   enter continue   s skip")
	b.WriteString(footer)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBrand).
		Padding(1, 2)
	box := boxStyle.Render(b.String())

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// themeDots renders a "● ○ ○ ○ ○ ○" position indicator for the theme cycler.
func themeDots(idx, n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		if i == idx {
			parts = append(parts, "●")
		} else {
			parts = append(parts, "○")
		}
	}
	return strings.Join(parts, " ")
}

// renderAnnotatedSample renders the synthetic sidebar with teaching callouts
// pointing at representative rows. Row indices match the RenderSidebarPreview
// fixture's ordering at normal density, so the sample must be rendered tall
// enough to avoid scroll indicators shifting the rows.
func renderAnnotatedSample(width int) string {
	callouts := map[int]string{
		1: "← branch · PR approved",
		2: "← running · Claude",
		3: "← waiting · Codex",
		4: "← worktree · dirty",
		5: "← finished · hotkey [1]",
	}
	sidebar := RenderSidebarPreview(width, 11)
	lines := strings.Split(sidebar, "\n")
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		lw := lipgloss.Width(line)
		if lw < width {
			line += strings.Repeat(" ", width-lw)
		}
		if c, ok := callouts[i]; ok {
			line += "  " + dim.Render(c)
		}
		b.WriteString(line)
	}
	return b.String()
}
