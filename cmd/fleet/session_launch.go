package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
		if claudeaccount.RanksByUsage(cfg.GetAccountStrategy()) && accounts.Len() > 1 {
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

// launchOverrides carries the per-launch choices a command hands to the agent.
type launchOverrides struct {
	account string
	prompt  string
	model   string
	effort  string
	group   groupFlags
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
//
// It returns the line describing which sidebar group the session joined ("" =
// ungrouped), for the caller to print after its own confirmation.
func launchSession(s *session.Session, ag agent.Type, o launchOverrides, storage *session.StateDB, notes launchNotes) string {
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
	// Resolved only once the session is running: inheriting a group can move
	// the *parent* into a fresh one, and a launch that failed would leave the
	// parent in a group with nothing it spawned.
	groupID, groupNote := resolveLaunchGroup(storage, o.group, os.Getenv(session.InstanceIDEnvVar))
	s.SetGroupID(groupID)
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
	return groupNote
}

// groupFlags is the parsed --group / --no-group pair, shared by `fleet add` and
// `fleet worktree`.
type groupFlags struct {
	// name is --group: join the group with exactly this name, or create it.
	name string
	// none is --no-group: launch ungrouped even when run from inside a session.
	none bool
}

// registerGroupFlags binds --group and --no-group into g.
func registerGroupFlags(fs *flag.FlagSet, g *groupFlags) {
	fs.StringVar(&g.name, "group", "", "sidebar group to put the session in, joined by name or created (default: the group of the fleet session running this command)")
	fs.BoolVar(&g.none, "no-group", false, "don't group the session with the fleet session running this command")
}

// validateGroupFlags rejects the combinations that can't mean anything. An
// explicitly empty --group is refused rather than read as "no group": it is
// almost always a substitution that expanded to nothing, and --no-group says
// that on purpose.
func validateGroupFlags(fs *flag.FlagSet, g *groupFlags) error {
	var nameSet bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "group" {
			nameSet = true
		}
	})
	g.name = strings.TrimSpace(g.name)
	if nameSet && g.name == "" {
		return fmt.Errorf("-group was empty (use --no-group to launch ungrouped)")
	}
	if g.name != "" && g.none {
		return fmt.Errorf("--group and --no-group conflict")
	}
	return nil
}

// resolveLaunchGroup decides which sidebar group a CLI-launched session joins,
// returning the group id ("" = ungrouped) and a line to print, if any.
//
// The default is inheritance: every fleet pane exports its session id, so a
// session that runs `fleet wt` through the skill is the parent of what it
// creates. The child joins the parent's group — or, when the parent has none,
// a new group led by the parent, which moves the parent into it too. That is
// how "one ticket across five services" ends up as one sidebar section with no
// flag at all. Groups are flat: a grandchild joins the same group.
//
// Never fatal. The session is already running by the time this is called, and
// a grouping failure costs a sidebar section, never the session.
func resolveLaunchGroup(storage *session.StateDB, g groupFlags, parentID string) (string, string) {
	if g.none {
		return "", ""
	}
	now := time.Now()
	if g.name != "" {
		existing, err := storage.FindGroupByName(g.name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not look up group %q, launching ungrouped: %v\n", g.name, err)
			return "", ""
		}
		if existing != nil {
			return existing.ID, fmt.Sprintf("Joined group '%s'", g.name)
		}
		grp := &session.Group{ID: session.NewGroupID(), Name: g.name, CreatedAt: now}
		if err := storage.CreateGroup(grp); err != nil {
			fmt.Fprintf(os.Stderr, "Could not create group %q, launching ungrouped: %v\n", g.name, err)
			return "", ""
		}
		return grp.ID, fmt.Sprintf("Created group '%s'", g.name)
	}
	if parentID == "" {
		return "", ""
	}
	id, created, ok, err := storage.EnsureLeadGroup(parentID, now)
	switch {
	case err != nil:
		debuglog.Logger.Error("group inheritance failed; launching ungrouped", "parent", parentID, "err", err)
		return "", ""
	case !ok:
		// A stale id (the parent was deleted) or one from another state.db
		// (FLEET_DEMO_PREFIX, a second install). Nothing to group with.
		debuglog.Logger.Info("parent session not found; launching ungrouped", "parent", parentID)
		return "", ""
	case created:
		return id, "Grouped with the session that launched it (pass --no-group to opt out)"
	default:
		return id, "Joined the launching session's group (pass --no-group to opt out)"
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
