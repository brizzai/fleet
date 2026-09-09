package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/frost"
)

// gateDialog is the palette-side front of frost mode: a small prompt that
// takes an answer and, given the right one, drops a hint. Its palette row is
// hidden until a search matches it, and every line of copy comes from the
// frost package, so nothing here says what it is for.
type gateDialog struct {
	input   textinput.Model
	visible bool
	width   int
	height  int
	note    string // what the last answer earned: nothing yet, the wrong line, or the hint
	open    bool   // the answer was right; enter now closes

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
	d.open = false
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
			if d.open {
				d.Hide()
				return d, nil
			}
			if d.unlock(d.input.Value()) {
				d.open = true
				d.note = frost.Gate().Hint
				d.input.Blur()
				return d, nil
			}
			d.note = frost.Gate().Wrong
			d.input.SetValue("")
			return d, nil
		}
	}
	if d.open {
		return d, nil
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
	if d.open {
		b.WriteString(g.Hint)
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("enter: close"))
	} else {
		b.WriteString(DimStyle.Render(g.Prompt))
		b.WriteString("\n")
		b.WriteString(d.input.View())
		b.WriteString("\n\n")
		if d.note != "" {
			b.WriteString(ErrorStyle.Render(d.note))
			b.WriteString("\n\n")
		}
		b.WriteString(DimStyle.Render("enter: answer • esc: close"))
	}

	dialogWidth := min(max(d.width-4, 30), 64)
	box := DialogStyle.Width(dialogWidth).Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
