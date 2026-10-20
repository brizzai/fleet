package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/session"
)

const statusUsage = "Usage: fleet status [flags] [session...]"

type statusOpts struct {
	selectors []string
	json      bool
	all       bool
	active    bool
	waiting   bool
	full      bool
}

func statusFlagSet(o *statusOpts) *flag.FlagSet {
	fs := newFlagSet("fleet status")
	fs.BoolVar(&o.json, "json", false, "print JSON (one array) instead of text")
	fs.BoolVar(&o.all, "all", false, "include suspended sessions")
	fs.BoolVar(&o.active, "active", false, "only sessions running, waiting or starting")
	fs.BoolVar(&o.waiting, "waiting", false, "only sessions waiting on a prompt")
	fs.BoolVar(&o.full, "full", false, "don't truncate prompts and replies in JSON")
	return fs
}

func printStatusUsage(w io.Writer) {
	fmt.Fprintln(w, statusUsage)
	var o statusOpts
	fs := statusFlagSet(&o)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
Without a session: one line per session (suspended ones are counted, not
listed, unless -all), plus an indented line when a session is waiting on a
prompt (? question and its numbered options), holds an unsent draft (✎), or
has exited (!). With sessions: a detail block for each — last prompt, last
reply, the prompt it is waiting on and the input box.

Status is recomputed live with the same pipeline the TUI runs, so it is
current whether or not the TUI is open. Selectors work as in fleet send.`)
}

func parseStatusArgs(args []string) (statusOpts, error) {
	var o statusOpts
	pos, err := parseInterspersed(statusFlagSet(&o), args)
	o.selectors = pos
	return o, err
}

func runStatus(args []string) {
	opts, err := parseStatusArgs(args)
	handleParseErr(err, printStatusUsage)

	st := openCLIState()
	defer st.close()

	rows := st.rows
	if len(opts.selectors) > 0 {
		rows = nil
		seen := map[string]bool{}
		for _, sel := range opts.selectors {
			r := st.resolve(sel)
			if !seen[r.ID] {
				seen[r.ID] = true
				rows = append(rows, r)
			}
		}
	}

	// Suspended sessions have no pane to read; observing them only costs the
	// transcript reads, so skip them unless they will be shown.
	var observed, suspended []*session.SessionRow
	for _, r := range rows {
		if session.Status(r.Status) == session.StatusSuspended && !opts.all && len(opts.selectors) == 0 {
			suspended = append(suspended, r)
			continue
		}
		observed = append(observed, r)
	}

	obs := st.observer(1).Observe(observed)
	obs = filterObservations(obs, opts)
	sortObservations(obs)

	textMax := 300
	if opts.full {
		textMax = 0
	}
	if opts.json {
		out := make([]sessionJSON, 0, len(obs))
		for _, ob := range obs {
			out = append(out, toJSON(ob, textMax, false))
		}
		printJSON(out)
		return
	}
	if len(opts.selectors) > 0 {
		for i, ob := range obs {
			if i > 0 {
				fmt.Println()
			}
			printDetail(os.Stdout, ob)
		}
		return
	}
	printStatusTable(os.Stdout, obs, len(suspended), opts)
}

func filterObservations(obs []*control.Observation, o statusOpts) []*control.Observation {
	if !o.active && !o.waiting {
		return obs
	}
	var out []*control.Observation
	for _, ob := range obs {
		switch {
		case o.waiting && ob.Status == session.StatusWaiting:
			out = append(out, ob)
		case o.active && !o.waiting && ob.Pane != control.PaneShell &&
			(ob.Status == session.StatusRunning || ob.Status == session.StatusWaiting || ob.Status == session.StatusStarting):
			out = append(out, ob)
		}
	}
	return out
}

// statusRank orders the table by what most wants the reader: blocked first,
// then broken, then working, then done.
func statusRank(ob *control.Observation) int {
	if ob.Pane == control.PaneShell {
		return 1
	}
	switch ob.Status {
	case session.StatusWaiting:
		return 0
	case session.StatusError:
		return 1
	case session.StatusRunning, session.StatusStarting:
		return 2
	case session.StatusFinished:
		return 3
	case session.StatusIdle:
		return 4
	default:
		return 5
	}
}

func sortObservations(obs []*control.Observation) {
	sort.SliceStable(obs, func(i, j int) bool {
		ri, rj := statusRank(obs[i]), statusRank(obs[j])
		if ri != rj {
			return ri < rj
		}
		return obs[i].Row.Title < obs[j].Row.Title
	})
}

func printStatusTable(w io.Writer, obs []*control.Observation, suspended int, o statusOpts) {
	if len(obs) == 0 {
		switch {
		case o.waiting:
			fmt.Fprintln(w, "No sessions waiting.")
		case o.active:
			fmt.Fprintln(w, "No active sessions.")
		default:
			fmt.Fprintln(w, "No sessions.")
		}
		if suspended > 0 {
			fmt.Fprintf(w, "%d suspended (fleet status -all lists them)\n", suspended)
		}
		return
	}

	type row struct{ cols [7]string }
	rows := make([]row, 0, len(obs)+1)
	rows = append(rows, row{[7]string{"ID", "STATUS", "FOR", "AGENT", "TITLE", "BRANCH", "PR"}})
	for _, ob := range obs {
		forCol := "-"
		if !ob.Since.IsZero() {
			forCol = humanDur(time.Since(ob.Since))
		}
		rows = append(rows, row{[7]string{
			hexID(ob.Row.ID), statusWord(ob), forCol, string(ob.Agent),
			clip(ob.Row.Title, 40), clip(ob.Branch, 30), prShort(ob.PR),
		}})
	}
	var width [7]int
	for _, r := range rows {
		for i, c := range r.cols {
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	for ri, r := range rows {
		var b strings.Builder
		for i, c := range r.cols {
			if i == len(r.cols)-1 {
				b.WriteString(c)
				break
			}
			pad := width[i] - len([]rune(c))
			if i == 2 { // durations read better right-aligned
				b.WriteString(strings.Repeat(" ", pad) + c)
			} else {
				b.WriteString(c + strings.Repeat(" ", pad))
			}
			b.WriteString("  ")
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
		if ri > 0 {
			for _, a := range annotations(obs[ri-1]) {
				fmt.Fprintf(w, "%s  %s\n", strings.Repeat(" ", width[0]), a)
			}
		}
	}

	counts := map[string]int{}
	for _, ob := range obs {
		counts[statusWord(ob)]++
	}
	parts := []string{fmt.Sprintf("%d shown", len(obs))}
	for _, s := range []string{"waiting", "error", "exited", "running", "starting", "finished", "idle", "suspended"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if suspended > 0 {
		parts = append(parts, fmt.Sprintf("%d suspended hidden (-all)", suspended))
	}
	fmt.Fprintln(w, strings.Join(parts, " · "))
}

// printDetail writes one session's detail block.
func printDetail(w io.Writer, ob *control.Observation) {
	fmt.Fprintln(w, ob.Row.Title)
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-8s %s\n", k, v)
		}
	}
	id := ob.Row.ID
	if ob.Slot >= 0 {
		id += fmt.Sprintf("  (slot @%d)", ob.Slot)
	}
	kv("id", id)

	state := statusWord(ob)
	if !ob.Since.IsZero() {
		state += " for " + humanDur(time.Since(ob.Since))
	}
	state += " · " + string(ob.Agent) + " · pane " + string(ob.Pane)
	if ob.PaneCmd != "" && ob.Pane == control.PaneShell {
		state += " (" + ob.PaneCmd + ")"
	}
	kv("status", state)
	kv("note", ob.Note)
	if ob.Exit != "" {
		kv("exit", ob.Exit)
	}
	where := homeRel(ob.Row.ProjectPath)
	if ob.Branch != "" {
		where += "  branch " + ob.Branch
	}
	kv("path", where)
	if ob.PR != nil && ob.PR.Number != 0 {
		kv("pr", fmt.Sprintf("#%d %s  %s", ob.PR.Number, prState(ob.PR), ob.PR.URL))
	}
	if ob.HookEvent != "" {
		kv("hook", ob.HookEvent+" "+ago(ob.HookAt)+" ago")
	}
	if !ob.LastActivity.IsZero() {
		kv("activity", ago(ob.LastActivity)+" ago")
	}
	if until := ob.Row.SnoozedUntil; until.After(time.Now()) {
		kv("snoozed", "until "+until.Local().Format("Jan 2 15:04"))
	}
	if ob.LastPrompt != "" {
		kv("prompt", clip(ob.LastPrompt, 300))
	}
	if ob.LastReply != "" {
		kv("reply", clip(ob.LastReply, 600))
	} else if n := len(ob.Turns); n > 0 && !ob.Turns[n-1].Done {
		kv("reply", fmt.Sprintf("(working — %d tool call%s so far)", ob.Turns[n-1].Tools, plural(ob.Turns[n-1].Tools)))
	}
	printMenuAndInput(w, ob, "  ")
}

// printMenuAndInput writes the waiting menu (all options, cursor marked) and
// the input box, which `fleet status <s>`, `fleet read` and `fleet wait` share.
func printMenuAndInput(w io.Writer, ob *control.Observation, indent string) {
	if m := ob.View.Menu; m != nil {
		fmt.Fprintf(w, "%swaiting  %s: %s\n", indent, m.Kind, m.Question)
		for _, c := range m.Context {
			fmt.Fprintf(w, "%s           %s\n", indent, c)
		}
		for i, o := range m.Options {
			mark := " "
			if i == m.Cursor {
				mark = "❯"
			}
			line := fmt.Sprintf("%d. %s", o.N, o.Label)
			if o.Detail != "" {
				line += " — " + clip(o.Detail, 120)
			}
			fmt.Fprintf(w, "%s         %s %s\n", indent, mark, line)
		}
		fmt.Fprintf(w, "%s           answer: fleet answer %s <n|yes|no>\n", indent, hexID(ob.Row.ID))
	}
	switch in := ob.View.Input; in.Kind {
	case session.InputDraft:
		fmt.Fprintf(w, "%sinput    unsent draft: %q  (fleet submit / fleet clear)\n", indent, clip(in.Text, 300))
	case session.InputSuggestion:
		fmt.Fprintf(w, "%sinput    empty (suggestion shown, not typed: %q)\n", indent, clip(in.Text, 120))
	}
}
