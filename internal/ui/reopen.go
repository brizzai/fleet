package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// closeNeedsYes reports whether closing a session in this status should need an
// explicit `y` (Enter declines). Only work in flight qualifies: a session that is
// running or sitting on a prompt loses that turn when closed, while an idle or
// finished one loses nothing the conversation doesn't already hold.
func closeNeedsYes(st session.Status) bool {
	return st == session.StatusRunning || st == session.StatusWaiting
}

// reopenVerdict is the outcome of checking one closed-session entry.
type reopenVerdict struct {
	ok          bool
	alreadyOpen bool   // a live row already carries this conversation: nothing to do
	reason      string // why it can't come back (shown to the user)
}

// reopenCheck decides whether a closed session can be brought back. It is pure:
// the filesystem and the open-session list arrive as functions so each refusal is
// testable without a disk.
//
//   - already open: the same row id, or the same conversation id, is on screen —
//     reopening would start a second agent on one conversation.
//   - no conversation: there is nothing to resume, so "reopen" would silently
//     start a different conversation in that folder.
//   - Claude writes no transcript until the first prompt, and `claude --resume`
//     on a conversation without one prints "No conversation found" and exits —
//     the same guard `fleet restart` applies.
//   - the folder is gone (a deleted worktree): the launch would fail in the shell.
func reopenCheck(row session.SessionRow, isOpen func(id, convID string) bool,
	dirExists func(string) bool, hasTranscript func(convID, projectPath string) bool) reopenVerdict {
	if isOpen(row.ID, row.ClaudeSessionID) {
		return reopenVerdict{alreadyOpen: true}
	}
	if row.ClaudeSessionID == "" {
		return reopenVerdict{reason: "it never started a conversation, so there is nothing to resume"}
	}
	if !dirExists(row.ProjectPath) {
		return reopenVerdict{reason: fmt.Sprintf("its folder %s no longer exists", row.ProjectPath)}
	}
	if agent.Parse(row.Agent) == agent.Claude && !hasTranscript(row.ClaudeSessionID, row.ProjectPath) {
		return reopenVerdict{reason: "its conversation has no transcript (Claude writes none until the first prompt)"}
	}
	return reopenVerdict{ok: true}
}

// reopenLastClosed brings back the most recently closed session: the row returns
// to the sidebar and its agent resumes the same conversation, in the same folder
// and on the same Claude account, as if it had been suspended.
//
// Inside the 5s undo window the session's tmux is still alive, so this is
// undoDelete — not a second launch. Otherwise it walks the closed list newest
// first; an entry that can't come back is dropped with the reason on screen (an
// entry that can never succeed would otherwise block every U behind it).
func (h *Home) reopenLastClosed() (tea.Model, tea.Cmd) {
	if len(h.pendingDeletes) > 0 {
		return h.undoDelete()
	}
	closed, err := h.storage.LoadClosed()
	if err != nil {
		h.setError("reopen_failed", fmt.Errorf("could not read closed sessions: %w", err))
		return h, nil
	}
	if len(closed) == 0 {
		h.setInfo("No closed session to reopen")
		return h, nil
	}

	open := func(id, convID string) bool {
		if _, ok := h.sessionByID[id]; ok {
			return true
		}
		for _, s := range h.sessions {
			if convID != "" && s.ClaudeSessionID == convID {
				return true
			}
		}
		return false
	}
	dirExists := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	hasTranscript := func(convID, projectPath string) bool {
		p := session.ClaudeTranscriptPath(convID, projectPath)
		if p == "" {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}

	for _, c := range closed {
		v := reopenCheck(c.Row, open, dirExists, hasTranscript)
		switch {
		case v.alreadyOpen:
			// Reopened already (from another window, or by hand): forget the entry
			// and look at the next one — a second U must not duplicate anything.
			_ = h.storage.DeleteClosed(c.Row.ID)
			continue
		case !v.ok:
			_ = h.storage.DeleteClosed(c.Row.ID)
			h.logAction("reopen closed session", c.Row.Title, false)
			h.setInfo(fmt.Sprintf("Can't reopen %q: %s. Dropped from the list — press U for the previous one.", c.Row.Title, v.reason))
			return h, nil
		}
		return h.restoreClosed(c.Row)
	}
	h.setInfo("No closed session to reopen")
	return h, nil
}

// restoreClosed re-inserts a closed row as a suspended session and resumes it.
func (h *Home) restoreClosed(row session.SessionRow) (tea.Model, tea.Cmd) {
	row.Status = string(session.StatusSuspended)
	row.TmuxSession = ""
	row.Acknowledged = true
	row.LastAccessed = time.Now()
	row.SnoozedUntil = time.Time{}
	if err := h.storage.SaveSession(&row); err != nil {
		h.setError("reopen_failed", fmt.Errorf("could not reopen %q: %w", row.Title, err))
		return h, nil
	}
	if err := h.storage.DeleteClosed(row.ID); err != nil {
		debuglog.Logger.Warn("could not forget reopened session", "id", row.ID, "err", err)
	}

	s := session.FromRow(&row)
	repoPath := session.GetRepoRoot(row.ProjectPath)
	if !h.pinnedRepos[repoPath] {
		h.pinnedRepos[repoPath] = true
		if err := h.storage.PinRepo(repoPath); err != nil {
			debuglog.Logger.Error("failed to re-pin repo on reopen", "repo", repoPath, "err", err)
		}
	}
	h.workerMu.Lock()
	h.sessions = append(h.sessions, s)
	h.rebuildSessionMap()
	h.workerMu.Unlock()

	h.repoExpanded[repoPath] = true
	h.rebuildFlatItems()
	for i, item := range h.flatItems {
		if !item.IsRepoHeader && item.Session != nil && item.Session.ID == s.ID {
			h.cursor = i
			h.syncViewport()
			break
		}
	}

	h.logAction("reopen closed session", row.Title, true)
	h.setInfo(reopenMessage(row))
	return h, h.resumeSelected(s)
}

// reopenMessage says what was restored, so the user can tell the conversation,
// folder and account came back without opening the session to check.
func reopenMessage(row session.SessionRow) string {
	msg := fmt.Sprintf("Reopened %q — resuming its conversation in %s", row.Title, filepath.Base(row.ProjectPath))
	if row.Account != "" {
		msg += " on " + row.Account
	}
	return msg
}
