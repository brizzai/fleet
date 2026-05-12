package ui

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/workspace"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// PendingWorkspace represents an in-flight workspace creation shown as a phantom sidebar entry.
type PendingWorkspace struct {
	ID       string // unique ID for matching results back
	Name     string // display name
	RepoPath string // which repo group to show under
	Frame    int    // spinner animation frame counter
}

func generatePendingID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("pending-%x", b)
}

// Messages for workspace creation flow.
type (
	workspaceCreateMsg struct {
		name, branch, baseBranch string
		repoPath                 string
		provider                 workspace.Provider
	}
	workspaceCreateResultMsg struct {
		info      *workspace.WorkspaceInfo
		err       error
		pendingID string
		repoPath  string
	}
	// deleteCleanupDoneMsg fires when finalizeDelete's background cleanup
	// (tmux kill, hook removal, optional workspace destroy) completes. The
	// Update handler uses sessionID to drop the entry from finalizingDeletes;
	// workspaceErr surfaces destroy failures to the user.
	deleteCleanupDoneMsg struct {
		sessionID    string
		workspaceErr error
	}
)

// CreateWorkspaceDialog handles workspace creation input.
type CreateWorkspaceDialog struct {
	visible     bool
	width       int
	height      int
	nameInput   textinput.Model
	branchInput textinput.Model
	focusIndex  int // 0=name/branch, 1=branch (custom mode only)
	creating    bool
	err         string
	frame       int
	isNative    bool // true for git worktree (single field), false for custom shell
	repoPath    string
	provider    workspace.Provider
}

// NewCreateWorkspaceDialog creates a new create workspace dialog.
func NewCreateWorkspaceDialog() *CreateWorkspaceDialog {
	ni := textinput.New()
	ni.Placeholder = "workspace name"
	ni.CharLimit = 64
	ni.Width = 40
	ni.Focus()

	bi := textinput.New()
	bi.Placeholder = "branch name"
	bi.CharLimit = 128
	bi.Width = 40

	return &CreateWorkspaceDialog{
		nameInput:   ni,
		branchInput: bi,
	}
}

func (d *CreateWorkspaceDialog) Show(provider workspace.Provider, repoPath string) {
	d.visible = true
	d.creating = false
	d.err = ""
	d.provider = provider
	d.repoPath = repoPath
	d.isNative = !provider.IsCustom()

	if d.isNative {
		// Native git mode: single branch input.
		d.focusIndex = 0
		d.branchInput.SetValue("")
		d.branchInput.Placeholder = "branch name (e.g. feature/login)"
		d.branchInput.Focus()
		d.nameInput.Blur()
	} else {
		// Custom shell mode: name + branch inputs.
		d.focusIndex = 0
		d.nameInput.SetValue("")
		d.branchInput.SetValue("")
		d.branchInput.Placeholder = "branch (optional)"
		d.nameInput.Focus()
		d.branchInput.Blur()
	}
}

func (d *CreateWorkspaceDialog) Hide() {
	d.visible = false
	d.nameInput.Blur()
	d.branchInput.Blur()
}

func (d *CreateWorkspaceDialog) IsVisible() bool { return d.visible }

