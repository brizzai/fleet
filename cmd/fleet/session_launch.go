package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
)

// This file holds what `fleet add` and `fleet worktree` do identically once
// they know where the session goes: pick the agent, pick the Claude account,
// install the agent's hooks, launch, and record it.
//
// It exists because the two commands had drifted. `fleet add` carried its own
// shorter copy of the account logic with no explicit-account branch, no note
// when the strategy couldn't rank, and GuardConflictingAuth called from inside
// the wrong branch — a second, weaker implementation of a policy whose whole
// point is that a mistake bills the wrong subscription. Account policy now has
// one implementation and both commands call it.
//
// These helpers exit the process on failure rather than returning an error.
// That is the existing shape of the code they were lifted from, kept so the
// move reads as a move; the trade-off is that the policy stays untestable and
// only review catches a change to it.

// resolveLaunchAgent picks the agent to launch: the --agent flag when given,
// otherwise the default_agent config. When needBinary is set (anything that
// actually starts a session), the binary must be on PATH.
//
// agent.Parse falls back to Claude for anything it doesn't recognize, so an
// unrecognized name must already have been rejected at parse time — this only
// resolves, it does not validate.
func resolveLaunchAgent(cfg *config.Config, explicit string, needBinary bool) agent.Type {
	ag := agent.Parse(cfg.GetDefaultAgent())
	if explicit != "" {
		ag = agent.Parse(explicit)
	}
	if needBinary {
		if _, err := exec.LookPath(ag.Binary()); err != nil {
			fmt.Fprintf(os.Stderr, "%s CLI not found — install %s to create sessions\n", ag.Binary(), ag.DisplayName())
			os.Exit(1)
		}
	}
	return ag
}

// resolveLaunchAccount picks the Claude account the session authenticates as,
// for a session about to be started at repoPath.
//
// Returns "" for a non-Claude agent (the account is a claude.ai credential the
// others never read) and for a Claude session that should run on the ambient
// login.
func resolveLaunchAccount(cfg *config.Config, accounts *claudeaccount.Store, repoPath string, ag agent.Type, explicit string) string {
	// Re-checked here, not only at parse time: the parse-time guard can compare
	// --account against --agent only when --agent was given. With default_agent
	// set to codex or opencode, an explicit --account would otherwise be dropped
	// on the floor and the session created as the wrong agent — the silent typo
	// the explicit validation exists to prevent.
	if explicit != "" && ag != agent.Claude {
		fmt.Fprintf(os.Stderr, "--account only applies to claude sessions (default_agent is %s)\n", ag)
		os.Exit(1)
	}
	if ag != agent.Claude {
		return ""
	}

	account := ""
	// The same per-origin allowlist the TUI enforces. Without it the policy
	// held in one surface and not the other, which is worse than not having
	// one — including for an explicit --account, which could name an account
	// the origin disallows.
	allowed := cfg.GetAllowedAccounts(originExpandKey(git.GetOriginKey(repoPath)))
	if explicit != "" {
		if _, ok := accounts.Get(explicit); !ok {
			fmt.Fprintf(os.Stderr, "unknown account %q — configure it in fleet first\n", explicit)
			os.Exit(1)
		}
		if !accountAllowed(explicit, allowed) {
			fmt.Fprintf(os.Stderr, "account %q is not in allowed_accounts for this origin\n", explicit)
			os.Exit(1)
		}
		account = explicit
	} else if acct, ok, notes := selectLaunchAccount(cfg, accounts.List(), allowed, readLaunchUsage); ok {
		account = acct.Email
		// On stderr, not only in debug.log: whoever runs a scripted
		// `fleet worktree` never opens that, and a strategy that couldn't rank
		// can land on a nearly spent account.
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, n)
		}
		debuglog.Logger.Info("account select", "chosen", account,
			"strategy", cfg.GetAccountStrategy(), "notes", notes)
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
	return account
}

// guardEffortSupported refuses --effort for an agent whose launch command has
// no way to carry it (see agent.SupportsEffort).
//
// Re-checked here, not only at parse time, for the same reason --account is:
// the parse-time guard can compare --effort against --agent only when --agent
// was given. With default_agent set to opencode, an explicit --effort would
// otherwise reach BuildLaunchCmd, be dropped there, and the session would start
// silently ignoring the flag.
func guardEffortSupported(ag agent.Type, effort string) {
	if effort != "" && !ag.SupportsEffort() {
		fmt.Fprintf(os.Stderr, "--effort has no effect on %s sessions — it can only be set inside the agent\n", ag)
		os.Exit(1)
	}
	if effort != "" && !ag.ValidEffort(effort) {
		fmt.Fprintf(os.Stderr, "--effort %q isn't accepted by %s — expected one of: %s\n", effort, ag.DisplayName(), ag.EffortChoices())
		os.Exit(1)
	}
}

// usageReader produces the quota readings Select ranks on, plus the accounts
// it couldn't read.
type usageReader func(accounts []claudeaccount.Account) (map[string]claudeaccount.Usage, []claudeaccount.Unread)

// readLaunchUsage is cliUsageReader; tests replace it.
var readLaunchUsage usageReader = cliUsageReader

// cliUsageReader reads quota the way the TUI's poll does, through the shared
// cache: a reading a TUI or an earlier CLI run took within the poll floor is
// reused, otherwise it is fetched under claudeaccount.CLIFetchTimeout.
func cliUsageReader(accounts []claudeaccount.Account) (map[string]claudeaccount.Usage, []claudeaccount.Unread) {
	claudeaccount.SetUserAgent(version)
	return claudeaccount.UsageForLaunch(context.Background(), claudeaccount.LaunchUsageOpts{
		Accounts:  accounts,
		CachePath: claudeaccount.UsageCachePath(),
	})
}

