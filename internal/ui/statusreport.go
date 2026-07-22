package ui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/diagnostics"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// statusReportPaneLines is how many trailing pane lines ride along in the issue
// body when the reporter leaves content included. Enough to hold the input box,
// the activity line, and the tool/agent rows above them — the structures pane
// detection keys on — without pasting a whole scrollback.
const statusReportPaneLines = 40

// statusReportPreviewLines is how many of those the dialog shows before
// submitting. The excerpt is the reporter's actual screen, so it is previewed
// rather than described: the checkbox below it is only meaningful if you can see
// what it governs.
const statusReportPreviewLines = 6

// expectedStatusChoices are the statuses a reporter can name as "what it should
// have been". Deliberately not every session.Status: `starting` is transient
// (nobody disputes a status that resolves itself in a second) and `suspended` is
// user-initiated, so neither is a claim about misdetection.
var expectedStatusChoices = []session.Status{
	session.StatusRunning,
	session.StatusWaiting,
	session.StatusFinished,
	session.StatusIdle,
	session.StatusError,
}

// statusReportForm is the "wrong status" branch of the bug-report dialog: the
// frozen evidence plus the two things only the reporter knows — what the status
// should have been, and whether they're willing to share their screen.
type statusReportForm struct {
	// Frozen at the `!` keypress, not read live. The whole point of capturing
	// on keypress is that status moves; by submit time the session may have
	// self-corrected, and the report must describe the moment complained about.
	//
	// sessionID is what SetSnapshot matches an arriving capture against, so a
	// slow capture for a previously-reported session can't land here.
	sessionID    string
	sessionTitle string
	shownStatus  session.Status
	agent        string

	// Arrives asynchronously via reportSnapshotMsg. captured stays false until
	// then, which is what the excerpt area and the toggle key gate on.
	snap     snapshotResult
	captured bool

	// expected indexes expectedStatusChoices, or expectedUnset for the "(pick
	// one)" sentinel. Submit refuses while unset rather than defaulting: every
	// candidate default is either the disputed status itself or a guess put in
	// the reporter's mouth.
	expected       int
	includeContent bool
}

const expectedUnset = -1

func newStatusReportForm(sessionID, title string, shown session.Status, agent string) statusReportForm {
	return statusReportForm{
		sessionID:    sessionID,
		sessionTitle: title,
		shownStatus:  shown,
		agent:        agent,
		expected:     expectedUnset,
		// On by default: a status report whose evidence was left behind is the
		// report we already get and can't action. The excerpt is previewed
		// directly above the toggle, so this is a visible default, not a silent
		// one.
		includeContent: true,
	}
}

// expectedStatus returns the chosen status and whether one was chosen.
func (f *statusReportForm) expectedStatus() (session.Status, bool) {
	if f.expected < 0 || f.expected >= len(expectedStatusChoices) {
		return "", false
	}
	return expectedStatusChoices[f.expected], true
}

// cycleExpected moves the selection by delta, stepping off the sentinel on the
// first press in either direction and stopping at the ends. It deliberately does
// not wrap: wrapping past the last entry back to "(pick one)" would let someone
// arrow through the whole list and land back on an unsubmittable state.
func (f *statusReportForm) cycleExpected(delta int) {
	next := f.expected + delta
	if f.expected == expectedUnset && delta < 0 {
		next = len(expectedStatusChoices) - 1
	}
	if next < 0 {
		next = 0
	}
	if next >= len(expectedStatusChoices) {
		next = len(expectedStatusChoices) - 1
	}
	f.expected = next
}

