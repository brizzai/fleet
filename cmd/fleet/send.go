package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/shell"
	"github.com/brizzai/fleet/internal/tmux"
)

const sendUsage = "Usage: fleet send [flags] <session> <message>"

// errMissingSendArgs is returned when the selector or the message is absent.
// runSend prints the usage block alongside it, so the message stays a plain
// error string.
var errMissingSendArgs = errors.New("missing session or message")

// sendOpts holds a parsed `fleet send` invocation. message may still be "-"
// (read stdin); resolving that is I/O and belongs to runSend.
type sendOpts struct {
	selector string
	message  string
	force    bool
}

// sendFlagSet builds the `fleet send` flag set, binding into o. Silent for the
// same reason worktreeFlagSet is: flag.ContinueOnError still prints the error
// and the usage block itself, which would emit both twice.
func sendFlagSet(o *sendOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.BoolVar(&o.force, "force", false, "send even though the agent is waiting on a prompt")
	return fs
}

// printSendUsage writes the usage line, the flag defaults, and how a session is
// named to w. The selector rules are the part people actually need, and there
// is nowhere else in the CLI they are written down.
func printSendUsage(w io.Writer) {
	fmt.Fprintln(w, sendUsage)
	var discard sendOpts
	fs := sendFlagSet(&discard)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
<session> is matched, in order, against: session id, exact title, workspace
name, git branch, id prefix, then any title containing it. @0-@9 picks the
session bound to that hotkey slot. An ambiguous match lists the candidates.

<message> is every remaining argument, joined by spaces; a lone - reads stdin.
Flags must come before <session> so a message can start with a dash.`)
}

// parseSendArgs parses the `fleet send` arguments. Kept pure (no db, no tmux,
// no stdin) so the argument rules are testable.
//
// Unlike `fleet worktree`, flags are NOT accepted after the positionals: every
// argument after the selector is message text, verbatim. A message is free-form
// user prose that can perfectly well begin with a dash, and having the flag
// parser claim part of it — silently dropping words from what the agent
// receives — is worse than requiring flags to come first.
func parseSendArgs(args []string) (sendOpts, error) {
	var o sendOpts
	fs := sendFlagSet(&o)
	if err := fs.Parse(args); err != nil {
		return o, err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return o, errMissingSendArgs
	}
	o.selector = strings.TrimSpace(rest[0])
	o.message = strings.TrimSpace(strings.Join(rest[1:], " "))
	if o.selector == "" || o.message == "" {
		return o, errMissingSendArgs
	}
	return o, nil
}

func runSend(args []string) {
	opts, err := parseSendArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printSendUsage(os.Stdout) // `-h` is a request, not a failure
			return
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errMissingSendArgs) || strings.HasPrefix(err.Error(), "flag ") {
			printSendUsage(os.Stderr)
		}
		os.Exit(1)
	}

	message, err := readStdinArg(opts.message, os.Stdin, "message")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Same ordering rule as runWorktree: migrate the legacy config dir before
	// anything creates ~/.config/fleet/, then init debuglog so session logging
	// doesn't fall back to stderr and scribble over the user's terminal.
	migration.Run()
	debuglog.Init()
	defer debuglog.Close()

	if err := tmux.IsTmuxAvailable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	rows, err := storage.LoadSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}
	// A missing slot table only costs the @N selector, so it is logged rather
	// than fatal — every other way of naming a session still works.
	slots, err := storage.LoadSlotBindings()
	if err != nil {
		debuglog.Logger.Error("failed to load slot bindings", "err", err)
		slots = nil
	}

	row, err := resolveSession(rows, slots, opts.selector, git.GetBranchName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := deliver(storage, row, message, opts.force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// deliver hands message to the session behind row, waking it first if it was
// suspended. Returns a user-facing error; prints its own success lines.
func deliver(storage *session.StateDB, row *session.SessionRow, message string, force bool) error {
	ag := agent.Parse(row.Agent)
	s := session.FromRow(row)

	// A suspended session has no tmux to type into — fleet killed it to free
	// memory. Its conversation survives in ClaudeSessionID, so wake it with the
	// message as the launch prompt: the agent resumes already working on it,
	// which is the same one-shot path `fleet worktree -p` uses.
	if session.Status(row.Status) == session.StatusSuspended {
		s.InitialPrompt = message
		if err := s.Restart(); err != nil {
			return fmt.Errorf("failed to wake suspended session %q: %w", row.Title, err)
		}
		// Restart built a new tmux session; without persisting the new name the
		// row points at a session that no longer exists and the next reader
		// (TUI or CLI) reads the session as dead.
		if err := storage.UpdateTmuxSession(row.ID, s.TmuxSessionName); err != nil {
			debuglog.Logger.Error("failed to persist tmux session after wake", "id", row.ID, "err", err)
		}
		if err := storage.UpdateStatus(row.ID, string(s.GetStatus())); err != nil {
			debuglog.Logger.Error("failed to persist status after wake", "id", row.ID, "err", err)
		}
		fmt.Printf("Woke suspended session '%s' (%s) with your message\n", row.Title, ag.DisplayName())
		fmt.Printf("Sent: %s\n", summarizeText(message, 60))
		// The TUI reads sessions from SQLite once, at startup, and only ever
		// adopts rows it has never seen — so an already-open fleet keeps showing
		// this one as suspended against a session that is now running. Say so
		// rather than let the sidebar look broken.
		fmt.Println("Note: a fleet TUI opened before now still shows it suspended until restarted.")
		return nil
	}

	ts := s.GetTmuxSession()
	if !ts.Exists() {
		return fmt.Errorf("session %q has no live tmux session (status %s) — restart it with r in the TUI",
			row.Title, row.Status)
	}
	// remain-on-exit keeps a dead pane on screen, so an exited agent looks
	// identical to a live one from the outside. Typing into it does nothing.
	if ts.IsPaneDead() {
		return fmt.Errorf("the %s process in session %q has exited — restart it with r in the TUI",
			ag.DisplayName(), row.Title)
	}
	// A live tmux session is not proof an agent is listening: the pane runs a
	// shell and the agent is a process inside it, so `/exit` (or a crash) drops
	// the pane back to a prompt with tmux none the wiser. Text pasted there is a
	// command line and the Enter runs it — "delete the old build dir and rerun
	// make" would be executed by the shell. Deliberately not bypassable with
	// -force: there is no version of this the user wanted.
	tmux.RefreshSessionCache() // cache is populated by the TUI's tick, which isn't running here
	if paneCmd := tmux.PaneCurrentCommand(row.TmuxSession); shell.IsShellCommand(paneCmd) {
		return fmt.Errorf("the %s process in session %q has exited — its pane is back at a %s prompt, "+
			"where the message would run as a shell command.\nRestart it with r in the TUI",
			ag.DisplayName(), row.Title, paneCmd)
	}

	if !force {
		if pane, err := ts.CapturePane(); err == nil && session.PaneIndicatesWaiting(ag, pane) {
			return fmt.Errorf("session %q is waiting on a prompt — a message typed into a menu is "+
				"discarded and its Enter confirms the highlighted option.\nAnswer it first (attach, "+
				"or Y to approve), or pass -force to send anyway", row.Title)
		}
	}

	if err := ts.PasteAndSubmit(message); err != nil {
		return fmt.Errorf("failed to send to session %q: %w", row.Title, err)
	}

	fmt.Printf("Sent to '%s' (%s)\n", row.Title, ag.DisplayName())
	fmt.Printf("Message: %s\n", summarizeText(message, 60))
	return nil
}

// resolveSession finds the one session named by selector.
//
// Tiers run most-precise first and stop at the first tier that matches
// anything: a tier matching several sessions is an error, not a reason to fall
// through to a vaguer one — falling through would let a fuzzy match silently
// beat the exact match the user was actually trying to disambiguate.
//
// branchOf is injected so the resolver stays pure and testable; it shells out
// to git in production, which is why it only runs in its own tier, after the
// free comparisons have failed.
func resolveSession(
	rows []*session.SessionRow,
	slots map[int]string,
	selector string,
	branchOf func(repoPath string) string,
) (*session.SessionRow, error) {
	if selector == "" {
		return nil, errMissingSendArgs
	}
	if len(rows) == 0 {
		return nil, errors.New("no sessions — create one with `fleet worktree <branch>` or in the TUI")
	}

	if strings.HasPrefix(selector, "@") {
		return resolveSlot(rows, slots, selector)
	}

	eq := func(a string) bool { return a != "" && strings.EqualFold(a, selector) }
	tiers := []struct {
		what  string
		match func(*session.SessionRow) bool
	}{
		{"id", func(r *session.SessionRow) bool { return r.ID == selector }},
		{"title", func(r *session.SessionRow) bool { return eq(r.Title) }},
		{"workspace", func(r *session.SessionRow) bool { return eq(r.WorkspaceName) }},
		{"branch", func(r *session.SessionRow) bool {
			return branchOf != nil && r.ProjectPath != "" && branchOf(r.ProjectPath) == selector
		}},
		{"id prefix", func(r *session.SessionRow) bool { return strings.HasPrefix(r.ID, selector) }},
		{"title", func(r *session.SessionRow) bool {
			return strings.Contains(strings.ToLower(r.Title), strings.ToLower(selector))
		}},
	}

	for _, tier := range tiers {
		var hits []*session.SessionRow
		for _, r := range rows {
			if tier.match(r) {
				hits = append(hits, r)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return nil, ambiguousError(selector, tier.what, hits)
		}
	}

	return nil, fmt.Errorf("no session matches %q — `fleet list` shows them all", selector)
}

// resolveSlot handles the @N selector: the hotkey slot a session is bound to in
// the sidebar.
func resolveSlot(rows []*session.SessionRow, slots map[int]string, selector string) (*session.SessionRow, error) {
	n, err := strconv.Atoi(selector[1:])
	if err != nil || n < 0 || n > 9 {
		return nil, fmt.Errorf("invalid slot %q — slots run @0 to @9", selector)
	}
	id, ok := slots[n]
	if !ok {
		return nil, fmt.Errorf("no session is bound to slot %d — bind one with alt+%d in the TUI", n, n)
	}
	for _, r := range rows {
		if r.ID == id {
			return r, nil
		}
	}
	// A slot binding outlives its session only if the FK cascade didn't fire.
	return nil, fmt.Errorf("slot %d points at a session that no longer exists", n)
}

// ambiguousError names every candidate, since the whole problem is that the
// user's selector didn't say which one they meant. Sorted by title so repeated
// runs print the same order (map-free, but LoadSessions' order is a query plan
// detail, not a promise).
func ambiguousError(selector, what string, hits []*session.SessionRow) error {
	sorted := make([]*session.SessionRow, len(hits))
	copy(sorted, hits)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Title < sorted[j].Title })

	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d sessions by %s:\n", selector, len(sorted), what)
	for _, r := range sorted {
		fmt.Fprintf(&b, "  %s  %s  (%s)\n", shortID(r.ID), r.Title, r.ProjectPath)
	}
	b.WriteString("Use one of the ids above.")
	return errors.New(b.String())
}

// shortID trims a session id to its hex half, the part `fleet list` shows.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
