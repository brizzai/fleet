package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/charmbracelet/x/ansi"
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

	// accounts is the Account cycle, built by app.go at Show time: an "Auto"
	// head naming what account_strategy would pick, then every configured
	// account. Empty below two accounts — with one or none every session runs
	// on the same credential, so the row would be a constant, which is the same
	// rule previewAccountLabel already applies to the preview footer.
	accounts   []accountPickerRow
	accountIdx int
	focus      sessionCreateFocus
}

// sessionCreateFocus names the row ←→ acts on. The dialog is a stack of
// cyclers like the Settings detail pane, so j/k move between rows and h/l
// change the value of whichever holds the highlight.
type sessionCreateFocus int

const (
	focusCreateAgent sessionCreateFocus = iota
	focusCreateAccount
)

// NewSessionCreateDialog creates the dialog.
func NewSessionCreateDialog() *SessionCreateDialog {
	return &SessionCreateDialog{agent: agent.Default}
}

// Show opens the dialog for the given repo, defaulting to defaultAgent.
//
// accounts is the Account cycler's options, or nil to omit the row entirely —
// the dialog never consults the account store itself, so it is a pure function
// of what it was handed and can be tested without one.
func (d *SessionCreateDialog) Show(repoPath, title string, defaultAgent agent.Type, accounts []accountPickerRow) {
	d.visible = true
	d.repoPath = repoPath
	d.title = title
	d.agent = agent.Parse(string(defaultAgent))
	d.accounts = accounts
	d.accountIdx = 0
	d.focus = focusCreateAgent
}

func (d *SessionCreateDialog) Hide()           { d.visible = false }
func (d *SessionCreateDialog) IsVisible() bool { return d.visible }
func (d *SessionCreateDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// accountRowActive reports whether the Account row can be moved to and cycled.
//
// The row is rendered whenever accounts exist but goes inert for a non-Claude
// agent: the account is a claude.ai subscription credential that Codex and
// OpenCode neither read nor need. Inert rather than hidden, because cycling the
// agent must not change the dialog's height mid-keystroke.
func (d *SessionCreateDialog) accountRowActive() bool {
	return len(d.accounts) > 0 && d.agent == agent.Claude
}

// selectedAccount returns the highlighted account option.
func (d *SessionCreateDialog) selectedAccount() (accountPickerRow, bool) {
	if d.accountIdx < 0 || d.accountIdx >= len(d.accounts) {
		return accountPickerRow{}, false
	}
	return d.accounts[d.accountIdx], true
}

// submitBlocker names why ⏎ won't create right now, or "" when it will.
//
// One function, consulted by both the enter handler and the footer, so the key
// can never act on a state the footer calls unready — the shape statusReportForm
// uses, and for the same reason.
//
// A cycler can only honour "a disabled option is shown with its reason" by
// letting you land on one, so ⏎ has to refuse there. It is never blocked for a
// non-Claude agent: the selection is ignored on those, not applied.
func (d *SessionCreateDialog) submitBlocker() string {
	if !d.accountRowActive() {
		return ""
	}
	r, ok := d.selectedAccount()
	if !ok || r.enabled {
		return ""
	}
	return r.note
}

// account returns the email ⏎ should carry. "" means "let account_strategy
// pick", which is both the Auto option and what every other creation path
// sends — see sessionCreateMsg.account.
func (d *SessionCreateDialog) account() string {
	if !d.accountRowActive() {
		return ""
	}
	r, _ := d.selectedAccount()
	return r.email
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
	case "up", "k":
		d.moveFocus(-1)
	case "down", "j":
		d.moveFocus(1)
	case "shift+tab":
		d.tab(-1)
	case "tab":
		d.tab(1)
	case "left", "h":
		d.cycle(-1)
	case "right", "l":
		d.cycle(1)
	case "enter":
		if d.submitBlocker() != "" {
			// The footer already says why; acting would create a session on an
			// account that cannot run it.
			return d, nil
		}
		path, title, ag, acct := d.repoPath, d.title, d.agent, d.account()
		d.Hide()
		return d, func() tea.Msg {
			return sessionCreateMsg{path: path, title: title, agent: ag, account: acct}
		}
	}
	return d, nil
}

