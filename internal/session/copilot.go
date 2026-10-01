package session

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/tmux"
)

// copilotHookAccepted reports whether a hook belongs to this session's
// conversation.
//
// Copilot's id is minted by fleet (agent.PreMintsSessionID), so it is
// authoritative: none of Claude's rotation machinery applies. One Copilot
// process holds several conversations — /new opens a fresh one and keeps the
// old one open, the Sessions sidebar runs more side by side — so a foreign id
// from the same process is NOT a rotation. What moves the session is the user
// typing: a foreign id from the same process is adopted when it is the
// conversation the user last submitted a prompt in (PromptSessionID), which
// covers /new, /clear, /resume and a sidebar switch, and survives fleet being
// closed meanwhile. Subagents
// fire prompt hooks too, but the hook handler drops those before they reach
// the status file.
func (s *Session) copilotHookAccepted(hs *HookStatus) bool {
	if hs.SessionID == "" {
		return true
	}
	s.mu.RLock()
	expected := s.ownerSessionID
	if expected == "" {
		expected = s.ClaudeSessionID
	}
	ownerPID := s.ownerPID
	s.mu.RUnlock()
	if expected == "" || hs.SessionID == expected {
		return true
	}
	// Same process only: an agent CLI the user's Copilot runs as a tool inherits
	// FLEET_INSTANCE_ID, and its prompt must not take the session over. An
	// unknown owner (fresh launch, fleet reopened) can't be checked, and a dead
	// one (/exit, then `copilot` typed into the pane) has nothing to protect.
	samePID := ownerPID == 0 || hs.AgentPID == ownerPID || !agentProcessAlive(ownerPID)
	if hs.PromptSessionID == hs.SessionID && samePID {
		debuglog.Logger.Info("adopting copilot session: user is typing in it",
			"id", s.ID, "old", expected, "new", hs.SessionID)
		return true
	}
	debuglog.Logger.Debug("hook dropped: foreign copilot session id",
		"id", s.ID, "expected", expected, "foreign", hs.SessionID)
	return false
}

// copilotWaitPatterns mark a Copilot menu blocked on the user. The permission
// prompt and the folder-trust menu share the first footer, drawn inside their
// box — /theme's picker has the same words outside one, so the box edge is part
// of the match. The ask_user prompt carries the second. Matched in the bottom
// lines only, so the same words in scrollback can't trip it.
var copilotWaitPatterns = []string{
	"│ ↑/↓ to navigate · enter to select", // permission prompt, folder-trust menu
	"enter accept · ctrl+d decline",       // ask_user question
}

// copilotPaneWaiting reports whether Copilot's pane shows a menu awaiting the user.
func copilotPaneWaiting(content string) bool {
	return recentContains(content, copilotWaitPatterns)
}

// copilotPaneRunning reports whether Copilot's footer shows an active turn
// ("◎ Working · … esc interrupt") that is not blocked on a menu.
func copilotPaneRunning(content string) bool {
	return !copilotPaneWaiting(content) && recentContains(content, []string{"esc interrupt"})
}

// updateCopilotStatus is the Copilot branch of UpdateStatus.
//
// Hooks drive it, but two gaps in Copilot's hook set need out-of-band signals:
//
//   - A cancelled turn (Ctrl+C mid-turn, Esc at a permission prompt) fires no
//     hook at all, so a running/waiting hook would stick forever. Copilot's own
//     events.jsonl records it as a trailing `abort` (copilotTurnAborted).
//   - SessionEnd fires for /clear and /exit alike, both with reason
//     "user_exit". Back at a shell means the agent really exited. A pane still
//     in Copilot is /clear, or /exit with another conversation open, which
//     switches to that one; the pane can't tell those two apart, and the id
//     stays on the closed conversation until the user types again.
//
// A definite pane state (a menu, or the working footer) overrides a stale hook,
// as for Codex: approving a permission fires no hook until the tool finishes.
func (s *Session) updateCopilotStatus(oldStatus Status, hasHook bool, hookStatus, hookReason string, log *slog.Logger) {
	if hasHook && tmux.IsShellCommand(s.paneCommand()) {
		// The agent has run (it fired a hook) and its pane is back at a shell.
		s.applyHookStatus(oldStatus, "dead", hookReason, log)
		return
	}

	s.updatePaneLedStatus("copilot", oldStatus, hasHook, hookStatus, hookReason, copilotPaneWaiting, copilotPaneRunning,
		func(hook string) string {
			if (hook == "running" || hook == "waiting") && s.copilotTurnAborted() {
				return "finished"
			}
			return hook
		}, log)
}

