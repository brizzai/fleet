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

const readUsage = "Usage: fleet read [flags] <session>"

type readOpts struct {
	selector string
	turns    int
	lines    int
	json     bool
}

func readFlagSet(o *readOpts) *flag.FlagSet {
	fs := newFlagSet("fleet read")
	fs.IntVar(&o.turns, "turns", 3, "how many recent turns to show from the agent's transcript")
	fs.IntVar(&o.lines, "lines", 0, "show the last N lines of the cleaned screen (with scrollback) instead of turns")
	fs.BoolVar(&o.json, "json", false, "print JSON instead of text")
	return fs
}

func printReadUsage(w io.Writer) {
	fmt.Fprintln(w, readUsage)
	var o readOpts
	fs := readFlagSet(&o)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
By default: the session's last turns from its own transcript — each prompt, the
agent's closing reply in full, and how many tool calls it made — then the prompt
it is waiting on and its input box. Transcripts are read for Claude; for the
other agents, or with -lines, it prints the screen instead, with the status bar,
input box, box frames and spinner lines stripped.`)
}

func parseReadArgs(args []string) (readOpts, error) {
	var o readOpts
	pos, err := parseInterspersed(readFlagSet(&o), args)
	if err != nil {
		return o, err
	}
	switch len(pos) {
	case 0:
		return o, fmt.Errorf("missing session")
	case 1:
		o.selector = pos[0]
	default:
		return o, fmt.Errorf("unexpected argument %q — fleet read takes one session", pos[1])
	}
	if o.turns < 1 {
		return o, fmt.Errorf("-turns must be at least 1")
	}
	if o.lines < 0 {
		return o, fmt.Errorf("-lines must be positive")
	}
	return o, nil
}

// defaultScreenLines is how much screen `fleet read` shows when there is no
// transcript to read.
const defaultScreenLines = 40

func runRead(args []string) {
	opts, err := parseReadArgs(args)
	handleParseErr(err, printReadUsage)

	st := openCLIState()
	defer st.close()
	_, ob := st.observeOne(opts.selector, opts.turns)

	var screen []string
	lines := opts.lines
	if lines == 0 && len(ob.Turns) == 0 {
		lines = defaultScreenLines
	}
	if lines > 0 {
		screen = readScreen(ob, lines)
	}

	if opts.json {
		j := toJSON(ob, 0, opts.lines == 0)
		j.Screen = screen
		printJSON(j)
		return
	}

	w := os.Stdout
	header := fmt.Sprintf("── %s · %s", ob.Row.Title, statusWord(ob))
	if !ob.Since.IsZero() {
		header += " " + humanDur(time.Since(ob.Since))
	}
	header += " · " + string(ob.Agent) + " · " + hexID(ob.Row.ID)
	fmt.Fprintln(w, header)
	if ob.Note != "" {
		fmt.Fprintln(w, "!! "+ob.Note)
	}
	if screen != nil {
		for _, l := range screen {
			fmt.Fprintln(w, l)
		}
		if len(screen) == 0 {
			fmt.Fprintln(w, "(nothing on screen)")
		}
	} else {
		printTurns(w, ob)
	}
	if ob.View.Menu != nil || ob.View.Input.Kind != session.InputEmpty {
		fmt.Fprintln(w, "──")
		printMenuAndInput(w, ob, "")
	}
}

// readScreen captures the pane's scrollback and returns its last n cleaned
// lines. Empty when the session has no pane to read.
func readScreen(ob *control.Observation, n int) []string {
	if ob.Pane != control.PaneAlive && ob.Pane != control.PaneShell && ob.Pane != control.PaneDead {
		return []string{}
	}
	// Blank runs and chrome are dropped after capture, so ask for more than n.
	raw, err := ob.Session.GetTmuxSession().CaptureHistory(n * 3)
	if err != nil {
		raw = ob.Capture
	}
	lines := session.CleanPaneLines(raw)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func printTurns(w io.Writer, ob *control.Observation) {
	if len(ob.Turns) == 0 {
		fmt.Fprintln(w, "(no transcript turns)")
		return
	}
	for _, t := range ob.Turns {
		fmt.Fprintf(w, "─── you%s\n", stamp(t.PromptAt))
		fmt.Fprintln(w, strings.TrimSpace(t.Prompt))
		meta := string(ob.Agent)
		if t.Tools > 0 {
			meta += fmt.Sprintf(" · %d tool call%s", t.Tools, plural(t.Tools))
		}
		switch {
		case t.Interrupted:
			meta += " · interrupted"
		case !t.Done && t.Reply == "":
			meta += " · working"
		case !t.Done:
			meta += " · still working"
		}
		fmt.Fprintf(w, "─── %s%s\n", meta, stamp(t.ReplyAt))
		if t.Reply != "" {
			fmt.Fprintln(w, t.Reply)
		}
	}
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if time.Since(t) > 20*time.Hour {
		return " · " + t.Local().Format("Jan 2 15:04")
	}
	return " · " + t.Local().Format("15:04")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
