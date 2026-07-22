package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/diagnostics"
	"github.com/brizzai/fleet/internal/session"
)

const secretPrompt = "/review-team-pr-v2 4208 and do not leak me"

// statusFormFixture builds a captured form whose snapshot carries the same
// shape a real capture produces, including the prompt text buried in the hook
// block.
func statusFormFixture() *statusReportForm {
	f := newStatusReportForm("4208", session.StatusIdle, "claude")
	f.captured = true
	f.snap = snapshotResult{
		path:      "/tmp/snap",
		paneClean: "line one\nline two\n❯ 1. Yes\n  2. No\nEsc to cancel\n",
		debugTail: "time=... msg=\"status changed\" old=running new=idle",
		meta: map[string]any{
			"captured_at": "2026-07-22T14:00:00Z",
			"hook": map[string]any{
				"status": "waiting",
				"age":    "5m34s",
				"file_contents": map[string]any{
					"event":       "PermissionRequest",
					"user_prompt": secretPrompt,
					"session_id":  "61fd77f6",
				},
			},
			"detection": map[string]any{
				"pane_detected": "running",
				"tui_shows":     "waiting",
				"mismatch":      true,
			},
			"worker": map[string]any{
				"stalled":        true,
				"last_cycle_ago": "5m18s",
			},
			"claude_log": map[string]any{
				"advanced_past_hook": true,
				"seconds_past_hook":  130.7,
				"last_entry_age":     "3m23s",
			},
		},
	}
	return &f
}

// The snapshot map holds the reporter's verbatim prompt under
// hook.file_contents.user_prompt. It is never shown in the dialog, so it must
// never reach a public issue — this is the guard against someone "simplifying"
// the allowlisted field reads into a json.Marshal of the whole map.
func TestBuildStatusReportBody_NeverLeaksUserPrompt(t *testing.T) {
	f := statusFormFixture()
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	for _, includeContent := range []bool{true, false} {
		f.includeContent = includeContent
		body := buildStatusReportBody("shows idle but it was asking me", session.StatusWaiting, f, r)
		if strings.Contains(body, secretPrompt) {
			t.Fatalf("issue body leaked user_prompt (includeContent=%v):\n%s", includeContent, body)
		}
		if strings.Contains(body, "user_prompt") {
			t.Fatalf("issue body mentions user_prompt key (includeContent=%v)", includeContent)
		}
	}
}

func TestBuildStatusReportBody_AlwaysCarriesSignals(t *testing.T) {
	f := statusFormFixture()
	f.includeContent = false
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("shows idle but it was asking me", session.StatusWaiting, f, r)

	// These are what make a report diagnosable without any user content.
	for _, want := range []string{
		"showed `idle`", "should have been `waiting`",
		"pane detected", "running",
		"worker stalled", "true",
		"advanced past hook",
		"PermissionRequest",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("signals block missing %q:\n%s", want, body)
		}
	}
}

func TestBuildStatusReportBody_ContentFollowsToggle(t *testing.T) {
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	on := statusFormFixture()
	on.includeContent = true
	bodyOn := buildStatusReportBody("desc", session.StatusWaiting, on, r)
	if !strings.Contains(bodyOn, "Esc to cancel") {
		t.Fatal("expected pane excerpt in body when content is included")
	}
	if !strings.Contains(bodyOn, "status changed") {
		t.Fatal("expected debug tail in body when content is included")
	}

	off := statusFormFixture()
	off.includeContent = false
	bodyOff := buildStatusReportBody("desc", session.StatusWaiting, off, r)
	if strings.Contains(bodyOff, "Esc to cancel") {
		t.Fatal("pane excerpt must not ship when the reporter switched content off")
	}
	if strings.Contains(bodyOff, "status changed") {
		t.Fatal("debug tail must not ship when the reporter switched content off")
	}
}

// Opting out of content must also drop the *global* debug log the shared
// diagnostics block would otherwise append — otherwise the toggle removes the
// session's log and smuggles a wider one back in.
func TestBuildStatusReportBody_ContentOffAlsoDropsGlobalLog(t *testing.T) {
	const globalLog = "time=... msg=\"some other session entirely\""
	r := &diagnostics.Report{
		Version: "v2.22.0", OS: "darwin", Arch: "arm64",
		RecentLogs: globalLog,
	}

	off := statusFormFixture()
	off.includeContent = false
	if body := buildStatusReportBody("desc", session.StatusWaiting, off, r); strings.Contains(body, globalLog) {
		t.Fatal("global debug log must not ship when the reporter switched content off")
	}

	on := statusFormFixture()
	on.includeContent = true
	if body := buildStatusReportBody("desc", session.StatusWaiting, on, r); !strings.Contains(body, globalLog) {
		t.Fatal("expected the global debug log when content is included")
	}
}

