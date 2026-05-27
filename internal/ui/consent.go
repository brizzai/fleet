package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// consentResultMsg is emitted when the user answers the first-launch
// analytics consent prompt. `accepted` is true when they opted in.
type consentResultMsg struct {
	accepted bool
}

// ConsentDialog is the first-launch analytics opt-in prompt. It surfaces
// exactly what fleet collects before initializing the Mixpanel client so
// the user can make an informed choice. Dismissing the dialog any way
// other than explicit "Yes" is treated as a decline.
type ConsentDialog struct {
	visible bool
	width   int
	height  int
	cursor  int // 0 = Yes (default), 1 = No
}

// NewConsentDialog creates a consent dialog with the cursor on "Yes".
func NewConsentDialog() *ConsentDialog {
	return &ConsentDialog{cursor: 0}
}

func (d *ConsentDialog) Show() {
	d.visible = true
	d.cursor = 0
}

func (d *ConsentDialog) Hide()           { d.visible = false }
func (d *ConsentDialog) IsVisible() bool { return d.visible }
func (d *ConsentDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// Update handles key events. Y / Enter on Yes = accept. N / Esc / Enter on
// No = decline. Arrow keys toggle the selection between Yes and No.
func (d *ConsentDialog) Update(msg tea.Msg) (*ConsentDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "y", "Y":
		return d.answer(true)
	case "n", "N", "esc":
		return d.answer(false)
	case "enter":
		return d.answer(d.cursor == 0)
	case "left", "h", "right", "l", "tab", "shift+tab":
		d.cursor = 1 - d.cursor
	}
	return d, nil
}

func (d *ConsentDialog) answer(accepted bool) (*ConsentDialog, tea.Cmd) {
	d.Hide()
	return d, func() tea.Msg { return consentResultMsg{accepted: accepted} }
}

func (d *ConsentDialog) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(titleStyle.Render("Help improve fleet?"))
	b.WriteString("\n\n")

	body := lipgloss.NewStyle().Foreground(ColorText)
	b.WriteString(body.Render("fleet sends anonymous usage events so I can see"))
	b.WriteString("\n")
	b.WriteString(body.Render("what works and what doesn't. With your permission"))
	b.WriteString("\n")
	b.WriteString(body.Render("it also includes:"))
	b.WriteString("\n\n")

	bullets := []string{
		"git user.name",
		"git user.email",
		"OS version, theme, session counts, error categories",
	}
	for _, item := range bullets {
		b.WriteString(DimStyle.Render("  • " + item))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(DimStyle.Render("Never sent: file paths, prompts, code, repo/branch names."))
	b.WriteString("\n")
	b.WriteString(DimStyle.Render("Change this any time in Settings (S key)."))
	b.WriteString("\n\n")

	// Buttons. Selected button gets the accent color background.
	yesLabel := " Y  Yes, help out "
	noLabel := " N  No thanks "

	yesStyle := lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder).Padding(0, 1)
	noStyle := lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder).Padding(0, 1)
	selStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent).Padding(0, 1)

	if d.cursor == 0 {
		b.WriteString(selStyle.Render(yesLabel))
		b.WriteString("  ")
		b.WriteString(noStyle.Render(noLabel))
	} else {
		b.WriteString(yesStyle.Render(yesLabel))
		b.WriteString("  ")
		b.WriteString(selStyle.Render(noLabel))
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2)

	box := boxStyle.Render(b.String())

	// Center the box on screen.
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
