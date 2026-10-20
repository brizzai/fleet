package session

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

// PaneView is what a *reader* needs from a pane, as opposed to what the status
// classifier needs: the prompt the agent is blocked on and what sits in its input
// box. It exists for the agent-facing CLI (`fleet status`, `fleet read`, `fleet
// answer`), which used to leave a supervising agent scraping `tmux capture-pane`
// and guessing.
type PaneView struct {
	// Menu is the numbered prompt the agent is blocked on, nil when there is none.
	// Only set when the agent's own waiting detector agrees the pane is waiting,
	// so a numbered list in the conversation is never mistaken for a prompt.
	Menu *Menu
	// Input is the agent's input box. Claude only: the other agents' boxes are
	// not parsed, and their Input is always empty.
	Input Input
}

// Menu is a prompt on screen that wants a choice.
type Menu struct {
	// Kind is "permission" (a tool approval), "question" (AskUserQuestion),
	// "plan" (ExitPlanMode approval) or "menu" when the footer names none of them.
	Kind     string
	Question string
	// Context is what the question is about — the dialog's lines above it,
	// chrome stripped and capped. "Do you want to proceed?" says nothing on its
	// own; the Bash command above it is the point.
	Context []string
	// Subject is the part of Context inside the dialog's dashed frame: the
	// exact command a Bash permission would run, or the diff an edit would make.
	Subject string
	Options []MenuOption
	// Cursor indexes Options at the highlighted row, or -1 when no option holds
	// the cursor (AskUserQuestion with focus on its tab row). An answer can only
	// be driven from a known cursor.
	Cursor int
}

// MenuOption is one row of a Menu.
type MenuOption struct {
	N     int
	Label string
	// Detail is the option's continuation lines — a wrapped label or an
	// AskUserQuestion description, which nothing on screen tells apart.
	Detail string
}

// InputKind says whether the input box holds something a person typed.
type InputKind string

const (
	InputEmpty InputKind = ""
	// InputDraft is text typed into the box and not yet submitted.
	InputDraft InputKind = "draft"
	// InputSuggestion is Claude's dim ghost text — a prompt suggestion or the
	// empty box's placeholder. Nobody typed it; Enter does not submit it as a
	// message the user wrote.
	InputSuggestion InputKind = "suggestion"
)

// Input is the agent's input box.
type Input struct {
	Text string
	Kind InputKind
}

// ParsePaneView reads raw (ANSI-preserved) pane content into a PaneView.
// The ANSI matters: a typed draft and Claude's prompt suggestion are the same
// characters on screen, told apart only by the suggestion being rendered dim.
func ParsePaneView(a agent.Type, rawPane string) PaneView {
	var v PaneView
	rawLines := strings.Split(strings.TrimRight(rawPane, "\n"), "\n")
	clean := make([]string, len(rawLines))
	for i, l := range rawLines {
		clean[i] = StripANSI(l)
	}

	if PaneIndicatesWaiting(a, rawPane) {
		switch a {
		case agent.Copilot:
			// Copilot draws its menus inside a "│ … │" frame: numbered for a
			// permission or trust prompt, unnumbered for ask_user.
			if v.Menu = parseNumberedMenu(unframe(clean)); v.Menu == nil {
				v.Menu = parseBoxedMenu(clean)
			}
		default:
			// Claude's folder-trust dialog is the unnumbered kind.
			if v.Menu = parseNumberedMenu(clean); v.Menu == nil {
				v.Menu = parseBoxedMenu(clean)
			}
		}
	}
	if a == agent.Claude {
		if in, _, ok := findClaudeInput(rawLines, clean); ok {
			v.Input = in
		}
	}
	return v
}

