package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/session"
)

const waitUsage = "Usage: fleet wait [flags] <session...>"

// Exit codes. A supervising agent branches on these, so they are part of the
// interface: 0 the condition holds, 2 it timed out, 3 a session is blocked on
// a prompt nobody will answer by waiting, 4 a session's agent exited. With
// -until done (which all three end), 0 means it finished cleanly.
const (
	waitExitTimeout = 2
	waitExitBlocked = 3
	waitExitExited  = 4
)

type waitOpts struct {
	selectors []string
	until     string
	timeout   time.Duration
	any       bool
	json      bool
}

func waitFlagSet(o *waitOpts) *flag.FlagSet {
	fs := newFlagSet("fleet wait")
	fs.StringVar(&o.until, "until", "done", "done | idle | waiting | running")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Minute, "give up after this long (exit 2)")
	fs.BoolVar(&o.any, "any", false, "with several sessions, return when the first one matches")
	fs.BoolVar(&o.json, "json", false, "print JSON instead of text")
	return fs
}

func printWaitUsage(w io.Writer) {
	fmt.Fprintln(w, waitUsage)
	var o waitOpts
	fs := waitFlagSet(&o)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
Blocks until the session (every session, or with -any the first) reaches the
-until state, then prints its status and last reply:

  done     not working any more: finished, idle, waiting, error or exited
  idle     finished its turn with nothing pending (finished or idle)
  waiting  blocked on a prompt
  running  started working

It returns early — rather than sitting out the timeout — when a session gets
stuck on a prompt (exit 3) or its agent exits (exit 4). Exit 2 is a timeout.
With -until done the exit code says how it stopped: 0 finished, 3 waiting on
a prompt, 4 exited or errored.

A state that already holds when the wait starts only counts once it has held
for a few seconds, so "fleet send s msg; fleet wait s" does not return before
the agent has picked the message up. fleet send -wait does both in one step.`)
}

func parseWaitArgs(args []string) (waitOpts, error) {
	var o waitOpts
	pos, err := parseInterspersed(waitFlagSet(&o), args)
	if err != nil {
		return o, err
	}
	if len(pos) == 0 {
		return o, fmt.Errorf("missing session")
	}
	o.selectors = pos
	switch o.until {
	case "done", "idle", "waiting", "running":
	default:
		return o, fmt.Errorf("-until %q: want done, idle, waiting or running", o.until)
	}
	if o.timeout <= 0 {
		return o, fmt.Errorf("-timeout must be positive")
	}
	return o, nil
}

func runWait(args []string) {
	opts, err := parseWaitArgs(args)
	handleParseErr(err, printWaitUsage)

	st := openCLIState()
	var rows []*session.SessionRow
	seen := map[string]bool{}
	for _, sel := range opts.selectors {
		if r := st.resolve(sel); !seen[r.ID] {
			seen[r.ID] = true
			rows = append(rows, r)
		}
	}
	code := waitFor(st, rows, waitSpec{
		until: opts.until, timeout: opts.timeout, any: opts.any, json: opts.json,
		since: time.Now(), settle: waitSettle,
	})
	st.close()
	os.Exit(code)
}

// waitSettle is how long a state that already held when `fleet wait` started
// must keep holding before it counts. Long enough for an agent to pick up a
// message `fleet send` just delivered — UserPromptSubmit fires on the Enter —
// short enough that waiting on a session already at rest costs little.
const waitSettle = 3 * time.Second

// sendWaitSettle is the same fallback for `fleet send -wait`, which has better
// evidence (a hook newer than the send) and only needs this if no hook fires.
const sendWaitSettle = 20 * time.Second

type waitSpec struct {
	until   string
	timeout time.Duration
	any     bool
	json    bool
	// since is when the thing being waited on began: the wait itself, or the
	// send that started a turn. A hook written after it is fresh evidence.
	since  time.Time
	settle time.Duration
}

// waitMatches reports whether an observation satisfies until.
func waitMatches(until string, ob *control.Observation) bool {
	switch until {
	case "running":
		return ob.Pane == control.PaneAlive && ob.Status == session.StatusRunning
	case "waiting":
		return ob.Pane == control.PaneAlive && ob.Status == session.StatusWaiting
	case "idle":
		return ob.Pane == control.PaneAlive && (ob.Status == session.StatusFinished || ob.Status == session.StatusIdle)
	default: // done
		return ob.Pane != control.PaneAlive || (ob.Status != session.StatusRunning && ob.Status != session.StatusStarting)
	}
}

// waitBlocked and waitExited are the states a wait gives up on early: nothing
// changes for a session blocked on a prompt or one whose agent is gone, however
// long it waits.
func waitBlocked(until string, ob *control.Observation) bool {
	return until != "done" && until != "waiting" && ob.Pane == control.PaneAlive && ob.Status == session.StatusWaiting
}

func waitExited(until string, ob *control.Observation) bool {
	if until == "done" {
		return false
	}
	return ob.Pane == control.PaneShell || ob.Pane == control.PaneDead || ob.Pane == control.PaneGone ||
		ob.Pane == control.PaneSuspended || ob.Status == session.StatusError
}

// waitCounts reports whether an observation that matches counts yet. A state
// that already held when the wait began says nothing about the turn being
// waited on, so it counts only once something shows the session moved on: it
// was seen not matching, a hook landed after since, or settle has passed.
//
// Hook stamps are whole seconds, so only a strictly later second is after
// since — a Stop from earlier in the send's own second would otherwise pass for
// the end of the turn the send started. UserPromptSubmit opens a turn rather
// than ending one. SessionStart (mapped to finished) is an agent starting up: a
// launch or a wake with a first prompt fires it just before that prompt's
// UserPromptSubmit, after a relaunch has already shown running, so it is
// neither fresh nor proof of movement.
func waitCounts(ob *control.Observation, moved bool, sinceSec time.Time, settled bool) bool {
	if settled {
		return true
	}
	if ob.HookEvent == "SessionStart" {
		return false
	}
	return moved || (ob.HookAt.After(sinceSec) && ob.HookEvent != "UserPromptSubmit")
}

// reloadRows re-reads the watched rows, so a session another process
// relaunched into a new tmux session is followed (the observer rebuilds it)
// rather than read as gone. A vanished row or a failed read keeps the last copy.
func reloadRows(st *cliState, rows []*session.SessionRow) []*session.SessionRow {
	fresh, err := st.storage.LoadSessions()
	if err != nil {
		return rows
	}
	byID := make(map[string]*session.SessionRow, len(fresh))
	for _, r := range fresh {
		byID[r.ID] = r
	}
	out := make([]*session.SessionRow, len(rows))
	for i, r := range rows {
		if f := byID[r.ID]; f != nil {
			out[i] = f
		} else {
			out[i] = r
		}
	}
	return out
}

// waitPoll is the gap between observations. Each costs two tmux calls for all
// the sessions together, so a second is cheap and well under any turn.
const waitPoll = time.Second

// waitFor polls rows until spec is met, prints the outcome, and returns the
// exit code.
func waitFor(st *cliState, rows []*session.SessionRow, spec waitSpec) int {
	o := st.observer(1)
	start := time.Now()
	sinceSec := spec.since.Truncate(time.Second)
	moved := map[string]bool{} // seen not matching since the wait began
	for {
		rows = reloadRows(st, rows)
		obs := o.Observe(rows)
		settled := time.Since(start) >= spec.settle
		var counted, blocked, exited []*control.Observation
		for _, ob := range obs {
			m := waitMatches(spec.until, ob)
			if !m {
				moved[ob.Row.ID] = true
			}
			if m && waitCounts(ob, moved[ob.Row.ID], sinceSec, settled) {
				counted = append(counted, ob)
			}
			switch {
			case waitBlocked(spec.until, ob):
				blocked = append(blocked, ob)
			case waitExited(spec.until, ob):
				exited = append(exited, ob)
			}
		}

		switch {
		case len(blocked) > 0:
			printWaitResult(obs, blocked, "blocked", start, spec)
			return waitExitBlocked
		case len(exited) > 0:
			printWaitResult(obs, exited, "exited", start, spec)
			return waitExitExited
		case spec.any && len(counted) > 0, len(counted) == len(obs):
			// "done" covers several ways of stopping; the exit code says which, so
			// a supervisor can branch without parsing the text.
			if spec.until == "done" {
				for _, ob := range counted {
					if ob.Pane != control.PaneAlive || ob.Status == session.StatusError {
						printWaitResult(obs, counted, "exited", start, spec)
						return waitExitExited
					}
				}
				for _, ob := range counted {
					if ob.Status == session.StatusWaiting {
						printWaitResult(obs, counted, "blocked", start, spec)
						return waitExitBlocked
					}
				}
			}
			printWaitResult(obs, counted, "matched", start, spec)
			return 0
		case time.Since(start) >= spec.timeout:
			printWaitResult(obs, nil, "timeout", start, spec)
			return waitExitTimeout
		}
		time.Sleep(waitPoll)
	}
}

type waitJSON struct {
	Result   string        `json:"result"`
	Until    string        `json:"until"`
	Waited   string        `json:"waited"`
	Sessions []sessionJSON `json:"sessions"`
	// Trigger names the sessions that decided the result.
	Trigger []string `json:"trigger,omitempty"`
}

func printWaitResult(all, trigger []*control.Observation, result string, start time.Time, spec waitSpec) {
	waited := humanDur(time.Since(start))
	if spec.json {
		j := waitJSON{Result: result, Until: spec.until, Waited: waited}
		for _, ob := range all {
			j.Sessions = append(j.Sessions, toJSON(ob, 0, false))
		}
		for _, ob := range trigger {
			j.Trigger = append(j.Trigger, ob.Row.ID)
		}
		printJSON(j)
		return
	}

	switch result {
	case "matched":
		fmt.Printf("%s after %s\n", untilPhrase(spec.until, len(trigger)), waited)
	case "blocked":
		fmt.Printf("Stopped after %s: waiting on a prompt\n", waited)
	case "exited":
		fmt.Printf("Stopped after %s: agent not running\n", waited)
	case "timeout":
		fmt.Printf("Timed out after %s (-until %s)\n", waited, spec.until)
	}
	show := trigger
	if result == "timeout" || (result == "matched" && !spec.any) {
		show = all
	}
	for _, ob := range show {
		printWaitSession(os.Stdout, ob)
	}
}

func untilPhrase(until string, n int) string {
	who := "Session"
	if n > 1 {
		who = fmt.Sprintf("%d sessions", n)
	}
	switch until {
	case "running":
		return who + " started running"
	case "waiting":
		return who + " waiting on a prompt"
	case "idle":
		return who + " finished"
	default:
		return who + " done"
	}
}

// printWaitSession is a session's line plus whatever a supervisor acts on
// next: the prompt it is blocked on, why it stopped, or its full last reply —
// the result it was waiting for.
func printWaitSession(w io.Writer, ob *control.Observation) {
	fmt.Fprintf(w, "── %s · %s · %s\n", ob.Row.Title, statusWord(ob), hexID(ob.Row.ID))
	if ob.Note != "" {
		fmt.Fprintln(w, "!! "+ob.Note)
	}
	if ob.View.Menu != nil || ob.View.Input.Kind == session.InputDraft {
		printMenuAndInput(w, ob, "")
		return
	}
	n := len(ob.Turns)
	switch r := strings.TrimSpace(ob.LastReply); {
	case r != "":
		fmt.Fprintln(w, r)
	case n > 0 && ob.Turns[n-1].Interrupted:
		fmt.Fprintln(w, "(the turn ended without a reply — interrupted, or a prompt was declined)")
	case n > 0 && !ob.Turns[n-1].Done:
		fmt.Fprintf(w, "(still working — %d tool call%s so far)\n", ob.Turns[n-1].Tools, plural(ob.Turns[n-1].Tools))
	}
}
