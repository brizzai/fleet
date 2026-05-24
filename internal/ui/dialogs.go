package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sessionCreateMsg is sent when the user confirms creating a new session.
// An empty agent means "use the configured default" (resolved in handleSessionCreate).
type sessionCreateMsg struct {
	path          string
	title         string
	workspaceName string
	agent         agent.Type
}

// forkSessionMsg is sent when the user forks an existing session.
// sourcePath is the parent's ProjectPath; for in-place forks (today's `f`)
// it equals path, and the JSONL copy is skipped. For fork-to-worktree
// (Shift+F) it differs from path and the parent's JSONL is staged in the
// destination's Claude project dir before the new session launches.
type forkSessionMsg struct {
	parentClaudeSessionID string
	sourcePath            string
	path                  string
	title                 string
	workspaceName         string
	agent                 agent.Type // inherited from the parent session
}

// NewSessionDialog handles the new session creation flow with directory autocomplete.
type NewSessionDialog struct {
	pathInput        textinput.Model
	visible          bool
	width            int
	height           int
	err              string
	suggestions      []string
	suggestionCursor int
	lastInput        string // track input changes for recomputing suggestions
}

// NewNewSessionDialog creates a new session dialog.
func NewNewSessionDialog() *NewSessionDialog {
	ti := textinput.New()
	ti.Placeholder = "~/code/my-project"
	ti.CharLimit = 256
	ti.Width = 40
	ti.Focus()

	return &NewSessionDialog{
		pathInput: ti,
	}
}

// Show makes the dialog visible.
func (d *NewSessionDialog) Show() {
	d.visible = true
	d.pathInput.SetValue("")
	d.err = ""
	d.suggestions = nil
	d.suggestionCursor = 0
	d.lastInput = ""
	d.pathInput.Focus()
}

// Hide hides the dialog.
func (d *NewSessionDialog) Hide() {
	d.visible = false
	d.pathInput.Blur()
}

// IsVisible returns whether the dialog is shown.
func (d *NewSessionDialog) IsVisible() bool {
	return d.visible
}

// SetSize updates the dialog dimensions.
func (d *NewSessionDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
	inputWidth := width - 10
	if inputWidth > 60 {
		inputWidth = 60
	}
	if inputWidth < 20 {
		inputWidth = 20
	}
	d.pathInput.Width = inputWidth
}

// Update handles input events for the dialog.
func (d *NewSessionDialog) Update(msg tea.Msg) (*NewSessionDialog, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			path := d.expandPath(d.pathInput.Value())
			if path == "" {
				d.err = "Path cannot be empty"
				return d, nil
			}

			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				d.err = "Directory does not exist"
				return d, nil
			}

			title := filepath.Base(path)
			d.Hide()
			return d, func() tea.Msg {
				return sessionCreateMsg{path: path, title: title}
			}

		case "esc":
			d.Hide()
			return d, nil

		case "tab":
			if len(d.suggestions) > 0 {
				d.pathInput.SetValue(d.suggestions[d.suggestionCursor] + "/")
				d.pathInput.CursorEnd()
				d.computeSuggestions()
			}
			return d, nil

		case "down":
			if len(d.suggestions) > 0 && d.suggestionCursor < len(d.suggestions)-1 {
				d.suggestionCursor++
			}
			return d, nil

		case "up":
			if len(d.suggestions) > 0 && d.suggestionCursor > 0 {
				d.suggestionCursor--
			}
			return d, nil
		}
	}

	var cmd tea.Cmd
	d.pathInput, cmd = d.pathInput.Update(msg)

	// Recompute suggestions when input changes.
	current := d.pathInput.Value()
	if current != d.lastInput {
		d.lastInput = current
		d.computeSuggestions()
	}

	return d, cmd
}

