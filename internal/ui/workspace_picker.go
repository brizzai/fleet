package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
)

// Messages for workspace/worktree flow.
type (
	workspaceListMsg struct {
		workspaces    []workspace.WorkspaceInfo
		provider      workspace.Provider
		repoPath      string
		defaultBranch string
		err           error
	}
	workspaceSelectedMsg struct {
		info workspace.WorkspaceInfo
	}
	showCreateWorkspaceMsg struct {
		provider workspace.Provider
		repoPath string
	}
	showWorktreeDialogMsg struct {
		repoPath string
	}
)

type worktreeFocus int

const (
	focusBaseBranch worktreeFocus = iota
	focusNewBranch
	focusWorktreeList
)

// WorktreeDialog shows base branch + new branch inputs + existing worktrees.
type WorktreeDialog struct {
	visible         bool
	width, height   int
	baseBranchInput textinput.Model
	newBranchInput  textinput.Model
	workspaces      []workspace.WorkspaceInfo
	cursor          int // cursor in the worktree list
	focus           worktreeFocus
	loading         bool
	err             string
	frame           int
	repoPath        string
	provider        workspace.Provider
	sessionCounts   map[string]int
	defaultBranch   string
}

// NewWorktreeDialog creates a new worktree dialog.
func NewWorktreeDialog() *WorktreeDialog {
	base := textinput.New()
	base.Placeholder = "master"
	base.CharLimit = 128
	base.SetWidth(40)

	branch := textinput.New()
	branch.Placeholder = "feature/my-feature"
	branch.CharLimit = 128
	branch.SetWidth(40)

	return &WorktreeDialog{
		baseBranchInput: base,
		newBranchInput:  branch,
		sessionCounts:   make(map[string]int),
	}
}

// Show populates and shows the dialog.
func (d *WorktreeDialog) Show(workspaces []workspace.WorkspaceInfo, sessions []*session.Session, provider workspace.Provider, repoPath, defaultBranch string) {
	d.visible = true
	d.workspaces = workspaces
	d.provider = provider
	d.repoPath = repoPath
	d.defaultBranch = defaultBranch
	d.cursor = 0
	d.focus = focusNewBranch
	d.err = ""
	d.loading = false
	d.baseBranchInput.SetValue(defaultBranch)
	d.baseBranchInput.Blur()
	d.newBranchInput.SetValue("")
	d.newBranchInput.Focus()

	// Build session counts by project path.
	d.sessionCounts = make(map[string]int)
	for _, s := range sessions {
		d.sessionCounts[s.ProjectPath]++
	}
}

// ShowLoading shows the dialog in loading state.
func (d *WorktreeDialog) ShowLoading() {
	d.visible = true
	d.loading = true
	d.err = ""
	d.frame = 0
}

// ShowError shows an error in the dialog.
func (d *WorktreeDialog) ShowError(err string) {
	d.loading = false
	d.err = err
}

func (d *WorktreeDialog) Hide() {
	d.visible = false
	d.baseBranchInput.Blur()
	d.newBranchInput.Blur()
}

func (d *WorktreeDialog) IsVisible() bool { return d.visible }

func (d *WorktreeDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *WorktreeDialog) updateFocus() {
	d.baseBranchInput.Blur()
	d.newBranchInput.Blur()
	switch d.focus {
	case focusBaseBranch:
		d.baseBranchInput.Focus()
	case focusNewBranch:
		d.newBranchInput.Focus()
	}
}

