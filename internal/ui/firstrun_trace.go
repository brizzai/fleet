package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
)

// The first-run action trace: the ordered list of what a newcomer did between
// their first launch and their first quit, sent once per install.
//
// Most newcomers leave inside their first few minutes and nothing in the funnel
// says why — the launchpad, a session that broke, never finding Enter, or simply
// not wanting it. A TUI has no session replay, so the nearest thing is a trace
// of what they pressed and what fleet showed them.
//
// The privacy line is the whole design: an entry is a second offset plus an enum
// from the fixed table below. Never a path, title, prompt, repo or branch name,
// error string, or typed character — keys typed into a text input or forwarded
// to a pane are structurally out of scope (see the hook's placement in
// handleKey). ActionLog is deliberately untouched: it carries `detail` and
// serves bug reports, which stay on the user's machine until they file one.
//
// Recording stops the moment the milestone is marked, and the recorder is nil
// for everyone past their first run, so a regular pays one nil check per key.

// Trace vocabulary. Every string the recorder can emit is one of these
// constants or comes from traceDialog / traceCommand / traceMenu below;
// TestFirstRunTraceEmitsOnlyEnums parses the call sites to keep it that way.
const (
	// Consent + first-run onboarding.
	traceConsentFull  = "consent_full"
	traceConsentBasic = "consent_basic"
	traceThemeKeep    = "theme_keep"
	traceThemeSkip    = "theme_skip"
	traceThemeCycle   = "theme_cycle"

	// Launchpad.
	traceLaunchpadShown  = "launchpad_shown"
	traceLaunchpadToggle = "launchpad_toggle"
	traceLaunchpadEnter  = "launchpad_enter"
	traceLaunchpadEsc    = "launchpad_esc"

	// Navigation and top-level keys.
	traceNav             = "nav"
	traceSpaceJump       = "space_jump"
	traceAttach          = "attach"
	traceAttachViaSlot   = "attach_via_slot"
	traceDetach          = "detach"
	traceDetachBail      = "detach_bail"
	traceHelp            = "help"
	tracePalette         = "palette"
	traceSettings        = "settings"
	traceFilter          = "filter"
	traceNewSession      = "new_session"
	traceNewSessionPick  = "new_session_picker"
	traceNewWorktree     = "new_worktree"
	traceFork            = "fork"
	traceDelete          = "delete"
	traceUndo            = "undo"
	traceRestart         = "restart"
	traceRename          = "rename"
	traceQuickApprove    = "quick_approve"
	traceDrawer          = "drawer"
	traceContextMenuOpen = "context_menu"
	traceToggleGroup     = "toggle_group"
	traceEsc             = "esc"
	traceQuit            = "quit"

	// What fleet showed them.
	traceSessionError   = "session_error"
	traceSessionWaiting = "session_waiting"
	traceToastError     = "toast_error"
	traceTipShown       = "tip_shown"
)

const (
	// firstRunTraceMaxEntries caps the array a single event carries. 300 × ~12
	// bytes is well inside PostHog's per-event limit; past it the oldest entry
	// is dropped and counted in entries_dropped, so a long first run reports
	// what it lost rather than truncating silently.
	firstRunTraceMaxEntries = 300

	// firstRunTraceWriteEvery is the floor between persists. The file only
	// exists to survive a terminal closed without quitting, so it never needs
	// to be current to the keystroke.
	firstRunTraceWriteEvery = 2 * time.Second

	firstRunTraceFileName = "first_run_trace.json"
)

// traceTokenPattern is the shape every emitted action must have: lowercase
// words joined by underscores, optionally carrying a collapsed-run count
// (nav_x12). Enforced at the point of recording as well as by the source guard,
// because this is the line that must not be crossed by accident.
var traceTokenPattern = regexp.MustCompile(`^[a-z_]+(_x[0-9]+)?$`)

// traceTrack is analytics.Track, swappable so tests can see what was sent.
var traceTrack = analytics.Track

// traceDialog names a dialog lifecycle step: traceDialog("settings", "open").
func traceDialog(name, phase string) string { return "dialog_" + name + "_" + phase }

// traceCommand names a command-palette pick, and traceMenu a context-menu one.
// The id is an enum from dispatchCommand's own case list, so the pair is a
// fixed token plus a known id — the palette's free-text query never gets near
// this.
func traceCommand(id string) string { return "command_" + id }
func traceMenu(id string) string    { return "menu_" + id }

