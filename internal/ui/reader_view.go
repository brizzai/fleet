package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
)

// commentIndent is how far a comment box is inset from the gutter, so it reads
// as hanging off the line it is about rather than as another line of code.
const commentIndent = 5

// tabWidth fixes what a tab is worth. Terminals disagree, and a tab rendered at
// the terminal's own width shears every column after it — including the diff's
// own gutter on the row below.
const tabWidth = 4

// View renders the reader: a file tree beside the diff, under a header that
// says what this PR is and how far through it you are.
//
// Two panels rather than one stream, because orientation and reading are
// different jobs: the tree answers "what is in here and where am I", the diff
// answers "what changed". Collapsing them into one column made you hold the
// first question in your head while doing the second.
func (d *ReaderDialog) View() string {
	if d.doc == nil {
		return ""
	}
	rows := d.body()
	treeW := d.treeWidth()
	diffW := d.diffWidth()

	// The accent border marks which panel has the keyboard — the only
	// border-level focus signal fleet has (design-system §4), and the same one
	// the sidebar and preview use on the main screen.
	// The session takes the whole width. There is no navigation to put beside a
	// terminal — the pane is not a list of anything — and a pane squeezed into
	// three quarters of the screen wraps differently from the one the agent is
	// actually drawing into.
	if d.tab == tabSession {
		return d.header() + "\n" + d.tabBar() + "\n" +
			RenderBorderedPanelTopRight(
				strings.Join(d.renderSession(d.width-2, rows), "\n"),
				d.sessionTitle(d.width), d.sessionStatus(),
				d.width, rows+2, true) + "\n" + d.footer()
	}

	right, rightTitle, rightStatus := d.rightPanel(diffW, rows)
	diff := RenderBorderedPanelTopRight(
		strings.Join(right, "\n"), rightTitle, rightStatus,
		diffW, rows+2, !d.treeFocus)

	body := diff
	if treeW > 0 {
		left, leftTitle := d.renderTree(treeW-2, rows), "Files"
		if d.tourMode() {
			left, leftTitle = d.renderTourPanel(treeW-2, rows), "Tour"
		}
		tree := RenderBorderedPanelInsets(
			strings.Join(left, "\n"),
			leftTitle, "", d.treeFooter(), "",
			treeW, rows+2, d.treeFocus)
		body = lipgloss.JoinHorizontal(lipgloss.Top, tree, diff)
	}

	out := d.header() + "\n" + d.tabBar() + "\n" + body + "\n" + d.footer()
	if d.help {
		return centreOverlay(d.helpSheet(), out, d.width, d.height)
	}
	if d.mode == modeSubmit {
		// Centred and over a dimmed backdrop, because this is the one action in
		// the reader that reaches another person's pull request: it takes the
		// whole screen's attention rather than sitting in a corner of it.
		return centreOverlay(d.submitSheet(), out, d.width, d.height)
	}
	return out
}

// centreOverlay drops a panel in the middle of a dimmed screen.
func centreOverlay(panel, under string, width, height int) string {
	x := max((width-lipgloss.Width(panel))/2, 0)
	y := max((height-lipgloss.Height(panel))/2, 0)
	return overlayAt(panel, dimBackdrop(under), x, y)
}

// tabBar names the three views and which one you are in.
//
// A row of its own rather than squeezed into the header: the header already
// carries the pull request's identity and its counts, and a bar that has to
// share space with them is a bar that disappears on a narrow terminal — which
// is where a reminder that there are two other views is worth the most.
//
// Rendered as a SELECTION and not as a mode, per the design system's test:
// switching a tab does not move the keyboard. The digit leads each label
// because the digit is how you get there.
func (d *ReaderDialog) tabBar() string {
	var b strings.Builder
	for _, t := range readerTabs {
		label := " " + t.digit + " " + t.label + " "
		if t.tab == d.tab {
			b.WriteString(SelectionPill(true).Render(label))
			continue
		}
		b.WriteString(HelpKeyStyle.Render(" "+t.digit) + DimStyle.Render(" "+t.label+" "))
	}
	bar := " " + b.String()
	pad := max(d.width-lipgloss.Width(bar), 0)
	return bar + strings.Repeat(" ", pad)
}

// rightPanel is the diff, or the narration above it, or a step that has nowhere
// to send you at all.
//
// A tour step with no anchors takes the whole panel: an opening step explaining
// the shape of a change before any one file makes sense is worth more than a
// file it could have pointed at, and a diagram needs the wide side of the
// screen rather than the quarter.
func (d *ReaderDialog) rightPanel(diffW, rows int) (lines []string, title, status string) {
	if d.tourMode() {
		if st, an, ok := d.tourSelection(); ok && an == nil && len(st.Anchors) == 0 {
			return d.renderTourOverview(st, diffW-2, rows), "Tour",
				fmt.Sprintf("step %d/%d", d.tourStepIndex()+1, len(d.tour.Steps))
		}
	}

	title, status = d.diffTitle(diffW), d.diffStatus()
	var band []string
	if d.tourMode() {
		band = d.tourBand(diffW - 2)
	}
	// The band comes out of the diff's rows, never on top of them: this panel
	// promises exactly rows lines, and a band that added to them would push the
	// footer off the bottom of the screen.
	lines = append(band, d.renderDiff(diffW-2, max(rows-len(band), 1))...)
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return lines, title, status
}

