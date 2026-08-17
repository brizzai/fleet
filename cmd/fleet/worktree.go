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
	"slices"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/workspace"
)

const worktreeUsage = "Usage: fleet worktree <branch> [flags]"

// errMissingBranch is returned when no branch was given. runWorktree prints the
// usage line alongside it, so the message itself stays a plain error string.
var errMissingBranch = errors.New("missing branch name")

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

	if len(positional) == 0 {
		return o, errMissingBranch
	}
	if len(positional) > 1 {
		return o, fmt.Errorf("unexpected argument %q — expected a single branch name", positional[1])
	}
	o.branch = strings.TrimSpace(positional[0])

	if msg := workspace.ValidateBranchName(o.branch); msg != "" {
		return o, fmt.Errorf("%s", msg)
	}
	// An explicitly empty prompt is almost always a command substitution that
	// failed — `-p "$(gh issue view 999)"` on a missing issue. Silently starting
	// a session with no prompt would look like the flag isn't wired up, so say
	// so. fs.Visit is what separates "-p ''" from "-p not given": both leave the
	// value empty.
	promptSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt" || f.Name == "p" {
			promptSet = true
		}
	})
	if promptSet {
		if strings.TrimSpace(o.prompt) == "" {
			return o, fmt.Errorf("-prompt was empty")
		}
		if o.noSession {
			return o, fmt.Errorf("-prompt has no effect with -no-session (no session is started)")
		}
	}
	o.prompt = strings.TrimSpace(o.prompt)
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
	ag := agent.Parse(cfg.GetDefaultAgent())
	if opts.agentName != "" {
		ag = agent.Parse(opts.agentName)
	}
	if !opts.noSession {
		if _, err := exec.LookPath(ag.Binary()); err != nil {
			fmt.Fprintf(os.Stderr, "%s CLI not found — install %s to create sessions\n", ag.Binary(), ag.DisplayName())
			os.Exit(1)
		}
	}

	// Resolve the Claude account before doing any work. A typo here bills the
	// wrong subscription, so an unknown name is an error rather than a silent
	// fallback — the same rule --agent follows, and for a costlier reason.
	accounts := claudeaccount.Load()
	session.SetAccountConfigDirFunc(accounts.ConfigDirFor)
	account := ""
	// Re-checked here, not only at parse time: the parse-time guard can compare
	// --account against --agent only when --agent was given. With default_agent
	// set to codex or opencode, an explicit --account would otherwise be dropped
	// on the floor and the session created as the wrong agent — the silent typo
	// the explicit validation exists to prevent.
	if opts.account != "" && ag != agent.Claude {
		fmt.Fprintf(os.Stderr, "--account only applies to claude sessions (default_agent is %s)\n", ag)
		os.Exit(1)
	}
	if !opts.noSession && ag == agent.Claude {
		// The same per-origin allowlist the TUI enforces. Without it the policy
		// held in one surface and not the other, which is worse than not having
		// one — including for an explicit --account, which could name an account
		// the origin disallows.
		allowed := cfg.GetAllowedAccounts(originExpandKey(git.GetOriginKey(repoPath)))
		if opts.account != "" {
			if _, ok := accounts.Get(opts.account); !ok {
				fmt.Fprintf(os.Stderr, "unknown account %q — configure it in fleet first\n", opts.account)
				os.Exit(1)
			}
			if !accountAllowed(opts.account, allowed) {
				fmt.Fprintf(os.Stderr, "account %q is not in allowed_accounts for this origin\n", opts.account)
				os.Exit(1)
			}
			account = opts.account
		} else if acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
			Accounts: accounts.List(),
			Strategy: cfg.GetAccountStrategy(),
			Manual:   cfg.DefaultAccount,
			Allowed:  allowed,
		}); ok {
			account = acct.Email
			// Quota lives only in a running TUI's memory, so SelectOpts.Usage is
			// empty here and least_used degrades to configured order. Said out
			// loud — on stderr, not only in debug.log, since the person running a
			// scripted `fleet worktree` never opens that: it can otherwise land on
			// a nearly spent account with nothing to indicate the strategy didn't
			// apply.
			if cfg.GetAccountStrategy() == claudeaccount.StrategyLeastUsed && accounts.Len() > 1 {
				fmt.Fprintf(os.Stderr, "Note: quota isn't available outside the TUI, so configured order chose %s.\n", account)
				debuglog.Logger.Info("account select: no quota available from the CLI, configured order decided",
					"chosen", account, "strategy", cfg.GetAccountStrategy())
			}
		} else if accounts.Len() > 0 {
			// Select declined, and the two reasons it can decline want opposite
			// answers — see claudeaccount.AllowedConfigured.
			if !claudeaccount.AllowedConfigured(accounts.List(), allowed) {
				fmt.Fprintf(os.Stderr, "allowed_accounts for this origin names no account fleet knows about (%s)\n",
					strings.Join(allowed, ", "))
				fmt.Fprintln(os.Stderr, "add one of them, or drop the restriction — launching would bill whichever account you happen to be logged into")
				os.Exit(1)
			}
			// Every allowed account is logged out. Falling back to the ambient
			// login is deliberate (see dropLoggedOut) — but saying nothing about
			// it is not: the session is about to run as somebody fleet did not
			// choose.
			fmt.Fprintln(os.Stderr, "Note: every account allowed here is logged out — starting on your ambient Claude login.")
			debuglog.Logger.Warn("account select: all allowed accounts logged out, using the ambient login",
				"allowed", allowed, "configured", accounts.Len())
		}
		if account != "" {
			if conflict := claudeaccount.GuardConflictingAuth(); !conflict.Empty() {
				fmt.Fprintln(os.Stderr, conflict.Message(account))
				// Only an env var is fatal — see AuthConflict.
				if conflict.Fatal {
					os.Exit(1)
				}
			}
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

	// --no-session prints the path and nothing else, so the command composes:
	// cd "$(fleet worktree my-branch --no-session)"
	if opts.noSession {
		fmt.Println(info.Path)
		return
	}

	// A CLI-created session needs the agent's hooks installed for status
	// detection, which normally only happens on TUI launch. Only the chosen
	// agent's hooks are touched — never create a config dir for an agent that
	// isn't being launched.
	installAgentHooks(ag, info.Path)

	s := session.NewSession(name, info.Path)
	s.Agent = ag
	s.Account = account
	s.WorkspaceName = name
	s.InitialPrompt = prompt
	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Created worktree %s, but failed to start session: %v\n", info.Path, err)
		os.Exit(1)
	}
	if err := storage.SaveSession(s.ToRow()); err != nil {
		// The tmux session is already live but nothing will ever point at it —
		// no DB row means the TUI can't list it, adopt it, or offer to delete
		// it. Tear it down so the failure is clean; if that also fails, name it
		// so the user can find it with `tmux ls`.
		fmt.Fprintf(os.Stderr, "Failed to save session: %v\n", err)
		if killErr := s.GetTmuxSession().Kill(); killErr != nil {
			fmt.Fprintf(os.Stderr, "Also failed to stop its tmux session %q: %v\n", s.TmuxSessionName, killErr)
		} else {
			fmt.Fprintf(os.Stderr, "Stopped the orphaned tmux session. The worktree %s was kept.\n", info.Path)
		}
		os.Exit(1)
	}
	// Pin the new checkout so it shows in the sidebar even before it has a
	// running session, mirroring what the TUI does on session create.
	if err := storage.PinRepo(session.GetRepoRoot(info.Path)); err != nil {
		debuglog.Logger.Error("failed to pin repo", "repo", info.Path, "err", err)
	}

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

// installAgentHooks installs the status hooks for the agent about to be
// launched. Failures are logged, not fatal: a session without hooks still runs,
// it just falls back to pane-based status detection.
func installAgentHooks(ag agent.Type, projectPath string) {
	switch ag {
	case agent.Codex:
		if _, err := hooks.InjectCodexHooks(hooks.GetCodexConfigDir()); err != nil {
			debuglog.Logger.Error("codex hook inject failed", "err", err)
		}
		// Codex prompts to trust a new directory on first launch; pre-seed trust
		// so the session opens straight to the prompt.
		if err := hooks.EnsureCodexDirTrust(hooks.GetCodexConfigDir(), projectPath); err != nil {
			debuglog.Logger.Error("codex dir trust seeding failed", "path", projectPath, "err", err)
		}
	case agent.OpenCode:
		if _, err := hooks.InjectOpenCodePlugin(hooks.GetOpenCodeConfigDir()); err != nil {
			debuglog.Logger.Error("opencode plugin inject failed", "err", err)
		}
	default:
		if _, err := hooks.InjectClaudeHooks(hooks.GetClaudeConfigDir()); err != nil {
			debuglog.Logger.Error("claude hook inject failed", "err", err)
		}
	}
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
