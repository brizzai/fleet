package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/linear"
)

// connectLinearTimeout bounds the verification round trip. One GraphQL call,
// measured at ~260ms; past this the network is the problem, not the key.
const connectLinearTimeout = 20 * time.Second

// linearConnectedMsg lands when a credential has been verified.
//
// persistErr is carried separately from a connect failure on purpose: the
// credential is live in memory the moment it verifies, so a keychain that
// refuses to store it costs you the NEXT launch, not this one. Reporting that
// as "connect failed" contradicted the code and sent people back to re-paste a
// key that was already working.
type linearConnectedMsg struct {
	workspace  linear.Workspace
	via        string
	persistErr error
}

// linearConnectFailedMsg lands when it hasn't.
type linearConnectFailedMsg struct{ err error }

// connectStage is which half of the dialog is showing.
type connectStage int

const (
	// connectChoosing shows the two ways in. It is the first thing you see,
	// because picking the method is a real decision: browser sign-in is shorter,
	// but over SSH it cannot work at all.
	connectChoosing connectStage = iota
	connectPasting
	connectWorking
	connectDone
)

// connect rows, in the order they render.
const (
	connectRowBrowser = 0
	connectRowPaste   = 1
	connectRowCount   = 2
)

// ConnectLinearDialog connects fleet to Linear.
//
// Two paths on purpose. Browser sign-in is the shorter one; pasting a personal
// API key is the one that works over SSH, in CI, where a workspace admin has
// disabled OAuth installs, and where the user wants to grant read-only or
// team-scoped access rather than whatever this app happens to ask for.
type ConnectLinearDialog struct {
	visible bool
	width   int
	height  int

	stage connectStage
	// focus indexes the method rows while choosing. There is exactly one
	// highlight and the caret lives with it — the same rule SnoozeDialog
	// follows, because a dialog with a blinking caret in one place and a ▸ in
	// another has stopped saying what Enter does.
	focus int
	input textinput.Model

	err        error
	persistErr error
	workspace  linear.Workspace
	via        string
}

func NewConnectLinearDialog() *ConnectLinearDialog {
	ti := NewTextInput()
	ti.Placeholder = "lin_api_…"
	ti.CharLimit = 200
	ti.SetWidth(44)
	// The key is a credential: it must not sit in plain text on a screen that
	// gets shared, photographed, or pasted into a bug report.
	ti.EchoMode = textinput.EchoPassword
	return &ConnectLinearDialog{input: ti}
}

func (d *ConnectLinearDialog) IsVisible() bool { return d.visible }

func (d *ConnectLinearDialog) SetSize(w, h int) { d.width, d.height = w, h }

func (d *ConnectLinearDialog) Show() {
	d.visible = true
	d.err, d.persistErr = nil, nil
	d.input.SetValue("")
	d.input.Blur()
	d.workspace, _ = linear.WorkspaceInfo()
	d.via = linear.ConnectedVia()
	if d.via != "" {
		d.stage = connectDone
		return
	}
	d.stage = connectChoosing
	d.setFocus(connectRowBrowser)
}

func (d *ConnectLinearDialog) Hide() {
	d.visible = false
	d.input.Blur()
}

// setFocus is the single writer of focus, so the caret can never be left
// blinking on a row the highlight has moved off.
func (d *ConnectLinearDialog) setFocus(i int) {
	if i < 0 {
		i = 0
	}
	if i >= connectRowCount {
		i = connectRowCount - 1
	}
	d.focus = i
}

func (d *ConnectLinearDialog) Update(msg tea.Msg) (*ConnectLinearDialog, tea.Cmd) {
	switch m := msg.(type) {
	case linearConnectedMsg:
		d.stage, d.workspace, d.via = connectDone, m.workspace, m.via
		d.err, d.persistErr = nil, m.persistErr
		return d, nil
	case linearConnectFailedMsg:
		// Back to the field that produced it, so the fix is one keystroke away
		// rather than one navigation away.
		d.err = m.err
		if d.stage == connectWorking {
			d.stage = connectPasting
			d.input.Focus()
		}
		return d, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch d.stage {
	case connectWorking:
		if keyMsg.String() == "esc" {
			d.Hide()
		}
		return d, nil

	case connectDone:
		switch keyMsg.String() {
		case "esc", "enter", "q":
			d.Hide()
		case "d":
			// Disconnecting a credential fleet does not own would be a lie: an
			// environment variable can only be unset in the shell that set it.
			if strings.HasPrefix(d.via, "environment") {
				d.err = fmt.Errorf("this credential comes from %s — unset it in your shell", linear.APIKeyEnvVar)
				return d, nil
			}
			return d, func() tea.Msg {
				_ = linear.Disconnect()
				return linearDisconnectedMsg{}
			}
		}
		return d, nil

	case connectPasting:
		switch keyMsg.String() {
		case "esc":
			d.stage = connectChoosing
			d.input.Blur()
			d.err = nil
			return d, nil
		case "enter":
			key := strings.TrimSpace(d.input.Value())
			if key == "" {
				d.err = fmt.Errorf("paste a key first")
				return d, nil
			}
			d.stage = connectWorking
			d.err = nil
			d.input.Blur()
			return d, verifyAndStoreLinearKey(key)
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return d, cmd

	default: // connectChoosing
		switch keyMsg.String() {
		case "esc":
			d.Hide()
		case "up", "shift+tab", "k":
			d.setFocus(d.focus - 1)
		case "down", "tab", "j":
			d.setFocus(d.focus + 1)
		case "enter":
			if d.focus == connectRowBrowser {
				if !linear.OAuthConfigured() {
					d.err = fmt.Errorf("this build carries no OAuth app — paste a key instead")
					d.setFocus(connectRowPaste)
					return d, nil
				}
				d.stage = connectWorking
				d.err = nil
				return d, signInToLinear()
			}
			d.stage = connectPasting
			d.err = nil
			d.input.Focus()
			return d, textinput.Blink
		}
		return d, nil
	}
}

// linearDisconnectedMsg closes the loop after a disconnect.
type linearDisconnectedMsg struct{}

// verifyAndStoreLinearKey proves a key works before anything is written.
//
// Verifying first is what lets the dialog say "connected" as a fact rather than
// a hope, and it means a typo is caught while the user is still looking at the
// field they typed it into — not three days later when a worktree quietly
// starts without its ticket.
func verifyAndStoreLinearKey(key string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectLinearTimeout)
		defer cancel()

		cred := linear.Credential{Kind: linear.KindAPIKey, Token: key}
		ws, err := linear.VerifyCredential(ctx, cred)
		if err != nil {
			return linearConnectFailedMsg{err: err}
		}
		cred.Workspace = ws.Name
		// Not an error path: the credential is already live. See linearConnectedMsg.
		persistErr := linear.SetCredential(cred)
		return linearConnectedMsg{workspace: ws, via: linear.ConnectedVia(), persistErr: persistErr}
	}
}

