package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/frost"
)

// gateOpenMsg is sent when the prompt has been answered correctly and has
// closed itself; the app starts a run in reply.
type gateOpenMsg struct{}

// gateDialog is the palette-side front of frost mode. It names the other way
// in — a key sequence, hinted at but not spelled out — and takes an answer as
// a shortcut past it. Its palette row is hidden until a search matches it,
// and every line of copy comes from the frost package, so nothing here says
// what it is for.
type gateDialog struct {
	input   textinput.Model
	visible bool
	width   int
	height  int
	note    string // what the last answer earned: nothing yet, or the wrong line

	// unlock checks an answer. A field so a test can install its own.
	unlock func(string) bool
}

func newGateDialog() *gateDialog {
	ti := NewTextInput()
	ti.EchoMode = textinput.EchoPassword
	ti.CharLimit = 64
	ti.SetWidth(32)
	return &gateDialog{input: ti, unlock: frost.Unlock}
}

// Show opens the prompt fresh: empty answer, nothing said yet.
func (d *gateDialog) Show() {
	d.visible = true
	d.note = ""
	d.input.SetValue("")
	d.input.Focus()
}

func (d *gateDialog) Hide()           { d.visible = false; d.input.Blur() }
func (d *gateDialog) IsVisible() bool { return d.visible }
func (d *gateDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *gateDialog) Update(msg tea.Msg) (*gateDialog, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			d.Hide()
			return d, nil
		case "enter":
			if d.unlock(d.input.Value()) {
				d.Hide()
				return d, func() tea.Msg { return gateOpenMsg{} }
			}
			d.note = frost.Gate().Wrong
			d.input.SetValue("")
			return d, nil
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

func (d *gateDialog) View() string {
	g := frost.Gate()
	var b strings.Builder
	b.WriteString(TitleStyle.Render(g.Label))
	b.WriteString("\n\n")
	b.WriteString(g.Hint)
	b.WriteString("\n\n")
	b.WriteString(DimStyle.Render(g.Prompt))
	b.WriteString("\n")
	b.WriteString(d.input.View())
	b.WriteString("\n\n")
	if d.note != "" {
		b.WriteString(ErrorStyle.Render(d.note))
		b.WriteString("\n\n")
	}
	b.WriteString(DimStyle.Render("enter: try • esc: close"))

	dialogWidth := min(max(d.width-4, 30), 64)
	box := DialogStyle.Width(dialogWidth).Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