// readerKeys is every key the reader answers to, grouped by what you are doing.
//
// The source of truth for the `?` sheet, so a key that works and is not listed
// here is a key nobody finds. Arrows lead each row: the letters are aliases
// kept for muscle memory, not the advertised way in.
var readerKeys = []struct {
	section string
	rows    [][2]string
}{
	{"Move", [][2]string{
		{"↑ ↓", "one row  (j k)"},
		{"⇧↑ ⇧↓", "previous / next hunk  ([ ])"},
		{"⇧← ⇧→", "previous / next file  (, .)"},
		{"pgdn pgup", "page down / up"},
		{"⌃d  ⌃u", "half page"},
		{"g  G", "top / bottom"},
	}},
	{"Read", [][2]string{
		{"⏎", "expand the fold under the cursor"},
		{"+", "expand it completely"},
		{"space", "mark read, go to the next unread file"},
		{"m", "mark read / unread, stay where you are"},
		{"s", "show / fold the files salience hid"},
		{"|", "side-by-side ↔ unified"},
	}},
	{"Tabs", [][2]string{
		{"1", "the tour — fleet's route through the change"},
		{"2", "the diff, with the file tree"},
		{"3", "the agent working on this review"},
	}},
	{"Tour", [][2]string{
		{"t", "fleet's route through the change ↔ the file list"},
		{"↑ ↓", "walk the route — the diff follows"},
		{"→ ←", "into a step's stops, and back out"},
		{"⏎", "on a stop: keep it and go read"},
	}},
	{"Comment", [][2]string{
		{"c", "comment on this line"},
		{"⇥ ⇧⇥", "issue / nit / question / suggestion"},
		{"⌃j", "newline inside a comment"},
		{"⏎", "save · esc discards"},
		{"e  d", "edit / delete the comment under the cursor"},
	}},
	{"Submit", [][2]string{
		{"S", "send the review to GitHub"},
		{"⇥ ←→", "comment / approve / request changes"},
		{"⏎", "submit · esc sends nothing"},
	}},
	{"Find", [][2]string{
		{"/", "search the code"},
		{"n  N", "next / previous match"},
	}},
	{"Panels", [][2]string{
		{"⇥", "move the keyboard to the file tree, and back"},
		{"↑ ↓", "in the tree: pick a file — the diff follows"},
		{"⏎", "on a file: keep it and go back to the diff"},
		{"⏎ ← →", "on a folder: fold and unfold it"},
		{"←", "on a file: jump out to its folder"},
	}},
	{"Leave", [][2]string{
		{"?", "this sheet — any key closes it"},
		{"⌃q", "quit — esc also works"},
	}},
}

// helpSheet renders the full key list as a centred overlay.
//
// Two columns wherever they fit. One column of twenty-odd rows is taller than a
// short terminal and gets clipped from the bottom — which silently loses the
// last section, and the last section is how you leave.
func (d *ReaderDialog) helpSheet() string {
	blocks := make([][]string, len(readerKeys))
	for i, g := range readerKeys {
		rows := []string{PaletteSectionStyle.Render(g.section)}
		for _, r := range g.rows {
			pad := max(helpKeyCol-lipgloss.Width(r[0]), 1)
			rows = append(rows, "  "+HelpKeyStyle.Render(r[0])+
				strings.Repeat(" ", pad)+HelpDescStyle.Render(r[1]))
		}
		blocks[i] = rows
	}

	cols := 1
	if d.width >= 96 {
		cols = 2
	}
	body := joinHelpColumns(blocks, cols)

	inner := 0
	for _, r := range body {
		inner = max(inner, lipgloss.Width(r))
	}
	w := min(inner+4, max(d.width-4, 20))
	h := min(len(body)+2, max(d.height-2, 5))
	return RenderBorderedPanelInsets(strings.Join(body, "\n"),
		"Reader keys", "", "any key closes", "", w, h, true)
}

// helpKeyCol is the width of the key column, sized to the widest chord.
const helpKeyCol = 10

// submitSheetWidth is wide enough for a path, a line number and the start of a
// comment on one row, and no wider: this is a confirmation, not a second reader.
const submitSheetWidth = 76

// submitSheetComments is how many comment rows the sheet lists before counting
// the rest. The list is here to let you recognise what you wrote, not to read it
// again — the comments are still on screen behind this box.
const submitSheetComments = 6

// submitSheet is the confirmation that posts the review.
//
// It exists because submitting is the one thing the reader does that another
// person sees. Everything else here is a local act you can undo by pressing the
// key again; this one arrives as a notification on someone's pull request, so it
// states the verdict, counts what goes with it, and refuses until the summary
// GitHub requires is actually there.
func (d *ReaderDialog) submitSheet() string {
	inner := min(submitSheetWidth, max(d.width-6, 30)) - 4
	pending := d.pendingComments()

	var body []string

	// The verdict, as three words with the chosen one carrying the selection —
	// the same two-state pill the file tree and the session list use, so "this
	// is what is selected" reads identically wherever it appears.
	var verdict []string
	for i, label := range submitLabels {
		if i == d.submitEvent {
			verdict = append(verdict, SelectionPill(true).Render(" "+label+" "))
			continue
		}
		verdict = append(verdict, DimStyle.Render(" "+label+" "))
	}
	body = append(body, " "+strings.Join(verdict, " "), "")

	if len(pending) == 0 {
		body = append(body, " "+DimStyle.Render("no comments — the summary goes on its own"))
	} else {
		body = append(body, " "+SessionItemStyle.Render(plural(len(pending), "comment")))
		for i, c := range pending {
			if i >= submitSheetComments {
				body = append(body, "   "+DimStyle.Render("… "+plural(len(pending)-i, "more")))
				break
			}
			body = append(body, "   "+d.submitCommentRow(c, inner-3))
		}
	}
	body = append(body, "")

	// The summary. Labelled even when empty, because an unlabelled empty row is
	// indistinguishable from the box simply having nothing in it.
	body = append(body, " "+DimStyle.Render("Summary"))
	for _, line := range d.submitBodyRows(inner - 2) {
		body = append(body, " "+line)
	}

	if d.submitErr != "" {
		// GitHub's own words, not ours: "line must be part of the diff" names a
		// thing to fix, and paraphrasing it would lose that.
		body = append(body, "", " "+PRFailStyle.Render("✕ "+ansi.Truncate(d.submitErr, max(inner-2, 8), "…")))
	}

	foot := keyHints("⏎", "submit", "⇥", "verdict", "esc", "cancel")
	if reason := d.submitBlocker(); reason != "" {
		// The footer names what is missing rather than the key that is dead —
		// an unexplained refusal on a key the sheet advertises reads as a bug.
		foot = DimStyle.Render("  " + reason)
	}

	title := "Submit review"
	right := d.repo + " #" + itoa(d.pr)
	return RenderBorderedPanelInsets(strings.Join(body, "\n"),
		title, right, foot, "", inner+4, len(body)+2, true)
}