// traceForAction maps an ActionLog action name onto the trace vocabulary. Only
// the action is read, never the detail, which is where paths and titles live.
// An action with no vocabulary entry records nothing: the table is the
// vocabulary, not a transformation of whatever a call site happens to pass.
func traceForAction(action string) string {
	// The two free-text prefixes. The suffix is a dispatchCommand id.
	if id, ok := strings.CutPrefix(action, "command: "); ok {
		return traceCommand(id)
	}
	if id, ok := strings.CutPrefix(action, "context menu: "); ok {
		return traceMenu(id)
	}
	switch action {
	case "attach session":
		return traceAttach
	case "attach via slot":
		return traceAttachViaSlot
	case "create session":
		return traceNewSession
	case "fork session", "fork to worktree":
		return traceFork
	case "delete session", "delete worktree", "delete repo", "delete origin":
		return traceDelete
	case "undo delete":
		return traceUndo
	case "restart session":
		return traceRestart
	case "quick approve":
		return traceQuickApprove
	case "open terminal drawer":
		return traceDrawer
	}
	return ""
}

// traceEntry is one recorded action. Consecutive navigation keys collapse into
// a single entry carrying a count, so scrolling a long list costs one slot
// instead of forty and the shape of the run stays readable.
type traceEntry struct {
	at     int // whole seconds since launch
	action string
	count  int
}

func (e traceEntry) String() string {
	if e.count > 1 {
		return fmt.Sprintf("%d:%s_x%d", e.at, e.action, e.count)
	}
	return fmt.Sprintf("%d:%s", e.at, e.action)
}

// firstRunTrace records the run. Every method is safe to call from any
// goroutine: the status worker reports session_error, the attach callback
// reports the detach, and everything else arrives on the Update loop.
type firstRunTrace struct {
	mu        sync.Mutex
	startedAt time.Time
	entries   []traceEntry
	seen      map[string]bool
	dropped   int
	lastWrite time.Time
	stopped   bool
}

func newFirstRunTrace(startedAt time.Time) *firstRunTrace {
	return &firstRunTrace{startedAt: startedAt, seen: map[string]bool{}}
}

// firstRunTracePayload is both the event's properties and the on-disk file, so
// an unclean run is sent as exactly the event a clean one would have been.
type firstRunTracePayload struct {
	Trace          []string `json:"trace"`
	EntriesDropped int      `json:"entries_dropped"`
	UptimeSeconds  int      `json:"uptime_seconds"`
}

func firstRunTracePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", firstRunTraceFileName)
}

// firstRunTraceShouldRecord reports whether this launch is the one being
// recorded. Two ways it is not: the run has already been reported, or a
// previous run left one waiting — in which case this launch's job is to report
// that, not to record over it. The recorder persists on a debounce and would
// otherwise replace the leftover with its own entries before
// sendUncleanFirstRunTrace ever reads it. A stat that fails for any other
// reason counts as "there is a file": a trace this process cannot manage is not
// one it should start writing.
func firstRunTraceShouldRecord() bool {
	if analytics.MilestoneReached(analytics.MilestoneFirstRunTrace) {
		return false
	}
	_, err := os.Stat(firstRunTracePath())
	return os.IsNotExist(err)
}

// trace records one action. Nil-safe: past the first run h.firstRun is nil and
// this is the only cost the recorder has.
func (h *Home) trace(action string) {
	if h.firstRun == nil {
		return
	}
	h.firstRun.record(action)
}

// traceOnce records an action only if this run has not recorded it before —
// "the first time any session went waiting", not every time one does.
func (h *Home) traceOnce(action string) {
	if h.firstRun == nil {
		return
	}
	h.firstRun.recordOnce(action)
}

// logAction is ActionLog.Add plus the trace. One funnel so a new call site is
// traced by naming itself, and so the mapping lives in exactly one table.
// ActionLog still gets the detail; the recorder never sees it.
func (h *Home) logAction(action, detail string, success bool) {
	h.actionLog.Add(action, detail, success)
	if token := traceForAction(action); token != "" {
		h.trace(token)
	}
}

func (t *firstRunTrace) record(action string) { t.add(action, false) }

func (t *firstRunTrace) recordOnce(action string) { t.add(action, true) }

func (t *firstRunTrace) add(action string, once bool) {
	if !traceTokenPattern.MatchString(action) {
		// Defence in depth against a call site that builds a token out of
		// something it shouldn't. The source guard is the real check; this is
		// what keeps a mistake off the wire rather than merely off review.
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	if once {
		if t.seen[action] {
			return
		}
		t.seen[action] = true
	}
	// Collapse a run of navigation keys into the entry that started it.
	if n := len(t.entries); action == traceNav && n > 0 && t.entries[n-1].action == traceNav {
		t.entries[n-1].count++
	} else {
		t.entries = append(t.entries, traceEntry{
			at:     int(time.Since(t.startedAt).Seconds()),
			action: action,
			count:  1,
		})
	}
	if len(t.entries) > firstRunTraceMaxEntries {
		// Shift left to drop the oldest, as ActionLog does.
		copy(t.entries, t.entries[1:])
		t.entries = t.entries[:firstRunTraceMaxEntries]
		t.dropped++
	}
	t.persistLocked(false)
}

// payloadLocked renders the trace as it will be sent.
func (t *firstRunTrace) payloadLocked() firstRunTracePayload {
	out := make([]string, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, e.String())
	}
	return firstRunTracePayload{
		Trace:          out,
		EntriesDropped: t.dropped,
		UptimeSeconds:  int(time.Since(t.startedAt).Seconds()),
	}
}

