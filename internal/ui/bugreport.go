package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/diagnostics"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// bugReportClosedMsg asks app.go to close the report dialog. The esc paths have
// already hidden themselves by the time they emit it, but the submit path runs
// inside a tea.Cmd and cannot touch dialog state, so the handler — not the
// emitter — is what actually closes it. url is set only on a successful file.
type bugReportClosedMsg struct{ url string }

// bugReportOpenErrMsg is sent when opening the GitHub issue fails.
type bugReportOpenErrMsg struct{ err error }

// statusMisdetectedMsg is emitted when a wrong-status report is submitted, so
// app.go can record the analytics event without the dialog importing analytics.
type statusMisdetectedMsg struct {
	shown, expected, agent string
	hookStatus, paneDetect string
	mismatch, wroteContent bool
}

// reportStage is which screen the dialog is on. The type picker comes first so
// the report lands under the right label with a body sized to its kind — a
// feature request has no business carrying a debug log, and a status complaint
// filed as a generic bug is the failure mode this whole flow exists to fix.
type reportStage int

const (
	stagePick reportStage = iota
	stageForm
)

// reportKind is what the user said they were reporting.
type reportKind int

const (
	kindStatus reportKind = iota
	kindBug
	kindFeature
)

// BugReportDialog displays diagnostics and recent errors for bug reporting.
type BugReportDialog struct {
	visible bool
	width   int
	height  int
	scroll  int // scroll offset for content

	stage      reportStage
	kind       reportKind
	pickCursor int
	// kinds is the picker's rows for this invocation. kindStatus is present
	// only when a session was under the cursor at `!` — there is nothing to
	// report a wrong status *about* otherwise.
	kinds  []reportKind
	status statusReportForm

	descInput     textinput.Model
	report        *diagnostics.Report
	renderStats   string // pre-formatted render stats markdown
	errorEntries  []ErrorEntry
	actionEntries []ActionEntry
	contentLines  int // total rendered content lines
	submitting    bool
}

// NewBugReportDialog creates a bug report dialog.
func NewBugReportDialog() *BugReportDialog {
	ti := textinput.New()
	ti.Placeholder = "Describe what happened..."
	ti.CharLimit = 256
	ti.SetWidth(48)
	return &BugReportDialog{descInput: ti}
}

// Show collects diagnostics and shows the dialog.
//
// sess describes the session under the cursor at the keypress, or nil when there
// isn't one. Its status is read here and never refreshed: the capture and this
// label both freeze the instant being complained about, and a status that
// self-corrects before submit must not silently rewrite the report.
func (d *BugReportDialog) Show(version string, sessionCount int, errors *ErrorHistory, actions *ActionLog, tuiWidth, tuiHeight int, rs *RenderStats, uptime time.Duration, sess *session.Session) {
	d.visible = true
	d.scroll = 0
	d.submitting = false
	d.descInput.SetValue("")
	d.descInput.Focus()

	d.stage = stagePick
	d.pickCursor = 0
	d.kinds = []reportKind{kindBug, kindFeature}
	d.status = statusReportForm{}
	if sess != nil {
		d.kinds = []reportKind{kindStatus, kindBug, kindFeature}
		d.status = newStatusReportForm(sess.ID, sess.Title, sess.GetStatus(), string(sess.Agent))
	}
	d.kind = d.kinds[0]

	d.report = diagnostics.Collect(version, sessionCount)
	d.report.TUIWidth = tuiWidth
	d.report.TUIHeight = tuiHeight
	if rs != nil {
		d.renderStats = rs.FormatMarkdown(uptime)
	} else {
		d.renderStats = ""
	}
	d.errorEntries = errors.Entries()
	d.actionEntries = actions.Entries()

	// Pre-format errors and actions into the report.
	d.report.RecentErrors = d.formatErrors()
	d.report.RecentActions = d.formatActions()
}

func (d *BugReportDialog) Hide()           { d.visible = false }
func (d *BugReportDialog) IsVisible() bool { return d.visible }
func (d *BugReportDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// SetSnapshot installs an async status capture, matched by session id.
//
// The id check is what makes this safe, not the visibility check: a capture
// takes long enough (capture-pane, transcript scan) that `!` on session A, esc,
// then `!` on B can land A's result while B's form is open. Both are visible
// status forms, so without the id comparison the issue would be headed with B's
// title and shown status while its signals table, screen excerpt and debug log
// all came from A — publishing A's screen, which this dialog never previewed.
func (d *BugReportDialog) SetSnapshot(snap snapshotResult) {
	if !d.visible || d.status.sessionID == "" || snap.sessionID != d.status.sessionID {
		return
	}
	d.status.snap = snap
	d.status.captured = snap.err == nil
}

// Update handles key events for the bug report dialog.
func (d *BugReportDialog) Update(msg tea.Msg) (*BugReportDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		// Non-key messages (notably tea.PasteMsg for cmd+v) go to the input.
		if d.submitting || d.stage == stagePick {
			return d, nil
		}
		var cmd tea.Cmd
		d.descInput, cmd = d.descInput.Update(msg)
		return d, cmd
	}

	if d.stage == stagePick {
		return d.updatePick(keyMsg)
	}
	return d.updateForm(keyMsg)
}