// The status body supplies its own narrative, so it must not append the shared
// block's "## Bug Report" heading and empty description placeholder.
func TestBuildStatusReportBody_NoDuplicateBugReportHeading(t *testing.T) {
	f := statusFormFixture()
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("desc", session.StatusWaiting, f, r)

	if strings.Contains(body, "## Bug Report") {
		t.Fatalf("status body must not carry a second report heading:\n%s", body)
	}
	if strings.Contains(body, "Please describe what happened") {
		t.Fatal("status body must not carry an empty description placeholder")
	}
}

// A capture can fail (no live tmux pane). The report still has to file, since
// the reporter's own account of what they saw is the point.
func TestBuildStatusReportBody_UncapturedStillFiles(t *testing.T) {
	f := newStatusReportForm("4208", session.StatusIdle, "claude")
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("it was waiting", session.StatusWaiting, &f, r)

	if !strings.Contains(body, "it was waiting") {
		t.Fatal("expected the description to survive a failed capture")
	}
	if !strings.Contains(body, "snapshot unavailable") {
		t.Fatal("expected the body to say the snapshot is missing")
	}
}

func TestStatusForm_SubmitRefusedUntilExpectedPicked(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	d.status = *statusFormFixture()
	d.descInput.SetValue("shows idle but it was asking me")

	// Description is set but no expected status has been chosen yet.
	if _, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("expected enter to be inert until an expected status is picked")
	}
	if d.submitting {
		t.Fatal("submitting must stay false while the expected status is unset")
	}
	if blocker := d.status.submitBlocker(d.descInput.Value()); blocker == "" {
		t.Fatal("expected the footer to name the missing expected status")
	}
}

// The cycler uses ↑↓ specifically so ←→ stay with the focused description
// input's caret. Swapping it back to ←→ would silently cost typo-fixing.
func TestStatusForm_ArrowsLeftRightStayWithTheInput(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	d.status = *statusFormFixture()

	d.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	d.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if _, ok := d.status.expectedStatus(); ok {
		t.Fatal("←→ must not drive the expected-status cycler; they belong to the caret")
	}

	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if _, ok := d.status.expectedStatus(); !ok {
		t.Fatal("expected ↓ to move the expected-status cycler")
	}
}

func TestStatusForm_CycleExpectedStopsAtEnds(t *testing.T) {
	f := newStatusReportForm("t", session.StatusIdle, "claude")

	if _, ok := f.expectedStatus(); ok {
		t.Fatal("expected status must start unset so submit refuses rather than guessing")
	}

	f.cycleExpected(1)
	if got, _ := f.expectedStatus(); got != expectedStatusChoices[0] {
		t.Fatalf("first right press should land on the first choice, got %q", got)
	}

	// Walking off either end must not wrap back onto the unset sentinel.
	for range len(expectedStatusChoices) + 3 {
		f.cycleExpected(1)
	}
	if got, _ := f.expectedStatus(); got != expectedStatusChoices[len(expectedStatusChoices)-1] {
		t.Fatalf("expected to clamp at the last choice, got %q", got)
	}
	for range len(expectedStatusChoices) + 3 {
		f.cycleExpected(-1)
	}
	if _, ok := f.expectedStatus(); !ok {
		t.Fatal("cycling left must clamp at the first choice, never back to unset")
	}
}

func TestStatusForm_ToggleContentRequiresCapture(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	d.status = newStatusReportForm("4208", session.StatusIdle, "claude")

	// Capture hasn't landed — there is nothing to include or exclude yet, so
	// the toggle must not flip a state the dialog isn't showing.
	before := d.status.includeContent
	d.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if d.status.includeContent != before {
		t.Fatal("content toggle must be inert until the capture lands")
	}

	d.status = *statusFormFixture()
	d.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if d.status.includeContent {
		t.Fatal("expected ctrl+p to switch content off once captured")
	}
}

func TestReportPicker_OmitsStatusRowWithoutSession(t *testing.T) {
	d := NewBugReportDialog()
	d.Show("v2.22.0", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)

	for _, k := range d.kinds {
		if k == kindStatus {
			t.Fatal("status row must not be offered when no session is under the cursor")
		}
	}
	if d.kind == kindStatus {
		t.Fatal("default kind must not be status without a session")
	}
}

// Enter on the picker must never index past the rows it actually rendered.
func TestReportPicker_EnterWithNoKindsDoesNotPanic(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true

	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if d.stage != stageForm {
		t.Fatal("expected enter to advance to a form")
	}
}

func TestReportPicker_DigitBeyondRowsIgnored(t *testing.T) {
	d := NewBugReportDialog()
	d.Show("v2.22.0", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)

	// Only two rows exist without a session; "3" must not advance.
	d.Update(tea.KeyPressMsg{Code: '3'})

	if d.stage != stagePick {
		t.Fatal("a digit past the last row must not select anything")
	}
}
