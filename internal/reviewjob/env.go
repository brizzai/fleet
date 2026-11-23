package reviewjob

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// gitTimeout bounds the small git reads here (rev-parse, log, cat-file).
const gitTimeout = 15 * time.Second

// gitOut runs `git -C dir args…` and returns trimmed stdout. Same hardening
// as internal/git's diff runner: no prompts, no opportunistic index writes,
// WaitDelay so a pipe-holding grandchild can't outlive the timeout.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, "", args...)
	return strings.TrimSpace(string(out)), err
}

func gitRaw(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

// snapshotTree is git.SnapshotTree behind a var, for tests that want to count
// or fail it.
var snapshotTree = git.SnapshotTree

// configuredModel is config.json's review_model (config.GetReviewModel),
// shape-checked.
func configuredModel() string { return ModelOrDefault(config.Load().GetReviewModel()) }

// ModelOrDefault returns m when it is shaped like a model id, else
// selfreview.DefaultModel. The model rides in the reviewer's argv, so anything
// outside agent.ValidateLaunchValue's charset is refused rather than passed.
func ModelOrDefault(m string) string {
	m = strings.TrimSpace(m)
	if m == "" {
		return selfreview.DefaultModel
	}
	if agent.ValidateLaunchValue("review_model", m) != nil {
		debuglog.Logger.Warn("review_model ignored: not a model id", "value", m)
		return selfreview.DefaultModel
	}
	return m
}

// usageForAccounts is claudeaccount.UsageForLaunch behind a var (tests).
var usageForAccounts = func(ctx context.Context, accounts []claudeaccount.Account) map[string]claudeaccount.Usage {
	usage, _ := claudeaccount.UsageForLaunch(ctx, claudeaccount.LaunchUsageOpts{
		Accounts:  accounts,
		CachePath: claudeaccount.UsageCachePath(),
	})
	return usage
}

// resolveAccount picks the account the reviewer runs as.
//
// The account of the checkout's default send target first: its owner already
// pays for the checkout's work, and the review is part of that work. Not when it is logged out (the
// call would fail) or spent (it would wait out a window). Then the same
// claudeaccount.Select a new session gets, under the origin's allowlist —
// using a disallowed account is the one wrong answer an allowlist exists to
// prevent. Nothing selectable → the ambient login ("" email, "" dir), as
// everywhere else in fleet.
func resolveAccount(ctx context.Context, c Checkout) (string, string) {
	store := claudeaccount.Load()
	if store.Len() == 0 {
		return "", ""
	}
	all := store.List()
	usage := usageForAccounts(ctx, all)
	now := time.Now()
	return pickAccount(store, all, usage, c, config.Load(), now)
}

// pickAccount is resolveAccount's decision, without the I/O.
func pickAccount(store *claudeaccount.Store, all []claudeaccount.Account, usage map[string]claudeaccount.Usage,
	c Checkout, cfg *config.Config, now time.Time) (string, string) {
	if s := DefaultTarget(c, ""); s != nil && s.Account != "" && s.Agent == agent.Claude {
		own := s.Account
		if _, ok := store.Get(own); ok {
			u := usage[own]
			if !u.LoggedOut && !u.Exhausted(now) {
				return own, provisioned(store.ConfigDirFor(own))
			}
		}
	}
	allowed := cfg.GetAllowedAccounts(originKey(git.GetOriginKey(c.Root)))
	acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
		Accounts: all, Usage: usage, Strategy: cfg.GetAccountStrategy(), Manual: cfg.DefaultAccount,
		Allowed: allowed, Now: now,
	})
	if !ok {
		return "", ""
	}
	return acct.Email, provisioned(acct.ConfigDir)
}

// provisioned refreshes an account dir the way every session launch does
// (settings, symlinks to ~/.claude), best effort.
func provisioned(dir string) string {
	if dir == "" {
		return ""
	}
	if err := claudeaccount.Provision(dir); err != nil {
		debuglog.Logger.Warn("review: provisioning account dir failed", "dir", dir, "err", err)
	}
	return dir
}

// originKey mirrors the TUI's per-origin config key (`origin:<key>`).
func originKey(k string) string {
	if k == "" {
		return ""
	}
	return "origin:" + k
}

// defaultPR finds the checkout's PR in the TUI's persisted cache, but only
// while that cache still describes the branch checked out now (the same rule
// control.Observer uses).
func defaultPR(st Store) func(root, branch string) *github.PR {
	return func(root, branch string) *github.PR {
		prs, err := st.LoadPRCache()
		if err != nil {
			return nil
		}
		if row, ok := prs[root]; ok && row.PR != nil && (branch == "" || row.Branch == branch) {
			return row.PR
		}
		return nil
	}
}

// turnsToRead bounds how far back the intent reaches. ReadClaudeTurns reads a
// tail window that doubles to 16MB, so this is a ceiling, not a cost.
const turnsToRead = 200

// defaultTurns reads the Claude transcript; other agents have none fleet reads.
func defaultTurns(s *session.Session) []session.Turn {
	if s.Agent != agent.Claude && s.Agent != "" {
		return nil
	}
	p := session.ClaudeTranscriptPath(s.GetClaudeSessionID(), s.ProjectPath)
	turns, err := session.ReadClaudeTurns(p, turnsToRead)
	if err != nil {
		debuglog.Logger.Debug("review: transcript unreadable", "path", p, "err", err)
		return nil
	}
	return turns
}

func ciState(pr *github.PR) selfreview.CIInfo {
	if pr == nil {
		return selfreview.CIInfo{}
	}
	ci := selfreview.CIInfo{Failed: slices.Clone(pr.FailedChecks)}
	switch strings.ToUpper(pr.CIStatus) {
	case "SUCCESS":
		ci.State = selfreview.CISuccess
	case "FAILURE":
		ci.State = selfreview.CIFailure
	case "PENDING":
		ci.State = selfreview.CIPending
	}
	return ci
}
