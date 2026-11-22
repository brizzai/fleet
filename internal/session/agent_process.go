package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/tmux"
)

// conversationSucceeds reports whether a hook arriving from newPID, under a session
// id we don't own, is the owner conversation continued rather than a nested agent
// running beside it. known=false means one side has no pid (a status file older
// than StatusFile.AgentPID) and the caller must fall back to transcripts.
//
// This is the signal a nested agent cannot produce, and it is about identity, not
// just liveness:
//
//   - Same process, new session id. Claude rotates its id in place — `/clear`
//     starts a fresh conversation inside the running process — so the report is
//     the owner itself, under a new name. A rotation, unambiguously.
//   - Different process, owner gone. The conversation that held this session
//     ended; whatever is reporting now succeeded it.
//   - Different process, owner alive. Two agents at once: a `claude` spawned
//     inside a tool call inherited FLEET_INSTANCE_ID and is firing hooks under
//     its own id while its parent waits. Adopting it would hand the session's
//     status and resume id to a scratch conversation.
//
// Liveness alone cannot make that call. It gets the third case right and the first
// one exactly wrong — an in-process `/clear` leaves the owner's pid very much
// alive, which is the shape issue #226 was reported in.
//
// It trusts both pids to be agent processes, which is StatusFile.AgentPID's
// contract, not something checked here. Note how this fails if that contract
// breaks: a pid belonging to something short-lived is never alive, so the third
// case collapses into the second and EVERY foreign session id is adopted — the
// gate inverts rather than degrades. See StatusFile.AgentPID for what the
// contract rests on.
func conversationSucceeds(ownerPID, newPID int) (succeeds, known bool) {
	if ownerPID <= 0 || newPID <= 0 {
		return false, false
	}
	if ownerPID == newPID {
		return true, true // in-process rotation
	}
	return !agentProcessAlive(ownerPID), true
}

// agentProcessAlive reports whether a process is still running.
//
// Signal 0 checks for existence and permission without delivering anything. It
// answers for zombies too (a not-yet-reaped child is still a process), which is the
// answer we want: the agent has not been replaced.
//
// Fails closed. A pid can be recycled onto an unrelated process, which reads as
// "owner alive" and leaves the session frozen — exactly what happens today without
// this check, so a wrong answer here costs nothing that was not already lost, while
// the opposite bias would hand a session's status and resume id to a scratch
// conversation.
func agentProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Unix FindProcess never fails; the signal is what reports.
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means it exists and belongs to someone else — alive for our purposes.
	return errors.Is(err, syscall.EPERM)
}

// awaitRetiredAgent waits for the agent a relaunch is replacing to exit, for
// the two agents that need it. Copilot holds a per-process lock on its
// conversation, and a new process that starts while the old one is still
// shutting down opens on a "Session in use — Resume anyway?" menu instead of
// the conversation. Antigravity opens fine but warns "Conversation already
// open … Sending messages from both may cause conflicts", and a busy one takes
// ~3.3s to exit after the kill (measured), so the two would share the
// conversation for that long. stop sends the old process
// the SIGHUP tmux would, for callers that haven't killed it yet. The pid comes
// from a hook and could have been recycled, so nothing happens unless it is
// still that agent's process. Blocks for up to retiredExitTimeout: callers run
// off the Update loop. pid is 0 when the agent never fired a hook — an idle one
// exits in well under a second anyway (Copilot ~30ms, Antigravity ~0.2s).
func (s *Session) awaitRetiredAgent(pid int, stop bool) {
	if s.Agent != agent.Copilot && s.Agent != agent.Antigravity {
		return
	}
	if pid <= 0 || !agentProcessAlive(pid) || !isAgentProcess(pid, s.Agent.Binary()) {
		return
	}
	if stop {
		// A pane already back at a shell has no agent left to stop, and the
		// pid — kept since its last hook — may now be another session's.
		if tmux.IsShellCommand(s.paneCommand()) {
			return
		}
		_ = syscall.Kill(pid, syscall.SIGHUP)
	}
	deadline := time.Now().Add(retiredExitTimeout)
	for agentProcessAlive(pid) {
		if time.Now().After(deadline) {
			debuglog.Logger.Warn("relaunch: old agent process still running", "id", s.ID, "agent", s.Agent, "pid", pid)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// agentOwnerGone reports whether the agent process behind the last hook has
// exited. An unknown owner (status file without a pid) counts as gone, which
// keeps the pane-only rule for that case.
func (s *Session) agentOwnerGone() bool {
	if s.ownerAliveFn != nil {
		return !s.ownerAliveFn()
	}
	pid := s.ownerAgentPID()
	return pid <= 0 || !agentProcessAlive(pid)
}

func (s *Session) ownerAgentPID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerPID
}

// isAgentProcess reports whether pid's executable is named binary (Copilot's
// native binary is "copilot", Antigravity's "agy"; a hook's parent is that
// process). The pid came from a hook and may have been recycled since.
func isAgentProcess(pid int, binary string) bool {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && filepath.Base(strings.TrimSpace(string(out))) == binary
}