func (d *NewSessionDialog) computeSuggestions() {
	d.suggestions = nil
	d.suggestionCursor = 0

	raw := d.pathInput.Value()
	if raw == "" {
		return
	}

	expanded := d.expandPath(raw)

	// Check if the expanded path is itself a directory (user typed a complete path with trailing /).
	if info, err := os.Stat(expanded); err == nil && info.IsDir() && strings.HasSuffix(raw, "/") {
		// List children of this directory.
		entries, err := os.ReadDir(expanded)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			d.suggestions = append(d.suggestions, filepath.Join(expanded, entry.Name()))
			if len(d.suggestions) >= 5 {
				break
			}
		}
		d.shortenSuggestions()
		return
	}

	// Split into parent dir + prefix.
	parentDir := filepath.Dir(expanded)
	prefix := filepath.Base(expanded)

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return
	}

	lowerPrefix := strings.ToLower(prefix)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(entry.Name()), lowerPrefix) {
			d.suggestions = append(d.suggestions, filepath.Join(parentDir, entry.Name()))
			if len(d.suggestions) >= 5 {
				break
			}
		}
	}
	d.shortenSuggestions()
}

// shortenSuggestions replaces home dir prefix with ~ for display.
func (d *NewSessionDialog) shortenSuggestions() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for i, s := range d.suggestions {
		if strings.HasPrefix(s, home+"/") {
			d.suggestions[i] = "~" + s[len(home):]
		}
	}
}

// View renders the dialog.
func (d *NewSessionDialog) View() string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("New Session"))
	b.WriteString("\n\n")
	b.WriteString(DimStyle.Render("Project directory:"))
	b.WriteString("\n")
	b.WriteString(d.pathInput.View())
	b.WriteString("\n")

	if len(d.suggestions) > 0 {
		b.WriteString("\n")
		for i, s := range d.suggestions {
			if i == d.suggestionCursor {
				b.WriteString(SessionSelectionPrefix.Render("▸ ") + SessionTitleSelStyle.Render(s))
			} else {
				b.WriteString("  " + DimStyle.Render(s))
			}
			b.WriteString("\n")
		}
	}

	if d.err != "" {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render("  " + d.err))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("tab: complete  enter: create  esc: cancel"))

	// Center the dialog.
	dialogWidth := d.width - 4
	if dialogWidth > 64 {
		dialogWidth = 64
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}

	box := DialogStyle.Width(dialogWidth).Render(b.String())

	// Center vertically and horizontally.
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

func (d *NewSessionDialog) expandPath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		if path == "~" {
			return home
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// sessionRenameMsg is sent when the user confirms renaming a session.
type sessionRenameMsg struct {
	id       string
	newTitle string
}

// RenameDialog handles session rename flow.
type RenameDialog struct {
	titleInput textinput.Model
	visible    bool
	width      int
	height     int
	sessionID  string
}

// NewRenameDialog creates a new rename dialog.
func NewRenameDialog() *RenameDialog {
	ti := textinput.New()
	ti.Placeholder = "session name"
	ti.CharLimit = 64
	ti.Width = 40
	ti.Focus()

	return &RenameDialog{
		titleInput: ti,
	}
}

// Show makes the dialog visible, pre-filled with the current title.
func (d *RenameDialog) Show(sessionID, currentTitle string) {
	d.visible = true
	d.sessionID = sessionID
	d.titleInput.SetValue(currentTitle)
	d.titleInput.Focus()
	d.titleInput.CursorEnd()
}

func (d *RenameDialog) Hide()           { d.visible = false; d.titleInput.Blur() }
func (d *RenameDialog) IsVisible() bool { return d.visible }
func (d *RenameDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
	inputWidth := w - 10
	if inputWidth > 60 {
		inputWidth = 60
	}
	if inputWidth < 20 {
		inputWidth = 20
	}
	d.titleInput.Width = inputWidth
}

// Update handles input events for the rename dialog.
func (d *RenameDialog) Update(msg tea.Msg) (*RenameDialog, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			newTitle := strings.TrimSpace(d.titleInput.Value())
			if newTitle == "" {
				return d, nil
			}
			id := d.sessionID
			d.Hide()
			return d, func() tea.Msg {
				return sessionRenameMsg{id: id, newTitle: newTitle}
			}
		case "esc":
			d.Hide()
			return d, nil
		}
	}

	var cmd tea.Cmd
	d.titleInput, cmd = d.titleInput.Update(msg)
	return d, cmd
}

