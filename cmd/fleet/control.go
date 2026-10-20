package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// The control verbs act on one session's pane. Each one observes the session
// first — through the same pipeline `fleet status` uses — refuses anything that
// would type into the wrong place, acts, and then checks the pane for the
// outcome rather than assuming it.

// actOpts is the shape every control verb parses into.
type actOpts struct {
	selector string
	arg      string // answer's choice
	force    bool
	expect   string
	fresh    bool
}

type verb struct {
	name  string
	usage string
	help  string
	// needsArg is the name of a required second positional ("" for none).
	needsArg string
	flags    func(fs *flag.FlagSet, o *actOpts)
	run      func(st *cliState, o actOpts) error
}

var verbs = map[string]verb{
	"answer": {
		name:     "answer",
		usage:    "fleet answer [flags] <session> <n|yes|no>",
		needsArg: "choice",
		help: `Picks an option in the prompt a session is waiting on: a number, or yes/no
for the option whose label starts with that word. The cursor is moved with the
arrow keys and checked before Enter is pressed, and the pane is checked again
afterwards; the next question, if the prompt has several, is printed.
-expect guards against answering a different prompt than the one you read.`,
		flags: func(fs *flag.FlagSet, o *actOpts) {
			fs.StringVar(&o.expect, "expect", "", "refuse unless the prompt's question or context contains this text")
		},
		run: runAnswerVerb,
	},
	"interrupt": {
		name:  "interrupt",
		usage: "fleet interrupt [flags] <session>",
		help: `Presses Esc to stop the agent's current turn, then checks the pane stopped
showing it working. Refuses when the session is not running, and when it is
waiting on a prompt — Esc there cancels the prompt (use fleet answer <s> no).`,
		flags: func(fs *flag.FlagSet, o *actOpts) {
			fs.BoolVar(&o.force, "force", false, "send Esc anyway")
		},
		run: runInterruptVerb,
	},
	"submit": {
		name:  "submit",
		usage: "fleet submit <session>",
		help: `Presses Enter on text typed into a Claude session's input box but never sent.
Refuses when the box is empty or shows only Claude's dim suggestion.`,
		run: runSubmitVerb,
	},
	"clear": {
		name:  "clear",
		usage: "fleet clear <session>",
		help: `Erases text typed into a Claude session's input box (Ctrl+U, line by line,
until the box reads empty). Never Esc or Ctrl+C: Esc interrupts a working agent
and opens the rewind menu on an empty box, and a second Ctrl+C exits Claude.`,
		run: runClearVerb,
	},
	"restart": {
		name:  "restart",
		usage: "fleet restart [flags] <session>",
		help: `Relaunches a session's agent, resuming its conversation: in place when its
tmux session is still up (an agent that exited back to a shell), with a fresh
tmux session when it is gone. A suspended session is resumed. Refuses a session
that is working or waiting unless -force, and refuses to resume a Claude
conversation whose transcript is missing — that launch fails with "No
conversation found" — unless -fresh, which starts a new conversation.`,
		flags: func(fs *flag.FlagSet, o *actOpts) {
			fs.BoolVar(&o.force, "force", false, "restart even though the agent is working or waiting")
			fs.BoolVar(&o.fresh, "fresh", false, "start a new conversation instead of resuming")
		},
		run: runRestartVerb,
	},
	"suspend": {
		name:  "suspend",
		usage: "fleet suspend [flags] <session>",
		help: `Hibernates a session: kills its agent and tmux session to free memory,
keeping the conversation. fleet resume (or fleet send, or Enter in the TUI)
brings it back. Refuses a session that is working or waiting unless -force.`,
		flags: func(fs *flag.FlagSet, o *actOpts) {
			fs.BoolVar(&o.force, "force", false, "suspend even though the agent is working or waiting")
		},
		run: runSuspendVerb,
	},
	"resume": {
		name:  "resume",
		usage: "fleet resume [flags] <session>",
		help: `Wakes a suspended session, resuming its conversation. Refuses a Claude
conversation with no transcript (one that never got a prompt has none) unless
-fresh, which starts a new conversation.`,
		flags: func(fs *flag.FlagSet, o *actOpts) {
			fs.BoolVar(&o.fresh, "fresh", false, "start a new conversation instead of resuming")
		},
		run: runResumeVerb,
	},
}