// Update handles key events.
func (d *WorktreeDialog) Update(msg tea.Msg) (*WorktreeDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	if d.loading {
		if keyMsg.String() == "esc" {
			d.Hide()
		}
		return d, nil
	}

	switch keyMsg.String() {
	case "esc":
		d.Hide()
		return d, nil

	case "tab", "down":
		switch d.focus {
		case focusBaseBranch:
			d.focus = focusNewBranch
			d.updateFocus()
		case focusNewBranch:
			if len(d.workspaces) > 0 {
				d.focus = focusWorktreeList
				d.cursor = 0
				d.updateFocus()
			}
		case focusWorktreeList:
			if d.cursor < len(d.workspaces)-1 {
				d.cursor++
			}
		}
		return d, nil

	case "shift+tab", "up":
		switch d.focus {
		case focusBaseBranch:
			// Already at top, no-op.
		case focusNewBranch:
			d.focus = focusBaseBranch
			d.updateFocus()
		case focusWorktreeList:
			if d.cursor > 0 {
				d.cursor--
			} else {
				d.focus = focusNewBranch
				d.updateFocus()
			}
		}
		return d, nil

	case "enter":
		if d.focus == focusWorktreeList && d.cursor >= 0 && d.cursor < len(d.workspaces) {
			// Select existing worktree.
			info := d.workspaces[d.cursor]
			d.Hide()
			return d, func() tea.Msg { return workspaceSelectedMsg{info: info} }
		}
		// Create new worktree from inputs.
		newBranch := strings.TrimSpace(d.newBranchInput.Value())
		if errMsg := workspace.ValidateBranchName(newBranch); errMsg != "" {
			d.err = errMsg
			return d, nil
		}
		baseBranch := strings.TrimSpace(d.baseBranchInput.Value())
		if baseBranch == "" {
			d.err = "Base branch cannot be empty"
			return d, nil
		}
		d.err = ""
		name := workspace.SanitizeBranchName(newBranch)
		provider := d.provider
		repoPath := d.repoPath
		d.Hide()
		return d, func() tea.Msg {
			return workspaceCreateMsg{name: name, branch: newBranch, baseBranch: baseBranch, repoPath: repoPath, provider: provider}
		}
	}

	// Route to focused input.
	var cmd tea.Cmd
	switch d.focus {
	case focusBaseBranch:
		d.baseBranchInput, cmd = d.baseBranchInput.Update(msg)
	case focusNewBranch:
		d.newBranchInput, cmd = d.newBranchInput.Update(msg)
		current := d.newBranchInput.Value()
		sanitized, newPos := workspace.SanitizeBranchInputWithCursor(current, d.newBranchInput.Position())
		if sanitized != current {
			d.newBranchInput.SetValue(sanitized)
			d.newBranchInput.SetCursor(newPos)
		}
	}
	return d, cmd
}

// View renders the worktree dialog.
func (d *WorktreeDialog) View() string {
	var b strings.Builder

	// Title with repo name.
	title := "New Worktree"
	if d.repoPath != "" {
		title += " — " + filepath.Base(d.repoPath)
	}
	b.WriteString(TitleStyle.Render(title))
	b.WriteString("\n\n")

	if d.loading {
		spinner := spinnerFrames[d.frame%len(spinnerFrames)]
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("  "+spinner) + DimStyle.Render(" Loading worktrees..."))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("esc: cancel"))
		return d.wrapDialog(b.String())
	}

	// Base branch input.
	b.WriteString(DimStyle.Render("Base branch:"))
	b.WriteString("\n")
	b.WriteString(d.baseBranchInput.View())
	b.WriteString("\n\n")

	// New branch input.
	b.WriteString(DimStyle.Render("New branch:"))
	b.WriteString("\n")
	b.WriteString(d.newBranchInput.View())
	b.WriteString("\n")

	// Path preview.
	newBranch := strings.TrimSpace(d.newBranchInput.Value())
	if newBranch != "" {
		name := workspace.SanitizeBranchName(newBranch)
		preview := workspace.DeriveWorktreePathPreview(d.repoPath, name)
		b.WriteString(DimStyle.Render("  → " + preview))
		b.WriteString("\n")
	}

	if d.err != "" {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render("  " + d.err))
		b.WriteString("\n")
	}

	// Existing worktrees.
	if len(d.workspaces) > 0 {
		b.WriteString("\n")
		b.WriteString(DimStyle.Render("Existing worktrees:"))
		b.WriteString("\n")
		for i, ws := range d.workspaces {
			selected := d.focus == focusWorktreeList && i == d.cursor
			b.WriteString(d.renderWorktreeRow(&ws, selected))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("tab: next  enter: create  esc: cancel"))

	return d.wrapDialog(b.String())
}

func (d *WorktreeDialog) renderWorktreeRow(ws *workspace.WorkspaceInfo, selected bool) string {
	prefix := "  "
	if selected {
		prefix = SessionSelectionPrefix.Render("▸ ")
	}

	// Name.
	name := ws.Name
	if len(name) > 20 {
		name = name[:20]
	}

	// Branch.
	branch := ws.Branch
	if len(branch) > 16 {
		branch = branch[:13] + "..."
	}

	// Session count.
	count := d.sessionCounts[ws.Path]

	if selected {
		line := fmt.Sprintf("%-20s", name)
		if branch != "" {
			line += "  " + branch
		}
		if count > 0 {
			line += fmt.Sprintf("  %d", count)
		}
		return prefix + SessionTitleSelStyle.Render(line)
	}

	nameStyled := lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("%-20s", name))
	var parts []string
	parts = append(parts, prefix+nameStyled)
	if branch != "" {
		parts = append(parts, BranchStyle.Render(branch))
	}
	if count > 0 {
		parts = append(parts, DimStyle.Render(fmt.Sprintf("%d", count)))
	}
	return strings.Join(parts, "  ")
}

func (d *WorktreeDialog) wrapDialog(content string) string {
	dialogWidth := d.width - 4
	if dialogWidth > 64 {
		dialogWidth = 64
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}

	box := DialogStyle.Width(dialogWidth).Render(content)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