func signInToLinear() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		cred, err := linear.SignIn(ctx)
		if err != nil {
			return linearConnectFailedMsg{err: err}
		}
		ws, err := linear.VerifyCredential(ctx, cred)
		if err != nil {
			return linearConnectFailedMsg{err: err}
		}
		cred.Workspace = ws.Name
		persistErr := linear.SetCredential(cred)
		return linearConnectedMsg{workspace: ws, via: linear.ConnectedVia(), persistErr: persistErr}
	}
}

func (d *ConnectLinearDialog) View() string {
	if !d.visible {
		return ""
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Connect Linear"))
	b.WriteString("\n\n")

	switch d.stage {
	case connectDone:
		b.WriteString(StatusRunningStyle.Render("✓ connected"))
		if d.workspace.Name != "" {
			b.WriteString("  " + d.workspace.Name)
		}
		b.WriteString("\n")
		if d.via != "" {
			b.WriteString(DimStyle.Render("via " + d.via))
			b.WriteString("\n")
		}
		if d.persistErr != nil {
			b.WriteString("\n")
			b.WriteString(ErrorStyle.Render("⚠ couldn't save it: " + d.persistErr.Error()))
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("Linear works for this session; you'll reconnect after a restart."))
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("To make it stick, set " + linear.APIKeyEnvVar + " in your shell instead."))
			b.WriteString("\n")
		}
		if len(d.workspace.TeamKeys) > 0 {
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("Teams: " + strings.Join(d.workspace.TeamKeys, ", ")))
			b.WriteString("\n\n")
			// The per-repo step is the part people miss, so it is spelled out
			// rather than left to documentation: a connected fleet still does
			// nothing in a repo until that repo names a team.
			b.WriteString(DimStyle.Render("Turn a repo on by naming its team in .fleet.local.json:"))
			b.WriteString("\n")
			b.WriteString(DimStyle.Render(fmt.Sprintf(`  {"linear": {"team": %q}}`, d.workspace.TeamKeys[0])))
			b.WriteString("\n")
		}

	case connectWorking:
		b.WriteString("Checking…")
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("If a browser opened, approve fleet there."))
		b.WriteString("\n")

	case connectPasting:
		b.WriteString(DimStyle.Render("API key"))
		b.WriteString("\n")
		b.WriteString(d.input.View())
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("linear.app → Settings → Security & access → New API key"))
		b.WriteString("\n")
		b.WriteString(DimStyle.Render("Grant Read and Write. You can scope it to specific teams."))
		b.WriteString("\n")

	default:
		// The browser row names WHICH workspace it will connect, because that
		// is the one thing about it a user cannot control from here and will
		// otherwise get wrong: Linear's consent screen targets whatever
		// workspace the browser is currently in, so signing in from the wrong
		// one yields a credential that works perfectly and can see none of
		// your issues.
		rows := []struct{ label, detail string }{
			{"Sign in with Linear", "connects the workspace your browser is in"},
			{"Paste an API key", "works over SSH; scoped to that key"},
		}
		for i, r := range rows {
			marker := "  "
			label := r.label
			if i == d.focus {
				marker = SessionSelectedStyle.Render("▸ ")
				label = SessionSelectedStyle.Render(label)
			}
			b.WriteString(marker + label + "\n")
			b.WriteString(DimStyle.Render("    " + r.detail))
			b.WriteString("\n")
		}
	}

	if d.err != nil {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render(connectErrorLine(d.err)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render(d.footer()))

	dialogWidth := clampInt(d.width-4, 40, 68)
	box := DialogStyle.Width(dialogWidth).Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

func (d *ConnectLinearDialog) footer() string {
	switch d.stage {
	case connectDone:
		return "d: disconnect • esc: close"
	case connectWorking:
		return "esc: cancel"
	case connectPasting:
		return "enter: connect • esc: back"
	}
	return "↑↓: choose • enter: select • esc: cancel"
}

// connectErrorLine turns a failure into something a user can act on.
//
// Each branch names the next move, because every one of these has a different
// fix and "couldn't connect" would send all of them to the same dead end.
func connectErrorLine(err error) string {
	switch {
	case err == nil:
		return ""
	case strings.Contains(err.Error(), "unavailable here"):
		return "No browser available — paste an API key instead."
	}
	return err.Error()
}
