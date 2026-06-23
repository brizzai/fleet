package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/git"
)

// Messages for branch checkout flow.
type (
	branchListMsg struct {
		branches  []git.BranchInfo
		repoPath  string
		isDirty   bool
		userEmail string
		err       error
	}
	branchCheckoutMsg struct {
		branch   string
		repoPath string
		err      error
	}
)

// BranchCheckoutDialog shows a filterable branch picker for git checkout.
type BranchCheckoutDialog struct {
	visible     bool
	width       int
	height      int
	branches    []git.BranchInfo // full list
	filtered    []git.BranchInfo // after filter
	cursor      int
	scrollOff   int // scroll window offset
	filterInput textinput.Model
	loading     bool
	err         string
	repoPath    string
	isDirty     bool
	frame       int // spinner animation
	myOnly      bool
	userEmail   string
}

const branchMaxVisible = 12

// NewBranchCheckoutDialog creates a new branch checkout dialog.
func NewBranchCheckoutDialog() *BranchCheckoutDialog {
	fi := textinput.New()
	fi.Placeholder = "type to filter"
	fi.CharLimit = 64
	fi.SetWidth(30)

	return &BranchCheckoutDialog{
		filterInput: fi,
	}
}

// ShowLoading shows the dialog in loading state.
func (d *BranchCheckoutDialog) ShowLoading() {
	d.visible = true
	d.loading = true
	d.err = ""
	d.frame = 0
}

// Show populates and shows the dialog.
func (d *BranchCheckoutDialog) Show(branches []git.BranchInfo, repoPath string, isDirty bool, userEmail string) {
	d.visible = true
	d.branches = branches
	d.repoPath = repoPath
	d.isDirty = isDirty
	d.userEmail = userEmail
	d.myOnly = userEmail != "" // default to "mine" when email is available
	d.cursor = 0
	d.scrollOff = 0
	d.err = ""
	d.loading = false
	d.filterInput.SetValue("")
	d.filterInput.Focus()
	d.rebuildFiltered()
}

// ShowError shows an error in the dialog.
func (d *BranchCheckoutDialog) ShowError(err string) {
	d.loading = false
	d.err = err
}

// Hide closes the dialog.
func (d *BranchCheckoutDialog) Hide() {
	d.visible = false
	d.filterInput.Blur()
}

// IsVisible returns whether the dialog is shown.
func (d *BranchCheckoutDialog) IsVisible() bool { return d.visible }

// SetSize updates the dialog dimensions.
func (d *BranchCheckoutDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *BranchCheckoutDialog) rebuildFiltered() {
	filter := strings.ToLower(strings.TrimSpace(d.filterInput.Value()))
	d.filtered = nil

	for _, b := range d.branches {
		if filter != "" && !strings.Contains(strings.ToLower(b.Name), filter) {
			continue
		}
		// Always show current branch regardless of filter.
		if d.myOnly && d.userEmail != "" && !b.IsCurrent && b.AuthorEmail != d.userEmail {
			continue
		}
		d.filtered = append(d.filtered, b)
	}

	// Clamp cursor.
	if d.cursor >= len(d.filtered) {
		d.cursor = len(d.filtered) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	d.syncScroll()
}

func (d *BranchCheckoutDialog) syncScroll() {
	if len(d.filtered) <= branchMaxVisible {
		d.scrollOff = 0
		return
	}
	if d.cursor < d.scrollOff {
		d.scrollOff = d.cursor
	}
	if d.cursor >= d.scrollOff+branchMaxVisible {
		d.scrollOff = d.cursor - branchMaxVisible + 1
	}
}

// Update handles key events.
func (d *BranchCheckoutDialog) Update(msg tea.Msg) (*BranchCheckoutDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		// Non-key messages (notably tea.PasteMsg for cmd+v) go to the filter input.
		if d.loading {
			return d, nil
		}
		var cmd tea.Cmd
		d.filterInput, cmd = d.filterInput.Update(msg)
		d.rebuildFiltered()
		return d, cmd
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
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
			d.syncScroll()
		}
		return d, nil
	case "down", "j":
		if d.cursor < len(d.filtered)-1 {
			d.cursor++
			d.syncScroll()
		}
		return d, nil
	case "tab":
		if d.userEmail != "" {
			d.myOnly = !d.myOnly
			d.rebuildFiltered()
		}
		return d, nil
	case "enter":
		if d.cursor < 0 || d.cursor >= len(d.filtered) {
			return d, nil
		}
		branch := d.filtered[d.cursor]
		repoPath := d.repoPath
		d.Hide()
		return d, func() tea.Msg {
			err := git.CheckoutBranch(repoPath, branch.Name)
			return branchCheckoutMsg{branch: branch.Name, repoPath: repoPath, err: err}
		}
	}

	// Route to filter input.
	var cmd tea.Cmd
	d.filterInput, cmd = d.filterInput.Update(msg)
	d.rebuildFiltered()
	return d, cmd
}