func verbUsage(v verb) func(io.Writer) {
	return func(w io.Writer) {
		fmt.Fprintln(w, "Usage: "+v.usage)
		var o actOpts
		fs := verbFlagSet(v, &o)
		fs.SetOutput(w)
		fs.PrintDefaults()
		fmt.Fprintln(w, "\n"+v.help)
	}
}

func verbFlagSet(v verb, o *actOpts) *flag.FlagSet {
	fs := newFlagSet("fleet " + v.name)
	if v.flags != nil {
		v.flags(fs, o)
	}
	return fs
}

func parseVerbArgs(v verb, args []string) (actOpts, error) {
	var o actOpts
	pos, err := parseInterspersed(verbFlagSet(v, &o), args)
	if err != nil {
		return o, err
	}
	want := 1
	if v.needsArg != "" {
		want = 2
	}
	if len(pos) < want {
		if len(pos) == 0 {
			return o, errors.New("missing session")
		}
		return o, fmt.Errorf("missing %s", v.needsArg)
	}
	if len(pos) > want {
		return o, fmt.Errorf("unexpected argument %q", pos[want])
	}
	o.selector = pos[0]
	if want == 2 {
		o.arg = pos[1]
	}
	return o, nil
}

func runVerb(name string, args []string) {
	v := verbs[name]
	o, err := parseVerbArgs(v, args)
	handleParseErr(err, verbUsage(v))
	st := openCLIState()
	defer st.close()
	if err := v.run(st, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		st.close() // os.Exit skips the deferred close
		os.Exit(1)
	}
}

// requireLivePane refuses a session whose pane has no agent to talk to, naming
// what to do instead.
func requireLivePane(ob *control.Observation) error {
	title := ob.Row.Title
	switch ob.Pane {
	case control.PaneSuspended:
		return fmt.Errorf("session %q is suspended — wake it with fleet resume (or fleet send a message)", title)
	case control.PaneGone, control.PaneDead:
		return fmt.Errorf("session %q has no running agent (%s) — fleet restart brings it back", title, ob.Pane)
	case control.PaneShell:
		msg := fmt.Sprintf("the %s process in session %q has exited — its pane is at a %s prompt, where keys "+
			"would go to the shell.\n", ob.Agent.DisplayName(), title, ob.PaneCmd)
		if ob.Note != "" {
			msg += ob.Note + "\n"
		}
		return errors.New(msg + "fleet restart brings it back")
	}
	return nil
}

// reobserve reads the session again with the same observer, so the status
// pipeline's state carries over between reads.
func reobserve(o *control.Observer, ob *control.Observation) *control.Observation {
	return o.Observe([]*session.SessionRow{ob.Row})[0]
}

// pollPane re-observes until done reports true or the budget runs out,
// returning the last observation.
func pollPane(o *control.Observer, ob *control.Observation, budget time.Duration, done func(*control.Observation) bool) (*control.Observation, bool) {
	deadline := time.Now().Add(budget)
	for {
		time.Sleep(200 * time.Millisecond)
		cur := reobserve(o, ob)
		if done(cur) {
			return cur, true
		}
		if time.Now().After(deadline) {
			return cur, false
		}
	}
}

// --- answer ---