// persistLocked writes the trace, at most once per firstRunTraceWriteEvery.
//
// The write is synchronous on whatever goroutine recorded — normally the Update
// loop, where blocking I/O is otherwise banned. It earns the exception by being
// a few KB to a local file, at most once every two seconds, and only ever
// during a single install's first run; handing it to a goroutine would buy a
// write ordering problem and a test that has to wait for it.
func (t *firstRunTrace) persistLocked(force bool) {
	if !force && time.Since(t.lastWrite) < firstRunTraceWriteEvery {
		return
	}
	data, err := json.Marshal(t.payloadLocked())
	if err != nil {
		return
	}
	t.lastWrite = time.Now()
	path := firstRunTracePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		debuglog.Logger.Debug("first-run trace: mkdir", "err", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		debuglog.Logger.Debug("first-run trace: write", "err", err)
	}
}

// stopAndTake ends recording and returns what was recorded. Recording never
// resumes: the milestone is one-shot per install, so a second trace would have
// nowhere to go.
func (t *firstRunTrace) stopAndTake() firstRunTracePayload {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.payloadLocked()
	t.stopped = true
	return p
}

// flush writes the trace regardless of the debounce, for the one caller that
// needs the file complete: a quit that couldn't mark the milestone and so has
// to leave the run for the next launch to report.
func (t *firstRunTrace) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.persistLocked(true)
}

// sendFirstRunTrace ends the recording and sends it as the clean ending. Called
// from the quit path, beside the first-quit milestone.
func (h *Home) sendFirstRunTrace(uptimeSeconds int) {
	if h.firstRun == nil || analytics.MilestoneReached(analytics.MilestoneFirstRunTrace) {
		// Already reported — by an earlier pass through here, or by this
		// launch picking up an unclean one at startup.
		return
	}
	payload := h.firstRun.stopAndTake()
	payload.UptimeSeconds = uptimeSeconds
	if !analytics.MarkOnboardingMilestone(analytics.MilestoneFirstRunTrace) {
		// install_state.json could not be written, so the one-shot can't be
		// closed here: leave the file for the next launch to report as
		// unclean rather than losing the run. Flush it first — the last
		// entries, the quit among them, are newer than the debounced write.
		h.firstRun.flush()
		return
	}
	h.emitFirstRunTrace(payload, "clean")
	removeFirstRunTraceFile()
}

// sendUncleanFirstRunTrace picks up a trace left behind by a run that ended
// without reaching the quit path — a closed terminal, a crash — and sends it.
//
// Called from fireStartupAnalytics, the one place per launch that initializes
// the client: sending before Init would drop the event silently, and on a first
// launch Init doesn't happen until the consent prompt is answered.
func (h *Home) sendUncleanFirstRunTrace() {
	if analytics.MilestoneReached(analytics.MilestoneFirstRunTrace) {
		return
	}
	data, err := os.ReadFile(firstRunTracePath())
	if err != nil {
		return // no leftover run, or unreadable — nothing to report either way
	}
	var payload firstRunTracePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		removeFirstRunTraceFile()
		return
	}
	if !analytics.MarkOnboardingMilestone(analytics.MilestoneFirstRunTrace) {
		return
	}
	h.emitFirstRunTrace(payload, "unclean")
	removeFirstRunTraceFile()
	// The milestone is marked, so this launch is no longer recording one.
	if h.firstRun != nil {
		h.firstRun.stopAndTake()
	}
}

// emitFirstRunTrace sends the event, unless telemetry is off — in which case
// the milestone is still marked and the file still deleted, so an install that
// never reports is still one-shot rather than retrying every launch.
func (h *Home) emitFirstRunTrace(p firstRunTracePayload, ended string) {
	if h.cfg != nil && h.cfg.GetTelemetryMode() == config.TelemetryOff {
		return
	}
	traceTrack(analytics.EventOnboardingFirstRunTrace, map[string]interface{}{
		"trace":           p.Trace,
		"ended":           ended,
		"uptime_seconds":  p.UptimeSeconds,
		"entries_dropped": p.EntriesDropped,
	})
}

