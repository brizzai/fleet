package ui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/diagnostics"
	"github.com/brizzai/fleet/internal/session"
)

const secretPrompt = "/review-team-pr-v2 4208 and do not leak me"

var errCaptureFailed = errors.New("no tmux session")

// statusFormFixture builds a captured form whose snapshot carries the same
// shape a real capture produces, including the prompt text buried in the hook
// block.
func statusFormFixture() *statusReportForm {
	f := newStatusReportForm("sess-a", "4208", session.StatusIdle, "claude")
	f.captured = true
	f.snap = snapshotResult{
		path:      "/tmp/snap",
		paneClean: "line one\nline two\n❯ 1. Yes\n  2. No\nEsc to cancel\n",
		debugTail: "time=... msg=\"status changed\" old=running new=idle",
		meta: map[string]any{
			"captured_at": "2026-07-22T14:00:00Z",
			"hook": map[string]any{
				"status":               "waiting",
				"age":                  "5m34s",
				"file_status":          "running",
				"file_session_id":      "61fd77f6-2b1a-4c0d-9e88-1122aabbccdd",
				"file_age":             "1.2s",
				"applied_matches_file": false,
				// Far enough behind that lag can't explain it — the dropped-hook shape.
				"divergence_significant": true,
				"file_newer_by":          "5m33s",
				"owner_session_id":       "0aa11bb2-3c4d-5e6f-8899-aabbccddeeff",
				"owner_pid":              4242,
				"file_contents": map[string]any{
					"event":       "PermissionRequest",
					"user_prompt": secretPrompt,
					"session_id":  "61fd77f6-2b1a-4c0d-9e88-1122aabbccdd",
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

// Issue #220 arrived showing `hook status: running` beside `hook event: Stop` —
// two rows read from different sources (memory vs the file on disk) under labels
// that implied one. Since the handler maps event→status deterministically, that
// pair means fleet was not applying the file at all, but the report gave no way
// to tell WHY: the session ids that decide it were captured and then dropped on
// the way to the issue body. These rows are what makes the next one answerable
// without a follow-up round trip.
func TestBuildStatusReportBody_CarriesAppliedVsDiskDivergence(t *testing.T) {
	f := statusFormFixture()
	f.includeContent = false
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("stuck running forever", session.StatusIdle, f, r)

	for _, want := range []string{
		"hook status (applied) | `waiting`",
		"hook status (on disk) | `running`",
		"applied matches disk", "false",
		"hook file age (on disk) | 1.2s",
		"hook session (on disk) | `61fd77f6`",
		"owner session (latched) | `0aa11bb2`",
		"owner pid | 4242",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("divergence rows missing %q:\n%s", want, body)
		}
	}

	// Short ids are the point: enough to answer "same or different" without
	// pasting full conversation ids into a public issue.
	if strings.Contains(body, "61fd77f6-2b1a-4c0d-9e88-1122aabbccdd") {
		t.Fatal("issue body carried a full session id; want the shortened form")
	}
}

// The applied hook lags disk by design (fsnotify debounce + a worker cycle), so a
// capture landing mid-write reads `false` for the same reason a dropped hook does.
// Only the gap tells them apart, so only the large gap gets the bold — otherwise
// the loudest row in the table cries wolf on a healthy session.
func TestBuildStatusReportBody_BoldsOnlySignificantDivergence(t *testing.T) {
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	significant := statusFormFixture()
	significant.includeContent = false
	body := buildStatusReportBody("desc", session.StatusIdle, significant, r)
	if !strings.Contains(body, "| **applied matches disk** | **false** (applied hook is 5m34s old) |") {
		t.Fatalf("a real divergence should be bolded and dated:\n%s", body)
	}

	// Same false, gap under divergenceLagGrace: a write still in flight.
	inFlight := statusFormFixture()
	inFlight.includeContent = false
	hook := inFlight.snap.meta["hook"].(map[string]any)
	hook["divergence_significant"] = false
	hook["file_newer_by"] = "412ms"
	body = buildStatusReportBody("desc", session.StatusIdle, inFlight, r)
	if !strings.Contains(body, "| applied matches disk | false (file 412ms newer — in flight) |") {
		t.Fatalf("an in-flight write should render unbolded and qualified:\n%s", body)
	}
	if strings.Contains(body, "**applied matches disk**") {
		t.Fatalf("in-flight divergence must not be bolded:\n%s", body)
	}
}

// 0 is not a pid — it is "no pid was recorded", which is exactly the state where
// conversationSucceeds returns known=false and the recovery cannot run. Rendering
// it as `0` disguises an unavailable mechanism as a reading.
func TestBuildStatusReportBody_UnknownOwnerPIDRendersAsAbsent(t *testing.T) {
	f := statusFormFixture()
	f.includeContent = false
	f.snap.meta["hook"].(map[string]any)["owner_pid"] = 0
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("desc", session.StatusIdle, f, r)

	if !strings.Contains(body, "| owner pid | - |") {
		t.Fatalf("unknown owner pid should render as `-`:\n%s", body)
	}
	if strings.Contains(body, "| owner pid | 0 |") {
		t.Fatal("owner pid 0 rendered as a pid")
	}
}

func TestShortPID(t *testing.T) {
	for _, tc := range []struct {
		pid  int
		want string
	}{
		{0, "-"},
		{-1, "-"},
		{4242, "4242"},
	} {
		if got := shortPID(tc.pid); got != tc.want {
			t.Errorf("shortPID(%d) = %q, want %q", tc.pid, got, tc.want)
		}
	}
}

// A session that has never had a hook has neither file nor owner. The rows must
// drop out rather than render blank or "0" cells that read as real readings.
func TestBuildStatusReportBody_OmitsDivergenceRowsWithoutHookFile(t *testing.T) {
	f := statusFormFixture()
	f.includeContent = false
	hook := f.snap.meta["hook"].(map[string]any)
	for _, k := range []string{"file_status", "file_session_id", "file_age", "applied_matches_file", "owner_session_id", "owner_pid", "file_contents"} {
		delete(hook, k)
	}
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("desc", session.StatusIdle, f, r)

	for _, unwanted := range []string{"hook status (on disk)", "applied matches disk", "owner session (latched)"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("row %q rendered with no hook file:\n%s", unwanted, body)
		}
	}
	if !strings.Contains(body, "hook status (applied)") {
		t.Fatal("applied status must still render")
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

// config.json carries default_project_path, which can be confidential in its own
// right. The dialog never showed it and the checkbox never named it, so opting
// out of content must drop it too.
func TestBuildStatusReportBody_ContentOffAlsoDropsConfig(t *testing.T) {
	const cfg = `{"default_project_path":"~/work/acme-confidential"}`
	r := &diagnostics.Report{
		Version: "v2.22.0", OS: "darwin", Arch: "arm64",
		Config: cfg,
	}

	off := statusFormFixture()
	off.includeContent = false
	if body := buildStatusReportBody("desc", session.StatusWaiting, off, r); strings.Contains(body, "acme-confidential") {
		t.Fatal("config must not ship when the reporter switched content off")
	}

	on := statusFormFixture()
	on.includeContent = true
	if body := buildStatusReportBody("desc", session.StatusWaiting, on, r); !strings.Contains(body, "acme-confidential") {
		t.Fatal("expected config when content is included")
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
	f := newStatusReportForm("sess-a", "4208", session.StatusIdle, "claude")
	r := &diagnostics.Report{Version: "v2.22.0", OS: "darwin", Arch: "arm64"}

	body := buildStatusReportBody("it was waiting", session.StatusWaiting, &f, r)

	if !strings.Contains(body, "it was waiting") {
		t.Fatal("expected the description to survive a failed capture")
	}
	if !strings.Contains(body, "snapshot unavailable") {
		t.Fatal("expected the body to say the snapshot is missing")
	}
}

// `!` on session A, esc, `!` on B: A's slow capture must not land in B's form.
// Without the id match the issue would be headed with B's title and status while
// every piece of evidence came from A — publishing A's screen unpreviewed.
func TestSetSnapshot_RejectsCaptureForADifferentSession(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.status = newStatusReportForm("sess-b", "session B", session.StatusIdle, "claude")

	d.SetSnapshot(snapshotResult{sessionID: "sess-a", paneClean: "session A's secrets"})

	if d.status.captured {
		t.Fatal("a capture for another session must not be installed")
	}
	if strings.Contains(d.status.snap.paneClean, "secrets") {
		t.Fatal("another session's pane content leaked into this form")
	}

	d.SetSnapshot(snapshotResult{sessionID: "sess-b", paneClean: "session B's screen"})
	if !d.status.captured {
		t.Fatal("the matching capture should have been installed")
	}
}

// Enter while the capture is still in flight would file a body claiming the
// capture failed — a false cause, on the exact undiagnosable report this flow
// exists to prevent.
func TestStatusForm_SubmitBlockedWhileCapturing(t *testing.T) {
	f := newStatusReportForm("sess-a", "4208", session.StatusIdle, "claude")
	f.cycleExpected(1)

	if blocker := f.submitBlocker("it was waiting"); blocker == "" {
		t.Fatal("expected submit to be blocked while the capture is pending")
	}

	// A capture that genuinely failed must still submit: there the body's
	// "snapshot unavailable" line is true, and the account still has value.
	f.snap.err = errCaptureFailed
	if blocker := f.submitBlocker("it was waiting"); blocker != "" {
		t.Fatalf("a failed capture must not block submit, got %q", blocker)
	}
}

// The Enter handler and the footer must consult one gate, or the key acts on a
// state the footer says isn't ready.
func TestStatusForm_EnterInertWhileCapturing(t *testing.T) {
	d := NewBugReportDialog()
	d.Show("v0.0.0", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	d.status = newStatusReportForm("sess-a", "4208", session.StatusIdle, "claude")
	d.status.cycleExpected(1)
	d.descInput.SetValue("it was waiting")

	if _, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter must be inert while the capture is still pending")
	}
	if d.submitting {
		t.Fatal("submitting must stay false while the capture is pending")
	}
}

// The preview must show only what will actually be filed — restating content
// under an unticked box is the ambiguity previewing was meant to remove.
func TestStatusForm_UntickedRenderOmitsPaneText(t *testing.T) {
	d := NewBugReportDialog()
	d.Show("v0.0.0", 0, NewErrorHistory(50), NewActionLog(100), 120, 40, nil, 0, nil)
	d.width, d.height = 120, 40
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	d.status = *statusFormFixture()

	d.status.includeContent = true
	if !strings.Contains(d.viewStatusForm(), "Esc to cancel") {
		t.Fatal("expected the excerpt to render when content is included")
	}

	d.status.includeContent = false
	off := d.viewStatusForm()
	if strings.Contains(off, "Esc to cancel") {
		t.Fatal("excerpt must not render under an unticked box")
	}
	if strings.Contains(off, "more lines") {
		t.Fatal("the '+N more lines' note must not promise content the body omits")
	}
}

// Pane lines are dense with 3-byte box-drawing runes. Byte slicing cuts at a
// third of the intended width and can split a rune, so the excerpt the reporter
// approves wouldn't match their screen.
func TestStatusForm_ExcerptTruncationIsRuneSafe(t *testing.T) {
	d := NewBugReportDialog()
	d.Show("v0.0.0", 0, NewErrorHistory(50), NewActionLog(100), 120, 40, nil, 0, nil)
	d.width, d.height = 120, 40
	d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
	d.stage = stageForm
	d.kind = kindStatus
	f := statusFormFixture()
	f.snap.paneClean = strings.Repeat("│╭─❯ ", 60)
	d.status = *f

	out := d.viewStatusForm()
	if !utf8.ValidString(out) {
		t.Fatal("rendered form contains invalid UTF-8 — a rune was split")
	}
	// Byte slicing would have kept roughly a third of the intended columns.
	if !strings.Contains(out, strings.Repeat("│╭─❯ ", 8)) {
		t.Fatal("excerpt truncated far short of the intended visible width")
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
	f := newStatusReportForm("sess-a", "t", session.StatusIdle, "claude")

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
	d.status = newStatusReportForm("sess-a", "4208", session.StatusIdle, "claude")

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