// paneExcerpt returns the trailing pane lines that would be filed, oldest first.
func (f *statusReportForm) paneExcerpt(n int) []string {
	if !f.captured {
		return nil
	}
	lines := strings.Split(strings.TrimRight(f.snap.paneClean, "\n"), "\n")
	// Trailing blank lines are dead space in a preview only a few rows tall.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// submitBlocker names what still stops submission, or "" when ready. The dialog
// footer renders this, so a disabled Enter always says which field it wants
// instead of silently doing nothing.
func (f *statusReportForm) submitBlocker(desc string) string {
	descEmpty := strings.TrimSpace(desc) == ""
	_, haveExpected := f.expectedStatus()
	switch {
	case descEmpty && !haveExpected:
		return "Describe what happened and pick the correct status"
	case descEmpty:
		return "Describe what happened, then press enter"
	case !haveExpected:
		return "Pick what the status should have been (↑↓)"
	case !f.captured && f.snap.err == nil:
		// The capture is still in flight. Submitting now would file a report
		// whose body reads "the capture failed or the session had no live tmux
		// pane" — a false cause that sends the maintainer hunting a bug that
		// never happened, on exactly the undiagnosable report this flow exists
		// to prevent. A capture that genuinely failed (err != nil) is NOT
		// blocked: there the message is true and the account still has value.
		return "Capturing the session's status…"
	default:
		return ""
	}
}

// reportKindLabel and reportKindHint are the picker's two columns: what the
// choice is, and what it routes to.
func reportKindLabel(k reportKind) string {
	switch k {
	case kindStatus:
		return "Wrong status"
	case kindFeature:
		return "Feature"
	default:
		return "Bug"
	}
}

// viewPick renders the type picker. It is the first screen after `!`, so the
// status row can name the session and status already frozen — the reporter
// either recognizes it as their complaint or sees that it isn't.
func (d *BugReportDialog) viewPick() string {
	dialogWidth := 62
	if dialogWidth > d.width-4 {
		dialogWidth = d.width - 4
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}
	innerWidth := dialogWidth - 6

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	selStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Report"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("What are you reporting?"))
	b.WriteString("\n\n")

	for i, k := range d.kinds {
		label := reportKindLabel(k)
		hint := ""
		switch k {
		case kindStatus:
			// StatusSymbolRaw, not StatusSymbol: the whole hint is dim-styled as
			// one string below, so an inner color would be overridden anyway.
			hint = fmt.Sprintf("%q shows %s %s",
				ansi.Truncate(d.status.sessionTitle, 22, "…"),
				StatusSymbolRaw(d.status.shownStatus),
				d.status.shownStatus)
		case kindBug:
			hint = "something else broke"
		case kindFeature:
			hint = "an idea or request"
		}

		cursor := "  "
		labelOut := label
		if i == d.pickCursor {
			cursor = selStyle.Render("❯ ")
			labelOut = selStyle.Render(label)
		}
		// Pad the raw text before styling — padding a styled string counts the
		// ANSI bytes and the columns come out ragged.
		pad := 14 - lipgloss.Width(label)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(cursor + labelOut + strings.Repeat(" ", pad) +
			dimStyle.Render(ansi.Truncate(hint, innerWidth-18, "…")))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", innerWidth)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("1-3 / j k") + " Select    " +
		dimStyle.Render("enter") + " Continue    " + dimStyle.Render("esc") + " Cancel")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth).
		Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// viewStatusForm renders the wrong-status form.
//
// Wider than the other forms (the shared 60 mangles pane lines badly), because
// the screen excerpt has to be legible: it is the reporter's own content, shown
// so the include toggle underneath it means something.
func (d *BugReportDialog) viewStatusForm() string {
	dialogWidth := 90
	if dialogWidth > d.width-4 {
		dialogWidth = d.width - 4
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}
	innerWidth := dialogWidth - 6

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	accentStyle := lipgloss.NewStyle().Foreground(ColorAccent)

	f := &d.status
	var b strings.Builder

	b.WriteString(titleStyle.Render("Wrong status: " + ansi.Truncate(f.sessionTitle, 40, "…")))
	b.WriteString("\n\n")

	// Frozen, not live — this is the moment being reported on.
	b.WriteString("  " + dimStyle.Render("fleet showed  ") +
		StatusSymbol(f.shownStatus) + " " + string(f.shownStatus))
	b.WriteString("\n")

	expectedLabel := dimStyle.Render("(pick one)")
	if exp, ok := f.expectedStatus(); ok {
		expectedLabel = StatusSymbol(exp) + " " + string(exp)
	}
	b.WriteString("  " + dimStyle.Render("should be     ") + accentStyle.Render("‹ ") +
		expectedLabel + accentStyle.Render(" ›") + "  " + dimStyle.Render("↑↓"))
	b.WriteString("\n\n")

	b.WriteString(sectionStyle.Render("What happened?"))
	b.WriteString("\n")
	d.descInput.SetWidth(innerWidth - 2)
	b.WriteString("  " + d.descInput.View())
	b.WriteString("\n\n")

	// The excerpt is previewed before the toggle that governs it, so switching
	// it off is an informed choice rather than a guess about what's attached.
	checkbox := "[ ]"
	if f.includeContent {
		checkbox = accentStyle.Render("[x]")
	}
	switch {
	case !f.captured && f.snap.err != nil:
		b.WriteString(dimStyle.Render("  Snapshot unavailable — " + f.snap.err.Error()))
		b.WriteString("\n")
	case !f.captured:
		b.WriteString(dimStyle.Render("  capturing…"))
		b.WriteString("\n")
	default:
		b.WriteString("  " + checkbox + " " + sectionStyle.Render("include screen + logs") +
			"    " + dimStyle.Render("ctrl+p"))
		b.WriteString("\n")
		// The excerpt shows only what will actually be filed. Rendering it under
		// an unticked box would restate content the body omits, which is the one
		// ambiguity previewing above the toggle exists to remove.
		if f.includeContent {
			lines := f.paneExcerpt(statusReportPreviewLines)
			total := len(f.paneExcerpt(statusReportPaneLines))
			for _, ln := range lines {
				b.WriteString(dimStyle.Render("   │ " + ansi.Truncate(ln, innerWidth-6, "…")))
				b.WriteString("\n")
			}
			if more := total - len(lines); more > 0 {
				b.WriteString(dimStyle.Render(fmt.Sprintf("   +%d more lines, plus this session's debug log", more)))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(dimStyle.Render("   │ not included — signals only"))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", innerWidth)))
	b.WriteString("\n")

	switch {
	case d.submitting:
		b.WriteString(accentStyle.Render("Creating issue..."))
	case !ghAvailable():
		b.WriteString(dimStyle.Render("gh CLI not found") + "    " + dimStyle.Render("esc") + " Back")
	default:
		// Enter is inert until both fields are set, so the footer names which
		// one is missing rather than letting the key silently do nothing.
		if blocker := f.submitBlocker(d.descInput.Value()); blocker != "" {
			b.WriteString(dimStyle.Render(blocker) + "    " + dimStyle.Render("esc") + " Back")
		} else {
			b.WriteString(dimStyle.Render("enter") + " Submit    " + dimStyle.Render("esc") + " Back")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth).
		Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// sanitizeHome rewrites the user's home directory to "~" in anything bound for
// a public issue. Every report kind must run its text through this, including
// issue *titles*: a body that sanitizes while the title above it publishes
// /Users/<name>/... verbatim leaks on the one line GitHub shows in search
// results and notification mail.
func sanitizeHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	return strings.ReplaceAll(s, home, "~")
}

// metaString/metaBool/metaSub read named fields out of the snapshot.json map.
//
// Everything filed goes through these. The map is NOT marshalled wholesale —
// it embeds hook.file_contents.user_prompt, the reporter's verbatim prompt
// text, which has no diagnostic value the other signals lack and which the
// dialog never shows them. Reading by allowlist makes that leak structurally
// impossible rather than something to remember not to do.
func metaSub(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	sub, _ := m[key].(map[string]any)
	return sub
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case bool:
		return fmt.Sprintf("%t", v)
	case float64:
		return fmt.Sprintf("%g", v)
	case int:
		return fmt.Sprintf("%d", v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// buildStatusReportBody renders the GitHub issue body for a wrong-status report.
//
// Two tiers. The signals table is always emitted and carries no user content:
// hook/detection/worker/claude_log are derived numbers and enum values, and the
// report is diagnosable from them alone. The screen excerpt and the filtered
// debug log are the reporter's actual content and ride only when includeContent
// is set, which the dialog previews and lets them switch off.
func buildStatusReportBody(desc string, expected session.Status, f *statusReportForm, r *diagnostics.Report) string {
	sanitize := sanitizeHome

	var b strings.Builder
	b.WriteString("## Wrong Status Detected\n\n")

	fmt.Fprintf(&b, "**fleet showed `%s` — should have been `%s`**\n\n", f.shownStatus, expected)

	b.WriteString("### What happened\n")
	b.WriteString(sanitize(strings.TrimSpace(desc)) + "\n\n")

	if !f.captured {
		b.WriteString("_Status snapshot unavailable — the capture failed or the session had no live tmux pane._\n\n")
		b.WriteString(r.FormatEnvironmentMarkdown(false))
		return b.String()
	}

	meta := f.snap.meta
	hook := metaSub(meta, "hook")
	detection := metaSub(meta, "detection")
	worker := metaSub(meta, "worker")
	claudeLog := metaSub(meta, "claude_log")
	claudeStatus := metaSub(meta, "claude_status")

	b.WriteString("### Status Signals\n")
	b.WriteString("| Signal | Value |\n|--------|-------|\n")
	fmt.Fprintf(&b, "| agent | `%s` |\n", f.agent)
	fmt.Fprintf(&b, "| captured at | %s |\n", metaString(meta, "captured_at"))
	fmt.Fprintf(&b, "| hook status | `%s` |\n", metaString(hook, "status"))
	if ev := metaString(metaSub(hook, "file_contents"), "event"); ev != "" {
		fmt.Fprintf(&b, "| hook event | `%s` |\n", ev)
	}
	fmt.Fprintf(&b, "| hook age | %s |\n", metaString(hook, "age"))
	if ov := metaString(hook, "overridden_at"); ov != "" {
		fmt.Fprintf(&b, "| hook overridden | %s ago |\n", ov)
	}
	fmt.Fprintf(&b, "| pane detected | `%s` |\n", metaString(detection, "pane_detected"))
	fmt.Fprintf(&b, "| TUI showed | `%s` |\n", metaString(detection, "tui_shows"))
	fmt.Fprintf(&b, "| **mismatch** | **%s** |\n", metaString(detection, "mismatch"))
	if worker != nil {
		fmt.Fprintf(&b, "| worker stalled | %s |\n", metaString(worker, "stalled"))
		fmt.Fprintf(&b, "| worker last cycle | %s ago |\n", metaString(worker, "last_cycle_ago"))
		if inflight := metaString(worker, "cycle_in_flight_for"); inflight != "" {
			fmt.Fprintf(&b, "| worker cycle in flight | %s |\n", inflight)
		}
	}
	if claudeLog != nil {
		fmt.Fprintf(&b, "| transcript advanced past hook | %s |\n", metaString(claudeLog, "advanced_past_hook"))
		fmt.Fprintf(&b, "| transcript seconds past hook | %s |\n", metaString(claudeLog, "seconds_past_hook"))
		fmt.Fprintf(&b, "| transcript last entry | %s ago |\n", metaString(claudeLog, "last_entry_age"))
		if gaps, ok := claudeLog["recent_gaps_s"].([]float64); ok && len(gaps) > 0 {
			fmt.Fprintf(&b, "| transcript recent gaps (s) | `%v` |\n", gaps)
		}
	}
	if claudeStatus != nil {
		fmt.Fprintf(&b, "| Claude own status | `%s` |\n", metaString(claudeStatus, "status"))
		fmt.Fprintf(&b, "| Claude pid alive | %s |\n", metaString(claudeStatus, "pid_alive"))
		fmt.Fprintf(&b, "| Claude version | %s |\n", metaString(claudeStatus, "version"))
	}
	b.WriteString("\n")

	if f.includeContent {
		if lines := f.paneExcerpt(statusReportPaneLines); len(lines) > 0 {
			fmt.Fprintf(&b, "<details><summary>Screen (last %d lines)</summary>\n\n```\n", len(lines))
			b.WriteString(sanitize(strings.Join(lines, "\n")))
			b.WriteString("\n```\n</details>\n\n")
		}
		if tail := strings.TrimSpace(f.snap.debugTail); tail != "" {
			b.WriteString("<details><summary>Debug log (this session only)</summary>\n\n```\n")
			b.WriteString(sanitize(tail))
			b.WriteString("\n```\n</details>\n\n")
		}
	} else {
		b.WriteString("_Reporter chose not to include the screen excerpt and session debug log._\n\n")
	}

	// Versions and terminal env, but not the description/errors/actions block:
	// this report already opened with its own narrative and signals table. The
	// global debug log follows the content toggle — the session-filtered tail
	// above is the relevant one, and shipping the global tail regardless would
	// walk straight around a reporter who opted out.
	b.WriteString(r.FormatEnvironmentMarkdown(f.includeContent))

	return b.String()
}