func (d *CreateWorkspaceDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// SetCreating sets the creating state (shows spinner).
func (d *CreateWorkspaceDialog) SetCreating(creating bool) {
	d.creating = creating
	d.frame = 0
}

// SetError sets an error message and re-enables inputs.
func (d *CreateWorkspaceDialog) SetError(err string) {
	d.err = err
	d.creating = false
}

func (d *CreateWorkspaceDialog) updateFocus() {
	if d.isNative {
		// Native mode only has branch input.
		d.branchInput.Focus()
		d.nameInput.Blur()
		return
	}
	if d.focusIndex == 0 {
		d.nameInput.Focus()
		d.branchInput.Blur()
	} else {
		d.nameInput.Blur()
		d.branchInput.Focus()
	}
}

// Update handles key events.
func (d *CreateWorkspaceDialog) Update(msg tea.Msg) (*CreateWorkspaceDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	if d.creating {
		// Only allow esc during creation.
		if keyMsg.String() == "esc" {
			d.Hide()
		}
		return d, nil
	}

	switch keyMsg.String() {
	case "esc":
		d.Hide()
		repoPath := d.repoPath
		return d, func() tea.Msg { return showWorktreeDialogMsg{repoPath: repoPath} }
	case "tab", "down":
		if !d.isNative {
			d.focusIndex = (d.focusIndex + 1) % 2
			d.updateFocus()
		}
		return d, nil
	case "shift+tab", "up":
		if !d.isNative {
			d.focusIndex = (d.focusIndex + 1) % 2
			d.updateFocus()
		}
		return d, nil
	case "enter":
		if d.isNative {
			// Native git mode: branch is the only input.
			branch := strings.TrimSpace(d.branchInput.Value())
			if errMsg := workspace.ValidateBranchName(branch); errMsg != "" {
				d.err = errMsg
				return d, nil
			}
			d.err = ""
			name := workspace.SanitizeBranchName(branch)
			provider := d.provider
			repoPath := d.repoPath
			return d, func() tea.Msg {
				return workspaceCreateMsg{name: name, branch: branch, repoPath: repoPath, provider: provider}
			}
		}
		// Custom shell mode.
		name := strings.TrimSpace(d.nameInput.Value())
		if name == "" {
			d.err = "Name cannot be empty"
			return d, nil
		}
		d.err = ""
		branch := strings.TrimSpace(d.branchInput.Value())
		provider := d.provider
		repoPath := d.repoPath
		return d, func() tea.Msg {
			return workspaceCreateMsg{name: name, branch: branch, repoPath: repoPath, provider: provider}
		}
	}

	// Route to focused input.
	var cmd tea.Cmd
	branchTouched := false
	if d.isNative {
		d.branchInput, cmd = d.branchInput.Update(msg)
		branchTouched = true
	} else if d.focusIndex == 0 {
		d.nameInput, cmd = d.nameInput.Update(msg)
	} else {
		d.branchInput, cmd = d.branchInput.Update(msg)
		branchTouched = true
	}
	if branchTouched {
		current := d.branchInput.Value()
		sanitized, newPos := workspace.SanitizeBranchInputWithCursor(current, d.branchInput.Position())
		if sanitized != current {
			d.branchInput.SetValue(sanitized)
			d.branchInput.SetCursor(newPos)
		}
	}
	return d, cmd
}

// View renders the create workspace dialog.
func (d *CreateWorkspaceDialog) View() string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("Create Workspace"))
	b.WriteString("\n\n")

	if d.creating {
		name := d.branchInput.Value()
		if !d.isNative {
			name = d.nameInput.Value()
		}
		spinner := spinnerFrames[d.frame%len(spinnerFrames)]
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("  "+spinner) + " " + lipgloss.NewStyle().Foreground(ColorText).Render("Creating \""+name+"\"..."))
		b.WriteString("\n")
		if d.isNative {
			b.WriteString(DimStyle.Render("    Running git worktree add"))
		} else {
			b.WriteString(DimStyle.Render("    Running provider command"))
		}
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("esc: cancel"))
		return d.wrapDialog(b.String())
	}

	if d.isNative {
		// Native git mode: single branch input with path preview.
		b.WriteString(DimStyle.Render("Branch:"))
		b.WriteString("\n")
		b.WriteString(d.branchInput.View())
		b.WriteString("\n")

		// Path preview.
		branch := strings.TrimSpace(d.branchInput.Value())
		if branch != "" {
			name := workspace.SanitizeBranchName(branch)
			preview := workspace.DeriveWorktreePathPreview(d.repoPath, name)
			b.WriteString(DimStyle.Render("  → " + preview))
			b.WriteString("\n")
		}
	} else {
		// Custom shell mode: name + branch inputs.
		b.WriteString(DimStyle.Render("Name:"))
		b.WriteString("\n")
		b.WriteString(d.nameInput.View())
		b.WriteString("\n\n")

		b.WriteString(DimStyle.Render("Branch (optional):"))
		b.WriteString("\n")
		b.WriteString(d.branchInput.View())
		b.WriteString("\n")
	}

	if d.err != "" {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render("  " + d.err))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if d.isNative {
		b.WriteString(DimStyle.Render("enter: create  esc: back"))
	} else {
		b.WriteString(DimStyle.Render("enter: create  tab: next  esc: back"))
	}

	return d.wrapDialog(b.String())
}

func (d *CreateWorkspaceDialog) wrapDialog(content string) string {
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
