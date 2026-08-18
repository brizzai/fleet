package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/linear"
)

func connectDialog(t *testing.T) *ConnectLinearDialog {
	t.Helper()
	d := NewConnectLinearDialog()
	d.SetSize(120, 40)
	d.stage = connectChoosing
	d.visible = true
	d.setFocus(connectRowBrowser)
	return d
}

func pressConnect(d *ConnectLinearDialog, key string) *ConnectLinearDialog {
	out, _ := d.Update(tea.KeyPressMsg{Code: connectKeyCode(key), Text: key})
	return out
}

// connectKeyCode maps the handful of keys these tests send. Named keys carry no
// Text, which is exactly how the dialog distinguishes them.
func connectKeyCode(key string) rune {
	switch key {
	case "down":
		return tea.KeyDown
	case "up":
		return tea.KeyUp
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEscape
	}
	return rune(key[0])
}

// TestConnectCaretAndHighlightNeverCoexist pins the focus rule this dialog
// shares with the snooze and worktree dialogs: exactly one thing is highlighted,
// and the caret lives with it. A ▸ on a method row while a caret blinks in the
// key field would mean the dialog has stopped saying what Enter does.
func TestConnectCaretAndHighlightNeverCoexist(t *testing.T) {
	d := connectDialog(t)
	if d.input.Focused() {
		t.Error("the key field must not hold the caret while the method rows own the highlight")
	}

	d = pressConnect(d, "down")
	if d.focus != connectRowPaste {
		t.Fatalf("focus = %d, want the paste row", d.focus)
	}
	if d.input.Focused() {
		t.Error("moving the highlight must not focus the field — the field is a later stage")
	}

	d = pressConnect(d, "enter")
	if d.stage != connectPasting {
		t.Fatalf("stage = %v, want connectPasting", d.stage)
	}
	if !d.input.Focused() {
		t.Error("once the field is the stage, it must own the caret")
	}

	// esc walks back a stage rather than closing, so a mistyped choice costs one
	// key, not the whole dialog.
	d = pressConnect(d, "esc")
	if d.stage != connectChoosing {
		t.Errorf("esc from the field should return to the chooser, got stage %v", d.stage)
	}
	if d.input.Focused() {
		t.Error("returning to the chooser must take the caret back out of the field")
	}
	if !d.IsVisible() {
		t.Error("esc from the field must not close the dialog")
	}
}

// TestConnectKeyIsNeverEchoed pins that the pasted credential is masked. It
// reaches a terminal others can see, and it is exactly the sort of thing that
// ends up in a screenshot attached to a bug report.
func TestConnectKeyIsNeverEchoed(t *testing.T) {
	const secret = "lin_api_SuperSecretValue123"
	d := connectDialog(t)
	d.stage = connectPasting
	d.input.Focus()
	d.input.SetValue(secret)

	if got := d.View(); strings.Contains(got, secret) {
		t.Fatalf("the dialog rendered the key in plain text:\n%s", got)
	}
	if d.input.Value() != secret {
		t.Error("masking must not damage the value that gets submitted")
	}
}

// TestConnectRefusesEmptyKey: submitting nothing must say so rather than firing
// a round trip that comes back as a generic rejection.
func TestConnectRefusesEmptyKey(t *testing.T) {
	d := connectDialog(t)
	d.stage = connectPasting
	d.input.Focus()

	d, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("an empty field must not start a verification round trip")
	}
	if d.err == nil {
		t.Error("an empty field must explain itself")
	}
	if d.stage != connectPasting {
		t.Error("an empty submit must leave you in the field you were typing into")
	}
}

// TestTicketTipStaysQuietUntilTheLookupFinishes covers the startup race.
//
// Available() answers false before the credential lookup has run, so a tip that
// keyed only on it would offer to connect Linear to a user who is already
// connected — for the first moments of every launch, on every ticket branch.
func TestTicketTipStaysQuietUntilTheLookupFinishes(t *testing.T) {
	h := &Home{}
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m["/repo"] = &git.RepoInfo{Branch: "brz-3182-fix-the-thing"}
		return true
	})
	if h.anyTicketShapedBranch() {
		t.Error("the tip must stay quiet until the credential lookup has resolved")
	}
}

// TestTicketShapedBranchMatchesRealBranches pins the looser pattern the tip
// uses. It is deliberately not the team-gated matcher — nobody has configured a
// team at the moment this tip should fire.
func TestTicketShapedBranchMatchesRealBranches(t *testing.T) {
	yes := []string{
		"brz-3182-magic-fix", "BRZ-3182", "alice/brz-1594-x", "eng-42", "prd-7-spec",
	}
	no := []string{
		"master", "main", "kinshasa", "frosty-mahavira", "release-2024-cleanup",
		"feature/add-the-thing", "brzctl-gcp-project-default",
	}
	for _, b := range yes {
		if !ticketShapedBranch.MatchString(b) {
			t.Errorf("%q should read as ticket work", b)
		}
	}
	for _, b := range no {
		if ticketShapedBranch.MatchString(b) {
			t.Errorf("%q must not read as ticket work — the tip would nag on ordinary branches", b)
		}
	}
}

// TestConnectWithoutOAuthAppFallsBackToPaste covers the state every build is in
// until an OAuth application is registered, and any fork is in permanently.
//
// Choosing browser sign-in with no client ID must say so and move the highlight
// onto the path that does work, rather than opening a browser at an authorize
// URL with an empty client_id and letting Linear produce the error.
func TestConnectWithoutOAuthAppFallsBackToPaste(t *testing.T) {
	if linear.OAuthConfigured() {
		t.Skip("this build carries an OAuth client ID")
	}
	d := connectDialog(t)

	d, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("with no OAuth app registered, choosing browser sign-in must not start anything")
	}
	if d.err == nil {
		t.Fatal("it must say why")
	}
	if d.focus != connectRowPaste {
		t.Error("the highlight must land on the path that actually works")
	}
	if d.stage != connectChoosing {
		t.Errorf("stage = %v, want to stay on the chooser", d.stage)
	}
}

// TestPersistFailureStillReportsConnected pins the distinction the first
// version got wrong.
//
// SetCredential makes the credential live before it tries to store it, so a
// keychain that refuses costs you the next launch, not this one. Reporting that
// as a connect failure sent people back to re-paste a key that was already
// working — and it contradicted the code's own stated posture.
func TestPersistFailureStillReportsConnected(t *testing.T) {
	d := connectDialog(t)
	d.stage = connectWorking

	d, _ = d.Update(linearConnectedMsg{
		workspace:  linear.Workspace{Name: "Brizz", TeamKeys: []string{"BRZ"}},
		via:        "API key",
		persistErr: errors.New("keychain write failed: signal: killed"),
	})

	if d.stage != connectDone {
		t.Fatalf("stage = %v, want connectDone — the credential works", d.stage)
	}
	if d.err != nil {
		t.Errorf("a persistence failure must not read as a connect failure, got %v", d.err)
	}

	got := d.View()
	if !strings.Contains(got, "connected") {
		t.Error("it must still say it is connected")
	}
	if !strings.Contains(got, "this session") {
		t.Errorf("it must say what was actually lost — the next launch, not this one:\n%s", got)
	}
	if !strings.Contains(got, linear.APIKeyEnvVar) {
		t.Errorf("it must name the way out that needs no keychain:\n%s", got)
	}
}
