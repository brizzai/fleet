package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/ticket"
	"github.com/brizzai/fleet/internal/ticketing"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/workspace"
)

const worktreeUsage = "Usage: fleet worktree <branch> [flags]"

// errMissingBranch is returned when no branch was given. runWorktree prints the
// usage line alongside it, so the message itself stays a plain error string.
var errMissingBranch = errors.New("missing branch name")

// ticketIDRe validates a ticket identifier shape before anything is created.
// One pattern for both trackers: a Linear team key and a Jira project key are
// the same shape, and which one owns it is settled by the repo's config, not
// by the text.
// Deliberately checked here rather than deferred: a typo'd ticket should fail
// while the worktree still doesn't exist.
var ticketIDRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}-\d{1,7}$`)

// worktreeOpts holds the parsed `fleet worktree` invocation. Base and agent are
// left empty when unset; their defaults depend on the repo (default branch) and
// the user's config (default agent), which parsing can't see.
type worktreeOpts struct {
	branch    string
	base      string
	repoPath  string
	agentName string
	account   string
	noSession bool
	// prompt is the raw flag value, which may still be "-" for stdin. Reading
	// stdin is I/O, and parsing stays pure — runWorktree resolves it.
	prompt string
	// ticket is a tracker issue identifier. It names the branch when no branch
	// is given, and materializes the issue (with its screenshots) into the new
	// worktree so the agent opens having been pointed at it.
	ticket string
	// noTicketStart opts out of the one mutation fleet makes: moving the issue
	// to its team's first started state.
	noTicketStart bool
	// model and effort override the agent's own defaults for this launch. Both
	// are embedded in the command string, so their shape is validated at parse
	// time — see validateLaunchOverrides.
	model  string
	effort string
}

// worktreeFlagSet builds the `fleet worktree` flag set, binding into o.
//
// The flag package is kept silent — `flag.ContinueOnError` does NOT suppress
// output: Parse calls failf, which writes the error to fs.Output() and then
// runs Usage. Left alone, a bad flag prints the error, the usage block, and
// then the error again from the caller. printWorktreeUsage owns the usage text
// and runWorktree owns the errors, so each is emitted exactly once, in order.
func worktreeFlagSet(o *worktreeOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet worktree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&o.base, "base", "", "base branch to branch from (default: the repo's default branch)")
	fs.StringVar(&o.repoPath, "path", "", "repo to create the worktree in (default: current directory)")
	fs.StringVar(&o.agentName, "agent", "", "agent to run: claude, codex, or opencode (default: default_agent config)")
	fs.StringVar(&o.account, "account", "", "Claude account email to run as (default: chosen by account_strategy config)")
	fs.BoolVar(&o.noSession, "no-session", false, "create the worktree only, print its path, and start no session")
	fs.StringVar(&o.prompt, "prompt", "", "first message for the agent, which it starts working on (use - to read stdin)")
	fs.StringVar(&o.prompt, "p", "", "shorthand for -prompt")
	fs.StringVar(&o.ticket, "ticket", "", "ticket to materialize into the worktree, e.g. BRZ-3182 (names the branch when none is given)")
	fs.StringVar(&o.ticket, "t", "", "shorthand for -ticket")
	fs.BoolVar(&o.noTicketStart, "no-ticket-start", false, "don't move the issue to its first started state")
	fs.StringVar(&o.model, "model", "", "model to launch on, e.g. opus or anthropic/claude-sonnet-5 (default: the agent's own)")
	fs.StringVar(&o.effort, "effort", "", "reasoning effort to launch at, e.g. high or xhigh (default: the agent's own)")
	return fs
}

// printWorktreeUsage writes the usage line and flag defaults to w.
func printWorktreeUsage(w io.Writer) {
	fmt.Fprintln(w, worktreeUsage)
	var discard worktreeOpts
	fs := worktreeFlagSet(&discard)
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// parseWorktreeArgs parses and validates the `fleet worktree` flags. Kept pure
// (no git, no config, no filesystem) so the argument rules are testable.
func parseWorktreeArgs(args []string) (worktreeOpts, error) {
	var o worktreeOpts
	fs := worktreeFlagSet(&o)
	// Parse in a loop, peeling off one positional at a time: Go's flag package
	// stops at the first non-flag argument, so a single Parse would reject
	// `fleet worktree my-branch --no-session` — the order most people type.
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			break
		}
		positional = append(positional, remaining[0])
		rest = remaining[1:]
	}

	// Which flags were actually given. fs.Visit is the only thing separating
	// `-ticket ''` from `-ticket` not given: both leave the value empty.
	var promptSet, ticketSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "prompt", "p":
			promptSet = true
		case "ticket", "t":
			ticketSet = true
		}
	})
	o.ticket = strings.ToUpper(strings.TrimSpace(o.ticket))

	// Checked before the missing-branch case: `-ticket "$(lookup)"` that
	// produced nothing would otherwise report "missing branch name", which
	// describes a symptom of the real problem rather than the problem.
	if ticketSet && o.ticket == "" {
		return o, fmt.Errorf("-ticket was empty")
	}
	if len(positional) == 0 && o.ticket == "" {
		return o, errMissingBranch
	}
	if len(positional) > 1 {
		return o, fmt.Errorf("unexpected argument %q — expected a single branch name", positional[1])
	}
	if len(positional) == 1 {
		o.branch = strings.TrimSpace(positional[0])
		if msg := workspace.ValidateBranchName(o.branch); msg != "" {
			return o, fmt.Errorf("%s", msg)
		}
	}
	// An explicitly empty prompt is almost always a command substitution that
	// failed — `-p "$(gh issue view 999)"` on a missing issue. Silently starting
	// a session with no prompt would look like the flag isn't wired up, so say so.
	if promptSet {
		if strings.TrimSpace(o.prompt) == "" {
			return o, fmt.Errorf("-prompt was empty")
		}
		if o.noSession {
			return o, fmt.Errorf("-prompt has no effect with -no-session (no session is started)")
		}
	}
	o.prompt = strings.TrimSpace(o.prompt)

	if ticketSet {
		if !ticketIDRe.MatchString(o.ticket) {
			return o, fmt.Errorf("not a ticket identifier: %q — expected something like BRZ-3182", o.ticket)
		}
		// Both set the agent's first message, and they say opposite things:
		// -prompt means "start working on this", -ticket means "read this and
		// do not start". Concatenating them yields an agent that does neither.
		if promptSet {
			return o, fmt.Errorf("-prompt and -ticket both set the agent's first message, and they " +
				"say opposite things (-ticket tells the agent not to start working yet) — pick one")
		}
	} else if o.noTicketStart {
		return o, fmt.Errorf("-no-ticket-start has no effect without -ticket")
	}
	// Note -ticket IS allowed with -no-session, unlike -prompt: a prompt with no
	// session is meaningless, but a materialized, git-excluded ticket directory
	// is useful on its own.

	// agent.Parse falls back to Claude for anything it doesn't recognize, so a
	// typo would silently launch the wrong agent. Reject it here instead.
	if o.agentName != "" {
		switch agent.Type(o.agentName) {
		case agent.Claude, agent.Codex, agent.OpenCode:
		default:
			return o, fmt.Errorf("unknown agent %q — expected claude, codex, or opencode", o.agentName)
		}
		if o.noSession {
			return o, fmt.Errorf("--agent has no effect with --no-session (no session is started)")
		}
	}
	if o.account != "" {
		if o.noSession {
			return o, fmt.Errorf("--account has no effect with --no-session (no session is started)")
		}
		// The account token is a claude.ai credential; the other agents
		// never read it, so accepting the flag here would silently do nothing.
		if o.agentName != "" && agent.Type(o.agentName) != agent.Claude {
			return o, fmt.Errorf("--account only applies to claude sessions (got --agent %s)", o.agentName)
		}
	}
	// Same rule the other session-shaping flags follow: nothing is launched, so
	// a flag that only shapes a launch could never have applied.
	if o.noSession {
		if o.model != "" {
			return o, fmt.Errorf("--model has no effect with --no-session (no session is started)")
		}
		if o.effort != "" {
			return o, fmt.Errorf("--effort has no effect with --no-session (no session is started)")
		}
	}
	if err := validateLaunchOverrides(o.model, o.effort, o.agentName); err != nil {
		return o, err
	}
	return o, nil
}

func runWorktree(args []string) {
	opts, err := parseWorktreeArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printWorktreeUsage(os.Stdout) // `-h` is a request, not a failure
			return
		}
		fmt.Fprintln(os.Stderr, err)
		// A bad flag or a missing branch is a usage error — show the flags. A
		// bad *value* (branch name, agent) isn't: the message already says what
		// to fix, and the flag list would bury it.
		if errors.Is(err, errMissingBranch) || strings.HasPrefix(err.Error(), "flag ") {
			printWorktreeUsage(os.Stderr)
		}
		os.Exit(1)
	}

	// Resolve `-p -` before anything is created: a pipe that produced nothing
	// should fail while the worktree still doesn't exist.
	prompt, err := readStdinArg(opts.prompt, os.Stdin, "prompt")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Migrate a legacy `brizz-code` config dir before anything below creates
	// `~/.config/fleet/` — debuglog.Init does, and session.Open creates
	// state.db. Once state.db exists, migrateConfigDir permanently bails out
	// ("both dirs have state.db") and still writes its marker, silently
	// stranding the user's sessions, pins and slot bindings. runTUI does this
	// for the same reason and in the same order.
	migration.Run()

	// Both the workspace provider and Session.Start log at Info level, and
	// debuglog's fallback logger writes to stderr — without Init those lines
	// land on the user's terminal instead of ~/.config/fleet/debug.log.
	debuglog.Init()
	defer debuglog.Close()

	if err := tmux.IsTmuxAvailable(); err != nil && !opts.noSession {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	repoPath, err := resolveWorktreeRepo(opts.repoPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	base := opts.base
	if base == "" {
		base = git.GetDefaultBranch(repoPath)
	}

	cfg := config.Load()
	ag := resolveLaunchAgent(cfg, opts.agentName, !opts.noSession)

	// Resolve the Claude account before doing any work. A typo here bills the
	// wrong subscription, so an unknown name is an error rather than a silent
	// fallback — the same rule --agent follows, and for a costlier reason.
	accounts := claudeaccount.Load()
	session.SetAccountConfigDirFunc(accounts.ConfigDirFor)
	// Nothing to resolve under --no-session, and nothing to guard either:
	// parseWorktreeArgs already rejects --account, --agent, --model and --effort
	// alongside it, so this can only be reached with all four unset.
	account := ""
	if !opts.noSession {
		guardEffortSupported(ag, opts.effort)
		account = resolveLaunchAccount(cfg, accounts, repoPath, ag, opts.account)
	}

	// Phase A: when -ticket named no branch, the fetch is required to name one,
	// so it may fail hard — and it does so while nothing has been created yet,
	// the same line `-p -` already draws. With an explicit branch this is
	// skipped and any later ticket failure is soft.
	var tkt *ticket.Ticket
	if opts.ticket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		// Warm first: this is a one-shot process, so nothing has read the
		// keychain yet and every provider would report itself unavailable.
		ticketing.Warm(ctx)
		t, ferr := ticketing.Fetch(ctx, repoPath, opts.ticket)
		cancel()
		switch {
		case errors.Is(ferr, ticket.ErrNotConnected):
			// No provider will read this key here, which is a configuration
			// answer rather than a network one — and worth its own words, since
			// the bare sentinel ("not connected") sends someone to re-paste a
			// credential that may already be fine.
			//
			// Which configuration, though, depends on WHICH gate failed. A repo
			// that already names the project needs a credential, not a config
			// edit, and telling someone to add a line they can see is already
			// there is a false statement about their own setup.
			//
			// Fatal only when the branch depended on the fetch. With an explicit
			// branch this is the same soft failure as any other: the worktree is
			// what was asked for, and the ticket was going to enrich it.
			fmt.Fprintln(os.Stderr, unclaimedTicketAdvice(repoPath, opts.ticket))
			if opts.branch == "" {
				os.Exit(1)
			}
		case ferr != nil && opts.branch == "":
			fmt.Fprintf(os.Stderr, "Couldn't read %s: %v\n", opts.ticket, ferr)
			os.Exit(1)
		case ferr != nil:
			fmt.Fprintf(os.Stderr, "Couldn't read %s: %v — creating the worktree anyway.\n", opts.ticket, ferr)
		default:
			tkt = &t
			fmt.Fprintf(os.Stderr, "Fetched %s — %s\n", t.Identifier, t.Title)
			if opts.branch == "" {
				opts.branch = ticket.BranchNameFor(t.Identifier, t.Title)
				if msg := workspace.ValidateBranchName(opts.branch); msg != "" {
					fmt.Fprintf(os.Stderr, "Derived branch %q is not valid: %s\n", opts.branch, msg)
					os.Exit(1)
				}
			}
		}
		if opts.branch == "" {
			opts.branch = strings.ToLower(opts.ticket)
		}
	}

	name := workspace.SanitizeBranchName(opts.branch)
	provider := workspace.ResolveProvider(repoPath)
	if !provider.CanCreate() {
		fmt.Fprintf(os.Stderr, "This repo's workspace provider can't create worktrees (no create command in .fleet.json)\n")
		os.Exit(1)
	}

	// GitWorktreeProvider.Create retries without `-b` when the branch already
	// exists, which silently drops --base too. In the TUI you'd have seen the
	// branch in the existing-worktrees list first; from a shell there's no
	// signal at all, and "Created … (branch X)" reads as "made X off your base".
	// Say it up front instead. Git provider only — a ShellProvider defines its
	// own branch semantics.
	reusedBranch := !provider.IsCustom() && branchExists(repoPath, opts.branch)
	if reusedBranch {
		fmt.Fprintf(os.Stderr, "Branch %q already exists — reusing it.\n", opts.branch)
		if opts.base != "" {
			fmt.Fprintf(os.Stderr, "--base %s ignored: the branch already has a base.\n", opts.base)
		}
	}

	// Open the DB before anything touches the disk: a database that can't be
	// opened at all should fail before a worktree exists, not after.
	var storage *session.StateDB
	if !opts.noSession {
		storage, err = session.Open(session.DefaultDBPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
			os.Exit(1)
		}
		defer storage.Close()
	}

	info, err := provider.Create(repoPath, name, opts.branch, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create worktree: %v\n", err)
		os.Exit(1)
	}
	if info == nil || info.Path == "" {
		fmt.Fprintln(os.Stderr, "Worktree provider returned no path")
		os.Exit(1)
	}

	if cfg.IsCopyClaudeSettingsEnabled() && !provider.IsCustom() {
		copyClaudeSettings(repoPath, info.Path)
	}
	workspace.CopyConfiguredFiles(repoPath, info.Path)

	// Phase B: past this point the worktree exists, so nothing may exit
	// non-zero — same contract as the two file copies above.
	if tkt != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		res, merr := ticketing.Materialize(ctx, repoPath, ticket.Opts{
			WorktreePath: info.Path,
			Identifier:   tkt.Identifier,
			Provider:     tkt.Provider,
			MoveState:    cfg.IsTicketStartStateEnabled() && !opts.noTicketStart,
		})
		cancel()
		if merr != nil {
			fmt.Fprintf(os.Stderr, "Couldn't materialize %s: %v\n", tkt.Identifier, merr)
		} else {
			fmt.Fprintf(os.Stderr, "Wrote %s (%s)\n", res.RelDir, describeTicketFiles(res))
			if res.StateMoved != "" {
				fmt.Fprintf(os.Stderr, "Moved %s to %s\n", res.Identifier, res.StateMoved)
			}
			if prompt == "" {
				prompt = res.Prompt
			}
		}
	}

	// --no-session prints the path and nothing else, so the command composes:
	// cd "$(fleet worktree my-branch --no-session)"
	if opts.noSession {
		fmt.Println(info.Path)
		return
	}

	s := session.NewSession(name, info.Path)
	s.WorkspaceName = name
	launchSession(s, ag, launchOverrides{
		account: account,
		prompt:  prompt,
		model:   opts.model,
		effort:  opts.effort,
	}, storage, launchNotes{
		startFailPrefix: fmt.Sprintf("Created worktree %s, but ", info.Path),
		keptOnTeardown:  fmt.Sprintf(" The worktree %s was kept.", info.Path),
	})

	fmt.Printf("Created worktree %s (%s)\n", info.Path, branchNote(opts.branch, reusedBranch))
	fmt.Printf("Started %s session '%s' (%s)\n", ag.DisplayName(), name, s.ID)
	// Echo the prompt back: with `-p -` the text came from a pipe the user never
	// saw, and this is the only confirmation that what arrived is what they meant.
	if prompt != "" {
		fmt.Printf("Working on: %s\n", summarizeText(prompt, 60))
	}
}

// resolveWorktreeRepo resolves the repo to create the worktree in: the given
// path (or the current directory), walked up to its git root and then to the
// main worktree. Basing on the main clone keeps derived worktree paths siblings
// of it — running this from inside worktree "repo-foo" otherwise yields
// "repo-foo-bar" — and matches pressing `w` on an origin header in the TUI.
func resolveWorktreeRepo(path string) (string, error) {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine current directory: %w", err)
		}
		path = cwd
	}
	path = expandPath(path)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", fmt.Errorf("invalid directory: %s", path)
	}
	root := repoRootOf(path)
	if root == "" {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	return git.GetMainWorktreePath(root), nil
}

// repoRootOf returns path's git repo root, or "" when path isn't inside a repo.
// session.GetRepoRoot can't be used here: it returns the input unchanged when
// git fails, which is indistinguishable from "path is already the root" — so a
// non-repo would silently proceed into `git worktree add`.
func repoRootOf(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// branchNote describes what happened to the branch, so the success line can't
// imply a fresh branch off --base when an existing one was checked out.
func branchNote(branch string, reused bool) string {
	if reused {
		return fmt.Sprintf("existing branch %s", branch)
	}
	return fmt.Sprintf("branch %s", branch)
}

// branchExists reports whether repoPath already has a local branch named
// branch. Used to warn that the worktree reuses it and that --base is moot,
// since GitWorktreeProvider.Create silently falls back to the existing branch.
func branchExists(repoPath, branch string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "git", "-C", repoPath,
		"show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	return err == nil
}

// copyClaudeSettings copies .claude/settings.local.json from srcRepo to dstRepo.
// Mirrors the TUI's copyClaudeSettingsFile, which lives in package ui.
func copyClaudeSettings(srcRepo, dstRepo string) {
	data, err := os.ReadFile(filepath.Join(srcRepo, ".claude", "settings.local.json"))
	if err != nil {
		return // source doesn't exist, nothing to copy
	}
	dstDir := filepath.Join(dstRepo, ".claude")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		debuglog.Logger.Error("copyClaudeSettings: failed to create .claude dir", "dst", dstDir, "err", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dstDir, "settings.local.json"), data, 0600); err != nil {
		debuglog.Logger.Error("copyClaudeSettings: failed to write settings file", "dst", dstRepo, "err", err)
	}
}

// originExpandKey mirrors the TUI's key space for per-origin config
// (`origin:<key>`), so allowed_accounts means the same thing from the CLI.
func originExpandKey(originKey string) string {
	if originKey == "" {
		return ""
	}
	return "origin:" + originKey
}

// accountAllowed reports whether email passes the origin's allowlist. An empty
// allowlist means unrestricted, matching claudeaccount.Select.
func accountAllowed(email string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, email)
}

// unclaimedTicketAdvice explains why no provider would read this identifier,
// naming the gate that actually failed.
func unclaimedTicketAdvice(repoPath, id string) string {
	key, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(id)), "-")

	if p, ok := ticketing.ClaimedBy(repoPath, id); ok {
		// The repo names this key; the tracker just is not connected.
		return fmt.Sprintf("This repo tracks %s in %s, but %s isn't connected.\n"+
			"Connect it in the TUI (Ctrl+K → Connect %s), or set its environment variables.",
			key, p.Name(), p.Name(), p.Name())
	}
	// Both forms, because this branch fires when NO provider claims the key —
	// so the reader is as likely to be a Linear user as a Jira one, and this
	// line is the only thing telling them what to write. A single Jira example
	// invites a Linear user to put a team key under a "jira" block, which then
	// fails a second time and less legibly.
	return fmt.Sprintf("No tracker in this repo claims %s.\n"+
		"Name its Linear team or Jira project in .fleet.local.json, e.g.\n"+
		"  {\"linear\": {\"team\": %q}}   or   {\"jira\": {\"project\": %q}}", id, key, key)
}

// describeTicketFiles summarizes what landed on disk, so the echo-back is
// specific rather than a bare "wrote it".
func describeTicketFiles(r ticket.Result) string {
	if r.Images == 0 {
		return "ticket.md, no images"
	}
	return fmt.Sprintf("ticket.md + %d image(s)", r.Images)
}
