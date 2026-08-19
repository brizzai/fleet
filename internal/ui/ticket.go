package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/session"
)

// ticketMaterializeBudget bounds the whole fetch-and-write step when it runs on
// the session-creation path, where a human is waiting for a pane to appear.
// Generous enough for a ticket with a dozen screenshots on a slow link, short
// enough that a wedged request doesn't feel like a hang: the session starts either
// way, and past this the prompt simply isn't seeded.
const ticketMaterializeBudget = 25 * time.Second

// ticketReadyMsg carries the outcome of an inferred materialization back to the
// Update loop, with the session-creation request it was blocking.
type ticketReadyMsg struct {
	create sessionCreateMsg
	res    *linear.Result
	err    error
}

// materializeTicket writes a Linear ticket and its screenshots into a freshly
// created worktree.
//
// Called from the worktree-creation closure, off the Update goroutine, beside
// copyClaudeSettingsFile and CopyConfiguredFiles — and with the same contract:
// it never fails its caller. A nil result means no prompt gets seeded, which is
// the honest outcome, because a prompt pointing at files that were never
// written is worse than no prompt.
func materializeTicket(worktreePath string, t *linear.Ticket, moveState bool) (*linear.Result, error) {
	if t == nil || !t.Ok() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ticketMaterializeBudget)
	defer cancel()

	res, err := linear.Materialize(ctx, linear.Opts{
		WorktreePath: worktreePath,
		Identifier:   t.Identifier,
		MoveState:    moveState,
	})
	if err != nil {
		debuglog.Logger.Warn("linear: materialize failed", "id", t.Identifier, "worktree", worktreePath, "err", err)
		return nil, err
	}
	return &res, nil
}

// ticketPromptFor resolves the first message for a session about to start in
// path, when that path's branch names a Linear issue.
//
// Runs on the Update goroutine, so it does no I/O beyond local reads on a known
// path: the branch comes from the git cache the worker already maintains, the
// identifier is a regex, and the reuse check is one ReadDir plus one ReadFile.
// That last check is the steady state — every session after the first in a
// ticket worktree hits it, with no network at all.
//
// It used to call session.GetRepoRoot, twice, and that comment was false: on a
// cache miss GetRepoRoot shells out to `git rev-parse` with an 8-second ceiling,
// on the goroutine that paints every frame. LookupRepoRoot never shells out, and
// a miss falls back to the path itself — which is the right answer for a
// worktree and for a main repo, since `rev-parse --show-toplevel` returns the
// checkout it is run in. Only a session created in a SUBDIRECTORY of a repo
// resolves differently, and there the cost of the miss is that the repo's team
// config is not found and ticket inference stays quiet — the same outcome as a
// repo that names no team, which is the designed opt-out.
//
// Returns (prompt, nil) for the fast path, ("", cmd) when a fetch is needed, and
// ("", nil) when there is nothing to do.
func (h *Home) ticketPromptFor(msg sessionCreateMsg) (string, tea.Cmd) {
	if msg.prompt != "" || msg.path == "" || !linear.Available() {
		return "", nil
	}
	if prompt, ok := linear.ExistingPrompt(msg.path); ok {
		return prompt, nil
	}

	repoRoot, _ := session.LookupRepoRoot(msg.path)
	if repoRoot == "" {
		repoRoot = msg.path
	}
	// The per-repo team gate is what keeps false positives free: a branch named
	// fix-123 in a repo that tracks no Linear team never costs a round trip.
	teamKeys := linear.TeamKeys(msg.path)
	if len(teamKeys) == 0 {
		if teamKeys = linear.TeamKeys(repoRoot); len(teamKeys) == 0 {
			return "", nil
		}
	}

	branch := ""
	if info, ok := h.gitInfo()[repoRoot]; ok && info != nil {
		branch = info.Branch
	}
	id := linear.IdentifierFromBranch(branch, teamKeys)
	if id == "" {
		// A worktree fleet made is named <repo>-<branch>, so the directory
		// still carries the identifier when the git cache is cold.
		id = linear.IdentifierFromBranch(pathTailAfterRepo(msg.path, repoRoot), teamKeys)
	}
	if id == "" || linear.NegativelyPinned(msg.path, id) {
		return "", nil
	}

	create := msg
	path := msg.path
	return "", func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), ticketMaterializeBudget)
		defer cancel()
		// Note MoveState is false on this path, always. Creating a worktree
		// from a ticket is an unambiguous "I'm starting this"; opening another
		// session in a worktree that already exists is not, and by then a human
		// may have moved the issue on.
		res, err := linear.Materialize(ctx, linear.Opts{
			WorktreePath: path,
			Identifier:   id,
			MoveState:    false,
		})
		if err != nil {
			return ticketReadyMsg{create: create, err: err}
		}
		return ticketReadyMsg{create: create, res: &res}
	}
}

// pathTailAfterRepo returns the part of a fleet-made worktree directory name
// that follows the repo name, e.g. /code/brizzai-brz-3182-fix → "brz-3182-fix".
// pathTailAfterRepo takes the resolved repoRoot rather than resolving it again:
// the caller already has it, and the second lookup was a second chance to shell
// out to git from the Update goroutine.
func pathTailAfterRepo(path, repoRoot string) string {
	base := filepath.Base(path)
	root := filepath.Base(repoRoot)
	if root != "" && root != base && len(base) > len(root)+1 && strings.HasPrefix(base, root+"-") {
		return base[len(root)+1:]
	}
	return base
}

// ticketStatusLine renders what happened, for the one line the user sees.
func ticketStatusLine(res *linear.Result, err error) string {
	switch {
	case err != nil:
		// Both are resting states, not failures: a branch that names no real
		// issue, and a fleet that was never connected to Linear. Neither is
		// worth a line on the session the user just started.
		if errors.Is(err, linear.ErrNotFound) || errors.Is(err, linear.ErrNotConnected) {
			return ""
		}
		return fmt.Sprintf("Linear: %v — starting without the ticket", err)
	case res == nil:
		return ""
	}

	line := fmt.Sprintf("%s materialized", res.Identifier)
	if res.Images > 0 {
		line += fmt.Sprintf(" with %d image(s)", res.Images)
	}
	if res.StateMoved != "" {
		line += " · moved to started"
	}
	return line
}
