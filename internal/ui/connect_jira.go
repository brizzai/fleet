package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/jira"
	"github.com/brizzai/fleet/internal/ticket"
)

// connectJiraTimeout bounds the verification round trips — /myself and the
// project list. Two small REST calls; past this the network is the problem, not
// the token.
const connectJiraTimeout = 20 * time.Second

// jiraConnectedMsg lands when a credential has been verified.
//
// persistErr is carried separately from a connect failure for the same reason
// Linear's is: the credential is live in memory the moment it verifies, so a
// keychain that refuses to store it costs you the NEXT launch, not this one.
type jiraConnectedMsg struct {
	account    ticket.Account
	via        string
	persistErr error
}

// jiraConnectFailedMsg lands when it hasn't.
type jiraConnectFailedMsg struct{ err error }

// jiraDisconnectedMsg closes the loop after a disconnect. It carries the error
// because Disconnect can genuinely fail — a denied keychain prompt is the
// ordinary case — and a discarded failure would let the dialog reopen showing
// "✓ connected" with nothing explaining why.
type jiraDisconnectedMsg struct{ err error }

// The three fields, in the order they render and in the order someone fills
// them: where, who, and the secret.
const (
	jiraFieldSite  = 0
	jiraFieldEmail = 1
	jiraFieldToken = 2
	jiraFieldCount = 3
)

// ConnectJiraDialog connects fleet to a Jira Cloud site.
//
// One path, unlike Linear's two. Atlassian's OAuth 2.0 (3LO) needs an app that
// brizz would have to register and own forever, and in many organizations a
// per-site admin has to approve each install — for a credential a user can mint
// themselves in two clicks. The API token also works over SSH and in CI, which
// is the case Linear needs its paste path for anyway.
//
// Three fields rather than one, because an Atlassian API token is only half a
// credential: it is scoped to an account, not to a site, and it identifies
// nobody on its own.
type ConnectJiraDialog struct {
	visible bool
	width   int
	height  int

	stage connectStage
	// focus indexes the fields. Exactly one highlight, and the caret lives with
	// it — the same rule SnoozeDialog and ConnectLinearDialog follow.
	focus  int
	inputs [jiraFieldCount]textinput.Model

	err        error
	persistErr error
	account    ticket.Account
	via        string
}

func NewConnectJiraDialog() *ConnectJiraDialog {
	d := &ConnectJiraDialog{}

	site := NewTextInput()
	site.Placeholder = "acme.atlassian.net"
	site.CharLimit = 200
	site.SetWidth(44)

	email := NewTextInput()
	email.Placeholder = "you@example.com"
	email.CharLimit = 200
	email.SetWidth(44)

	token := NewTextInput()
	token.Placeholder = "ATATT…"
	token.CharLimit = 400
	token.SetWidth(44)
	// The token is a credential: it must not sit in plain text on a screen that
	// gets shared, photographed, or pasted into a bug report.
	token.EchoMode = textinput.EchoPassword

	d.inputs = [jiraFieldCount]textinput.Model{site, email, token}
	return d
}

func (d *ConnectJiraDialog) IsVisible() bool { return d.visible }

func (d *ConnectJiraDialog) SetSize(w, h int) { d.width, d.height = w, h }

func (d *ConnectJiraDialog) Show() {
	d.visible = true
	d.err, d.persistErr = nil, nil
	for i := range d.inputs {
		d.inputs[i].SetValue("")
		d.inputs[i].Blur()
	}
	d.account, _ = jira.New().Account()
	d.via = jira.ConnectedVia()
	if d.via != "" {
		d.stage = connectDone
		return
	}
	// The site usually survives a disconnect-and-reconnect unchanged, and it is
	// the field nobody remembers the exact spelling of. Seeding it is the one
	// piece of state worth carrying across.
	if site := jira.StoredSite(); site != "" {
		d.inputs[jiraFieldSite].SetValue(site)
	}
	d.stage = connectPasting
	d.setFocus(jiraFieldSite)
}

func (d *ConnectJiraDialog) Hide() {
	d.visible = false
	for i := range d.inputs {
		d.inputs[i].Blur()
	}
}