// updatePick handles the type picker. Esc closes outright rather than stepping
// back, since the picker is the first screen.
func (d *BugReportDialog) updatePick(keyMsg tea.KeyMsg) (*BugReportDialog, tea.Cmd) {
	// Show always populates kinds, but the picker must not index a nil slice if
	// the dialog is ever made visible another way.
	if len(d.kinds) == 0 {
		d.kinds = []reportKind{kindBug, kindFeature}
	}
	if d.pickCursor >= len(d.kinds) {
		d.pickCursor = 0
	}

	switch key := keyMsg.String(); key {
	case "esc":
		d.Hide()
		return d, func() tea.Msg { return bugReportClosedMsg{} }
	case "up", "k":
		if d.pickCursor > 0 {
			d.pickCursor--
		}
		return d, nil
	case "down", "j":
		if d.pickCursor < len(d.kinds)-1 {
			d.pickCursor++
		}
		return d, nil
	case "1", "2", "3":
		idx := int(key[0] - '1')
		if idx >= len(d.kinds) {
			return d, nil
		}
		d.pickCursor = idx
		d.kind = d.kinds[idx]
		d.stage = stageForm
		return d, nil
	case "enter":
		d.kind = d.kinds[d.pickCursor]
		d.stage = stageForm
		return d, nil
	}
	return d, nil
}

// updateForm handles the per-kind form. Esc returns to the picker rather than
// closing: reaching the wrong form is a misclick, and losing a typed
// description to correct it would be a punishment for it.
func (d *BugReportDialog) updateForm(keyMsg tea.KeyMsg) (*BugReportDialog, tea.Cmd) {
	switch keyMsg.String() {
	case "esc":
		if d.submitting {
			d.Hide()
			return d, func() tea.Msg { return bugReportClosedMsg{} }
		}
		d.stage = stagePick
		return d, nil
	case "enter":
		if d.submitting {
			return d, nil
		}
		desc := strings.TrimSpace(d.descInput.Value())
		if desc == "" {
			return d, nil
		}
		if d.kind == kindStatus {
			// One gate for both the key and the footer, so Enter can never act
			// on a state the footer is telling the user isn't ready.
			if d.status.submitBlocker(desc) != "" {
				return d, nil
			}
			expected, _ := d.status.expectedStatus()
			d.submitting = true
			return d, tea.Batch(d.submitStatusReport(desc, expected), d.trackMisdetection(expected))
		}
		d.submitting = true
		return d, d.openGitHubIssue(desc)
	case "up", "down":
		// ↑↓ rather than the ←→ this codebase usually gives cyclers: the
		// description input holds focus here, and ←→ are its caret keys —
		// taking them would cost the ability to fix a typo mid-string. ↑↓ do
		// nothing in a single-line input, so they are free.
		if d.kind == kindStatus && !d.submitting {
			delta := 1
			if keyMsg.String() == "up" {
				delta = -1
			}
			d.status.cycleExpected(delta)
			return d, nil
		}
	case "ctrl+p":
		// Ctrl-modified so a literal "p" still types into the description.
		if d.kind == kindStatus && !d.submitting && d.status.captured {
			d.status.includeContent = !d.status.includeContent
			return d, nil
		}
	}

	if d.submitting {
		return d, nil
	}
	var cmd tea.Cmd
	d.descInput, cmd = d.descInput.Update(keyMsg)
	return d, cmd
}

// trackMisdetection emits the analytics signal for a submitted status report.
// It carries enum values and booleans only — no paths, no screen content — so
// the aggregate survives regardless of what the reporter chose to attach.
func (d *BugReportDialog) trackMisdetection(expected session.Status) tea.Cmd {
	// Every field is read here, on the Update goroutine, and the message is
	// built whole — the closure must not reach back into d.status, which a
	// later Show() reassigns while this Cmd may still be pending.
	f := &d.status
	detection := metaSub(f.snap.meta, "detection")
	hook := metaSub(f.snap.meta, "hook")
	mismatch, _ := detection["mismatch"].(bool)
	msg := statusMisdetectedMsg{
		shown:        string(f.shownStatus),
		expected:     string(expected),
		agent:        f.agent,
		hookStatus:   metaString(hook, "status"),
		paneDetect:   metaString(detection, "pane_detected"),
		mismatch:     mismatch,
		wroteContent: f.includeContent,
	}
	return func() tea.Msg { return msg }
}

