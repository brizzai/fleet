package ui

import (
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"github.com/charmbracelet/x/ansi"
)

// Messages for workspace/worktree flow.
type (
	workspaceListMsg struct {
		workspaces    []workspace.WorkspaceInfo
		provider      workspace.Provider
		repoPath      string
		defaultBranch string
		originKey     string // origin of repoPath (native provider); seeds gitInfoCache so the phantom groups correctly
		// linearTeams are the team keys this repo tracks, resolved off-loop
		// alongside the worktree list. Empty means the repo tracks no Linear
		// team and every ticket surface below stays inert.
		linearTeams []string
		err         error
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

// ticketOnInput is the ticket-cursor value meaning "the field itself": the
// caret is visible and no row carries the highlight.
const ticketOnInput = -1

// ticketMaxRows caps the suggestion list. Small on purpose — this is a branch
// field that learned to recognise tickets, not a ticket browser.
const ticketMaxRows = 5

// WorktreeDialog shows base branch + new branch inputs + existing worktrees.
//
// The New branch field doubles as a ticket picker: type an identifier and it
// resolves in place, type prose and matching issues appear one ↓ below. The
// field IS the literal option, so there is never a "use what I typed" row and
// never two things claiming the Enter key.
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

	// --- Linear ticket suggestions under the New branch field ---

	// linearTeams are the team keys this repo tracks. Empty means the whole
	// feature is inert: no lookups, no rows, no footer changes, and the dialog
	// renders exactly as it did before any of this existed.
	linearTeams []string

	// ticketCursor is the second coordinate of the highlight while focus is
	// focusNewBranch: ticketOnInput is the field, 0..n-1 is a row. Forced back
	// to ticketOnInput under any other focus, or two ▸ markers render at once.
	ticketCursor int

	tickets []linear.Ticket

	// resolved is the issue the CURRENT field text denotes. Cleared
	// synchronously the moment the text stops denoting it, so a stale title can
	// never sit under a changed identifier even for one frame.
	resolved *linear.Ticket

	lastInput     string // change detector, so a redraw doesn't refire a lookup
	ticketGen     int    // monotonic; tags the debounce tick and the lookup it fires
	ticketPending bool
	ticketNote    string // one dim line explaining a degradation; never blocks Enter

	// ticketsOff latches after a failure that will keep failing (not logged in,
	// CLI missing). Without it a broken `linear` forks a subprocess on every
	// pause, forever.
	ticketsOff bool
}

// NewWorktreeDialog creates a new worktree dialog.
func NewWorktreeDialog() *WorktreeDialog {
	base := NewTextInput()
	base.Placeholder = "master"
	base.CharLimit = 128
	base.SetWidth(40)

	branch := NewTextInput()
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
func (d *WorktreeDialog) Show(workspaces []workspace.WorkspaceInfo, sessions []*session.Session, provider workspace.Provider, repoPath, defaultBranch string, linearTeams []string) {
	d.visible = true
	d.workspaces = workspaces
	d.provider = provider
	d.repoPath = repoPath
	d.defaultBranch = defaultBranch
	d.cursor = 0
	d.err = ""
	d.loading = false
	d.baseBranchInput.SetValue(defaultBranch)
	d.newBranchInput.SetValue("")

	d.linearTeams = linearTeams
	d.tickets = nil
	d.resolved = nil
	d.lastInput = ""
	d.ticketPending = false
	d.ticketNote = ""
	d.ticketsOff = false
	// Monotonic, never reset to zero. A per-dialog counter that restarted would
	// recycle values, so a reply from a previous open could match a new one
	// where the user had typed the same number of characters.
	d.ticketGen++
	d.setSelection(focusNewBranch, ticketOnInput)

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

	// The inputs follow the box rather than a fixed 40 columns, so a long branch
	// name is readable instead of scrolling inside a field a third of the width
	// of the dialog holding it. NewTextInput owns its prompt (design-system.md
	// §7), so the writable column is the content width less that prompt.
	iw := d.innerWidth() - 2
	if iw < 20 {
		iw = 20
	}
	d.baseBranchInput.SetWidth(iw)
	d.newBranchInput.SetWidth(iw)
}

// setSelection is the ONLY place that moves the highlight.
//
// It clamps, it keeps exactly one thing highlighted, and it keeps the caret
// where the highlight is — the rule the snooze dialog established as "the
// highlight is the promise". Every navigation key is a one-liner through here,
// and TestWorktreeSelectionMutatorIsTheOnlyWriter keeps it that way.
//
// Resetting ticketCursor when focus leaves the New-branch region is not
// housekeeping: without it, moving to the worktree list leaves a ▸ on a ticket
// row as well as on a worktree row, and nothing downstream catches it.
func (d *WorktreeDialog) setSelection(f worktreeFocus, idx int) {
	if f == focusWorktreeList && len(d.workspaces) == 0 {
		f = focusNewBranch
		idx = ticketOnInput
	}
	d.focus = f
	d.ticketCursor = ticketOnInput

	switch f {
	case focusNewBranch:
		if hi := d.visibleTicketCount() - 1; idx > hi {
			idx = hi
		}
		if idx < ticketOnInput {
			idx = ticketOnInput
		}
		d.ticketCursor = idx
	case focusWorktreeList:
		d.cursor = clampInt(idx, 0, len(d.workspaces)-1)
	}

	d.baseBranchInput.Blur()
	d.newBranchInput.Blur()
	switch {
	case f == focusBaseBranch:
		d.baseBranchInput.Focus()
	case f == focusNewBranch && d.ticketCursor == ticketOnInput:
		d.newBranchInput.Focus()
	}
}

// Update handles key events.
func (d *WorktreeDialog) Update(msg tea.Msg) (*WorktreeDialog, tea.Cmd) {
	// Ticket messages are handled above BOTH the loading early-return and the
	// non-key fall-through below, which would otherwise feed them straight into
	// a text input. They self-guard on visibility and generation.
	switch m := msg.(type) {
	case worktreeTicketTickMsg:
		return d, d.onDebounceElapsed(m)
	case worktreeTicketsMsg:
		d.applyTickets(m)
		return d, nil
	}

	keyMsg, isKey := msg.(tea.KeyMsg)

	if d.loading {
		if isKey && keyMsg.String() == "esc" {
			d.Hide()
		}
		return d, nil
	}

	// Non-key messages (notably tea.PasteMsg for cmd+v, which is not a KeyMsg
	// in Bubble Tea v2) still need to reach the focused text input.
	if !isKey {
		return d.routeToInput(msg)
	}

	switch keyMsg.String() {
	case "esc":
		d.Hide()
		return d, nil

	case "tab", "down":
		switch d.focus {
		case focusBaseBranch:
			d.setSelection(focusNewBranch, ticketOnInput)
		case focusNewBranch:
			// Ticket rows sit between the field and the worktree list, so ↓
			// walks into them first when there are any.
			if next := d.ticketCursor + 1; next < d.visibleTicketCount() {
				d.setSelection(focusNewBranch, next)
			} else if len(d.workspaces) > 0 {
				d.setSelection(focusWorktreeList, 0)
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
			if d.ticketCursor > ticketOnInput {
				d.setSelection(focusNewBranch, d.ticketCursor-1)
			} else {
				d.setSelection(focusBaseBranch, 0)
			}
		case focusWorktreeList:
			if d.cursor > 0 {
				d.cursor--
			} else {
				d.setSelection(focusNewBranch, d.visibleTicketCount()-1)
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
		// Enter on a highlighted ticket fills the field; it does NOT create.
		// The base branch may still be wrong and the derived name must stay
		// editable, so the second Enter is the one that acts — and the footer
		// names each of them.
		if d.focus == focusNewBranch && d.ticketCursor >= 0 && d.ticketCursor < len(d.tickets) {
			d.pickTicket(d.tickets[d.ticketCursor])
			return d, nil
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
		// Capture before Hide, which clears the resolution.
		ticket := d.ticketForCurrentInput()
		d.Hide()
		return d, func() tea.Msg {
			return workspaceCreateMsg{
				name: name, branch: newBranch, baseBranch: baseBranch,
				repoPath: repoPath, provider: provider, ticket: ticket,
			}
		}
	}

	// Typing from a ticket row returns the highlight to the field AND keeps the
	// keystroke — setSelection runs before the fall-through, so the same message
	// is consumed by the input. Same ordering as the snooze dialog.
	if d.focus == focusNewBranch && d.ticketCursor != ticketOnInput && isTypingKey(keyMsg.String()) {
		d.setSelection(focusNewBranch, ticketOnInput)
	}
	return d.routeToInput(msg)
}

// routeToInput forwards a message — a fall-through key, or a non-key msg like
// tea.PasteMsg (cmd+v) — to the focused branch input (no-op when the worktree
// list has focus), applying branch-name sanitization to the new-branch field.
func (d *WorktreeDialog) routeToInput(msg tea.Msg) (*WorktreeDialog, tea.Cmd) {
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
			current = sanitized
		}
		// Change-triggered, not keystroke-triggered: a plain redraw must not
		// schedule a lookup.
		if current != d.lastInput {
			if tick := d.onFieldChanged(current); tick != nil {
				return d, tea.Batch(cmd, tick)
			}
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

	// New branch input. The team keys beside the label are the whole
	// configuration disclosure, a few characters: this repo tracks Linear and
	// these are its teams.
	b.WriteString(DimStyle.Render("New branch:"))
	if len(d.linearTeams) > 0 {
		b.WriteString(DimStyle.Render("   " + strings.Join(d.linearTeams, " ")))
	}
	b.WriteString("\n")
	b.WriteString(d.newBranchInput.View())
	b.WriteString("\n")

	// Ticket suggestions sit directly under the field: they are candidates for
	// its contents, so putting the path preview between them would break the
	// "one ↓ below" promise literally.
	b.WriteString(d.renderTicketBlock(d.innerWidth()))

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
		nameW, branchW, countW := d.worktreeColumnWidths()
		for i, ws := range d.workspaces {
			selected := d.focus == focusWorktreeList && i == d.cursor
			b.WriteString(d.renderWorktreeRow(&ws, selected, nameW, branchW, countW))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	// The footer names what Enter does right now, changing as the highlight
	// moves — so the highlight's promise is also stated in words. Falls back to
	// the long-standing hint when there is nothing more specific to say.
	footer := d.ticketFooter()
	if footer == "" {
		footer = "tab: next  enter: create  esc: cancel"
	}
	b.WriteString(DimStyle.Render(footer))

	return d.wrapDialog(b.String())
}

// worktreeColumns sizes the name and branch columns from the widest values
// actually present, within the space the two of them share.
//
// The previous fixed 20/16 split ellipsised every row in a real repo while the
// terminal had room to spare, and starved whichever column happened to hold the
// longer strings. Deriving the widths means a list of short names is never
// truncated at all, and a list of long ones degrades evenly.
func worktreeColumns(wss []workspace.WorkspaceInfo, avail int) (nameW, branchW int) {
	for i := range wss {
		if w := lipgloss.Width(wss[i].Name); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(wss[i].Branch); w > branchW {
			branchW = w
		}
	}
	if nameW+branchW <= avail {
		return nameW, branchW
	}

	// Over budget: split in proportion to what each column asked for, with a
	// floor so the narrower one is not cut to nothing to spare the other.
	const floor = 12
	if avail < 2*floor {
		nameW = avail / 2
		return nameW, avail - nameW
	}
	nameW = avail * nameW / (nameW + branchW)
	if nameW < floor {
		nameW = floor
	}
	if avail-nameW < floor {
		nameW = avail - floor
	}
	return nameW, avail - nameW
}

// fitCell truncates to width and pads out to it, by display columns.
//
// ansi.Truncate rather than a byte slice (design-system.md §7): a worktree name
// is arbitrary user text, and slicing bytes cuts at a fraction of the intended
// columns and can split a rune in half.
func fitCell(sw string, width int) string {
	if width <= 0 {
		return ""
	}
	sw = ansi.Truncate(sw, width, "…")
	if pad := width - lipgloss.Width(sw); pad > 0 {
		sw += strings.Repeat(" ", pad)
	}
	return sw
}

// worktreeRowGap is the space between each pair of columns in a worktree row.
const worktreeRowGap = 2

// worktreeColumnWidths sizes the three columns of the worktree list against the
// dialog's content width. Split out from View so the arithmetic is testable on
// its own: a row that overruns innerWidth wraps onto a second line inside the
// box, which shows up as a stray count on its own row rather than as anything
// measurable on the rendered width.
func (d *WorktreeDialog) worktreeColumnWidths() (nameW, branchW, countW int) {
	for i := range d.workspaces {
		if c := d.sessionCounts[d.workspaces[i].Path]; c > 0 {
			if w := len(strconv.Itoa(c)); w > countW {
				countW = w
			}
		}
	}
	// The name and branch columns share what is left after the selection marker,
	// the two gaps between the three columns, and the count.
	avail := d.innerWidth() - len("▸ ") - 2*worktreeRowGap - countW
	nameW, branchW = worktreeColumns(d.workspaces, avail)
	return nameW, branchW, countW
}

func (d *WorktreeDialog) renderWorktreeRow(ws *workspace.WorkspaceInfo, selected bool, nameW, branchW, countW int) string {
	prefix := "  "
	if selected {
		prefix = SelectionMarker(true).Render("▸ ")
	}

	// Padded raw, then styled — padding a styled string counts the ANSI bytes
	// and the columns come out ragged (design-system.md §7).
	name := fitCell(ws.Name, nameW)
	branch := fitCell(ws.Branch, branchW)

	count := ""
	if c := d.sessionCounts[ws.Path]; c > 0 {
		count = strconv.Itoa(c)
	}
	if pad := countW - lipgloss.Width(count); pad > 0 {
		count = strings.Repeat(" ", pad) + count // right-aligned, so digits line up
	}

	if selected {
		// SelectionPill, not SelectionBand, despite the filled region running past
		// SelectionFillWidthGuide once the columns are sized to their content.
		// The guide is a call-site judgement, and what is filled here is the row's
		// content rather than the full inner width of the dialog — and this list
		// shares focus with two text inputs, where SelectionBand has no blurred
		// weight to offer by design.
		return prefix + SelectionPill(true).Render(strings.TrimRight(name+"  "+branch+"  "+count, " "))
	}

	line := prefix + lipgloss.NewStyle().Foreground(ColorText).Render(name)
	if strings.TrimSpace(branch) != "" {
		line += "  " + BranchStyle.Render(branch)
	}
	if strings.TrimSpace(count) != "" {
		line += "  " + DimStyle.Render(count)
	}
	return strings.TrimRight(line, " ")
}

// innerWidth is the writable content column inside the dialog box. In Lip Gloss
// v2, Style.Width sets the TOTAL frame width (border + padding included), so
// subtract both: 2 (rounded border) + 4 (DialogStyle's Padding(1, 2), 2 each
// side). Subtracting only the padding left rows 2 cells too wide and wrapped the
// longest ones — the same off-by-two the command palette already fixed, latent
// here until worktree rows started being sized against this.
func (d *WorktreeDialog) innerWidth() int {
	return d.dialogWidth() - 6
}

func (d *WorktreeDialog) dialogWidth() int {
	w := d.width - 4
	// Same ceiling as the command palette, and for the same reason: wide enough
	// that a worktree name and its branch are readable rather than a pair of
	// ellipses, still an overlay rather than a takeover. At 64 every row in a
	// real repo was truncated with half the terminal unused.
	if w > 96 {
		w = 96
	}
	if w < 30 {
		w = 30
	}
	return w
}

func (d *WorktreeDialog) wrapDialog(content string) string {
	dialogWidth := d.dialogWidth()

	box := DialogStyle.Width(dialogWidth).Render(content)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
