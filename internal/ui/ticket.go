package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/linear"
)

// ticketMaterializeBudget bounds the whole fetch-and-write step, which runs on
// the worktree-creation path, where a human is waiting for a pane to appear.
// Generous enough for a ticket with a dozen screenshots on a slow link, short
// enough that a wedged request doesn't feel like a hang: the session starts either
// way, and past this the prompt simply isn't seeded.
const ticketMaterializeBudget = 25 * time.Second

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
		// ErrNotFound used to be swallowed beside ErrNotConnected, because
		// inference guessed an identifier out of a branch name and "no such
		// issue" was the ordinary answer for a branch that named none.
		// Inference is gone: the only caller left is worktree creation, where
		// the user picked this ticket in the `w` dialog and Materialize
		// re-fetched it. "Not found" there means it was deleted, or their
		// access to it changed, in the seconds since — a real event, and
		// swallowing it leaves a worktree that opens with no prompt and no
		// explanation. Worded rather than %v-formatted: the sentinel reads
		// "linear: issue not found", which renders as "Linear: linear: …".
		if errors.Is(err, linear.ErrNotFound) {
			return "Linear: issue not found — worktree created without the ticket"
		}
		// Still a resting state, and barely reachable from this caller anyway:
		// the dialog's own fetch had to succeed for there to be a ticket to
		// materialize at all.
		if errors.Is(err, linear.ErrNotConnected) {
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