// submitStatusReport files the wrong-status issue.
func (d *BugReportDialog) submitStatusReport(desc string, expected session.Status) tea.Cmd {
	body := buildStatusReportBody(desc, expected, &d.status, d.report)
	title := fmt.Sprintf("Wrong status: showed %s, expected %s — %s",
		d.status.shownStatus, expected, ansi.Truncate(sanitizeForIssue(desc), 60, "…"))
	return d.createGitHubIssue(title, body, "bug")
}

func (d *BugReportDialog) openGitHubIssue(description string) tea.Cmd {
	// Build title from description, truncated. Sanitized like the body: the
	// title is what GitHub shows in search results and notification mail, so a
	// home path leaking here survives even when the body below it is clean.
	title := ansi.Truncate(sanitizeForIssue(description), 60, "…")

	var body string
	label := "bug"
	if d.kind == kindFeature {
		// A feature request carries prose and versions only. The error history,
		// action log, and debug log are reproduction context for a defect; on an
		// idea they are noise the maintainer has to scroll past.
		label = "enhancement"
		body = "## Feature Request\n\n### Problem\n" + sanitizeForIssue(description) + "\n\n" +
			fmt.Sprintf("### Environment\n- **Version**: %s\n- **OS**: %s (%s)\n",
				d.report.Version, d.report.OSSummary(), d.report.Arch)
	} else {
		// Inject user description and render stats into the report.
		//
		// sanitizeForIssue, not the raw description: the report formatter only
		// rewrites the home directory, so a credential pasted into a bug
		// description reached a public issue verbatim. The feature branch above
		// already sanitizes; this path was missed. Someone describing a failed
		// account add is exactly the person likely to paste one.
		body = d.report.FormatMarkdownWithDesc(sanitizeForIssue(description), sanitizeForIssue)
		if d.renderStats != "" {
			body += "\n" + d.renderStats
		}
	}

	return d.createGitHubIssue(title, body, label)
}

// ghAvailable reports whether the gh CLI is on PATH. Filing goes through it, so
// its absence is a dead end the footers have to say out loud.
func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// createGitHubIssue files an issue and opens it in the browser. Shared by all
// three report kinds; only title, body, and label differ between them.
func (d *BugReportDialog) createGitHubIssue(title, body, label string) tea.Cmd {
	if _, err := exec.LookPath("gh"); err != nil {
		return func() tea.Msg { return bugReportOpenErrMsg{err: fmt.Errorf("gh CLI not found")} }
	}

	return func() tea.Msg {
		debuglog.Logger.Info("bug report: creating GitHub issue via API", "label", label)

		// Write body to temp file.
		tmpFile, err := os.CreateTemp("", "fleet-bug-*.md")
		if err != nil {
			debuglog.Logger.Error("bug report: failed to create temp file", "err", err)
			return bugReportOpenErrMsg{err: err}
		}
		if _, err := tmpFile.WriteString(body); err != nil {
			tmpFile.Close()
			return bugReportOpenErrMsg{err: err}
		}
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())

		// Create issue via API (no URL length limit), then open in browser.
		cmd := exec.Command("gh", "issue", "create",
			"--repo", "brizzai/fleet",
			"--title", title,
			"--label", label,
			"--body-file", tmpFile.Name(),
		)
		out, err := cmd.Output()
		if err != nil {
			debuglog.Logger.Error("bug report: gh create failed", "err", err)
			return bugReportOpenErrMsg{err: fmt.Errorf("gh issue create: %w", err)}
		}

		// gh outputs the issue URL on stdout.
		issueURL := strings.TrimSpace(string(out))
		debuglog.Logger.Info("bug report: issue created", "url", issueURL)
		if issueURL != "" {
			if err := openURL(issueURL); err != nil {
				debuglog.Logger.Warn("bug report: failed to open issue URL", "url", issueURL, "err", err)
			}
		}
		return bugReportClosedMsg{url: issueURL}
	}
}