// submitCommentRow is one queued comment: where it lands, and how it opens.
func (d *ReaderDialog) submitCommentRow(c review.Comment, width int) string {
	at := c.File + ":" + itoa(c.Line)
	// The path is cut from the LEFT for the reason it always is here: the
	// basename identifies the file, and every row shares the leading segments.
	at = truncFront(at, max(width/2, 12))
	head := strings.SplitN(c.Payload(), "\n", 2)[0]
	room := width - lipgloss.Width(at) - 2
	if room < 4 {
		return DiffNumStyle.Render(at)
	}
	return DiffNumStyle.Render(at) + "  " + DimStyle.Render(ansi.Truncate(head, room, "…"))
}

// submitBodyRows is the typed summary, wrapped, with the caret on the end.
//
// Always at least one row: a field that collapses to nothing when empty makes
// the box change height on the first keystroke.
func (d *ReaderDialog) submitBodyRows(width int) []string {
	var out []string
	for _, para := range strings.Split(d.submitBody, "\n") {
		rows := wrapTo(width, para)
		if len(rows) == 0 {
			rows = []string{""}
		}
		out = append(out, rows...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	if d.submitting {
		return []string{DimStyle.Render("posting to GitHub…")}
	}
	last := len(out) - 1
	out[last] = SessionItemStyle.Render(out[last]) + FocusCaret().Render("▏")
	for i := 0; i < last; i++ {
		out[i] = SessionItemStyle.Render(out[i])
	}
	return out
}

// joinHelpColumns lays the sections out in n columns, splitting where the
// running height first passes half — so the two columns come out level rather
// than one section per column.
func joinHelpColumns(blocks [][]string, cols int) []string {
	if cols < 2 || len(blocks) < 2 {
		return flattenHelp(blocks)
	}
	total := 0
	for _, b := range blocks {
		total += len(b) + 1
	}
	split, run := len(blocks), 0
	for i, b := range blocks {
		if run+len(b) > total/2 && i > 0 {
			split = i
			break
		}
		run += len(b) + 1
	}

	left, right := flattenHelp(blocks[:split]), flattenHelp(blocks[split:])
	leftW := 0
	for _, r := range left {
		leftW = max(leftW, lipgloss.Width(r))
	}

	out := make([]string, max(len(left), len(right)))
	for i := range out {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		// Pad the raw column, then append: padding a styled string counts the
		// ANSI bytes and the second column comes out ragged.
		l += strings.Repeat(" ", max(leftW-lipgloss.Width(l), 0))
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + "   " + r
	}
	return out
}

func flattenHelp(blocks [][]string) []string {
	var out []string
	for i, b := range blocks {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, b...)
	}
	return out
}

// header names the PR and how far through it you are.
func (d *ReaderDialog) header() string {
	readCount := 0
	for _, f := range d.doc.Stream.Files {
		if d.read[f] {
			readCount++
		}
	}
	left := fmt.Sprintf(" ⌘ #%d  %s", d.pr, d.title)
	meta := fmt.Sprintf("%s · %d files · +%d −%d · %d read ",
		d.author, len(d.doc.Stream.Files), d.adds, d.dels, readCount)

	left = ansi.Truncate(left, max(d.width-lipgloss.Width(meta)-2, 1), "…")
	gap := d.width - lipgloss.Width(left) - lipgloss.Width(meta)
	if gap < 1 {
		gap = 1
	}
	return TitleStyle.Render(left) + strings.Repeat(" ", gap) + DimStyle.Render(meta)
}

// diffTitle is the current file, inset in the panel's top border so it never
// scrolls away — which is what it did as a row inside the stream.
func (d *ReaderDialog) diffTitle(panelW int) string {
	f := d.doc.Stream.FileAt(d.streamAt(d.cursor))
	if f == "" {
		f = "diff"
	}
	// Truncate from the LEFT: the basename is what identifies a file, and a
	// deep monorepo path cut from the right leaves you reading `apps/backend/…`
	// on every row.
	return truncFront(f, max(panelW-24, 12))
}

// diffStatus is the counts and position, inset top-right.
func (d *ReaderDialog) diffStatus() string {
	n := len(d.doc.Stream.HunkStarts)
	if n == 0 {
		return ""
	}
	cur := d.streamAt(d.cursor)
	// Clamped to 1: sitting above the first hunk header still means you are
	// looking at the first hunk's file, and "hunk 0/4" reads as a bug.
	hunk := 1
	for i, h := range d.doc.Stream.HunkStarts {
		if h <= cur {
			hunk = i + 1
		}
	}
	return fmt.Sprintf("hunk %d/%d", hunk, n)
}

func (d *ReaderDialog) treeFooter() string {
	if d.tourMode() {
		return "t · files"
	}
	// The tour's own state lives here while it is being built, because this is
	// the panel it will appear in — a notice about it anywhere else would be a
	// notice about nothing the user can see.
	if st := d.tourStatus(); st != "" {
		return st
	}
	if d.tour != nil {
		return "t · tour"
	}
	if n := d.hiddenCount(); n > 0 {
		return fmt.Sprintf("⊞ %d folded · s", n)
	}
	if d.showAll {
		return "s · fold again"
	}
	return ""
}

// renderTree draws the file tree.
//
// Two states, and they are different things. Unfocused it is a READOUT: it
// follows the diff, marks the current file, and has no cursor to move. Focused
// it is a LIST: it carries the selection, and moving that selection scrolls the
// diff — the sidebar-and-preview relationship, where the list drives the panel
// beside it.
func (d *ReaderDialog) renderTree(width, rows int) []string {
	out := make([]string, rows)
	if width <= 0 {
		return out
	}
	current := d.doc.Stream.FileAt(d.streamAt(d.cursor))

	// Anchor the scroll on whichever row matters: the selection when this
	// panel is being driven, the diff's file when it is only reporting.
	anchor := 0
	for i, n := range d.treeView {
		if !n.IsDir && n.Path == current {
			anchor = i
			break
		}
	}
	if d.treeFocus {
		anchor = d.treeCursor
	}
	top := 0
	if anchor >= rows {
		top = anchor - rows + 1
	}

	line := 0
	for i := top; i < len(d.treeView) && line < rows; i++ {
		n := d.treeView[i]
		indent := strings.Repeat(" ", n.Depth)

		if n.IsDir {
			// chevronGlyph honours the user's triangle/plus-minus setting, so
			// a folded directory here looks like a folded group in the sidebar.
			chev := chevronGlyph(!d.collapsed[n.Path])
			if d.treeFocus && i == d.treeCursor {
				raw := chev + " " + indent + n.Label
				raw = ansi.Truncate(raw, max(width-1, 1), "…")
				raw += strings.Repeat(" ", max(width-lipgloss.Width(raw), 0))
				out[line] = SelectionPill(true).Render(raw)
				line++
				continue
			}
			out[line] = SelectionMarker(false).Render(chev) + " " +
				DimStyle.Render(ansi.Truncate(indent+n.Label, max(width-2, 1), "…"))
			line++
			continue
		}

		mark := " "
		if d.read[n.Path] {
			mark = "✓"
		}
		count := "+" + strconv.Itoa(n.Adds)

		// A row is: tick, space, indent, status, space, name, pad, count,
		// space — five fixed columns plus at least one of padding. Budget the
		// name against ALL of them: getting this short truncates the change
		// count instead of the filename, so `+11` silently renders as `+1`.
		// tick, space, chevron column, status, space, trailing space — the
		// chevron column is blank on a file so names line up under their
		// directory's label rather than under its glyph.
		const fixed = 6
		nameW := width - lipgloss.Width(indent) - len(count) - fixed - 1
		name := ansi.Truncate(n.Label, max(nameW, 1), "…")
		pad := width - lipgloss.Width(indent) - lipgloss.Width(name) - len(count) - fixed
		if pad < 1 {
			pad = 1
		}

		// The session list's two states, verbatim. Accent fill while this panel
		// owns the keyboard; a muted band when it does not, so the tree still
		// shows which file you are in without competing with the diff for
		// attention — exactly what the sidebar does while the preview is
		// focused. Anything else would make one list in fleet look selected in
		// a way no other list does.
		selected := (d.treeFocus && i == d.treeCursor) ||
			(!d.treeFocus && n.Path == current)
		if selected {
			// The selection owns the row's color, so the status letter and the
			// read tick give up theirs: a second color inside a fill reads as
			// two selections. Composed in two parts so the count keeps the
			// lighter weight while the fill stays one continuous block.
			head := mark + "  " + indent + statusRune(n.Status) + " " + name +
				strings.Repeat(" ", pad)
			out[line] = SelectionPill(d.treeFocus).Render(head) +
				SelectionPillSecondary(d.treeFocus).Render(count+" ")
			line++
			continue
		}

		tick := " "
		if d.read[n.Path] {
			tick = StatusRunningStyle.Render("✓")
		}
		nameStyled := SessionItemStyle.Render(name)
		if d.read[n.Path] {
			nameStyled = DimStyle.Render(name)
		}
		out[line] = tick + "  " + indent + statusLetter(n.Status) + " " + nameStyled +
			strings.Repeat(" ", pad) + DiffNumStyle.Render(count) + " "
		line++
	}
	return out
}

// statusRune is statusLetter's unstyled twin, for text that will be padded
// before it is styled.
func statusRune(s string) string {
	switch s {
	case "added":
		return "A"
	case "removed":
		return "D"
	case "renamed":
		return "R"
	}
	return "M"
}

// statusLetter is git's own single-letter status, colored the way the sidebar
// colors state: what happened to the file, not how much it changed.
func statusLetter(s string) string {
	switch s {
	case "added":
		return StatusRunningStyle.Render("A")
	case "removed":
		return ErrorStyle.Render("D")
	case "renamed":
		return StatusFinishedStyle.Render("R")
	}
	return StatusWaitingStyle.Render("M")
}

// renderDiff draws the code panel's inner rows.
func (d *ReaderDialog) renderDiff(width, rows int) []string {
	out := make([]string, rows)
	end := min(d.top+rows, len(d.rows))
	for i := d.top; i < end; i++ {
		out[i-d.top] = d.renderRow(i, width)
	}
	return out
}

// renderRow draws one display row.
func (d *ReaderDialog) renderRow(i, width int) string {
	r := d.rows[i]
	caret := " "
	if l, ok := d.lineAt(i); ok && l.Draft {
		// The box being typed into carries the accent border and the text
		// caret. A third marker on the row would be a second highlight for one
		// focus (design-system §6).
		caret = " "
	} else if i == d.cursor {
		// The caret is drawn on EVERY row kind, including the ones that carry
		// no code. A cursor that vanishes on a file header is a cursor you
		// lose, and the header is exactly where `.` and `,` leave you.
		caret = FocusCaret().Render("▸")
	}

	onCursor := i == d.cursor

	if r.full || !d.split {
		l, ok := d.streamLine(r.primary())
		if !ok {
			if onCursor {
				return caret + cursorFill(review.LineBlank).Render(strings.Repeat(" ", max(width-1, 0)))
			}
			return ""
		}
		return caret + d.renderFull(l, width-1, onCursor)
	}

	// Side by side: two independent cells with a rule between them. The
	// highlight spans BOTH, because the cursor is on a row, not on a column.
	half := (width - 2) / 2
	rule := DimStyle.Render("│")
	if onCursor {
		rule = cursorFill(review.LineBlank).Render("│")
	}
	left := d.renderCell(r.left, half, onCursor, true)
	right := d.renderCell(r.right, width-2-half, onCursor, false)
	return caret + left + rule + right
}

func (d *ReaderDialog) streamLine(i int) (review.Line, bool) {
	if i < 0 || i >= len(d.doc.Stream.Lines) {
		return review.Line{}, false
	}
	return d.doc.Stream.Lines[i], true
}

// cursorFill is the background for the row the keys act from.
//
// Derived from the row's own tint rather than replacing it: the row still has
// to say added or deleted while it says "you are here".
func cursorFill(kind review.LineKind) lipgloss.Style {
	switch kind {
	case review.LineAdd:
		return DiffCursorAddRowStyle
	case review.LineDel:
		return DiffCursorDelRowStyle
	}
	return DiffCursorRowStyle
}

// renderFull draws a row that spans the whole panel.
func (d *ReaderDialog) renderFull(l review.Line, width int, onCursor bool) string {
	if onCursor {
		switch l.Kind {
		case review.LineBlank:
			return cursorFill(l.Kind).Render(strings.Repeat(" ", max(width, 0)))
		case review.LineFileHeader:
			return padStyled(FileBandStyle, " "+ansi.Truncate(l.Text, max(width-2, 1), "…"), width)
		case review.LineHunkHeader:
			return padStyled(DiffCursorRowStyle.Foreground(ColorBlue), " "+l.Text, width)
		case review.LineNote:
			return padStyled(DiffCursorRowStyle.Foreground(ColorTextDim), " "+l.Text, width)
		case review.LineExpand:
			return d.renderExpandStyled(l, width, DiffCursorRowStyle)
		}
	}
	switch l.Kind {
	case review.LineBlank:
		return ""
	case review.LineFileHeader:
		// A band rather than a plain row. It is the only thing separating two
		// files in one continuous stream, and a bold line of text scrolls past
		// looking like a comment about the code above it.
		return padStyled(FileBandStyle, " "+ansi.Truncate(l.Text, max(width-2, 1), "…"), width)
	case review.LineHunkHeader:
		return padStyled(DiffHunkStyle, " "+l.Text, width)
	case review.LineNote:
		return " " + DimStyle.Render(ansi.Truncate(l.Text, max(width-1, 1), "…"))
	case review.LineExpand:
		return d.renderExpand(l, width)
	case review.LineCommentTop, review.LineCommentBody, review.LineCommentBottom:
		return d.renderComment(l, width)
	}
	return d.renderCodeLine(l, width, true, true, onCursor)
}

// renderCell draws one side of a side-by-side row. An index of -1 is the empty
// cell that keeps the two sides level.
func (d *ReaderDialog) renderCell(streamIdx, width int, onCursor, isLeft bool) string {
	l, ok := d.streamLine(streamIdx)
	if !ok {
		blank := strings.Repeat(" ", max(width, 0))
		if onCursor {
			return DiffCursorRowStyle.Render(blank)
		}
		return blank
	}
	return d.renderCodeLine(l, width, false, isLeft, onCursor)
}

// renderExpand draws a fold marker and says what opens it.
//
// The keys are named on the row itself rather than only in the footer: a fold
// is the one control in the reader you can walk past without noticing, and a
// row that reads "⋯ 31 lines hidden" with no verb is a statement, not an offer.
func (d *ReaderDialog) renderExpand(l review.Line, width int) string {
	return d.renderExpandStyled(l, width, DiffHunkStyle)
}

func (d *ReaderDialog) renderExpandStyled(l review.Line, width int, base lipgloss.Style) string {
	body := "  " + l.Text
	hint := "  ⏎ expand · + all"
	pad := width - lipgloss.Width(body) - lipgloss.Width(hint)
	if pad < 1 {
		pad = 1
		hint = ""
	}
	return base.Foreground(ColorBlue).Render(body) +
		base.Render(strings.Repeat(" ", pad)) +
		base.Foreground(ColorAccent).Render(hint)
}

// renderComment draws one row of a comment box, in the stream at its anchor.
func (d *ReaderDialog) renderComment(l review.Line, width int) string {
	if l.Comment == nil {
		return ""
	}
	inner := width - commentIndent
	if inner < 12 {
		return ""
	}
	border := DimStyle
	if l.Draft {
		border = lipgloss.NewStyle().Foreground(ColorAccent)
	}
	lead := strings.Repeat(" ", commentIndent)

	switch l.Kind {
	case review.LineCommentTop:
		tag := commentKindStyle(l.Comment.Kind).Render(l.Comment.Kind.String())
		label := tag + DimStyle.Render(" · L"+strconv.Itoa(l.Comment.Line))
		if l.Draft {
			label += DimStyle.Render(" · ") + HelpKeyStyle.Render("⇥") + DimStyle.Render(" type")
		} else if l.Comment.Author != "" {
			label += DimStyle.Render(" · " + l.Comment.Author)
		}
		fill := inner - lipgloss.Width(label) - 5
		if fill < 0 {
			fill = 0
		}
		return lead + border.Render("╭─ ") + label + border.Render(" "+strings.Repeat("─", fill)+"╮")

	case review.LineCommentBody:
		text := l.Text
		if l.Draft && l.CommentIdx == lastDraftRow(l) {
			text += "▏"
		}
		body := ansi.Truncate(text, max(inner-4, 1), "…")
		pad := inner - 4 - lipgloss.Width(body)
		if pad < 0 {
			pad = 0
		}
		return lead + border.Render("│ ") + SessionItemStyle.Render(body) +
			strings.Repeat(" ", pad) + border.Render(" │")

	default:
		var hint string
		if l.Draft {
			hint = HelpKeyStyle.Render("⏎") + DimStyle.Render(" save · ") +
				HelpKeyStyle.Render("⌃j") + DimStyle.Render(" newline · ") +
				HelpKeyStyle.Render("esc") + DimStyle.Render(" discard")
		} else if l.Comment.Sent {
			hint = DimStyle.Render("submitted")
		} else {
			hint = DimStyle.Render("unsent · ") + HelpKeyStyle.Render("e") +
				DimStyle.Render(" edit · ") + HelpKeyStyle.Render("d") + DimStyle.Render(" delete")
		}
		fill := inner - lipgloss.Width(hint) - 5
		if fill < 0 {
			fill = 0
		}
		return lead + border.Render("╰─ ") + hint + border.Render(" "+strings.Repeat("─", fill)+"╯")
	}
}

// lastDraftRow is which body row carries the caret. Comment rows do not know
// how many siblings they have, so the caret rides the row the draft's own text
// ends on — which wrapBody guarantees is the last one.
func lastDraftRow(l review.Line) int {
	if l.Comment == nil {
		return 0
	}
	return len(wrapCount(l.Comment.Body))
}

// wrapCount exists only so lastDraftRow can count rows without re-exporting the
// wrapper. The draft always renders its caret on its final body row.
func wrapCount(body string) []struct{} {
	n := strings.Count(body, "\n") + 1
	return make([]struct{}, n)
}

func commentKindStyle(k review.CommentKind) lipgloss.Style {
	switch k {
	case review.CommentNit:
		return CommentNitStyle
	case review.CommentQuestion:
		return CommentQuestionStyle
	case review.CommentSuggestion:
		return CommentSuggestionStyle
	}
	return CommentIssueStyle
}

// renderCodeLine draws one line of code: gutter, sign, then the code itself.
//
// The gutter cell carries a saturated tint and the code area a quiet wash, so
// the change rail reads at a glance while the code stays legible. The
// foreground is never spent on add/delete — it belongs to syntax.
func (d *ReaderDialog) renderCodeLine(l review.Line, width int, bothNums, isLeft, onCursor bool) string {
	rowStyle := lipgloss.NewStyle()
	gutStyle := DiffNumStyle
	signStyle := DimStyle

	sign := " "
	switch l.Kind {
	case review.LineAdd:
		rowStyle, gutStyle, signStyle = DiffAddRowStyle, DiffAddGutterStyle, DiffAddSignStyle
		sign = "+"
	case review.LineDel:
		rowStyle, gutStyle, signStyle = DiffDelRowStyle, DiffDelGutterStyle, DiffDelSignStyle
		// ASCII, deliberately: U+2212 MINUS SIGN is East-Asian-Ambiguous and
		// some terminals give it two cells, which shears the whole gutter.
		// Prose can afford the nicer glyph; a fixed column cannot.
		sign = "-"
	}

	// Gutter.
	var gutter string
	if bothNums {
		gutter = gutStyle.Render(fmt.Sprintf("%4s %4s ", num(l.Old), num(l.New)))
	} else if isLeft {
		gutter = gutStyle.Render(fmt.Sprintf("%5s ", num(l.Old)))
	} else {
		gutter = gutStyle.Render(fmt.Sprintf("%5s ", num(l.New)))
	}
	gutW := lipgloss.Width(gutter)

	if onCursor {
		// The gutter keeps its saturated block — it is the change rail, and the
		// cursor must not erase it — but everything else lifts.
		rowStyle = cursorFill(l.Kind)
		signStyle = rowStyle.Foreground(signColor(l.Kind)).Bold(true)
	}

	code := d.renderCode(l, width-gutW-2, onCursor)
	codeW := lipgloss.Width(code)
	pad := width - gutW - 2 - codeW
	if pad < 0 {
		pad = 0
	}
	return gutter + signStyle.Render(sign) + rowStyle.Render(" ") + code +
		rowStyle.Render(strings.Repeat(" ", pad))
}

func signColor(kind review.LineKind) color.Color {
	switch kind {
	case review.LineAdd:
		return ColorGreen
	case review.LineDel:
		return ColorRed
	}
	return ColorTextDim
}

func num(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// renderCode paints the code with syntax colours, brightening the runes that
// word-diff says actually changed.
//
// Two segmentations over the same runes — tokens from the lexer, marks from the
// word diff — so it walks a per-rune class and coalesces equal neighbours. Done
// as runs rather than per rune because a row emitted one escape sequence per
// character is a row the terminal spends real time on, fifty times a frame.
func (d *ReaderDialog) renderCode(l review.Line, width int, onCursor bool) string {
	if width <= 0 {
		return ""
	}
	rs := []rune(l.Text)
	if len(rs) == 0 {
		return ""
	}

	class := make([]review.TokenClass, len(rs))
	at := 0
	for _, t := range l.Toks {
		for range []rune(t.Text) {
			if at < len(class) {
				class[at] = t.Class
				at++
			}
		}
	}
	marked := make([]bool, len(rs))
	for _, m := range l.Marks {
		for i := m.Start; i < m.End && i < len(marked); i++ {
			if i >= 0 {
				marked[i] = true
			}
		}
	}
	// Search hits outrank both: you asked for these by name.
	hit := make([]bool, len(rs))
	if d.query != "" {
		lower, needle := strings.ToLower(l.Text), strings.ToLower(d.query)
		for off := 0; ; {
			j := strings.Index(lower[off:], needle)
			if j < 0 {
				break
			}
			start := len([]rune(lower[:off+j]))
			for i := start; i < start+len([]rune(needle)) && i < len(hit); i++ {
				hit[i] = true
			}
			off += j + len(needle)
		}
	}

	var b strings.Builder
	used := 0
	i := 0
	for i < len(rs) && used < width {
		j := i + 1
		for j < len(rs) && class[j] == class[i] && marked[j] == marked[i] && hit[j] == hit[i] {
			j++
		}
		text := strings.ReplaceAll(string(rs[i:j]), "\t", strings.Repeat(" ", tabWidth))
		if w := lipgloss.Width(text); used+w > width {
			text = ansi.Truncate(text, width-used, "…")
		}
		b.WriteString(codeRunStyle(l.Kind, class[i], marked[i], hit[i], onCursor).Render(text))
		used += lipgloss.Width(text)
		i = j
	}
	return b.String()
}

// codeRunStyle composes the two channels: background says what happened to the
// row, foreground says what the code is.
//
// Takes the row's KIND rather than its style, because a style carries no way to
// read a background back out — and a second table mapping styles to the colors
// they were built from is exactly how the two drift apart.
// wordRunStyle is the syntax style for a run sitting on a word-diff mark.
func wordRunStyle(c review.TokenClass) lipgloss.Style {
	switch c {
	case review.TokKeyword:
		return SynKeywordWordStyle
	case review.TokType:
		return SynTypeWordStyle
	case review.TokString:
		return SynStringWordStyle
	case review.TokNumber:
		return SynNumberWordStyle
	case review.TokComment:
		return SynCommentWordStyle
	case review.TokFunc:
		return SynFuncWordStyle
	}
	return SynPlainWordStyle
}

func codeRunStyle(kind review.LineKind, c review.TokenClass, marked, hit, onCursor bool) lipgloss.Style {
	if hit {
		// Search hits outrank both channels: you asked for these by name, and
		// a hit you cannot find on the row is a search that did not work.
		return SearchHitStyle
	}

	s := SynPlainStyle
	switch c {
	case review.TokKeyword:
		s = SynKeywordStyle
	case review.TokType:
		s = SynTypeStyle
	case review.TokString:
		s = SynStringStyle
	case review.TokNumber:
		s = SynNumberStyle
	case review.TokComment:
		s = SynCommentStyle
	case review.TokFunc:
		s = SynFuncStyle
	}

	if marked {
		// A marked run swaps to the LIFTED syntax colour, because the word
		// background is carrying a third fact on top of the row's own and the
		// plain colours were picked against the quiet wash, not against a mark.
		// Keeping them made a comment on a changed word score a contrast ratio
		// of 1.13 against its background — invisible.
		//
		// The mark stays as it is on the cursor row: it is already the
		// brightest thing on the line, and lifting it further would push it
		// past the caret in weight.
		switch kind {
		case review.LineAdd:
			return wordRunStyle(c).Background(ColorDiffAddWord)
		case review.LineDel:
			return wordRunStyle(c).Background(ColorDiffDelWord)
		}
	}
	if onCursor {
		switch kind {
		case review.LineAdd:
			return s.Background(ColorDiffCursorAddBg)
		case review.LineDel:
			return s.Background(ColorDiffCursorDelBg)
		}
		return s.Background(ColorDiffCursorBg)
	}
	switch kind {
	case review.LineAdd:
		return s.Background(ColorDiffAddBg)
	case review.LineDel:
		return s.Background(ColorDiffDelBg)
	}
	return s
}

// footer names the mode, the keys that work here, and the last thing that
// happened — and it changes as the cursor moves, because a key list that
// advertises a verb the row under the cursor refuses is a dead click waiting.
func (d *ReaderDialog) footer() string {
	mode := SelectionPill(true).Render(" " + d.mode.String() + " ")

	var keys string
	switch d.mode {
	case modeCompose:
		keys = keyHints("⇥", "type", "⏎", "save", "⌃j", "newline", "esc", "discard")
	case modeSubmit:
		keys = keyHints("⇥", "verdict", "type", "summary", "⏎", "submit", "esc", "cancel")
	case modeSearch:
		keys = HelpKeyStyle.Render(" /") + SessionItemStyle.Render(d.query) +
			FocusCaret().Render("▏") + "  " + keyHints("⏎", "keep", "esc", "cancel")
	default:
		keys = d.normalKeys()
	}

	right := d.footerRight()
	gap := d.width - lipgloss.Width(mode) - lipgloss.Width(keys) - lipgloss.Width(right)
	if gap < 1 {
		// Drop whole hints from the end rather than cutting the string: a
		// truncated key list ends mid-word ("⌃q q"), which reads as a rendering
		// fault and, worse, advertises a key that is not the key.
		keys = d.fitKeys(d.width - lipgloss.Width(mode) - lipgloss.Width(right) - 1)
		gap = max(d.width-lipgloss.Width(mode)-lipgloss.Width(keys)-lipgloss.Width(right), 1)
	}
	return mode + keys + strings.Repeat(" ", gap) + right
}

// fitKeys renders as many whole hints as fit, most useful first.
//
// Quit is pinned last and never dropped: it is the one key you must be able to
// find on a full-screen surface that has taken over the terminal.
func (d *ReaderDialog) fitKeys(width int) string {
	pairs := d.keyPairs()
	// `?` rides with quit: on a narrow terminal it is the key that reaches
	// every other key, so dropping it first would hide the way out of the
	// problem it solves.
	quit := keyHints("?", "keys", "⌃q", "quit")
	budget := width - lipgloss.Width(quit)
	out := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] == "⌃q" || pairs[i] == "?" {
			continue
		}
		next := out + keyHints(pairs[i], pairs[i+1])
		if lipgloss.Width(next) > budget {
			break
		}
		out = next
	}
	return out + quit
}