func removeFirstRunTraceFile() {
	if err := os.Remove(firstRunTracePath()); err != nil && !os.IsNotExist(err) {
		debuglog.Logger.Debug("first-run trace: remove", "err", err)
	}
}

// traceKeyToken maps a top-level key to its vocabulary entry, for the keys that
// have no ActionLog entry of their own. The ones that do (attach, a, d, r, u,
// f, Y, `) are recorded from there instead, so a gesture records once.
//
// A key that opens a dialog deliberately records alongside the dialog rather
// than instead of it: the press and the screen are two facts, and the case
// worth reading is the one where the second never follows the first — `w` on a
// row with no repo, `t` with no tracker connected. Both go nowhere, and a trace
// that only logged the dialog would show nothing at all for them.
func traceKeyToken(key string) string {
	switch key {
	case "j", "k", "up", "down", "pgup", "pgdown",
		"shift+up", "shift+down", "ctrl+shift+up", "ctrl+shift+down":
		return traceNav
	case "space":
		return traceSpaceJump
	case "?":
		return traceHelp
	case "ctrl+k", "t":
		return tracePalette
	case "S":
		return traceSettings
	case "/":
		return traceFilter
	case "A":
		return traceNewSessionPick
	case "w":
		return traceNewWorktree
	case "R":
		return traceRename
	case ".":
		return traceContextMenuOpen
	case "esc":
		return traceEsc
	}
	return ""
}

// traceKey records a top-level key press. Called from handleKey below the
// modal, launchpad, focus, filter and drawer branches — the same placement
// normalizeKey has, and for the same reason: everything that owns text has
// already returned, so nothing here can be a character the user typed.
func (h *Home) traceKey(key string) {
	if h.firstRun == nil {
		return
	}
	if token := traceKeyToken(key); token != "" {
		h.firstRun.record(token)
	}
}

// traceDialogName is the name a dialog goes by in the trace, in routeToModal's
// own precedence order. Consent and the theme picker are deliberately absent:
// they have tokens of their own that say which way the user answered, and a
// dialog_consent_enter beside a consent_full says nothing extra.
//
// TestTraceNamesEveryRoutedDialog parses routeToModal and fails if a dialog
// reaches it without a name here.
func (h *Home) traceDialogName() string {
	switch {
	case h.helpOverlay.IsVisible():
		return "help"
	case h.releaseNotes.IsVisible():
		return "release_notes"
	case h.consentDialog.IsVisible():
		return ""
	case h.onboardingDialog.IsVisible():
		return ""
	case h.bugReport.IsVisible():
		return "report"
	case h.settingsDialog.IsVisible():
		return "settings"
	case h.createWorkspaceDialog.IsVisible():
		return "create_workspace"
	case h.worktreeDialog.IsVisible():
		return "worktree"
	case h.branchDialog.IsVisible():
		return "branch"
	case h.commandPalette.IsVisible():
		return "palette"
	case h.accountPicker.IsVisible():
		return "account_picker"
	case h.allowedAccounts.IsVisible():
		return "allowed_accounts"
	case h.snoozeDialog.IsVisible():
		return "snooze"
	case h.contextMenu.IsVisible():
		return "context_menu"
	case h.sessionCreateDialog.IsVisible():
		return "session_create"
	case h.connectJira.IsVisible():
		return "connect_jira"
	case h.connectLinear.IsVisible():
		return "connect_linear"
	case h.accountsDialog.IsVisible():
		return "accounts"
	case h.newDialog.IsVisible():
		return "new_session"
	case h.confirmDialog.IsVisible():
		return "confirm"
	case h.renameDialog.IsVisible():
		return "rename"
	case h.gate.IsVisible():
		return "gate"
	}
	return ""
}

// traceModalOpened records a dialog appearing. Deferred from Update rather than
// hooked into each Show(), because dialogs open from messages as well as keys —
// a worktree picker lands when its branch list arrives, a confirm when a scan
// finishes — and only a check after the fact catches all of them.
func (h *Home) traceModalOpened() {
	name := h.traceDialogName()
	if name == h.traceModal {
		return
	}
	h.traceModal = name
	if name != "" {
		h.trace(traceDialog(name, "open"))
	}
}

// traceModalKey records the two answers a dialog gets: enter committed it, esc
// abandoned it. Called from routeToModal before the key is dispatched, so the
// name is still the dialog the key was aimed at.
func (h *Home) traceModalKey(msg tea.KeyPressMsg) {
	if h.firstRun == nil {
		return
	}
	name := h.traceDialogName()
	if name == "" {
		return
	}
	switch msg.String() {
	case "enter":
		h.firstRun.record(traceDialog(name, "enter"))
	case "esc":
		h.firstRun.record(traceDialog(name, "esc"))
	}
}
