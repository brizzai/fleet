package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/jira"
	"github.com/brizzai/fleet/internal/ticket"
)

func jiraDialog(t *testing.T) *ConnectJiraDialog {
	t.Helper()
	d := NewConnectJiraDialog()
	d.SetSize(120, 40)
	// Show() reads the live credential state, which would open straight onto
	// the done screen on a connected machine. The form is what these tests are
	// about, so it is entered directly.
	d.visible = true
	d.stage = connectPasting
	d.setFocus(jiraFieldSite)
	return d
}

func pressJira(d *ConnectJiraDialog, key string) *ConnectJiraDialog {
	t := tea.KeyPressMsg{Code: connectKeyCode(key)}
	out, _ := d.Update(t)
	return out
}

// TestJiraCaretAndHighlightNeverCoexist pins the focus rule this dialog shares
// with every other picker in fleet: exactly one thing is highlighted, and the
// caret lives with it.
//
// Three fields make it easier to get wrong than Linear's one — a caret left
// blinking in the token box while ▸ sits on the site row would mean the dialog
// has stopped saying what Enter does.
func TestJiraCaretAndHighlightNeverCoexist(t *testing.T) {
	d := jiraDialog(t)

	for _, want := range []int{jiraFieldSite, jiraFieldEmail, jiraFieldToken} {
		if d.focus != want {
			t.Fatalf("focus = %d, want %d", d.focus, want)
		}
		for i := range d.inputs {
			focused := d.inputs[i].Focused()
			if focused != (i == want) {
				t.Errorf("field %d focused = %v while the highlight is on %d", i, focused, want)
			}
		}
		if want != jiraFieldToken {
			d = pressJira(d, "down")
		}
	}

	// And back up, symmetrically.
	d = pressJira(d, "up")
	if d.focus != jiraFieldEmail || !d.inputs[jiraFieldEmail].Focused() {
		t.Error("↑ must move the highlight and the caret together")
	}
}

// TestJiraEnterAdvancesThenSubmits: in a three-field form, Enter that sometimes
// submits and sometimes moves would make one keystroke mean two things
// depending on where you happened to be.
func TestJiraEnterAdvancesThenSubmits(t *testing.T) {
	d := jiraDialog(t)
	d.inputs[jiraFieldSite].SetValue("acme")
	d.inputs[jiraFieldEmail].SetValue("you@example.com")
	d.inputs[jiraFieldToken].SetValue("ATATTsecret")

	d = pressJira(d, "enter")
	if d.focus != jiraFieldEmail || d.stage != connectPasting {
		t.Fatalf("Enter on the site field should advance, got focus=%d stage=%v", d.focus, d.stage)
	}
	d = pressJira(d, "enter")
	if d.focus != jiraFieldToken || d.stage != connectPasting {
		t.Fatalf("Enter on the email field should advance, got focus=%d stage=%v", d.focus, d.stage)
	}
	d = pressJira(d, "enter")
	if d.stage != connectWorking {
		t.Errorf("Enter on the last field should submit, got stage %v", d.stage)
	}
}

// TestJiraTokenIsNeverEchoed pins that the pasted credential is masked. It
// reaches a terminal others can see, and it is exactly the sort of thing that
// ends up in a screenshot attached to a bug report.
func TestJiraTokenIsNeverEchoed(t *testing.T) {
	const secret = "ATATT3xFfGF0SuperSecretValue123"
	d := jiraDialog(t)
	d.inputs[jiraFieldSite].SetValue("acme.atlassian.net")
	d.inputs[jiraFieldEmail].SetValue("you@example.com")
	d.inputs[jiraFieldToken].SetValue(secret)

	if got := d.View(); strings.Contains(got, secret) {
		t.Fatalf("the dialog rendered the token in plain text:\n%s", got)
	}
	if d.inputs[jiraFieldToken].Value() != secret {
		t.Error("masking must not damage the value that gets submitted")
	}
	// The site and the email are not secrets and must stay readable — a masked
	// site field is how someone spends five minutes not noticing a typo.
	if !strings.Contains(d.View(), "acme.atlassian.net") {
		t.Error("the site should render in plain text")
	}
}

// TestJiraRefusesIncompleteCredential: a missing part must name ITS OWN field
// and put the highlight there, rather than firing a round trip that comes back
// as a generic rejection.
func TestJiraRefusesIncompleteCredential(t *testing.T) {
	cases := []struct {
		name       string
		site       string
		email      string
		token      string
		wantFocus  int
		wantSaysID string
	}{
		{"no site", "", "you@example.com", "ATATTx", jiraFieldSite, "site"},
		{"bad site", "://", "you@example.com", "ATATTx", jiraFieldSite, "site address"},
		{"no email", "acme", "", "ATATTx", jiraFieldEmail, "email"},
		{"no token", "acme", "you@example.com", "", jiraFieldToken, "token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := jiraDialog(t)
			d.inputs[jiraFieldSite].SetValue(c.site)
			d.inputs[jiraFieldEmail].SetValue(c.email)
			d.inputs[jiraFieldToken].SetValue(c.token)
			d.setFocus(jiraFieldToken)

			d, cmd := d.submit()
			if cmd != nil {
				// submit returns textinput.Blink on refusal, never a network
				// command — the distinguishing signal is the stage.
				if d.stage == connectWorking {
					t.Fatal("an incomplete credential started a round trip")
				}
			}
			if d.err == nil {
				t.Fatal("an incomplete credential must say what is missing")
			}
			if !strings.Contains(strings.ToLower(d.err.Error()), c.wantSaysID) {
				t.Errorf("error %q should name the missing part (%q)", d.err, c.wantSaysID)
			}
			if d.focus != c.wantFocus {
				t.Errorf("focus = %d, want the field that is wrong (%d)", d.focus, c.wantFocus)
			}
		})
	}
}

