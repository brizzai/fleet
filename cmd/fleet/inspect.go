package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// cliState is what every agent-facing command loads before it can name a
// session: the rows, the hotkey slots, and the TUI's PR cache.
type cliState struct {
	storage *session.StateDB
	rows    []*session.SessionRow
	slots   map[int]string
	prs     map[string]*session.PRCacheRow
}

// openCLIState runs the same preamble `fleet send` does — migrate before
// anything creates ~/.config/fleet, debuglog so slog never falls back to
// stderr, the account resolver so a relaunch keeps its Claude account — then
// loads the state. Exits on failure, like the commands it was lifted from.
func openCLIState() *cliState {
	migration.Run()
	debuglog.Init()
	session.SetAccountConfigDirFunc(claudeaccount.Load().ConfigDirFor)

	if err := tmux.IsTmuxAvailable(); err != nil {
		fatalf("%v", err)
	}
	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fatalf("Failed to open database: %v", err)
	}
	rows, err := storage.LoadSessions()
	if err != nil {
		fatalf("Failed to load sessions: %v", err)
	}
	// Both are conveniences — a missing table costs the @N selector or the PR
	// column, never the command.
	slots, err := storage.LoadSlotBindings()
	if err != nil {
		debuglog.Logger.Error("failed to load slot bindings", "err", err)
	}
	prs, err := storage.LoadPRCache()
	if err != nil {
		debuglog.Logger.Error("failed to load PR cache", "err", err)
	}
	return &cliState{storage: storage, rows: rows, slots: slots, prs: prs}
}

func (c *cliState) close() {
	c.storage.Close()
	debuglog.Close()
}

func (c *cliState) observer(turns int) *control.Observer {
	return control.NewObserver(control.Options{Turns: turns, PRs: c.prs, Slots: c.slots})
}

// resolve names one session, exiting with the resolver's own message (which
// lists the candidates when ambiguous).
func (c *cliState) resolve(selector string) *session.SessionRow {
	row, err := resolveSession(c.rows, c.slots, selector, branchOfHead)
	if err != nil {
		fatalf("%v", err)
	}
	return row
}

// observeOne resolves selector and observes it.
func (c *cliState) observeOne(selector string, turns int) (*control.Observer, *control.Observation) {
	row := c.resolve(selector)
	o := c.observer(turns)
	return o, o.Observe([]*session.SessionRow{row})[0]
}

// branchOfHead is the resolver's branch lookup, read from HEAD rather than by
// forking git once per session.
func branchOfHead(path string) string {
	_, b := git.ReadHead(path)
	return b
}

// hexID is a session id's random half — `fleet status` shows it, and it
// resolves as an id prefix wherever a session is named.
func hexID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// parseInterspersed parses fs over args allowing flags on either side of the
// positionals — Go's flag package stops at the first non-flag, so a single
// Parse would reject `fleet read foo --lines 50`.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			return positional, nil
		}
		positional = append(positional, remaining[0])
		rest = remaining[1:]
	}
}

// newFlagSet builds a silent flag set: flag.ContinueOnError still prints the
// error and usage itself, which would emit both twice.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// handleParseErr turns a flag error into the command's exit: -h prints usage
// and succeeds, anything else prints the error and the usage and fails.
func handleParseErr(err error, usage func(io.Writer)) {
	if err == nil {
		return
	}
	if err == flag.ErrHelp {
		usage(os.Stdout)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, err)
	usage(os.Stderr)
	os.Exit(1)
}

// --- rendering ---