// setFocus is the single writer of focus, so the caret can never be left
// blinking on a row the highlight has moved off.
func (d *ConnectJiraDialog) setFocus(i int) {
	if i < 0 {
		i = 0
	}
	if i >= jiraFieldCount {
		i = jiraFieldCount - 1
	}
	d.focus = i
	for n := range d.inputs {
		if n == i {
			d.inputs[n].Focus()
		} else {
			d.inputs[n].Blur()
		}
	}
}

func (d *ConnectJiraDialog) Update(msg tea.Msg) (*ConnectJiraDialog, tea.Cmd) {
	switch m := msg.(type) {
	case jiraConnectedMsg:
		d.stage, d.account, d.via = connectDone, m.account, m.via
		d.err, d.persistErr = nil, m.persistErr
		return d, nil
	case jiraDisconnectedMsg:
		// Disconnect clears the in-memory credential before it touches the
		// store, so this session really is disconnected either way. What a
		// failure means is narrower and easier to miss: the credential is still
		// on disk, so it comes back at the next launch. Say exactly that.
		if m.err != nil {
			d.err = fmt.Errorf("disconnected here, but the stored credential could not be removed "+
				"— it will come back on the next launch: %w", m.err)
		}
		return d, nil
	case jiraConnectFailedMsg:
		// Back to the fields that produced it, so the fix is one keystroke away
		// rather than one navigation away.
		d.err = m.err
		if d.stage == connectWorking {
			d.stage = connectPasting
			d.setFocus(d.focus)
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
			// Disconnecting a credential fleet does not own would be a lie:
			// environment variables can only be unset in the shell that set
			// them.
			if strings.HasPrefix(d.via, "environment") {
				d.err = fmt.Errorf("this credential comes from %s — unset it in your shell", jira.SiteEnvVar)
				return d, nil
			}
			return d, func() tea.Msg {
				return jiraDisconnectedMsg{err: jira.Disconnect()}
			}
		}
		return d, nil

	default: // connectPasting
		switch keyMsg.String() {
		case "esc":
			d.Hide()
			return d, nil
		case "up", "shift+tab":
			d.setFocus(d.focus - 1)
			return d, textinput.Blink
		case "down", "tab":
			d.setFocus(d.focus + 1)
			return d, textinput.Blink
		case "enter":
			// Enter on any field but the last advances rather than submits: a
			// three-field form where Enter sometimes submits and sometimes
			// moves would make the same keystroke mean two things depending on
			// where you happened to be.
			if d.focus < jiraFieldToken {
				d.setFocus(d.focus + 1)
				return d, textinput.Blink
			}
			return d.submit()
		}
		var cmd tea.Cmd
		d.inputs[d.focus], cmd = d.inputs[d.focus].Update(msg)
		return d, cmd
	}
}

// submit validates what is on screen and starts verification.
//
// The site is normalized here rather than in the command so a malformed one is
// reported against the field the user is still looking at, and so the error
// names the field rather than arriving as a failed round trip.
func (d *ConnectJiraDialog) submit() (*ConnectJiraDialog, tea.Cmd) {
	rawSite := strings.TrimSpace(d.inputs[jiraFieldSite].Value())
	email := strings.TrimSpace(d.inputs[jiraFieldEmail].Value())
	token := strings.TrimSpace(d.inputs[jiraFieldToken].Value())

	site, err := jira.NormalizeSite(rawSite)
	switch {
	case rawSite == "":
		d.err = fmt.Errorf("name your Jira site first")
		d.setFocus(jiraFieldSite)
		return d, textinput.Blink
	case err != nil:
		d.err = err
		d.setFocus(jiraFieldSite)
		return d, textinput.Blink
	case email == "":
		d.err = fmt.Errorf("your Atlassian account email is part of the credential")
		d.setFocus(jiraFieldEmail)
		return d, textinput.Blink
	case token == "":
		d.err = fmt.Errorf("paste an API token first")
		d.setFocus(jiraFieldToken)
		return d, textinput.Blink
	}

	d.stage = connectWorking
	d.err = nil
	for i := range d.inputs {
		d.inputs[i].Blur()
	}
	return d, verifyAndStoreJiraToken(jira.Credential{Site: site, Email: email, Token: token})
}