// retiredExitTimeout bounds the wait for a killed Copilot to exit. A busy one
// was measured at 1.8s (it runs its sessionEnd hook on the way out).
const retiredExitTimeout = 5 * time.Second

// awaitRetiredCopilot waits for the Copilot a relaunch is replacing to exit.
// Copilot holds a per-process lock on its conversation, and a new process that
// starts while the old one is still shutting down opens on a "Session in use —
// Resume anyway?" menu instead of the conversation. stop sends the old process
// the SIGHUP tmux would, for callers that haven't killed it yet. The pid comes
// from a hook and could have been recycled, so nothing happens unless it is
// still a copilot process. Blocks for up to retiredExitTimeout: callers run off
// the Update loop. pid is 0 when the agent never fired a hook — an idle Copilot
// exits in ~30ms anyway.
func (s *Session) awaitRetiredCopilot(pid int, stop bool) {
	if s.Agent != agent.Copilot || pid <= 0 || !agentProcessAlive(pid) || !isCopilotProcess(pid) {
		return
	}
	if stop {
		_ = syscall.Kill(pid, syscall.SIGHUP)
	}
	deadline := time.Now().Add(retiredExitTimeout)
	for agentProcessAlive(pid) {
		if time.Now().After(deadline) {
			debuglog.Logger.Warn("copilot relaunch: old process still running", "id", s.ID, "pid", pid)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Session) ownerAgentPID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerPID
}

// isCopilotProcess reports whether pid's executable is Copilot's (the native
// binary is named "copilot"; the hook's parent is that process).
func isCopilotProcess(pid int) bool {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && filepath.Base(strings.TrimSpace(string(out))) == "copilot"
}

// paneCommand is the pane's foreground command from tmux's per-tick cache.
func (s *Session) paneCommand() string {
	if s.paneCmdFn != nil {
		return s.paneCmdFn()
	}
	s.mu.RLock()
	name := s.TmuxSessionName // Restart rewrites it under mu
	s.mu.RUnlock()
	return tmux.PaneCurrentCommand(name)
}

// copilotEventsTail bounds the read: the abort we look for is a small line at
// the very end, while a single model snapshot line can run to tens of KB.
const copilotEventsTail = 32 << 10

func (s *Session) copilotTurnAborted() bool {
	if s.copilotAbortedFn != nil {
		return s.copilotAbortedFn()
	}
	s.mu.RLock()
	id := s.ClaudeSessionID
	s.mu.RUnlock()
	if id == "" {
		return false
	}
	return copilotEventsEndInAbort(copilotEventsPath(id))
}

func copilotEventsPath(id string) string {
	return filepath.Join(hooks.CopilotSessionDir(id), "events.jsonl")
}

// copilotBookkeeping are the events.jsonl event prefixes that trail an abort
// (telemetry and bookkeeping), so they are skipped when looking for it.
var copilotBookkeeping = []string{"hook", "model", "session", "system"}

// copilotEventsEndInAbort reports whether the last conversation event in a
// Copilot events.jsonl is `abort`, i.e. the user cancelled the turn. Any read
// or parse failure is "no signal", never "aborted" — an unreadable file must
// not end a live turn.
func copilotEventsEndInAbort(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	off := max(fi.Size()-copilotEventsTail, 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return false
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is cut mid-way
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(lines[i]), &ev) != nil || ev.Type == "" {
			continue
		}
		if prefix, _, _ := strings.Cut(ev.Type, "."); slices.Contains(copilotBookkeeping, prefix) {
			continue
		}
		return ev.Type == "abort"
	}
	return false
}