// View renders the branch checkout dialog.
func (d *BranchCheckoutDialog) View() string {
	var b strings.Builder

	// Title with repo name.
	title := "Switch Branch"
	if d.repoPath != "" {
		title += " — " + filepath.Base(d.repoPath)
	}
	if d.isDirty {
		title += DirtyStyle.Render(" (*)")
	}
	b.WriteString(TitleStyle.Render(title))
	b.WriteString("\n\n")

	if d.loading {
		spinner := spinnerFrames[d.frame%len(spinnerFrames)]
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("  "+spinner) + DimStyle.Render(" Loading branches..."))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("esc: cancel"))
		return d.wrapDialog(b.String())
	}

	if d.err != "" {
		b.WriteString(ErrorStyle.Render("  " + d.err))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("esc: cancel"))
		return d.wrapDialog(b.String())
	}

	// Mode toggle.
	if d.userEmail != "" {
		if d.myOnly {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Mine") + DimStyle.Render(" │ All") + DimStyle.Render("  (tab)"))
		} else {
			b.WriteString("  " + DimStyle.Render("Mine │ ") + lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("All") + DimStyle.Render("  (tab)"))
		}
		b.WriteString("\n")
	}

	// Filter input.
	b.WriteString("  " + DimStyle.Render("/") + " " + d.filterInput.View())
	b.WriteString("\n\n")

	if len(d.filtered) == 0 {
		b.WriteString(DimStyle.Render("  No branches found"))
		b.WriteString("\n")
	} else {
		// Scroll indicators.
		if d.scrollOff > 0 {
			b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d above", d.scrollOff)))
			b.WriteString("\n")
		}

		end := d.scrollOff + branchMaxVisible
		if end > len(d.filtered) {
			end = len(d.filtered)
		}

		for i := d.scrollOff; i < end; i++ {
			branch := d.filtered[i]
			selected := i == d.cursor

			prefix := "  "
			if branch.IsCurrent {
				prefix = lipgloss.NewStyle().Foreground(ColorGreen).Render("✓ ")
			}
			if selected {
				prefix = SessionSelectionPrefix.Render("▸ ")
			}

			name := branch.Name
			if len(name) > 40 {
				name = name[:37] + "..."
			}

			if selected {
				line := name
				if branch.IsCurrent {
					line += " (current)"
				}
				if branch.IsRemote {
					line += " ↓"
				}
				b.WriteString(prefix + SessionTitleSelStyle.Render(line))
			} else if branch.IsCurrent {
				b.WriteString(prefix + lipgloss.NewStyle().Foreground(ColorGreen).Render(name) + DimStyle.Render(" (current)"))
			} else if branch.IsRemote {
				b.WriteString(prefix + DimStyle.Render(name+" ↓"))
			} else {
				b.WriteString(prefix + lipgloss.NewStyle().Foreground(ColorText).Render(name))
			}
			b.WriteString("\n")
		}

		below := len(d.filtered) - end
		if below > 0 {
			b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d below", below)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("↑↓ nav  enter: checkout  /: filter  esc: cancel"))

	return d.wrapDialog(b.String())
}

func (d *BranchCheckoutDialog) wrapDialog(content string) string {
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