// verifyAndStoreJiraToken proves a credential works before anything is written.
//
// Verifying first is what lets the dialog say "connected" as a fact rather than
// a hope, and it means a typo is caught while the user is still looking at the
// field they typed it into — not three days later when a worktree quietly
// starts without its ticket.
func verifyAndStoreJiraToken(cred jira.Credential) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectJiraTimeout)
		defer cancel()

		acct, err := jira.VerifyCredential(ctx, cred)
		if err != nil {
			return jiraConnectFailedMsg{err: err}
		}
		// Not an error path: the credential is already live. See jiraConnectedMsg.
		persistErr := jira.SetCredential(cred)
		return jiraConnectedMsg{account: acct, via: jira.ConnectedVia(), persistErr: persistErr}
	}
}

func (d *ConnectJiraDialog) View() string {
	if !d.visible {
		return ""
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Connect Jira"))
	b.WriteString("\n\n")

	switch d.stage {
	case connectDone:
		b.WriteString(StatusRunningStyle.Render("✓ connected"))
		if d.account.Name != "" {
			b.WriteString("  " + d.account.Name)
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
			b.WriteString(DimStyle.Render("Jira works for this session; you'll reconnect after a restart."))
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("To make it stick, set " + jira.SiteEnvVar + ", " +
				jira.EmailEnvVar + " and " + jira.TokenEnvVar + " in your shell instead."))
			b.WriteString("\n")
		}
		if len(d.account.Keys) > 0 {
			b.WriteString("\n")
			b.WriteString(DimStyle.Render("Projects: " + strings.Join(firstN(d.account.Keys, 12), ", ")))
			b.WriteString("\n\n")
			// The per-repo step is the part people miss, so it is spelled out
			// rather than left to documentation: a connected fleet still does
			// nothing in a repo until that repo names a project.
			b.WriteString(DimStyle.Render("Turn a repo on by naming its project in .fleet.local.json:"))
			b.WriteString("\n")
			b.WriteString(DimStyle.Render(fmt.Sprintf(`  {"jira": {"project": %q}}`, d.account.Keys[0])))
			b.WriteString("\n")
		}

	case connectWorking:
		b.WriteString("Checking…")
		b.WriteString("\n")

	default: // connectPasting
		labels := [jiraFieldCount]string{"Site", "Atlassian account email", "API token"}
		for i := range d.inputs {
			label := labels[i]
			if i == d.focus {
				b.WriteString(SessionSelectedStyle.Render("▸ " + label))
			} else {
				b.WriteString("  " + DimStyle.Render(label))
			}
			b.WriteString("\n")
			b.WriteString(d.inputs[i].View())
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(DimStyle.Render("id.atlassian.com → Security → Create and manage API tokens"))
		b.WriteString("\n")
		b.WriteString(DimStyle.Render("Jira Cloud only. A Server or Data Center site will be refused."))
		b.WriteString("\n")
	}

	if d.err != nil {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render(jiraConnectErrorLine(d.err)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render(d.footer()))

	dialogWidth := clampInt(d.width-4, 40, 68)
	box := DialogStyle.Width(dialogWidth).Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

func (d *ConnectJiraDialog) footer() string {
	switch d.stage {
	case connectDone:
		return "d: disconnect • esc: close"
	case connectWorking:
		return "esc: cancel"
	}
	if d.focus < jiraFieldToken {
		return "⏎ next field • ↑↓/tab: move • esc: cancel"
	}
	return "⏎ connect • ↑↓/tab: move • esc: cancel"
}

// jiraConnectErrorLine turns a failure into something a user can act on.
//
// The 404 branch is the one that earns its keep: /rest/api/3 exists on Cloud
// and nowhere else, so "not found" from the verification call almost always
// means the site is Server or Data Center — and reporting that as "issue not
// found" would be baffling on a screen with no issue on it.
func jiraConnectErrorLine(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ticket.ErrNotFound):
		return "That site didn't answer the Cloud API — fleet supports Jira Cloud only."
	case errors.Is(err, ticket.ErrNotAuthenticated):
		return "Rejected — check the email and token. The email must be the Atlassian account's."
	}
	return err.Error()
}

// firstN bounds a list rendered on one line. A real Jira site has hundreds of
// projects and the whole point of the line is the example underneath it.
func firstN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(in[:n:n], "…")
}
