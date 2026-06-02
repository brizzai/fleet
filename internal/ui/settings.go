package ui

import (
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// settingsClosedMsg is sent when the settings dialog closes.
type settingsClosedMsg struct{}

var (
	editorPresets = []string{"code", "cursor", "vim", "nvim", "nano", "emacs", "zed"}
	tickPresets   = []int{1, 2, 3, 5, 10}
)

// SettingsDialog provides a UI for configuring fleet settings.
type SettingsDialog struct {
	visible   bool
	width     int
	height    int
	cursor    int // 0=theme, 1=editor, 2=tick
	cfg       *config.Config
	origTheme string
}

// NewSettingsDialog creates a settings dialog.
func NewSettingsDialog(cfg *config.Config) *SettingsDialog {
	return &SettingsDialog{cfg: cfg}
}

func (d *SettingsDialog) Show() {
	d.visible = true
	d.cursor = 0
	d.origTheme = d.cfg.Theme
}

func (d *SettingsDialog) Hide()           { d.visible = false }
func (d *SettingsDialog) IsVisible() bool { return d.visible }
func (d *SettingsDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// Update handles key events for the settings dialog.
func (d *SettingsDialog) Update(msg tea.Msg) (*SettingsDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	numRows := 10 // theme, status-style, editor, tick, auto-name, auto-update, copy-claude, enter-mode, telemetry, default-agent
	switch keyMsg.String() {
	case "j", "down":
		d.cursor = (d.cursor + 1) % numRows
	case "k", "up":
		d.cursor = (d.cursor + numRows - 1) % numRows
	case "l", "right":
		d.cycleValue(1)
	case "h", "left":
		d.cycleValue(-1)
	case "esc", "q":
		// Save config and close.
		_ = d.cfg.Save()
		d.Hide()
		return d, func() tea.Msg { return settingsClosedMsg{} }
	}

	return d, nil
}

func (d *SettingsDialog) cycleValue(dir int) {
	switch d.cursor {
	case 0: // Theme
		names := make([]string, len(BuiltinPalettes))
		for i, p := range BuiltinPalettes {
			names[i] = p.Name
		}
		current := d.cfg.Theme
		if current == "" {
			current = DefaultPaletteName
		}
		idx := indexOf(names, current)
		idx = (idx + dir + len(names)) % len(names)
		d.cfg.Theme = names[idx]
		ApplyPalette(PaletteByName(d.cfg.Theme))
		analytics.Track(analytics.EventThemeChanged, map[string]interface{}{"theme": d.cfg.Theme})

	case 1: // Status style (icon / bar)
		if d.cfg.GetStatusIndicator() == "icon" {
			d.cfg.StatusIndicator = "bar"
		} else {
			d.cfg.StatusIndicator = "icon"
		}
		StatusIndicatorMode = d.cfg.GetStatusIndicator()

	case 2: // Editor
		current := d.cfg.GetEditor()
		idx := indexOf(editorPresets, current)
		if idx < 0 {
			idx = 0
		}
		idx = (idx + dir + len(editorPresets)) % len(editorPresets)
		d.cfg.Editor = editorPresets[idx]

	case 3: // Tick interval
		current := d.cfg.TickIntervalSec
		if current <= 0 {
			current = 2
		}
		idx := indexOfInt(tickPresets, current)
		if idx < 0 {
			idx = 1 // default to 2s
		}
		idx = (idx + dir + len(tickPresets)) % len(tickPresets)
		d.cfg.TickIntervalSec = tickPresets[idx]

	case 4: // Auto-name
		enabled := d.cfg.IsAutoNameEnabled()
		enabled = !enabled
		d.cfg.AutoNameSessions = &enabled

	case 5: // Auto-update
		enabled := d.cfg.IsAutoUpdateEnabled()
		enabled = !enabled
		d.cfg.AutoUpdate = &enabled

	case 6: // Copy Claude settings
		enabled := d.cfg.IsCopyClaudeSettingsEnabled()
		enabled = !enabled
		d.cfg.CopyClaudeSettings = &enabled

	case 7: // Enter mode
		if d.cfg.GetEnterMode() == "attach" {
			d.cfg.EnterMode = "split"
		} else {
			d.cfg.EnterMode = "attach"
		}

	case 8: // Telemetry
		enabled := d.cfg.IsTelemetryEnabled()
		enabled = !enabled
		d.cfg.Telemetry = &enabled

	case 9: // Default agent
		if d.cfg.GetDefaultAgent() == "codex" {
			d.cfg.DefaultAgent = "claude"
		} else {
			d.cfg.DefaultAgent = "codex"
		}
	}
}

// View renders the settings dialog.
func (d *SettingsDialog) View() string {
	var b strings.Builder

	type row struct {
		label string
		value string
	}

	theme := d.cfg.Theme
	if theme == "" {
		theme = DefaultPaletteName
	}

	autoNameValue := "on"
	if !d.cfg.IsAutoNameEnabled() {
		autoNameValue = "off"
	}

	autoUpdateValue := "on"
	if !d.cfg.IsAutoUpdateEnabled() {
		autoUpdateValue = "off"
	}

	copyClaudeValue := "on"
	if !d.cfg.IsCopyClaudeSettingsEnabled() {
		copyClaudeValue = "off"
	}

	telemetryValue := "on"
	if !d.cfg.IsTelemetryEnabled() {
		telemetryValue = "off"
	}

	agentValue := "Claude"
	if d.cfg.GetDefaultAgent() == "codex" {
		agentValue = "Codex"
	}

	rows := []row{
		{"Theme", PaletteDisplayName(theme)},
		{"Status style", d.cfg.GetStatusIndicator()},
		{"Editor", d.cfg.GetEditor()},
		{"Tick (sec)", fmt.Sprintf("%d", d.cfg.TickIntervalSec)},
		{"Auto-name", autoNameValue},
		{"Auto-update", autoUpdateValue},
		{"Copy .claude", copyClaudeValue},
		{"Enter mode", d.cfg.GetEnterMode()},
		{"Telemetry", telemetryValue},
		{"Default agent", agentValue},
	}

	for i, r := range rows {
		selected := i == d.cursor

		labelStyle := lipgloss.NewStyle().Width(14).Align(lipgloss.Right)
		var arrowStyle lipgloss.Style
		var valueStyle lipgloss.Style

		if selected {
			labelStyle = labelStyle.Foreground(ColorAccent).Bold(true)
			arrowStyle = lipgloss.NewStyle().Foreground(ColorAccent)
			valueStyle = lipgloss.NewStyle().Foreground(ColorText).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(ColorText)
			arrowStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
			valueStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
		}

		line := labelStyle.Render(r.label) + "   " +
			arrowStyle.Render("◂") + " " +
			valueStyle.Render(r.value) + " " +
			arrowStyle.Render("▸")
		b.WriteString(line)

		if i < len(rows)-1 {
			b.WriteString("\n")
		}
	}

	// Divider.
	dividerWidth := 34
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", dividerWidth)))
	b.WriteString("\n")

	// Controls.
	controls := lipgloss.NewStyle().Foreground(ColorTextDim).Render("↑↓ select   ←→ change   esc save")
	b.WriteString(controls)

	// Dialog box.
	dialogWidth := 50
	if dialogWidth > d.width-4 {
		dialogWidth = d.width - 4
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	title := titleStyle.Render("Settings")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth)

	content := title + "\n\n" + b.String()
	box := boxStyle.Render(content)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

func indexOf(slice []string, val string) int {
	for i, s := range slice {
		if s == val {
			return i
		}
	}
	return -1
}

func indexOfInt(slice []int, val int) int {
	for i, v := range slice {
		if v == val {
			return i
		}
	}
	return -1
}