// tab advances to the next thing the user can change: the next row when there
// are two, and the Agent row's own value when the Account row is absent.
//
// That second half is not a flourish. On master `tab` was bound to
// cycleAgent(1), and routing it straight to moveFocus made it do nothing at all
// for everyone with fewer than two accounts — the majority — with no footer
// hint to signal the key had changed meaning. ↑↓/j/k are pure row navigation
// and stay inert there instead, which regresses nothing: they were never bound.
func (d *SessionCreateDialog) tab(delta int) {
	if !d.accountRowActive() {
		d.cycleAgent(delta)
		return
	}
	d.moveFocus(delta)
}

// moveFocus walks between the rows that can actually be changed. With no
// account cycle, or on a non-Claude agent, the Agent row is the only one and
// this is a no-op rather than a highlight parked somewhere ←→ does nothing.
func (d *SessionCreateDialog) moveFocus(delta int) {
	if !d.accountRowActive() {
		d.focus = focusCreateAgent
		return
	}
	const rows = 2
	d.focus = sessionCreateFocus(((int(d.focus)+delta)%rows + rows) % rows)
}

// cycle advances the focused row's value by delta (+1 next, -1 prev), wrapping.
func (d *SessionCreateDialog) cycle(delta int) {
	if d.focus == focusCreateAccount && d.accountRowActive() {
		n := len(d.accounts)
		d.accountIdx = ((d.accountIdx+delta)%n + n) % n
		return
	}
	d.cycleAgent(delta)
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
	// The Account row goes inert on a non-Claude agent, so the highlight must
	// not stay parked on it — ←→ would silently do nothing. The selection
	// itself is kept: cycling back to Claude restores the chosen account.
	if !d.accountRowActive() {
		d.focus = focusCreateAgent
	}
}

const (
	// sessionCreateWidth fits "Auto — a long email address" beside the widest
	// state cell ("spent · 5-hour back Mon 15:04") on one line. Fixed so the box
	// doesn't reflow as options of different lengths take the highlight.
	sessionCreateWidth = 64
	// sessionCreateChrome is what DialogStyle spends before any content: two
	// border columns and two of padding a side. lipgloss v2's Width is
	// border-INCLUSIVE, so a line budgeted against the box width overflows the
	// content area and wraps — which grows the box a row.
	sessionCreateChrome = 6
	// sessionCreateFooter names what ⏎ does when nothing blocks it. The ↑↓ half
	// is dropped when the Agent row is the only one that can be changed, since a
	// footer advertising a key that does nothing is the dead-key the context
	// menu's dimmed rows exist to avoid.
	sessionCreateFooter     = "↑↓ field   ←→ change   ⏎ create   esc cancel"
	sessionCreateFooterOnly = "←→ change   ⏎ create   esc cancel"
	// sessionCreateLabelW is the label column, sized to the longer of the two
	// labels so both rows' arrows start in the same place.
	sessionCreateLabelW = len("Account")
	// The pieces a cycler row spends outside its value cell. Named so the budget
	// in accountValue and the line built in renderRow measure the same thing.
	sessionCreateGap   = "   " // between the label and the lead arrow
	sessionCreateLead  = "◂ "
	sessionCreateTrail = " ▸"
)

// sessionCreateRowChrome is how many COLUMNS a cycler row spends before its
// value: the label column plus the gap and both arrows.
//
// lipgloss.Width, never len: ◂ and ▸ are 3-byte runes but one column each, so a
// byte count budgets the value cell 4 columns narrower than the row it is laid
// into — the account label loses that much truncation headroom, and its ▸ stops
// short of the column the rest of the box uses. It is also the one measurement
// in this file that was not already going through lipgloss.Width.
var sessionCreateRowChrome = sessionCreateLabelW +
	lipgloss.Width(sessionCreateGap+sessionCreateLead+sessionCreateTrail)

// View renders the dialog.
func (d *SessionCreateDialog) View() string {
	boxW := sessionCreateWidth
	if maxW := d.width - 4; boxW > maxW {
		boxW = maxW
	}
	if boxW < 30 {
		boxW = 30
	}
	inner := max(boxW-sessionCreateChrome, 1)

	var b strings.Builder
	b.WriteString(TitleStyle.Render("New Session"))
	b.WriteString("\n\n")
	repoW := max(inner-len("Repo: "), 1)
	b.WriteString(DimStyle.Render("Repo: ") + ansi.Truncate(d.repoPath, repoW, "…") + "\n\n")

	b.WriteString(d.renderRow("Agent", d.agent.DisplayName(), inner,
		d.focus == focusCreateAgent, true))
	if len(d.accounts) > 0 {
		b.WriteString("\n")
		b.WriteString(d.renderRow("Account", d.accountValue(inner), inner,
			d.focus == focusCreateAccount, d.accountRowActive()))
	}

	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", min(34, inner))))
	b.WriteString("\n")
	b.WriteString(DimStyle.Render(ansi.Truncate(d.footer(), inner, "…")))

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center,
		DialogStyle.Width(boxW).Render(b.String()))
}

