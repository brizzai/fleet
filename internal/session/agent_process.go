package session

import (
	"errors"
	"os"
	"syscall"
)

// agentProcessAlive reports whether the agent process that fired a hook is still
// running. pid 0 means the status file predates StatusFile.AgentPID, which is not
// evidence of death — callers must treat false-with-unknown-pid separately.
//
// This is the signal a nested agent cannot produce. A `claude` spawned inside a
// tool call inherits FLEET_INSTANCE_ID and fires hooks under its own conversation
// id, which is indistinguishable from a session-id rotation by every transcript
// signal we have: both leave one conversation writing and another silent. The
// difference is that a rotation happens *inside* the owner process — the old
// conversation's process is gone — while a nested agent runs *alongside* an owner
// that is very much alive.
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
