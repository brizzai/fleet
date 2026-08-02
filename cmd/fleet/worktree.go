package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/hooks"
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
	noSession bool
}

// parseWorktreeArgs parses and validates the `fleet worktree` flags. Kept pure
// (no git, no config, no filesystem) so the argument rules are testable.
func parseWorktreeArgs(args []string) (worktreeOpts, error) {
	fs := flag.NewFlagSet("fleet worktree", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), worktreeUsage)
		fs.PrintDefaults()
	}
	var o worktreeOpts
	fs.StringVar(&o.base, "base", "", "base branch to branch from (default: the repo's default branch)")
	fs.StringVar(&o.repoPath, "path", "", "repo to create the worktree in (default: current directory)")
	fs.StringVar(&o.agentName, "agent", "", "agent to run: claude, codex, or opencode (default: default_agent config)")
	fs.BoolVar(&o.noSession, "no-session", false, "create the worktree only, print its path, and start no session")
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
	return o, nil
}

func runWorktree(args []string) {
	opts, err := parseWorktreeArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // flag package already printed usage
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errMissingBranch) {
			fmt.Fprintln(os.Stderr, worktreeUsage)
		}
		os.Exit(1)
	}

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

	name := workspace.SanitizeBranchName(opts.branch)
	provider := workspace.ResolveProvider(repoPath)
	if !provider.CanCreate() {
		fmt.Fprintf(os.Stderr, "This repo's workspace provider can't create worktrees (no create command in .fleet.json)\n")
		os.Exit(1)
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

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	s := session.NewSession(name, info.Path)
	s.Agent = ag
	s.WorkspaceName = name
	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Created worktree %s, but failed to start session: %v\n", info.Path, err)
		os.Exit(1)
	}
	if err := storage.SaveSession(s.ToRow()); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save session: %v\n", err)
		os.Exit(1)
	}
	// Pin the new checkout so it shows in the sidebar even before it has a
	// running session, mirroring what the TUI does on session create.
	if err := storage.PinRepo(session.GetRepoRoot(info.Path)); err != nil {
		debuglog.Logger.Error("failed to pin repo", "repo", info.Path, "err", err)
	}

	fmt.Printf("Created worktree %s (branch %s)\n", info.Path, opts.branch)
	fmt.Printf("Started %s session '%s' (%s)\n", ag.DisplayName(), name, s.ID)
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
