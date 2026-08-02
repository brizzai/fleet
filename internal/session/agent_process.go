package session

import (
	"errors"
	"os"
	"syscall"
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
