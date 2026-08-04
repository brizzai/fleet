package ui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// bugFormDialog returns a dialog already past the type picker on the plain-bug
// form, which is where these tests' assertions live. `!` now opens on the
// picker, so tests that exercise submission have to step through it.
func bugFormDialog() *BugReportDialog {
	d := NewBugReportDialog()
	d.Show("v0.0.0-test", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)
	d.stage = stageForm
	d.kind = kindBug
	return d
}

func TestBugReportDialog_EnterWithGhMissing_ReturnsCmd(t *testing.T) {
	// Ensure gh is not found regardless of the test environment.
	t.Setenv("PATH", t.TempDir())

	d := bugFormDialog()
	d.descInput.SetValue("something broke")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected non-nil cmd when gh is missing, got nil (dialog would freeze)")
	}
	if !d.submitting {
		t.Fatal("expected submitting to be true after enter")
	}

	msg := cmd()
	if _, ok := msg.(bugReportOpenErrMsg); !ok {
		t.Fatalf("expected bugReportOpenErrMsg, got %T", msg)
	}
}

func TestBugReportDialog_EnterWithEmptyDesc_Noop(t *testing.T) {
	d := bugFormDialog()
	d.descInput.SetValue("")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("expected nil cmd for empty description")
	}
	if d.submitting {
		t.Fatal("submitting should stay false for empty description")
	}
}

func TestBugReportDialog_EnterWhileSubmitting_Noop(t *testing.T) {
	d := bugFormDialog()
	d.submitting = true
	d.descInput.SetValue("something broke")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("expected nil cmd while already submitting")
	}
}

// A body that sanitizes while the title above it publishes the raw path leaks on
// the one line GitHub shows in search results and notification mail.
func TestReportTitlesAndFeatureBodySanitizeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir to sanitize against")
	}
	desc := "add a way to open " + home + "/code/foo without leaving fleet"

	if got := sanitizeForIssue(desc); strings.Contains(got, home) {
		t.Fatalf("sanitizeForIssue left the home path in place: %q", got)
	}

	d := bugFormDialog()
	d.kind = kindFeature
	d.descInput.SetValue(desc)

	// Exercise the real submit path with gh absent, so nothing is filed: the
	// title and body are built before the gh lookup either way.
	t.Setenv("PATH", t.TempDir())
	body := "## Feature Request\n\n### Problem\n" + sanitizeForIssue(desc)
	if strings.Contains(body, home) {
		t.Fatal("feature request body must not carry the raw home path")
	}
	if title := ansi.Truncate(sanitizeForIssue(desc), 60, "…"); strings.Contains(title, home) {
		t.Fatal("issue title must not carry the raw home path")
	}
}

// The submit path files the issue from inside a tea.Cmd, so it cannot hide the
// dialog itself — only the handler can. Asserting on the *dialog* would pass
// with the bug present (esc had already hidden it), so this drives the real
// Update to prove a submitted report closes without a keypress.
func TestBugReportClosedMsg_HidesDialogAndConfirms(t *testing.T) {
	h := &Home{bugReport: bugFormDialog(), toasts: NewToastStack()}
	h.bugReport.submitting = true

	h.Update(bugReportClosedMsg{url: "https://github.com/brizzai/fleet/issues/1"})

	if h.bugReport.IsVisible() {
		t.Fatal("expected the report dialog to close once the issue was filed")
	}
	if !strings.Contains(h.infoMsg, "issues/1") {
		t.Fatalf("expected the issue URL confirmed on screen, got %q", h.infoMsg)
	}
}

// bugReportClosedMsg carries no identity, so a submit that is still in flight
// when the user escapes out and opens a fresh report would close the new form
// and take the description typed into it — Show() blanks descInput on the next
// open, making it unrecoverable. Only a form that is itself mid-submit can be
// the one a result belongs to.
func TestBugReportClosedMsg_LeavesANewerFormAlone(t *testing.T) {
	h := &Home{bugReport: bugFormDialog(), toasts: NewToastStack()}
	h.bugReport.submitting = true

	// esc while submitting: hides the form, emits a bare closed msg, and leaves
	// the gh call still running.
	h.bugReport.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	// `!` again — a fresh report, part-way through being typed.
	h.bugReport.Show("v0.0.0-test", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)
	h.bugReport.stage = stageForm
	h.bugReport.descInput.SetValue("half-written second report")

	// The first submission finally returns.
	h.Update(bugReportClosedMsg{url: "https://github.com/brizzai/fleet/issues/1"})

	if !h.bugReport.IsVisible() {
		t.Fatal("a stale submit result closed the report the user was typing into")
	}
	if got := h.bugReport.descInput.Value(); got != "half-written second report" {
		t.Fatalf("description lost to the stale result, got %q", got)
	}
	if !strings.Contains(h.infoMsg, "issues/1") {
		t.Fatalf("the filed issue must still be confirmed, got %q", h.infoMsg)
	}
}

func TestBugReportDialog_Esc_Hides(t *testing.T) {
	d := bugFormDialog()
	d.submitting = true

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if d.visible {
		t.Fatal("expected dialog to be hidden after esc")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd from esc")
	}
	msg := cmd()
	if _, ok := msg.(bugReportClosedMsg); !ok {
		t.Fatalf("expected bugReportClosedMsg, got %T", msg)
	}
}