// normalKeys leads with whatever the row under the cursor makes possible.
func (d *ReaderDialog) normalKeys() string {
	return keyHints(d.keyPairs()...)
}

// keyPairs is the key list for the current row, most relevant first.
func (d *ReaderDialog) keyPairs() []string {
	if d.tab == tabSession {
		if !d.session.Present {
			return []string{"⏎", "start an agent", "①②", "back to the review", "?", "keys", "⌃q", "quit"}
		}
		return []string{"⏎", "attach", "①②", "back to the review", "?", "keys", "⌃q", "quit"}
	}
	if d.treeFocus {
		// The footer names what ⏎ does NOW, and ⏎ means two different things
		// depending on whether a folder or a file carries the selection.
		if d.tourMode() {
			if _, an, ok := d.tourSelection(); ok && an == nil {
				return []string{"↑↓", "route", "⏎ →", "its stops", "t", "files", "⇥", "back to diff", "?", "keys", "⌃q", "quit"}
			}
			return []string{"↑↓", "route", "⏎", "read it", "←", "step", "t", "files", "⇥", "back to diff", "?", "keys", "⌃q", "quit"}
		}
		if d.treeCursor < len(d.treeView) && d.treeView[d.treeCursor].IsDir {
			return []string{"↑↓", "move", "⏎ ←→", "fold", "⇥", "back to diff", "?", "keys", "⌃q", "quit"}
		}
		return []string{"↑↓", "file", "⏎", "read it", "←", "folder", "⇥", "back to diff", "?", "keys", "⌃q", "quit"}
	}
	base := []string{"↑↓", "scroll", "⇧↑↓", "hunk", "⇧←→", "file", "c", "comment", "S", "submit", "space", "read+next", "/", "find", "⇥", "files", "|", "split", "?", "keys", "⌃q", "quit"}
	if d.tour != nil || d.tourState == tourWorking {
		// Advertised only once there is something to show — a key that toasts
		// "no tour" is a key that taught you nothing.
		base = append([]string{"t", "tour"}, base...)
	}
	if d.hiddenCount() > 0 || d.showAll {
		base = append(base[:len(base)-2], append([]string{"s", "folded"}, base[len(base)-2:]...)...)
	}
	if l, ok := d.lineAt(d.cursor); ok {
		switch {
		case l.Kind == review.LineExpand:
			base = append([]string{"⏎", "expand", "+", "all"}, base...)
		case l.Kind.IsComment() && l.Comment != nil && !l.Comment.Sent:
			base = append([]string{"e", "edit", "d", "delete"}, base...)
		}
	}
	return base
}