// View renders the rename dialog.
func (d *RenameDialog) View() string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("Rename Session"))
	b.WriteString("\n\n")
	b.WriteString(DimStyle.Render("New title:"))
	b.WriteString("\n")
	b.WriteString(d.titleInput.View())
	b.WriteString("\n\n")
	b.WriteString(DimStyle.Render("enter: rename • esc: cancel"))

	dialogWidth := d.width - 4
	if dialogWidth > 64 {
		dialogWidth = 64
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}

	box := DialogStyle.Width(dialogWidth).Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// ConfirmDialog handles confirmation prompts (e.g., delete session).
type ConfirmDialog struct {
	visible    bool
	width      int
	height     int
	onYes      func() tea.Msg
	dialogType string // "danger", "warning", "info"
	title      string
	subject    string
	details    []string
}

// NewConfirmDialog creates a new confirmation dialog.
func NewConfirmDialog() *ConfirmDialog {
	return &ConfirmDialog{}
}

// ShowDanger shows a danger-style confirmation dialog (red border).
func (d *ConfirmDialog) ShowDanger(title, subject string, details []string, onYes func() tea.Msg) {
	d.visible = true
	d.dialogType = "danger"
	d.title = title
	d.subject = subject
	d.details = details
	d.onYes = onYes
}

// Show shows a basic info-style confirmation dialog (backward compatible).
func (d *ConfirmDialog) Show(message string, onYes func() tea.Msg) {
	d.visible = true
	d.dialogType = "info"
	d.title = message
	d.subject = ""
	d.details = nil
	d.onYes = onYes
}

func (d *ConfirmDialog) Hide()           { d.visible = false }
func (d *ConfirmDialog) IsVisible() bool { return d.visible }
func (d *ConfirmDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *ConfirmDialog) Update(msg tea.Msg) (*ConfirmDialog, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "enter":
			d.Hide()
			if d.onYes != nil {
				return d, func() tea.Msg { return d.onYes() }
			}
			return d, nil
		case "n", "N", "esc":
			d.Hide()
			return d, nil
		}
	}
	return d, nil
}

func (d *ConfirmDialog) borderColor() lipgloss.Color {
	switch d.dialogType {
	case "danger":
		return ColorRed
	case "warning":
		return ColorYellow
	default:
		return ColorAccent
	}
}

func (d *ConfirmDialog) View() string {
	bc := d.borderColor()

	var b strings.Builder

	// Title with warning icon for danger.
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(bc)
	if d.dialogType == "danger" {
		b.WriteString(titleStyle.Render("⚠ " + d.title))
	} else {
		b.WriteString(titleStyle.Render(d.title))
	}

	// Subject (quoted session name).
	if d.subject != "" {
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorText).Render(fmt.Sprintf(`"%s"`, d.subject)))
	}

	// Detail bullets.
	if len(d.details) > 0 {
		b.WriteString("\n")
		for _, detail := range d.details {
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("  • " + detail))
		}
	}

	// Buttons.
	b.WriteString("\n\n")
	actionLabel := "y Confirm"
	if d.dialogType == "danger" {
		actionLabel = "y Delete"
	}
	actionBtn := lipgloss.NewStyle().
		Background(bc).
		Foreground(ColorBg).
		Bold(true).
		Padding(0, 1).
		Render(actionLabel)

	cancelBtn := lipgloss.NewStyle().
		Background(ColorBorder).
		Foreground(ColorText).
		Padding(0, 1).
		Render("n Cancel")
	b.WriteString(actionBtn + "  " + cancelBtn)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bc).
		Padding(1, 2).
		Width(50)

	box := boxStyle.Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
