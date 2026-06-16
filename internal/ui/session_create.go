package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SessionCreateDialog is the `A` entry point for creating a session with an
// explicit agent choice (vs `a`, which silently uses the configured default).
// It is intentionally small but built to grow (e.g. a future resume picker).
type SessionCreateDialog struct {
	visible  bool
	width    int
	height   int
	repoPath string
	title    string
	agent    agent.Type
}

// NewSessionCreateDialog creates the dialog.
func NewSessionCreateDialog() *SessionCreateDialog {
	return &SessionCreateDialog{agent: agent.Default}
}

// Show opens the dialog for the given repo, defaulting to defaultAgent.
func (d *SessionCreateDialog) Show(repoPath, title string, defaultAgent agent.Type) {
	d.visible = true
	d.repoPath = repoPath
	d.title = title
	d.agent = agent.Parse(string(defaultAgent))
}

func (d *SessionCreateDialog) Hide()           { d.visible = false }
func (d *SessionCreateDialog) IsVisible() bool { return d.visible }
func (d *SessionCreateDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// Update handles key events for the dialog.
func (d *SessionCreateDialog) Update(msg tea.Msg) (*SessionCreateDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "esc", "q":
		d.Hide()
	case "left", "h":
		d.cycleAgent(-1)
	case "right", "l", "tab":
		d.cycleAgent(1)
	case "enter":
		path, title, ag := d.repoPath, d.title, d.agent
		d.Hide()
		return d, func() tea.Msg {
			return sessionCreateMsg{path: path, title: title, agent: ag}
		}
	}
	return d, nil
}

// agentCycle is the order the picker steps through (left/right). New agents are
// appended; the create path validates the binary is installed and errors if not.
var agentCycle = []agent.Type{agent.Claude, agent.Codex, agent.OpenCode}

// cycleAgent advances the selected agent by delta (+1 next, -1 prev), wrapping.
func (d *SessionCreateDialog) cycleAgent(delta int) {
	idx := 0
	for i, a := range agentCycle {
		if a == d.agent {
			idx = i
			break
		}
	}
	n := len(agentCycle)
	d.agent = agentCycle[((idx+delta)%n+n)%n]
}

// View renders the dialog.
func (d *SessionCreateDialog) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	arrowStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)

	var b strings.Builder
	b.WriteString(titleStyle.Render("New Session"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("Repo: ") + d.repoPath + "\n\n")

	label := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Agent")
	b.WriteString(label + "   " +
		arrowStyle.Render("◂") + " " +
		valueStyle.Render(d.agent.DisplayName()) + " " +
		arrowStyle.Render("▸"))
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", 34)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("←→ agent   ⏎ create   esc cancel"))

	dialogWidth := 54
	if dialogWidth > d.width-4 {
		dialogWidth = d.width - 4
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth)

	box := boxStyle.Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