// homeRel shortens a path under the home directory to ~/…, which is what a
// reader recognises and costs fewer tokens.
func homeRel(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

// humanDur renders a duration in the one or two largest units: 45s, 12m, 3h5m,
// 2d4h. Precision past that is noise in a status line.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m != 0 && h < 10 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	default:
		days := int(d.Hours()) / 24
		if h := int(d.Hours()) % 24; h != 0 && days < 10 {
			return fmt.Sprintf("%dd%dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return humanDur(time.Since(t))
}

// clip cuts s to max runes on one line, marking the cut. Newlines become " ⏎ "
// so a multi-line prompt still reads as one row.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ⏎ ")), " ")
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// prShort is a PR in one word: its number and the state that most wants a
// reader's attention.
func prShort(pr *github.PR) string {
	if pr == nil || pr.Number == 0 {
		return ""
	}
	return fmt.Sprintf("#%d(%s)", pr.Number, prState(pr))
}

func prState(pr *github.PR) string {
	switch {
	case pr.State == "MERGED":
		return "merged"
	case pr.State == "CLOSED":
		return "closed"
	case pr.IsDraft:
		return "draft"
	case pr.HasConflicts:
		return "conflicts"
	case pr.CIStatus == "FAILURE":
		return "ci-failed"
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return "changes-requested"
	case pr.UnresolvedThreads > 0:
		return fmt.Sprintf("%d-threads", pr.UnresolvedThreads)
	case pr.CIStatus == "PENDING":
		return "ci-running"
	case pr.ReviewDecision == "APPROVED":
		return "approved"
	default:
		return "open"
	}
}

// menuLine renders a waiting menu on one line: the question, what it is about,
// and the numbered options.
func menuLine(m *session.Menu, labelMax int) string {
	var b strings.Builder
	b.WriteString(m.Question)
	if ctx := menuContextSummary(m); ctx != "" {
		fmt.Fprintf(&b, " [%s]", ctx)
	}
	for _, o := range m.Options {
		fmt.Fprintf(&b, "  %d) %s", o.N, clip(o.Label, labelMax))
	}
	return b.String()
}

// menuContextSummary says in a line what the prompt is about: the dialog's
// header and its framed subject ("Bash command: rm -rf build"), else its first
// two context lines.
func menuContextSummary(m *session.Menu) string {
	if m.Subject != "" && len(m.Context) > 0 {
		subject, _, _ := strings.Cut(m.Subject, "\n")
		return clip(m.Context[0]+": "+subject, 90)
	}
	n := min(2, len(m.Context))
	return clip(strings.Join(m.Context[:n], ": "), 90)
}

// annotations are the indented lines `fleet status` adds under a session row,
// only when there is something to act on.
func annotations(ob *control.Observation) []string {
	var out []string
	if m := ob.View.Menu; m != nil {
		out = append(out, "? "+menuLine(m, 40))
	}
	if ob.View.Input.Kind == session.InputDraft {
		out = append(out, fmt.Sprintf("✎ unsent draft: %q", clip(ob.View.Input.Text, 80)))
	}
	if ob.Note != "" {
		out = append(out, "! "+ob.Note)
	} else if ob.Exit != "" {
		out = append(out, "! pane process ended ("+ob.Exit+")")
	}
	return out
}

// statusWord is the status as a reader should read it. A pane that dropped to a
// shell is reported as exited whatever the pipeline last concluded, because
// that is the fact that decides what to do next.
func statusWord(ob *control.Observation) string {
	switch ob.Pane {
	case control.PaneShell:
		return "exited"
	}
	return string(ob.Status)
}

// --- JSON ---

type sessionJSON struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	Since        *time.Time `json:"since,omitempty"`
	For          string     `json:"for,omitempty"`
	Agent        string     `json:"agent"`
	Path         string     `json:"path"`
	Branch       string     `json:"branch,omitempty"`
	PR           *prJSON    `json:"pr,omitempty"`
	Slot         *int       `json:"slot,omitempty"`
	Pane         string     `json:"pane"`
	PaneCommand  string     `json:"pane_command,omitempty"`
	LastActivity *time.Time `json:"last_activity,omitempty"`
	Hook         *hookJSON  `json:"hook,omitempty"`
	LastPrompt   string     `json:"last_prompt,omitempty"`
	LastReply    string     `json:"last_reply,omitempty"`
	Waiting      *menuJSON  `json:"waiting,omitempty"`
	Input        *inputJSON `json:"input,omitempty"`
	Note         string     `json:"note,omitempty"`
	Exit         string     `json:"exit,omitempty"`
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`
	Turns        []turnJSON `json:"turns,omitempty"`
	Screen       []string   `json:"screen,omitempty"`
}

type prJSON struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
}

type hookJSON struct {
	Event string    `json:"event"`
	At    time.Time `json:"at"`
}

type menuJSON struct {
	Kind     string       `json:"kind"`
	Question string       `json:"question"`
	Subject  string       `json:"subject,omitempty"`
	Context  []string     `json:"context,omitempty"`
	Options  []optionJSON `json:"options"`
	Cursor   *int         `json:"cursor,omitempty"`
}

type optionJSON struct {
	N      int    `json:"n"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type inputJSON struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

type turnJSON struct {
	Prompt      string     `json:"prompt"`
	PromptAt    *time.Time `json:"prompt_at,omitempty"`
	Reply       string     `json:"reply,omitempty"`
	ReplyAt     *time.Time `json:"reply_at,omitempty"`
	Tools       int        `json:"tools,omitempty"`
	Done        bool       `json:"done"`
	Interrupted bool       `json:"interrupted,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// toJSON converts an observation. textMax caps the prompt and reply (0 = no
// cap); turns are included only when asked for.
func toJSON(ob *control.Observation, textMax int, withTurns bool) sessionJSON {
	j := sessionJSON{
		ID:           ob.Row.ID,
		Title:        ob.Row.Title,
		Status:       statusWord(ob),
		Since:        timePtr(ob.Since),
		Agent:        string(ob.Agent),
		Path:         ob.Row.ProjectPath,
		Branch:       ob.Branch,
		Pane:         string(ob.Pane),
		PaneCommand:  ob.PaneCmd,
		LastActivity: timePtr(ob.LastActivity),
		LastPrompt:   clipKeepLines(ob.LastPrompt, textMax),
		LastReply:    clipKeepLines(ob.LastReply, textMax),
		Note:         ob.Note,
		Exit:         ob.Exit,
	}
	if !ob.Since.IsZero() {
		j.For = humanDur(time.Since(ob.Since))
	}
	if ob.Slot >= 0 {
		n := ob.Slot
		j.Slot = &n
	}
	if ob.PR != nil && ob.PR.Number != 0 {
		j.PR = &prJSON{Number: ob.PR.Number, State: prState(ob.PR), URL: ob.PR.URL, Title: ob.PR.Title}
	}
	if ob.HookEvent != "" {
		j.Hook = &hookJSON{Event: ob.HookEvent, At: ob.HookAt}
	}
	if m := ob.View.Menu; m != nil {
		mj := &menuJSON{Kind: m.Kind, Question: m.Question, Subject: m.Subject, Context: m.Context}
		for _, o := range m.Options {
			mj.Options = append(mj.Options, optionJSON{N: o.N, Label: o.Label, Detail: o.Detail})
		}
		if m.Cursor >= 0 {
			c := m.Options[m.Cursor].N
			mj.Cursor = &c
		}
		j.Waiting = mj
	}
	if in := ob.View.Input; in.Kind != session.InputEmpty {
		j.Input = &inputJSON{Text: in.Text, Kind: string(in.Kind)}
	}
	if until := ob.Row.SnoozedUntil; until.After(time.Now()) {
		j.SnoozedUntil = &until
	}
	if withTurns {
		for _, t := range ob.Turns {
			j.Turns = append(j.Turns, turnJSON{
				Prompt: t.Prompt, PromptAt: timePtr(t.PromptAt),
				Reply: t.Reply, ReplyAt: timePtr(t.ReplyAt),
				Tools: t.Tools, Done: t.Done, Interrupted: t.Interrupted,
			})
		}
	}
	return j
}

// clipKeepLines caps s at max runes without flattening its lines — JSON keeps
// structure that a one-line text row cannot.
func clipKeepLines(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

func printJSON(v any) {
	// Compact on purpose: the reader is usually an agent, and indentation is
	// a third of the tokens.
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fatalf("failed to encode JSON: %v", err)
	}
}