func runAnswerVerb(st *cliState, opts actOpts) error {
	o, ob := st.observeOne(opts.selector, 1)
	if err := requireLivePane(ob); err != nil {
		return err
	}
	m := ob.View.Menu
	if m == nil {
		if ob.Agent == agent.OpenCode {
			return fmt.Errorf("fleet can't read OpenCode's prompts — attach to answer it")
		}
		return fmt.Errorf("session %q is not waiting on a prompt (status %s)", ob.Row.Title, statusWord(ob))
	}
	if opts.expect != "" && !menuMentions(m, opts.expect) {
		return fmt.Errorf("the prompt on screen is %q, which doesn't mention %q — nothing was pressed", m.Question, opts.expect)
	}
	target, err := pickOption(m, opts.arg)
	if err != nil {
		return err
	}
	if isMultiSelect(m) {
		return fmt.Errorf("%q is a multi-select question, which fleet answer can't drive — attach to answer it", m.Question)
	}
	if m.Cursor < 0 {
		return fmt.Errorf("the prompt's cursor is not on its options (focus is on the question tabs) — attach to answer it")
	}

	ts := ob.Session.GetTmuxSession()
	if delta := target - m.Cursor; delta != 0 {
		key := "Down"
		if delta < 0 {
			key, delta = "Up", -delta
		}
		keys := make([]string, delta)
		for i := range keys {
			keys[i] = key
		}
		if err := ts.SendKeys(keys...); err != nil {
			return fmt.Errorf("failed to move the cursor: %w", err)
		}
		// Enter confirms whatever is highlighted, so it is only pressed once the
		// highlight is on the option that was asked for, in the same prompt.
		landed, ok := pollPane(o, ob, 2*time.Second, func(cur *control.Observation) bool {
			cm := cur.View.Menu
			return cm != nil && cm.Question == m.Question && cm.Cursor == target
		})
		if !ok {
			got := "the prompt changed"
			if cm := landed.View.Menu; cm != nil && cm.Question == m.Question && cm.Cursor >= 0 {
				got = fmt.Sprintf("the cursor is on %d", cm.Options[cm.Cursor].N)
			}
			return fmt.Errorf("the cursor did not land on option %d (%s) — nothing was confirmed", m.Options[target].N, got)
		}
	}
	if err := ts.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to press Enter: %w", err)
	}
	after, changed := pollPane(o, ob, 3*time.Second, func(cur *control.Observation) bool {
		cm := cur.View.Menu
		return cm == nil || cm.Question != m.Question || !sameOptions(cm, m)
	})
	if !changed {
		return fmt.Errorf("pressed Enter on option %d, but the prompt is still on screen — check with fleet status %s",
			m.Options[target].N, hexID(ob.Row.ID))
	}
	fmt.Printf("Answered %q → %d. %s\n", m.Question, m.Options[target].N, m.Options[target].Label)
	// The status is not reported: the pipeline needs a few seconds after a
	// prompt closes to say whether the agent resumed or stopped, and a guess
	// here would be wrong half the time. fleet wait is the honest answer.
	if after.View.Menu != nil {
		fmt.Println("Next prompt:")
		printMenuAndInput(os.Stdout, after, "  ")
	} else {
		fmt.Printf("No further prompt on screen (fleet wait %s collects the result)\n", hexID(ob.Row.ID))
	}
	return nil
}