// selectLaunchAccount runs account_strategy for a session with no --account:
// the same claudeaccount.Select the TUI calls, fed the same quota readings.
//
// Quota is read only when the strategy ranks by it and there is a choice to
// make; waterfall and manual pick as they always have. Unreadable quota never
// blocks a launch — Select scores those accounts as unknown, which with
// nothing readable is configured order — and each note says why.
func selectLaunchAccount(cfg *config.Config, all []claudeaccount.Account, allowed []string, read usageReader) (claudeaccount.Account, bool, []string) {
	strategy := cfg.GetAccountStrategy()
	var usage map[string]claudeaccount.Usage
	var unread []claudeaccount.Unread
	candidates := 0
	for _, a := range all {
		if accountAllowed(a.Email, allowed) {
			candidates++
		}
	}
	ranks := claudeaccount.RanksByUsage(strategy) && candidates > 1
	if ranks {
		usage, unread = read(all)
	}
	acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
		Accounts: all,
		Usage:    usage,
		Strategy: strategy,
		Manual:   cfg.DefaultAccount,
		Allowed:  allowed,
	})
	if !ok || !ranks {
		return acct, ok, nil
	}

	var unknown, skipped []string
	for _, u := range unread {
		switch {
		case !accountAllowed(u.Email, allowed):
		case u.LoggedOut:
			skipped = append(skipped, u.Email)
		default:
			unknown = append(unknown, u.Email+": "+u.Reason)
		}
	}
	var notes []string
	if len(unknown)+len(skipped) >= candidates {
		notes = append(notes, fmt.Sprintf("Note: couldn't read quota (%s), so configured order chose %s.",
			strings.Join(append(unknown, notLoggedIn(skipped)...), "; "), acct.Email))
		return acct, true, notes
	}
	notes = append(notes, fmt.Sprintf("Account: %s (%s).", acct.Email, claudeaccount.StrategyLabel(strategy)))
	if len(unknown) > 0 {
		notes = append(notes, fmt.Sprintf("Note: couldn't read quota for %s; ranked as half used.", strings.Join(unknown, "; ")))
	}
	if len(skipped) > 0 {
		notes = append(notes, fmt.Sprintf("Note: skipped %s.", strings.Join(notLoggedIn(skipped), "; ")))
	}
	return acct, true, notes
}

func notLoggedIn(emails []string) []string {
	out := make([]string, len(emails))
	for i, e := range emails {
		out[i] = e + ": not logged in"
	}
	return out
}

// launchOverrides carries the per-launch choices a command hands to the agent.
type launchOverrides struct {
	account string
	prompt  string
	model   string
	effort  string
}

// launchNotes carries what a caller has already created and is keeping, so a
// failure names what survived rather than leaving the user to guess. Both are
// empty for `fleet add`, which creates nothing before the session.
type launchNotes struct {
	// startFailPrefix leads the "failed to start session" line.
	startFailPrefix string
	// keptOnTeardown is appended when a failed save tears the tmux session down.
	keptOnTeardown string
}

// launchSession installs the agent's hooks, starts the session, and records it.
//
// A failed SaveSession tears the tmux session down: the pane is already live
// but nothing would ever point at it — the TUI can't list it, adopt it, or
// offer to delete it.
func launchSession(s *session.Session, ag agent.Type, o launchOverrides, storage *session.StateDB, notes launchNotes) {
	// A CLI-created session needs the agent's hooks installed for status
	// detection, which normally only happens on TUI launch. Only the chosen
	// agent's hooks are touched — never create a config dir for an agent that
	// isn't being launched.
	installAgentHooks(ag, s.ProjectPath)

	s.Agent = ag
	s.Account = o.account
	s.InitialPrompt = o.prompt
	s.Model = o.model
	s.Effort = o.effort

	if err := s.Start(); err != nil {
		// The prefix, when there is one, is a clause the failure continues
		// ("Created worktree X, but failed to start session"), so the capital
		// belongs to whichever of the two comes first.
		if notes.startFailPrefix == "" {
			fmt.Fprintf(os.Stderr, "Failed to start session: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "%sfailed to start session: %v\n", notes.startFailPrefix, err)
		}
		os.Exit(1)
	}
	if err := storage.SaveSession(s.ToRow()); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save session: %v\n", err)
		if killErr := s.GetTmuxSession().Kill(); killErr != nil {
			// Name it so the user can still find it with `tmux ls`.
			fmt.Fprintf(os.Stderr, "Also failed to stop its tmux session %q: %v\n", s.TmuxSessionName, killErr)
		} else {
			fmt.Fprintf(os.Stderr, "Stopped the orphaned tmux session.%s\n", notes.keptOnTeardown)
		}
		os.Exit(1)
	}
	// Pin the repo so it shows in the sidebar even before it has a running
	// session, mirroring what the TUI does on session create.
	if err := storage.PinRepo(session.GetRepoRoot(s.ProjectPath)); err != nil {
		debuglog.Logger.Error("failed to pin repo", "repo", s.ProjectPath, "err", err)
	}
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
	case agent.Copilot:
		if _, err := hooks.InjectCopilotHooks(hooks.GetCopilotConfigDir()); err != nil {
			debuglog.Logger.Error("copilot hook inject failed", "err", err)
		}
		// Without trust the pane opens on a folder-trust menu and no hook fires.
		if err := hooks.EnsureCopilotDirTrust(hooks.GetCopilotConfigDir(), projectPath); err != nil {
			debuglog.Logger.Error("copilot dir trust seeding failed", "path", projectPath, "err", err)
		}
	default:
		if _, err := hooks.InjectClaudeHooks(hooks.GetClaudeConfigDir()); err != nil {
			debuglog.Logger.Error("claude hook inject failed", "err", err)
		}
	}
}