func keyHints(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString("  " + HelpKeyStyle.Render(pairs[i]) + " " + DimStyle.Render(pairs[i+1]))
	}
	return b.String()
}

// footerRight carries the toast, else the standing counts.
func (d *ReaderDialog) footerRight() string {
	if d.toast != "" {
		return StatusWaitingStyle.Render("● "+d.toast) + " "
	}
	var parts []string
	if n := len(d.hits); n > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d for %s", d.hitIdx+1, n, d.query))
	}
	if n := d.doc.Pending(); n > 0 {
		parts = append(parts, plural(n, "comment")+" unsent")
	}
	if len(parts) == 0 {
		return ""
	}
	return DimStyle.Render(strings.Join(parts, " · ")) + " "
}

// padStyled renders s under style and fills the row to width with the same
// style, so a tint reaches the panel border. A fill that stops mid-row reads as
// a rendering fault, not as a highlight.
func padStyled(style lipgloss.Style, s string, width int) string {
	s = ansi.Truncate(s, max(width, 1), "")
	pad := width - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	return style.Render(s + strings.Repeat(" ", pad))
}

// truncFront cuts from the LEFT, which is right for a path: the basename is
// what identifies it, and a deep path cut from the right leaves every row
// reading the same prefix.
func truncFront(s string, w int) string {
	if w < 4 || lipgloss.Width(s) <= w {
		return s
	}
	rs := []rune(s)
	return "…" + string(rs[len(rs)-(w-1):])
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func itoa(n int) string { return strconv.Itoa(n) }