// CleanPaneLines turns a raw pane capture into the conversation a reader wants:
// ANSI stripped, the input box and status bar below it cut, separator rules and
// box frames removed, live spinner lines and "(ctrl+o to expand)" hints dropped,
// and blank runs collapsed. A menu on screen is kept — it is content.
func CleanPaneLines(rawPane string) []string {
	rawLines := strings.Split(strings.TrimRight(rawPane, "\n"), "\n")
	clean := make([]string, len(rawLines))
	for i, l := range rawLines {
		clean[i] = StripANSI(l)
	}
	if _, top, ok := findClaudeInput(rawLines, clean); ok {
		clean = clean[:top]
	}

	var out []string
	prevBlank := true // drops leading blanks too
	for _, line := range clean {
		line = cleanLine(line)
		if line == "\x00" { // dropped outright, not blanked
			continue
		}
		blank := line == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, line)
		prevBlank = blank
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

var expandHints = []string{"(ctrl+o to expand)", "(ctrl+r to expand)", "(ctrl+o to see all)"}

// cleanLine strips chrome from one ANSI-free line. Returns "\x00" for a line
// that is pure chrome and should disappear rather than leave a blank.
func cleanLine(line string) string {
	line = strings.TrimRight(line, " \t\u00a0")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if isSeparatorLine(line) || isBoxBorder(trimmed) {
		return "\x00"
	}
	if isWhimsicalActivity(line) {
		return "\x00"
	}
	for _, h := range expandHints {
		line = strings.ReplaceAll(line, h, "")
	}
	// A framed line ("│ text │") keeps its text. Only frames that open AND close
	// are stripped — a lone leading "│" is a tree or quote marker that carries
	// meaning in Claude's output.
	if t := strings.TrimSpace(line); strings.HasPrefix(t, "│") && strings.HasSuffix(t, "│") && utf8.RuneCountInString(t) >= 2 {
		inner := strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│")
		line = "  " + strings.TrimSpace(inner)
	}
	return strings.TrimRight(line, " \t\u00a0")
}

// isBoxBorder reports a box's top or bottom edge: a corner, a run of rule
// characters, a corner. The rule run may carry an inset title.
func isBoxBorder(t string) bool {
	if t == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(t)
	last, _ := utf8.DecodeLastRuneInString(t)
	switch first {
	case '╭', '╰', '┌', '└':
	default:
		return false
	}
	switch last {
	case '╮', '╯', '┐', '┘':
		return true
	}
	return false
}

// findClaudeInput locates Claude's input box — a prompt line between two
// separator rules near the bottom of the pane — and reads what it holds. top is
// the index of the upper rule, where the chrome below the conversation starts.
//
// Searched from the bottom so the box, not a dialog's own rules higher up, is
// what matches; bounded so a pane with no box (a menu is up, a shell prompt)
// costs a few dozen line checks.
func findClaudeInput(rawLines, clean []string) (in Input, top int, ok bool) {
	const window = 40
	lo := max(0, len(clean)-window)
	for k := len(clean) - 1; k > lo; k-- {
		if !isSeparatorLine(clean[k]) {
			continue
		}
		// k is a candidate lower rule; find the upper one above it.
		for i := k - 1; i >= lo && k-i <= 30; i-- {
			if !isSeparatorLine(clean[i]) {
				continue
			}
			if i+1 >= k {
				break // two adjacent rules: not a box
			}
			first := strings.TrimSpace(clean[i+1])
			rest, isPrompt := promptRemainder(first)
			if !isPrompt || isNumberedMenuOption(rest) {
				break
			}
			return readClaudeInput(rawLines[i+1:k], clean[i+1:k]), i, true
		}
	}
	return Input{}, 0, false
}

// readClaudeInput reads the box's lines: the first carries the prompt marker,
// continuation lines are indented under it.
func readClaudeInput(rawLines, clean []string) Input {
	var parts []string
	dimOnly := true
	sawText := false
	for idx, raw := range rawLines {
		text := clean[idx]
		// Claude parks an animated creature at the far right of the prompt line,
		// past a long run of spaces; it is not input.
		text = stripRightMargin(text)
		if idx == 0 {
			// Measure dimness only over what follows the marker; the marker itself
			// is never dim.
			if mi := markerEnd(raw); mi >= 0 {
				raw = raw[mi:]
			}
			rest, _ := promptRemainder(strings.TrimSpace(text))
			text = rest
		} else {
			text = strings.TrimSpace(text)
		}
		dim, any := visibleRunesAllDim(raw)
		if any {
			sawText = true
			if !dim {
				dimOnly = false
			}
		}
		parts = append(parts, text)
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" || !sawText {
		return Input{}
	}
	if dimOnly {
		return Input{Text: text, Kind: InputSuggestion}
	}
	return Input{Text: text, Kind: InputDraft}
}

// markerEnd returns the byte offset just past the prompt marker ("❯" or ">")
// in a raw line, or -1. Escape sequences before the marker are skipped.
func markerEnd(raw string) int {
	for _, m := range []string{"❯", ">"} {
		if i := strings.Index(raw, m); i >= 0 {
			// The marker must be the first visible character.
			if strings.TrimSpace(StripANSI(raw[:i])) == "" {
				return i + len(m)
			}
		}
	}
	return -1
}

// visibleRunesAllDim walks raw, tracking the SGR dim attribute, and reports
// whether every visible non-space rune was rendered dim. Reverse-video runes are
// skipped: that is the block cursor, which sits on top of whatever is under it.
// Reading stops at a run of 20+ spaces, the same right margin stripRightMargin
// cuts. any is false when the line has no visible text at all.
func visibleRunesAllDim(raw string) (allDim, any bool) {
	dim, reverse := false, false
	allDim = true
	spaces := 0
	for i := 0; i < len(raw) && spaces < 20; {
		if raw[i] == 0x1b && i+1 < len(raw) && raw[i+1] == '[' {
			j := i + 2
			for j < len(raw) && (raw[j] < 0x40 || raw[j] > 0x7e) {
				j++
			}
			if j < len(raw) && raw[j] == 'm' {
				dim, reverse = applySGR(raw[i+2:j], dim, reverse)
			}
			i = j + 1
			continue
		}
		if raw[i] == 0x1b {
			i = skipESCSequence(raw, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(raw[i:])
		i += size
		if r == ' ' {
			spaces++
		} else {
			spaces = 0
		}
		if unicode.IsSpace(r) || reverse {
			continue
		}
		any = true
		if !dim {
			allDim = false
		}
	}
	return allDim && any, any
}

// applySGR folds one SGR parameter list into the dim/reverse state. Extended
// colour parameters (38;5;N, 48;2;R;G;B) are consumed whole, so a colour index
// of 2 or 7 is never read as dim or reverse.
func applySGR(params string, dim, reverse bool) (bool, bool) {
	if params == "" {
		return false, false
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			continue
		}
		switch n {
		case 0:
			dim, reverse = false, false
		case 2:
			dim = true
		case 22:
			dim = false
		case 7:
			reverse = true
		case 27:
			reverse = false
		case 38, 48, 58:
			if i+1 < len(ps) {
				switch ps[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		}
	}
	return dim, reverse
}

// menuWindow bounds how far above the bottom a menu is looked for. The
// AskUserQuestion preview variant renders a ~20-line panel beside the options,
// which pushes the first option well up the pane.
const menuWindow = 45

// parseNumberedMenu reads a "❯ 1. Yes / 2. No" style menu — Claude's permission,
// question and plan prompts, and Codex's approval menus. The run of options
// nearest the bottom numbered 1..k wins.
func parseNumberedMenu(clean []string) *Menu {
	end := len(clean)
	for end > 0 && strings.TrimSpace(clean[end-1]) == "" {
		end--
	}
	lo := max(0, end-menuWindow)

	type optLine struct {
		idx    int
		n      int
		label  string
		cursor bool
		indent int
	}
	var opts []optLine
	for i := lo; i < end; i++ {
		n, label, cursor, indent, ok := numberedOption(clean[i])
		if !ok {
			continue
		}
		if n == 1 {
			opts = opts[:0] // a fresh run starts; keep only the last one
		} else if len(opts) == 0 || opts[len(opts)-1].n != n-1 {
			continue // not part of a 1..k run
		}
		opts = append(opts, optLine{i, n, label, cursor, indent})
	}
	if len(opts) < 2 {
		return nil
	}

	m := &Menu{Cursor: -1}
	for oi, o := range opts {
		next := end
		if oi+1 < len(opts) {
			next = opts[oi+1].idx
		}
		var detail []string
		for j := o.idx + 1; j < next; j++ {
			line := leftColumn(clean[j])
			t := strings.TrimSpace(line)
			ind := leadingSpaces(line)
			// A blank line or the footer (no deeper than the option) ends it, and so
			// does a side panel's line: far to the right, or a box edge.
			if t == "" || ind <= o.indent || ind > o.indent+8 || startsWithBoxChar(t) {
				break
			}
			detail = append(detail, t)
		}
		m.Options = append(m.Options, MenuOption{N: o.n, Label: o.label, Detail: strings.Join(detail, " ")})
		if o.cursor {
			m.Cursor = oi
		}
	}

	first := opts[0].idx
	qi := -1
	for i := first - 1; i >= max(0, first-6); i-- {
		if t := strings.TrimSpace(leftColumn(clean[i])); t != "" {
			if !isSeparatorLine(clean[i]) {
				qi = i
				// AskUserQuestion can draw its question with a quote bar.
				m.Question = strings.TrimSpace(strings.TrimPrefix(t, "│"))
			}
			break
		}
	}
	if qi > 0 {
		m.Context, m.Subject = menuContext(clean, qi)
	}
	m.Kind = menuKind(clean[opts[len(opts)-1].idx+1 : end])
	if m.Kind == "permission" && looksLikeQuestion(m) {
		m.Kind = "question"
	}
	return m
}

// looksLikeQuestion catches the AskUserQuestion variant whose footer is only
// "Esc to cancel" — the same footer a permission prompt has. Its own rows give
// it away: the free-text and chat escape hatches, or a ☐/☒ question tab.
func looksLikeQuestion(m *Menu) bool {
	for _, o := range m.Options {
		switch o.Label {
		case "Type something.", "Type something", "Chat about this":
			return true
		}
	}
	for _, c := range m.Context {
		if strings.ContainsAny(c, "☐☒") {
			return true
		}
	}
	return false
}

// numberedOption parses "  2. Label" or "❯ 1. Label" (Codex uses "›"). Only the
// left column is read: the AskUserQuestion preview renders a panel beside the
// options, separated by a wide gap.
func numberedOption(line string) (n int, label string, cursor bool, indent int, ok bool) {
	line = leftColumn(line)
	indent = leadingSpaces(line)
	t := strings.TrimSpace(line)
	for _, m := range []string{"❯", "›", ">"} {
		if rest, found := strings.CutPrefix(t, m); found {
			t = strings.TrimLeftFunc(rest, unicode.IsSpace)
			cursor = true
			break
		}
	}
	dot := strings.IndexByte(t, '.')
	if dot <= 0 || dot > 3 {
		return 0, "", false, 0, false
	}
	n, err := strconv.Atoi(t[:dot])
	if err != nil || n <= 0 {
		return 0, "", false, 0, false
	}
	label = strings.TrimSpace(t[dot+1:])
	if label == "" {
		return 0, "", false, 0, false
	}
	return n, label, cursor, indent, true
}

// leftColumn cuts a line at its first wide gap after the leading indentation,
// dropping a side-by-side panel. Indentation is kept: option continuation lines
// are recognised by being indented deeper than the option.
func leftColumn(line string) string {
	ind := leadingSpaces(line)
	body := line[ind:]
	if parts := splitOnWideGaps(body); len(parts) > 1 {
		body = parts[0]
	}
	return line[:ind] + strings.TrimRight(body, " ")
}

func startsWithBoxChar(t string) bool {
	r, _ := utf8.DecodeRuneInString(t)
	return strings.ContainsRune("│┌└├┐┘┤╭╰╮╯─", r)
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// menuContext returns the dialog lines between its top rule and the question,
// chrome stripped and capped, plus the text inside its dashed (╌) frame — the
// command or diff the question is about.
func menuContext(clean []string, qi int) (context []string, subject string) {
	const maxScan, maxLines, maxSubject = 30, 12, 6
	start := -1
	for i := qi - 1; i >= max(0, qi-maxScan); i-- {
		t := strings.TrimSpace(clean[i])
		if isSeparatorLine(clean[i]) && !strings.ContainsRune(t, '╌') {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, ""
	}
	var subj []string
	inFrame, framed := false, false
	for i := start; i < qi; i++ {
		raw := strings.TrimSpace(clean[i])
		if strings.Contains(raw, "╌") { // a dashed frame edge
			if !framed {
				inFrame = !inFrame
				framed = !inFrame // only the first frame is the subject
			}
			continue
		}
		l := cleanLine(clean[i])
		if l == "\x00" || strings.TrimSpace(l) == "" {
			continue
		}
		l = strings.TrimSpace(l)
		// Claude's "Tip: auto mode handles these prompts…" is advice to the
		// person at the keyboard, not part of the question. It wraps: a long tip
		// takes the next line with it.
		if strings.HasPrefix(l, "Tip:") {
			if utf8.RuneCountInString(l) >= 70 && i+1 < qi && !strings.Contains(clean[i+1], "╌") {
				i++
			}
			continue
		}
		if inFrame && len(subj) < maxSubject {
			subj = append(subj, l)
		}
		context = append(context, l)
	}
	if len(context) > maxLines {
		more := len(context) - maxLines
		context = append(context[:maxLines], "… ("+strconv.Itoa(more)+" more lines)")
	}
	return context, strings.Join(subj, "\n")
}

// menuKind classifies a menu by the footer under its options.
func menuKind(below []string) string {
	for _, l := range below {
		lower := strings.ToLower(l)
		switch {
		case strings.Contains(lower, "approve with this feedback"):
			return "plan"
		case isAskUserQuestionFooter(l), strings.Contains(lower, "enter to submit answer"):
			return "question"
		case strings.Contains(lower, "esc to cancel"), strings.Contains(lower, "press enter to confirm"):
			return "permission"
		}
	}
	return "menu"
}

// parseBoxedMenu reads an unnumbered selection menu: rows, the highlighted one
// marked "❯", above an "enter …" footer — Copilot's (inside a "│ … │" frame) and
// Claude's folder-trust dialog. Rows are numbered in screen order so `fleet
// answer` can address them.
func parseBoxedMenu(clean []string) *Menu {
	end := len(clean)
	for end > 0 && strings.TrimSpace(clean[end-1]) == "" {
		end--
	}
	lo := max(0, end-menuWindow)
	inner := unframe(clean[lo:end])
	for i, l := range inner {
		inner[i] = strings.TrimSpace(l)
	}
	footer := -1
	for i := len(inner) - 1; i >= 0; i-- {
		lower := strings.ToLower(inner[i])
		if strings.Contains(lower, "enter") &&
			(strings.Contains(lower, "↑/↓") || strings.Contains(lower, "decline") || strings.Contains(lower, "to confirm")) {
			footer = i
			break
		}
	}
	if footer < 0 {
		return nil
	}
	// Option rows are the run directly above the footer (blank lines aside):
	// "❯ red" or "  blue". Stop at the first blank that follows a row.
	var rows []string
	cursor := -1
	i := footer - 1
	for i >= 0 && strings.TrimSpace(inner[i]) == "" {
		i--
	}
	for ; i >= 0; i-- {
		t := inner[i]
		if strings.TrimSpace(t) == "" {
			break
		}
		if rest, ok := strings.CutPrefix(strings.TrimSpace(t), "❯"); ok {
			cursor = len(rows)
			t = strings.TrimSpace(rest)
		} else {
			t = strings.TrimSpace(t)
		}
		rows = append(rows, t)
	}
	if len(rows) < 2 || cursor < 0 {
		return nil
	}
	m := &Menu{Kind: "question", Cursor: len(rows) - 1 - cursor}
	if strings.Contains(strings.ToLower(inner[footer]), "to confirm") {
		m.Kind = "permission"
	}
	for k := len(rows) - 1; k >= 0; k-- {
		m.Options = append(m.Options, MenuOption{N: len(m.Options) + 1, Label: rows[k]})
	}
	// The question is the nearest line above the rows that asks one; failing
	// that, the nearest line. A dialog's prose can sit between ("Security guide").
	nearest := ""
	for j := i; j >= max(0, i-12); j-- {
		t := strings.TrimSpace(inner[j])
		if t == "" {
			continue
		}
		if isSeparatorLine(t) {
			break
		}
		if nearest == "" {
			nearest = t
		}
		if strings.Contains(t, "?") {
			m.Question = t
			break
		}
	}
	if m.Question == "" {
		m.Question = nearest
	}
	return m
}

// unframe strips a "│ … │" box frame from each line that has one, keeping the
// inner text's indentation so option rows still line up.
func unframe(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "│") && strings.HasSuffix(t, "│") && utf8.RuneCountInString(t) >= 2 {
			inner := strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│")
			out[i] = strings.TrimRight(strings.TrimPrefix(inner, " "), " ")
			continue
		}
		out[i] = l
	}
	return out
}

// PaneShowsWorking reports whether the pane shows the agent mid-turn — the
// footer or spinner each agent draws while it works. For the agent-facing CLI
// to check that an interrupt landed. False for OpenCode, which has no pane
// detector: the caller then cannot confirm, and says so.
func PaneShowsWorking(a agent.Type, rawPane string) bool {
	clean := StripANSI(rawPane)
	switch a {
	case agent.Codex:
		return codexPaneRunning(clean)
	case agent.Copilot:
		return copilotPaneRunning(clean)
	case agent.OpenCode:
		return false
	default:
		lines := strings.Split(strings.TrimRight(clean, "\n"), "\n")
		return detectRunning(extractRecentLines(lines, 50), "", debuglog.Logger) == StatusRunning
	}
}
