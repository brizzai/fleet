package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/envflag"
	"github.com/brizzai/fleet/internal/instancelock"
)

// allowMultipleEnvVar skips the one-TUI-per-config-dir check.
const allowMultipleEnvVar = "FLEET_ALLOW_MULTIPLE"

// acquireInstanceLock takes the TUI lock or exits with a message naming the
// running instance. Any other failure to lock (a filesystem without flock)
// is logged and ignored: refusing to start over the guard itself would be
// worse than the duplicate it guards against.
func acquireInstanceLock() *instancelock.Lock {
	if envflag.Set(allowMultipleEnvVar) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	lock, pid, err := instancelock.Acquire(filepath.Join(home, ".config", "fleet", "tui.lock"))
	switch {
	case err == nil:
		return lock
	case errors.Is(err, instancelock.ErrHeld):
		debuglog.Logger.Warn("refusing second TUI", "holder_pid", pid)
		fmt.Fprintln(os.Stderr, instanceHeldMessage(pid))
		os.Exit(1)
	default:
		debuglog.Logger.Warn("instance lock unavailable; continuing", "err", err)
	}
	return nil
}

func instanceHeldMessage(pid int) string {
	who := "another fleet TUI"
	if pid > 0 {
		who = fmt.Sprintf("another fleet TUI (pid %d)", pid)
	}
	return fmt.Sprintf("fleet: %s is already running.\n"+
		"Two TUIs poll the same sessions and write the same database, which doubles CPU and causes\n"+
		"\"database is locked\" errors. Switch to the running one, or set %s=1 to start anyway.\n"+
		"CLI commands (fleet send, status, add, …) work alongside it as usual.", who, allowMultipleEnvVar)
}