// footer names what ⏎ does now, and how to reach whatever is stopping it.
//
// The blocked form replaces the whole hint rather than sitting beside it: a
// footer that still read "⏎ create" while ⏎ refused is exactly the disagreement
// submitBlocker exists to prevent, which is also why the swap is deliberately
// NOT gated on focus — the blocked account is still the one that would be used
// wherever the highlight happens to be.
//
// But it must name a key that works from where the highlight IS. With focus on
// the Agent row, "←→ pick another" pointed at the agent cycler, and dropping the
// ↑↓ half left nothing on screen saying how to get back to the row the message
// was about. So the route changes with the focus, the same rule the context
// menu's shortcuts keep.
func (d *SessionCreateDialog) footer() string {
	if blocker := d.submitBlocker(); blocker != "" {
		if d.focus == focusCreateAccount {
			return blocker + " — ←→ pick another"
		}
		return blocker + " — ↓ then ←→ pick another"
	}
	if d.accountRowActive() {
		return sessionCreateFooter
	}
	return sessionCreateFooterOnly
}

// accountValue lays the highlighted account's name and state across the value
// cell, so the state right-aligns and the trailing ▸ never moves as you cycle.
//
// Deliberately unlike the Agent row, whose short values are left as they were:
// this cell carries two fields, and a state column that slid about would be
// unreadable next to a name column that also changes width.
func (d *SessionCreateDialog) accountValue(inner int) string {
	r, ok := d.selectedAccount()
	if !ok {
		return ""
	}
	if !d.accountRowActive() {
		// Names the clause that actually failed, matching the context menu's
		// wording for the same one — a row reading "logged out" on a Codex
		// session would be true and completely beside the point.
		//
		// The account itself is deliberately NOT shown beside it: "yuval@brizz.ai
		// Claude only" reads as "this session runs on yuval@brizz.ai, which is
		// Claude-only", the opposite of what happens. The selection is still
		// remembered and reappears the moment the agent cycles back to Claude.
		return "Claude only"
	}
	state := accountStateCell(r)

	// The value cell is what's left after the label, the two arrows and their
	// spaces. Budget off raw text and pad before styling — padding a styled
	// string counts the ANSI bytes and the columns come out ragged.
	width := max(inner-sessionCreateRowChrome, 1)
	const gap = 2
	nameW := max(width-lipgloss.Width(state)-gap, 1)
	name := r.label
	if lipgloss.Width(name) > nameW {
		name = ansi.Truncate(name, nameW, "…")
	}
	pad := max(gap, width-lipgloss.Width(name)-lipgloss.Width(state))
	return name + strings.Repeat(" ", pad) + state
}

// renderRow draws one "Label  ◂ value ▸" line, in the Settings detail pane's
// idiom: the row holding the highlight takes accent and weight, the rest go dim.
// An inactive row is dim whether or not it holds the highlight — it can't.
func (d *SessionCreateDialog) renderRow(label, value string, inner int, focused, active bool) string {
	labelStyle := lipgloss.NewStyle().Width(sessionCreateLabelW).Align(lipgloss.Left)
	var arrowStyle, valueStyle lipgloss.Style
	switch {
	case !active:
		labelStyle = labelStyle.Foreground(ColorTextDim)
		arrowStyle = DimStyle
		valueStyle = DimStyle
	case focused:
		labelStyle = labelStyle.Foreground(ColorAccent).Bold(true)
		arrowStyle = lipgloss.NewStyle().Foreground(ColorAccent)
		valueStyle = lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	default:
		labelStyle = labelStyle.Foreground(ColorText)
		arrowStyle = DimStyle
		valueStyle = DimStyle
	}
	line := labelStyle.Render(label) + sessionCreateGap +
		arrowStyle.Render(sessionCreateLead) +
		valueStyle.Render(value) +
		arrowStyle.Render(sessionCreateTrail)
	return ansi.Truncate(line, inner, "…")
}
