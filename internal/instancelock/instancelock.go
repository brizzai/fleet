// Package instancelock keeps a second fleet TUI from starting against the same
// config dir.
//
// Two TUIs share state.db, the hook status files and the tmux server, and each
// runs the full set of background workers: both poll every pane, both refresh
// git in every repo, both write the PR cache, both may auto-suspend or reload
// the same sessions. Measured with two instances open, the "database is
// locked" error made up 145 of the last 200 debug.log lines, and idle CPU
// doubled. The CLI commands (`fleet add`, `send`, `status`, …) are short-lived
// and are not affected: they never take this lock.
//
// The lock is flock(2) on a file, so it disappears with the process however it
// exits — a crashed or SIGKILLed fleet never leaves a stale lock behind. The
// file also records the holder's pid, read only to name it in the refusal.
package instancelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("another fleet TUI is already running")

// Lock is a held instance lock. Release it (or exit) to let another TUI start.
type Lock struct {
	f *os.File
}

// Acquire takes the lock at path without blocking. When another process
// holds it, the error wraps ErrHeld and holderPID is its pid (0 if unknown).
func Acquire(path string) (lock *Lock, holderPID int, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, 0, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := readPID(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, pid, ErrHeld
		}
		return nil, 0, fmt.Errorf("lock %s: %w", path, err)
	}
	// Record our pid for the next process's refusal message. Best effort: the
	// flock is the lock, the pid is only a label.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, 0, nil
}

// Release drops the lock. Safe on a nil Lock and safe to call twice.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
