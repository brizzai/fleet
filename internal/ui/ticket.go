package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/ticket"
	"github.com/brizzai/fleet/internal/ticketing"
)

// ticketMaterializeBudget bounds the whole fetch-and-write step, which runs on
// the worktree-creation path, where a human is waiting for a pane to appear.
// Generous enough for a ticket with a dozen screenshots on a slow link, short
// enough that a wedged request doesn't feel like a hang: the session starts either
// way, and past this the prompt simply isn't seeded.
const ticketMaterializeBudget = 25 * time.Second

// materializeTicket writes a ticket and its screenshots into a freshly created
// worktree.
//
// Called from the worktree-creation closure, off the Update goroutine, beside
// copyClaudeSettingsFile and CopyConfiguredFiles — and with the same contract:
// it never fails its caller. A nil result means no prompt gets seeded, which is
// the honest outcome, because a prompt pointing at files that were never
// written is worse than no prompt.
//
// repoPath is the checkout the ticket was picked in, not the new worktree: it
// is what names the tracker keys, and therefore which provider owns this
// identifier.
func materializeTicket(repoPath, worktreePath string, t *ticket.Ticket, moveState bool) (*ticket.Result, error) {
	if t == nil || !t.Ok() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ticketMaterializeBudget)
	defer cancel()

	res, err := ticketing.Materialize(ctx, repoPath, ticket.Opts{
		WorktreePath: worktreePath,
		Identifier:   t.Identifier,
		MoveState:    moveState,
	})
	if err != nil {
		debuglog.Logger.Warn("ticket: materialize failed",
			"provider", t.Provider, "id", t.Identifier, "worktree", worktreePath, "err", err)
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
//
// tracker is the provider's display name, taken from the ticket the dialog
// carried rather than re-resolved: by the time this runs the worktree exists
// and its repo path is no longer to hand, and naming the wrong tracker in an
// error line is worse than naming none.
func ticketStatusLine(tracker string, res *ticket.Result, err error) string {
	if tracker == "" {
		tracker = "Ticket"
	}
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
		// explanation.
		if errors.Is(err, ticket.ErrNotFound) {
			return tracker + ": issue not found — worktree created without the ticket"
		}
		// Still a resting state, and barely reachable from this caller anyway:
		// the dialog's own fetch had to succeed for there to be a ticket to
		// materialize at all.
		if errors.Is(err, ticket.ErrNotConnected) {
			return ""
		}
		return fmt.Sprintf("%s: %v — starting without the ticket", tracker, err)
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