// pickOption maps a choice to an index into m.Options: a number shown on
// screen, or yes/no for the first option whose label opens with that word.
func pickOption(m *session.Menu, choice string) (int, error) {
	c := strings.ToLower(strings.TrimSpace(choice))
	if n, err := strconv.Atoi(c); err == nil {
		for i, o := range m.Options {
			if o.N == n {
				return i, nil
			}
		}
		return 0, fmt.Errorf("the prompt has no option %d (options 1-%d)", n, len(m.Options))
	}
	var word string
	switch c {
	case "yes", "y", "approve", "allow":
		word = "yes"
	case "no", "n", "deny", "reject":
		word = "no"
	default:
		return 0, fmt.Errorf("choice %q is not a number, yes or no", choice)
	}
	for i, o := range m.Options {
		l := strings.ToLower(o.Label)
		if l == word || strings.HasPrefix(l, word+",") || strings.HasPrefix(l, word+" ") {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no option starts with %q — pick one by number:\n%s", word, menuLine(m, 60))
}

func menuMentions(m *session.Menu, text string) bool {
	hay := strings.ToLower(m.Question + "\n" + strings.Join(m.Context, "\n"))
	return strings.Contains(hay, strings.ToLower(text))
}

// isMultiSelect spots checkbox rows, where Enter toggles rather than answers.
func isMultiSelect(m *session.Menu) bool {
	for _, o := range m.Options {
		for _, box := range []string{"[ ]", "[x]", "[✔]", "[✓]", "☐", "☒", "◻", "◼"} {
			if strings.HasPrefix(o.Label, box) {
				return true
			}
		}
	}
	return false
}

func sameOptions(a, b *session.Menu) bool {
	if len(a.Options) != len(b.Options) {
		return false
	}
	for i := range a.Options {
		if a.Options[i].Label != b.Options[i].Label {
			return false
		}
	}
	return true
}

// --- interrupt ---

func runInterruptVerb(st *cliState, opts actOpts) error {
	o, ob := st.observeOne(opts.selector, 0)
	if err := requireLivePane(ob); err != nil {
		return err
	}
	if !opts.force {
		if ob.View.Menu != nil {
			return fmt.Errorf("session %q is waiting on a prompt — Esc would cancel it.\nTo decline, "+
				"fleet answer %s no; -force sends Esc anyway", ob.Row.Title, hexID(ob.Row.ID))
		}
		if ob.Status != session.StatusRunning && ob.Status != session.StatusStarting {
			return fmt.Errorf("session %q is not running (status %s), so there is nothing to interrupt", ob.Row.Title, statusWord(ob))
		}
	}
	if err := ob.Session.GetTmuxSession().SendKeys("Escape"); err != nil {
		return fmt.Errorf("failed to send Esc: %w", err)
	}
	if ob.Agent == agent.OpenCode {
		fmt.Printf("Sent Esc to %q (fleet can't confirm an OpenCode interrupt from its pane)\n", ob.Row.Title)
		return nil
	}
	if _, ok := pollPane(o, ob, 4*time.Second, func(cur *control.Observation) bool {
		return !session.PaneShowsWorking(cur.Agent, cur.Capture)
	}); !ok {
		return fmt.Errorf("sent Esc, but %q still shows it working — check with fleet read %s", ob.Row.Title, hexID(ob.Row.ID))
	}
	fmt.Printf("Interrupted %q\n", ob.Row.Title)
	return nil
}

// --- submit / clear ---

// requireClaudeInput gates the input-box verbs: only Claude's box is parsed,
// and a prompt on screen means keys go to it, not to the box.
func requireClaudeInput(ob *control.Observation) error {
	if err := requireLivePane(ob); err != nil {
		return err
	}
	if ob.Agent != agent.Claude {
		return fmt.Errorf("fleet reads only Claude's input box, and %q runs %s", ob.Row.Title, ob.Agent.DisplayName())
	}
	if ob.View.Menu != nil {
		return fmt.Errorf("session %q is waiting on a prompt, so its input box isn't focused — answer it first "+
			"(fleet answer %s)", ob.Row.Title, hexID(ob.Row.ID))
	}
	return nil
}

func runSubmitVerb(st *cliState, opts actOpts) error {
	o, ob := st.observeOne(opts.selector, 0)
	if err := requireClaudeInput(ob); err != nil {
		return err
	}
	in := ob.View.Input
	switch in.Kind {
	case session.InputEmpty:
		return fmt.Errorf("the input box of %q is empty — nothing to submit (fleet send types a message)", ob.Row.Title)
	case session.InputSuggestion:
		return fmt.Errorf("the input box of %q shows only Claude's suggestion %q — nobody typed it.\n"+
			"To send that text, fleet send %s %q", ob.Row.Title, clip(in.Text, 80), hexID(ob.Row.ID), in.Text)
	}
	if err := ob.Session.GetTmuxSession().SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to press Enter: %w", err)
	}
	if _, ok := pollPane(o, ob, 3*time.Second, func(cur *control.Observation) bool {
		return cur.View.Input.Kind != session.InputDraft || cur.View.Input.Text != in.Text
	}); !ok {
		return fmt.Errorf("pressed Enter, but the draft is still in the input box of %q", ob.Row.Title)
	}
	fmt.Printf("Submitted to %q: %s\n", ob.Row.Title, clip(in.Text, 80))
	return nil
}

func runClearVerb(st *cliState, opts actOpts) error {
	o, ob := st.observeOne(opts.selector, 0)
	if err := requireClaudeInput(ob); err != nil {
		return err
	}
	switch ob.View.Input.Kind {
	case session.InputEmpty:
		fmt.Printf("The input box of %q is already empty\n", ob.Row.Title)
		return nil
	case session.InputSuggestion:
		fmt.Printf("Nothing typed in %q (only Claude's suggestion is shown)\n", ob.Row.Title)
		return nil
	}
	cleared := ob.View.Input.Text
	ts := ob.Session.GetTmuxSession()
	cur := ob
	// Ctrl+U empties the line the cursor is on and Backspace joins it to the one
	// above, so a draft of n lines takes n rounds. Re-read between passes: a long
	// line that wraps takes more than its newline count says.
	for range 6 {
		lines := strings.Count(cur.View.Input.Text, "\n") + 1
		keys := []string{"C-u"}
		for i := 1; i < lines; i++ {
			keys = append(keys, "BSpace", "C-u")
		}
		if err := ts.SendKeys(keys...); err != nil {
			return fmt.Errorf("failed to clear the input box: %w", err)
		}
		var ok bool
		cur, ok = pollPane(o, cur, time.Second, func(c *control.Observation) bool {
			return c.View.Input.Kind != session.InputDraft
		})
		if ok {
			fmt.Printf("Cleared the input box of %q (was: %s)\n", ob.Row.Title, clip(cleared, 80))
			return nil
		}
		if cur.View.Menu != nil || cur.Pane != control.PaneAlive {
			break
		}
	}
	return fmt.Errorf("could not clear the input box of %q — check with fleet status %s", ob.Row.Title, hexID(ob.Row.ID))
}

// --- restart / suspend / resume ---

// busy reports a session an action would interrupt mid-work.
func busy(ob *control.Observation) bool {
	if ob.Pane != control.PaneAlive {
		return false
	}
	switch ob.Status {
	case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
		return true
	}
	return false
}

func runRestartVerb(st *cliState, opts actOpts) error {
	_, ob := st.observeOne(opts.selector, 0)
	if ob.Pane == control.PaneSuspended {
		return resumeSession(st, ob, opts.fresh)
	}
	if busy(ob) && !opts.force {
		return fmt.Errorf("session %q is %s — restarting kills what its agent is doing. Pass -force to restart anyway",
			ob.Row.Title, statusWord(ob))
	}
	s := ob.Session
	if err := prepareLaunch(ob, opts.fresh); err != nil {
		return err
	}

	oldTmux := s.TmuxSessionName
	// In place when the tmux session is still up: the TUI keeps pointing at the
	// same tmux session, and the pane keeps its scrollback. A gone or dead pane
	// needs the full relaunch, same as the TUI's r.
	var err error
	if ob.Pane == control.PaneAlive || ob.Pane == control.PaneShell {
		err = s.RespawnClaude()
	} else {
		err = s.Restart()
	}
	if err != nil {
		return fmt.Errorf("failed to restart %q: %w", ob.Row.Title, err)
	}
	persistRelaunch(st.storage, ob.Row, s)
	return confirmLaunched(ob, s, "Restarted", oldTmux != s.TmuxSessionName)
}

func runResumeVerb(st *cliState, opts actOpts) error {
	_, ob := st.observeOne(opts.selector, 0)
	if ob.Pane != control.PaneSuspended {
		if ob.Pane == control.PaneAlive {
			return fmt.Errorf("session %q is not suspended (status %s)", ob.Row.Title, statusWord(ob))
		}
		return fmt.Errorf("session %q is not suspended — its agent is not running (%s); fleet restart brings it back",
			ob.Row.Title, ob.Pane)
	}
	return resumeSession(st, ob, opts.fresh)
}

func resumeSession(st *cliState, ob *control.Observation, fresh bool) error {
	s := ob.Session
	if err := prepareLaunch(ob, fresh); err != nil {
		return err
	}
	if err := s.Restart(); err != nil {
		return fmt.Errorf("failed to resume %q: %w", ob.Row.Title, err)
	}
	persistRelaunch(st.storage, ob.Row, s)
	return confirmLaunched(ob, s, "Resumed", true)
}

// prepareLaunch refuses a relaunch that would land somewhere wrong: on a
// removed Claude account (see accountGoneErr), or resuming a Claude
// conversation with no transcript — `claude --resume` would print "No
// conversation found" and drop the pane to a shell, the state this command
// exists to get out of. With fresh, it drops the conversation id so the launch
// starts a new one.
func prepareLaunch(ob *control.Observation, fresh bool) error {
	if err := accountGoneErr(ob.Row); err != nil {
		return err
	}
	s := ob.Session
	if fresh {
		// Memory only: persistRelaunch writes the empty id once the launch has
		// worked, so a failed one still points at the old conversation.
		s.ClaudeSessionID = ""
		return nil
	}
	if ob.Agent != agent.Claude {
		return nil
	}
	// The id the relaunch will resume, after the heal it runs on the way in
	// (adoptResolvedLaunchID) — not the transcript status reads, which falls
	// back to the row's id when the current one has none.
	if id, _ := s.ResolveLaunchID(); id != "" {
		if p := session.ClaudeTranscriptPath(id, s.ProjectPath); p == "" || !fileExists(p) {
			return fmt.Errorf("session %q would resume conversation %s, which has no transcript (Claude writes "+
				"none until the first prompt) — `claude --resume` would fail with \"No conversation found\".\n"+
				"Pass -fresh to start a new conversation instead", ob.Row.Title, id)
		}
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// launchSettle is how long after the agent appears a relaunch is watched for
// dying straight back to the shell — a failed resume does so within ~2s.
const launchSettle = 4 * time.Second

// confirmLaunched waits for the relaunched agent to take the pane and stay
// there, and reports.
func confirmLaunched(ob *control.Observation, s *session.Session, verbWord string, newTmux bool) error {
	if paneCmd, atShell := waitForAgentInPane(s.TmuxSessionName); atShell {
		return fmt.Errorf("%s %q, but its pane is still at a %s prompt — the agent didn't start; check with fleet read %s",
			strings.ToLower(verbWord), ob.Row.Title, paneCmd, hexID(ob.Row.ID))
	}
	deadline := time.Now().Add(launchSettle)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		// A relaunch typed into a shell falls back to that shell when the agent
		// exits; one respawned in place is the pane's own process, so the pane
		// dies with it instead. Either is a failed launch.
		_, atShell := paneAtShell(s.TmuxSessionName)
		if atShell || s.GetTmuxSession().IsPaneDead() {
			// History, not the screen: the failing agent clears it on the way out.
			pane, _ := s.GetTmuxSession().CaptureHistory(200)
			if control.ResumeFailed(pane) {
				return fmt.Errorf("the agent in %q exited: \"No conversation found\". Run fleet restart -fresh %s "+
					"to start a new conversation", ob.Row.Title, hexID(ob.Row.ID))
			}
			return fmt.Errorf("the agent in %q exited right after launch — check with fleet read %s",
				ob.Row.Title, hexID(ob.Row.ID))
		}
	}
	what := "a new conversation"
	if id := s.GetClaudeSessionID(); id != "" {
		what = "resuming " + hexID(id)
	}
	fmt.Printf("%s %q (%s, %s)\n", verbWord, ob.Row.Title, s.Agent.DisplayName(), what)
	if newTmux {
		fmt.Println("Note: a fleet TUI picks this up within a few seconds.")
	}
	return nil
}

func runSuspendVerb(st *cliState, opts actOpts) error {
	_, ob := st.observeOne(opts.selector, 0)
	if ob.Pane == control.PaneSuspended {
		fmt.Printf("Session %q is already suspended\n", ob.Row.Title)
		return nil
	}
	if busy(ob) && !opts.force {
		return fmt.Errorf("session %q is %s — suspending kills what its agent is doing. Pass -force to suspend anyway",
			ob.Row.Title, statusWord(ob))
	}
	// The conversation id the hooks reported is what a resume reopens, and
	// Suspend deletes the hook file it came from. The TUI's worker persists it
	// as it arrives; with no TUI running nothing has, so save it here first.
	if id := ob.Session.GetClaudeSessionID(); id != "" && id != ob.Row.ClaudeSessionID {
		if err := st.storage.UpdateClaudeSessionID(ob.Row.ID, id); err != nil {
			return fmt.Errorf("not suspending %q: failed to save its conversation id: %w", ob.Row.Title, err)
		}
	}
	// Recorded before the kill, as Suspend marks memory before it: an open TUI
	// that sees the tmux session vanish reads the row first, and one still
	// naming a live state would have it log a crash and persist error over
	// this suspend. While tmux is up the TUI ignores a row saying suspended.
	if err := st.storage.UpdateStatus(ob.Row.ID, string(session.StatusSuspended)); err != nil {
		return fmt.Errorf("not suspending %q: failed to record it: %w", ob.Row.Title, err)
	}
	if err := ob.Session.Suspend(); err != nil {
		if rerr := st.storage.UpdateStatus(ob.Row.ID, ob.Row.Status); rerr != nil {
			debuglog.Logger.Error("failed to restore status after a failed suspend", "id", ob.Row.ID, "err", rerr)
		}
		return fmt.Errorf("failed to suspend %q: %w", ob.Row.Title, err)
	}
	fmt.Printf("Suspended %q — fleet resume %s brings it back\n", ob.Row.Title, hexID(ob.Row.ID))
	return nil
}