// View renders the bug report dialog.
func (d *BugReportDialog) View() string {
	if d.stage == stagePick {
		return d.viewPick()
	}
	if d.kind == kindStatus {
		return d.viewStatusForm()
	}

	dialogWidth := 60
	if dialogWidth > d.width-4 {
		dialogWidth = d.width - 4
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}
	innerWidth := dialogWidth - 6 // padding

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	errorStyle := lipgloss.NewStyle().Foreground(ColorRed)

	// Title.
	heading := "Bug Report"
	if d.kind == kindFeature {
		heading = "Feature Request"
	}
	b.WriteString(titleStyle.Render(heading))
	b.WriteString("\n")

	// Description input.
	b.WriteString("\n")
	b.WriteString(sectionStyle.Render("Description"))
	b.WriteString("\n")
	d.descInput.SetWidth(innerWidth - 2)
	b.WriteString("  " + d.descInput.View())
	b.WriteString("\n")

	// Errors and actions are reproduction context for a defect. A feature
	// request has nothing to reproduce, and its body omits them too.
	if d.kind != kindFeature {
		// Recent Errors.
		b.WriteString("\n")
		errCount := len(d.errorEntries)
		if errCount > 5 {
			errCount = 5
		}
		b.WriteString(sectionStyle.Render(fmt.Sprintf("Recent Errors (%d)", len(d.errorEntries))))
		b.WriteString("\n")
		if len(d.errorEntries) == 0 {
			b.WriteString(dimStyle.Render("  No errors recorded"))
			b.WriteString("\n")
		} else {
			for i := 0; i < errCount; i++ {
				e := d.errorEntries[i]
				ago := formatTimeAgo(e.Timestamp)
				line := fmt.Sprintf("  %s  %s", dimStyle.Render(ago), errorStyle.Render(truncate(e.Message, innerWidth-12)))
				b.WriteString(line)
				b.WriteString("\n")
			}
		}

		// Recent Actions.
		b.WriteString("\n")
		actionCount := len(d.actionEntries)
		if actionCount > 5 {
			actionCount = 5
		}
		b.WriteString(sectionStyle.Render("Recent Actions"))
		b.WriteString("\n")
		if len(d.actionEntries) == 0 {
			b.WriteString(dimStyle.Render("  No actions recorded"))
			b.WriteString("\n")
		} else {
			for i := 0; i < actionCount; i++ {
				a := d.actionEntries[i]
				ago := formatTimeAgo(a.Timestamp)
				result := dimStyle.Render("ok")
				if !a.Success {
					result = errorStyle.Render("ERROR")
				}
				detail := truncate(a.Detail, innerWidth-35)
				line := fmt.Sprintf("  %s  %-18s %-20s %s",
					dimStyle.Render(ago),
					a.Action,
					dimStyle.Render(detail),
					result,
				)
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}

	// Diagnostics summary.
	b.WriteString("\n")
	b.WriteString(sectionStyle.Render("Diagnostics"))
	b.WriteString("\n")
	r := d.report
	diag := fmt.Sprintf("  %s · %s · %s", r.Version, r.OSSummary(), r.Arch)
	if r.TmuxVersion != "" {
		diag += fmt.Sprintf(" · %s", r.TmuxVersion)
	}
	b.WriteString(dimStyle.Render(diag))
	b.WriteString("\n")

	// Divider.
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", innerWidth)))
	b.WriteString("\n")

	// Controls.
	if d.submitting {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("Creating issue..."))
	} else {
		ghAvailable := true
		if _, err := exec.LookPath("gh"); err != nil {
			ghAvailable = false
		}
		if ghAvailable {
			hasDesc := strings.TrimSpace(d.descInput.Value()) != ""
			if hasDesc {
				b.WriteString(dimStyle.Render("enter") + " Submit    " + dimStyle.Render("esc") + " Back")
			} else {
				b.WriteString(dimStyle.Render("Type a description, then press enter") + "    " + dimStyle.Render("esc") + " Back")
			}
		} else {
			b.WriteString(dimStyle.Render("gh CLI not found") + "    " + dimStyle.Render("esc") + " Back")
		}
	}

	content := b.String()
	d.contentLines = strings.Count(content, "\n") + 1

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth)

	box := boxStyle.Render(content)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

func (d *BugReportDialog) formatErrors() []string {
	var result []string
	for _, e := range d.errorEntries {
		ago := formatTimeAgo(e.Timestamp)
		result = append(result, fmt.Sprintf("%s | %s", ago, sanitizeForIssue(e.Message)))
	}
	return result
}

func (d *BugReportDialog) formatActions() []string {
	var result []string
	count := len(d.actionEntries)
	if count > 20 {
		count = 20
	}
	for i := 0; i < count; i++ {
		a := d.actionEntries[i]
		ts := a.Timestamp.Format("15:04:05")
		detail := sanitizeForIssue(a.Detail)
		result_ := "ok"
		if !a.Success {
			result_ = "ERROR"
		}
		result = append(result, fmt.Sprintf("%s | %s | %s | %s", ts, a.Action, detail, result_))
	}
	return result
}

func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