// TestJiraNormalizesTheSiteBeforeSubmitting: the credential must carry a bare
// host, because baseURL is rebuilt from it and anything left over would be a
// way to point a credentialed request somewhere else.
func TestJiraNormalizesTheSiteBeforeSubmitting(t *testing.T) {
	d := jiraDialog(t)
	d.inputs[jiraFieldSite].SetValue("https://acme.atlassian.net/jira/software/projects/OPS/boards/1")
	d.inputs[jiraFieldEmail].SetValue("you@example.com")
	d.inputs[jiraFieldToken].SetValue("ATATTx")
	d.setFocus(jiraFieldToken)

	d, _ = d.submit()
	if d.stage != connectWorking {
		t.Fatalf("stage = %v, want the verification to have started; err=%v", d.stage, d.err)
	}
	site, err := jira.NormalizeSite(d.inputs[jiraFieldSite].Value())
	if err != nil || site != "acme.atlassian.net" {
		t.Errorf("NormalizeSite = (%q, %v)", site, err)
	}
}

// TestJiraCloudOnlyDiagnosis: /rest/api/3 exists on Cloud and nowhere else, so
// a 404 from verification almost always means the site is Server or Data
// Center. Reporting that as "issue not found" would be baffling on a screen
// with no issue on it.
func TestJiraCloudOnlyDiagnosis(t *testing.T) {
	got := jiraConnectErrorLine(ticket.ErrNotFound)
	if !strings.Contains(got, "Cloud") {
		t.Errorf("a 404 at connect time should name the Cloud-only limit, got %q", got)
	}
	got = jiraConnectErrorLine(ticket.ErrNotAuthenticated)
	if !strings.Contains(strings.ToLower(got), "email") {
		t.Errorf("a rejection should point at the email/token pair, got %q", got)
	}
	if jiraConnectErrorLine(nil) != "" {
		t.Error("no error should render nothing")
	}
	// Anything else is passed through rather than replaced by a guess.
	other := fmt.Errorf("dial tcp: no such host")
	if got := jiraConnectErrorLine(other); got != other.Error() {
		t.Errorf("an unrecognized error should be shown as-is, got %q", got)
	}
}

// TestJiraDisconnectRefusesEnvCredential: disconnecting a credential fleet does
// not own would be a lie — environment variables can only be unset in the shell
// that set them.
func TestJiraDisconnectRefusesEnvCredential(t *testing.T) {
	d := jiraDialog(t)
	d.stage = connectDone
	d.via = "environment (" + jira.SiteEnvVar + ")"

	d = pressJira(d, "d")
	if d.err == nil || !strings.Contains(d.err.Error(), jira.SiteEnvVar) {
		t.Errorf("err = %v, want it to name the variable the user has to unset", d.err)
	}
	if !d.IsVisible() {
		t.Error("refusing must not close the dialog")
	}
}

// TestJiraEscCancelsVerification pins the promise the footer makes.
//
// `esc` used to hide the dialog and leave the round trip running, so a
// verification that succeeded afterwards called jira.SetCredential anyway —
// while the footer had said "esc: cancel" the whole time. That is the exact
// failure ConnectLinearDialog.abortSignIn exists for: the difference between "I
// changed my mind" and "I changed my mind and it happened anyway".
func TestJiraEscCancelsVerification(t *testing.T) {
	d := jiraDialog(t)
	d.inputs[jiraFieldSite].SetValue("acme")
	d.inputs[jiraFieldEmail].SetValue("you@example.com")
	d.inputs[jiraFieldToken].SetValue("ATATTsecret")
	d.setFocus(jiraFieldToken)

	d, cmd := d.submit()
	if d.stage != connectWorking || cmd == nil {
		t.Fatalf("verification did not start: stage=%v", d.stage)
	}
	if d.cancelVerify == nil {
		t.Fatal("the dialog must hold the cancel func, or esc has nothing to cancel")
	}

	d = pressJira(d, "esc")
	if d.IsVisible() {
		t.Error("esc should hide the dialog")
	}
	if d.cancelVerify != nil {
		t.Error("Hide must clear the cancel func after calling it")
	}
}

// TestJiraCancelledVerificationIsNotReportedAsRejection: the command surfaces
// cancellation as an error, and announcing "rejected — check the email and
// token" for something the user chose is the dialog arguing with them.
func TestJiraCancelledVerificationIsNotReportedAsRejection(t *testing.T) {
	d := jiraDialog(t)
	d.stage = connectWorking

	d, _ = d.Update(jiraConnectFailedMsg{err: context.Canceled})
	if d.err != nil {
		t.Errorf("a cancelled verification reported an error: %v", d.err)
	}
}
