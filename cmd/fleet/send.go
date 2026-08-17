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
	"time"
	"unicode"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
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

	// Installed here for the same reason main.go and worktree.go install it: this
	// command relaunches sessions. Waking a suspended one goes through Restart()
	// → sessionEnv, and without the resolver that lookup misses, no
	// CLAUDE_CONFIG_DIR is set, and the session comes back on the ambient login —
	// then persists a healthy-looking row while every fleet surface keeps naming
	// the account it is no longer running as. That is a wrong-subscription charge
	// discovered from an invoice.
	session.SetAccountConfigDirFunc(claudeaccount.Load().ConfigDirFor)

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
		// Restart may have *healed* the conversation id on the way in
		// (adoptResolvedLaunchID promotes a resolved id over a stale one — the
		// issue #226 path). The TUI gets away with not persisting that here
		// because its hook worker re-persists moments later; this process exits
		// as soon as deliver returns, so an unsaved heal is recomputed and thrown
		// away on every wake and the session stays pinned to a dead conversation.
		if id := s.ClaudeSessionID; id != "" && id != row.ClaudeSessionID {
			if err := storage.UpdateClaudeSessionID(row.ID, id); err != nil {
				debuglog.Logger.Error("failed to persist healed conversation id after wake", "id", row.ID, "err", err)
			}
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
	if paneCmd, atShell := waitForAgentInPane(row.TmuxSession); atShell {
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
		// The check above can't see an OpenCode prompt (no structural detector for
		// its UI), so for that agent the guard silently withdraws at the moment it
		// matters most: a paste into a permission menu is discarded and the Enter
		// confirms whatever was highlighted. Refusing outright would be worse —
		// OpenCode defaults to allow-all, so every send would need -force and the
		// flag would become reflex — but proceeding without saying so leaves the
		// user with a guarantee they don't have.
		if ag == agent.OpenCode {
			fmt.Fprintf(os.Stderr, "Note: fleet can't tell whether an OpenCode session is waiting on a "+
				"prompt.\nIf it is, this message is discarded and its Enter confirms the highlighted option.\n")
		}
	}

	if err := ts.PasteAndSubmit(message); err != nil {
		return fmt.Errorf("failed to send to session %q: %w", row.Title, err)
	}
	// A pane whose agent is *running* is not necessarily an agent that is
	// *listening*: for a couple of seconds after the process appears, the TUI is
	// still painting and anything written to the pty is discarded. Measured at
	// ~2.4s for Claude, and no signal available here distinguishes it — the
	// process name has already flipped, and the pane content sits static
	// mid-startup, so waiting for it to settle fires ~2s too early.
	//
	// So don't predict readiness, check the outcome: the message either shows up
	// in the pane or it didn't arrive. Reporting "Sent" for a message that
	// vanished is the worst thing this command could do, and it is exactly what
	// happens without this. Deliberately no auto-retry — a re-paste on a false
	// negative would submit the message twice.
	if !confirmDelivered(ts, message) {
		return fmt.Errorf("the message was not delivered — %q was still starting and its input "+
			"wasn't ready yet.\nNothing reached the agent; try again in a moment", row.Title)
	}

	fmt.Printf("Sent to '%s' (%s)\n", row.Title, ag.DisplayName())
	fmt.Printf("Message: %s\n", summarizeText(message, 60))
	return nil
}

// agentStartupGrace bounds how long waitForAgentInPane waits for a pane to stop
// looking like a bare shell. Generous because the cost is paid only on the
// genuinely-exited path, which is already an error, while under-waiting
// produces a *wrong* error on a healthy session: measured start times ranged
// from 0.6s to 7.4s, dominated by shell rc and the agent's own boot.
const agentStartupGrace = 10 * time.Second

// agentStartupPoll is the gap between pane reads during that grace period.
const agentStartupPoll = 150 * time.Millisecond

// deliveryGrace bounds how long confirmDelivered waits for the message to show
// up in the pane, and deliveryPoll is the gap between those reads.
const (
	deliveryGrace = 2 * time.Second
	deliveryPoll  = 250 * time.Millisecond
)

// waitForAgentInPane reports whether the pane is sitting at a shell prompt with
// no agent in the foreground, returning the command it settled on.
//
// It polls rather than reading once, because a single read cannot tell "the
// agent exited" from "the agent hasn't started yet" — and both are common. A
// session is launched by *typing* the agent's command into a fresh pane, so for
// as long as the shell takes to source its rc, pane_current_command is `zsh`.
// That is precisely the window the advertised script runs in:
//
//	gh issue view 242 | fleet wt fix-242 -p -
//	fleet send fix-242 "also update the tests"     # ← lands mid-startup
//
// Reading once made that fail with "the Claude process has exited", which was
// both false and — since this guard is deliberately not -force-able — a dead
// end. Waiting costs nothing on a healthy session (the first read after startup
// wins) and delays only the genuinely-exited case, which is already an error.
func waitForAgentInPane(tmuxSession string) (paneCmd string, atShell bool) {
	deadline := time.Now().Add(agentStartupGrace)
	for {
		// Re-refresh every pass: the cache is a snapshot, and the TUI's tick that
		// normally repopulates it isn't running in a one-shot CLI process.
		tmux.RefreshSessionCache()
		paneCmd = tmux.PaneCurrentCommand(tmuxSession)
		if !shell.IsShellCommand(paneCmd) {
			return paneCmd, false
		}
		if time.Now().After(deadline) {
			return paneCmd, true
		}
		time.Sleep(agentStartupPoll)
	}
}

// confirmDelivered reports whether message visibly reached the pane.
//
// "Visibly reached" is the right bar rather than "was submitted": a message
// sitting in the input box is in front of the user and recoverable, whereas one
// swallowed during startup is gone with nothing on screen to show for it.
func confirmDelivered(ts *tmux.Session, message string) bool {
	needle := deliveryNeedle(message)
	if needle == "" {
		return true // nothing distinctive to look for; don't invent a failure
	}
	deadline := time.Now().Add(deliveryGrace)
	for {
		if pane, err := ts.CapturePaneFresh(); err == nil {
			hay := squashSpace(session.StripANSI(pane))
			// Claude collapses a multi-line paste to a "[Pasted text #1 +N lines]"
			// placeholder, so the text itself is never on screen to match.
			if strings.Contains(hay, needle) || strings.Contains(hay, "[Pastedtext") {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(deliveryPoll)
	}
}

// deliveryNeedle builds what to look for in the pane: the head of the message's
// first line, with whitespace squashed out.
//
// Squashing is what makes the match survive rendering — the pane wraps long
// lines and indents continuations, so the message's own spacing is not what
// ends up on screen. Returns "" for a message too short to identify, where a
// match would be indistinguishable from coincidence; the caller then skips the
// check rather than guessing.
func deliveryNeedle(message string) string {
	line := message
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	squashed := squashSpace(line)
	r := []rune(squashed)
	if len(r) < 6 {
		return ""
	}
	if len(r) > 24 {
		r = r[:24]
	}
	return string(r)
}

// squashSpace removes all whitespace, so wrapped and re-indented pane text
// still matches the message it came from.
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
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
