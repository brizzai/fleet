package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	deliverpkg "github.com/brizzai/fleet/internal/deliver"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/session"
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
	wait     bool
	timeout  time.Duration
}

// sendFlagSet builds the `fleet send` flag set, binding into o. Silent for the
// same reason worktreeFlagSet is: flag.ContinueOnError still prints the error
// and the usage block itself, which would emit both twice.
func sendFlagSet(o *sendOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.BoolVar(&o.force, "force", false, "send even though the agent is waiting on a prompt")
	fs.BoolVar(&o.wait, "wait", false, "after sending, wait for the turn to end and print the reply (exit codes as fleet wait)")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Minute, "with -wait, give up after this long")
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

	sentAt := time.Now()
	if err := deliver(storage, row, message, opts.force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if opts.wait {
		os.Exit(waitAfterSend(storage, row.ID, slots, sentAt, opts.timeout))
	}
}

// waitAfterSend waits for the turn the message started to end. The row is
// reloaded because waking a suspended session gave it a new tmux session.
func waitAfterSend(storage *session.StateDB, id string, slots map[int]string, sentAt time.Time, timeout time.Duration) int {
	rows, err := storage.LoadSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to reload sessions: %v\n", err)
		return 1
	}
	var row *session.SessionRow
	for _, r := range rows {
		if r.ID == id {
			row = r
		}
	}
	if row == nil {
		fmt.Fprintln(os.Stderr, "the session disappeared while sending")
		return 1
	}
	prs, _ := storage.LoadPRCache()
	st := &cliState{storage: storage, rows: rows, slots: slots, prs: prs}
	fmt.Println()
	return waitFor(st, []*session.SessionRow{row}, waitSpec{
		until: "done", timeout: timeout, since: sentAt, settle: sendWaitSettle,
	})
}

// deliver hands message to the session behind row, waking it first if it was
// suspended. Returns a user-facing error; prints its own success lines. The
// guards themselves live in internal/deliver, shared with the TUI.
func deliver(storage *session.StateDB, row *session.SessionRow, message string, force bool) error {
	ag := agent.Parse(row.Agent)
	s := session.FromRow(row)

	res, err := deliverpkg.Deliver(context.Background(), deliverpkg.Deps{}, s, message, deliverpkg.Options{Force: force})
	if res.PromptUndetectable {
		fmt.Fprintf(os.Stderr, "Note: fleet can't tell whether an OpenCode session is waiting on a "+
			"prompt.\nIf it is, this message is discarded and its Enter confirms the highlighted option.\n")
	}
	if err != nil {
		return err
	}

	if res.Woke {
		persistRelaunch(storage, row, s)
		fmt.Printf("Woke suspended session '%s' (%s) with your message\n", row.Title, ag.DisplayName())
		fmt.Printf("Sent: %s\n", summarizeText(message, 60))
		// An open TUI adopts the new tmux session on its next sync sweep
		// (relaunchedElsewhere), so for a few seconds it still shows suspended.
		fmt.Println("Note: an open fleet TUI picks this up within a few seconds.")
		return nil
	}

	fmt.Printf("Sent to '%s' (%s)\n", row.Title, ag.DisplayName())
	fmt.Printf("Message: %s\n", summarizeText(message, 60))
	return nil
}

// accountGoneErr refuses to relaunch a Claude session pinned to an account
// that is no longer configured — see deliverpkg.AccountGoneErr.
func accountGoneErr(row *session.SessionRow) error {
	return deliverpkg.AccountGoneErr(agent.Parse(row.Agent), row.Account, row.Title)
}

// persistRelaunch records what a relaunch (Restart or RespawnClaude) changed
// on the session, from a CLI process that exits right after.
func persistRelaunch(storage *session.StateDB, row *session.SessionRow, s *session.Session) {
	deliverpkg.PersistRelaunch(storage, row.ID, row.TmuxSession, row.ClaudeSessionID, s)
}

// waitForAgentInPane reports whether the pane is sitting at a shell prompt with
// no agent in the foreground, after a startup grace — see
// deliverpkg.WaitForAgentInPane.
func waitForAgentInPane(tmuxSession string) (paneCmd string, atShell bool) {
	return deliverpkg.WaitForAgentInPane(tmuxSession)
}

// paneAtShell reads the pane's foreground command once, after refreshing the
// tmux cache the TUI's tick would otherwise keep current.
func paneAtShell(tmuxSession string) (paneCmd string, atShell bool) {
	return deliverpkg.PaneAtShell(tmuxSession)
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
	return resolveSessionTiers(rows, slots, selector, branchOf, true)
}

// resolveSessionExact is resolveSession without the title-substring tier, for
// commands where a near miss is worth refusing: `fleet remove frontend` must
// not delete the one session whose title happens to contain the word.
func resolveSessionExact(
	rows []*session.SessionRow,
	slots map[int]string,
	selector string,
	branchOf func(repoPath string) string,
) (*session.SessionRow, error) {
	return resolveSessionTiers(rows, slots, selector, branchOf, false)
}

func resolveSessionTiers(
	rows []*session.SessionRow,
	slots map[int]string,
	selector string,
	branchOf func(repoPath string) string,
	fuzzy bool,
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
	}
	if fuzzy {
		tiers = append(tiers, struct {
			what  string
			match func(*session.SessionRow) bool
		}{"title", func(r *session.SessionRow) bool {
			return strings.Contains(strings.ToLower(r.Title), strings.ToLower(selector))
		}})
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
