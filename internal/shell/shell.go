// Package shell models plain, non-agent terminals ("shells") shown in fleet's
// bottom drawer — dev servers, log tails, scratch command lines — scoped to a
// repo/worktree checkout. Shells are deliberately separate from session.Session:
// they have no hooks, no resume-by-id, and no auto-naming. They reuse the tmux
// layer for spawning, capture, and attach, but their status is derived purely
// from tmux pane state (foreground process + pane-dead), not from agent hooks.
package shell

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// ShellPrefix is the tmux session-name prefix for shells. It is intentionally
// NOT a prefix of tmux.SessionPrefix ("fleet_") — "fleetsh_" does not start with
// "fleet_" — so agent-session enumeration (tmux.ListSessions) never matches a
// shell, and the two populations stay cleanly separated in `list-panes -a`.
const ShellPrefix = "fleetsh_"

// Status is the derived run-state of a shell. Unlike sessions there are only
// three states, all read from tmux (no hooks).
type Status string

const (
	StatusRunning Status = "running" // a non-shell process is in the foreground
	StatusIdle    Status = "idle"    // sitting at a shell prompt
	StatusExited  Status = "exited"  // the pane's process exited (pane_dead)
)

// idleCommands are the foreground commands that mean "at a prompt". Login
// shells report a dash-prefixed name (e.g. "-zsh").
var idleCommands = map[string]bool{
	"zsh": true, "bash": true, "sh": true, "fish": true, "tcsh": true,
	"-zsh": true, "-bash": true, "-sh": true, "-fish": true, "-tcsh": true,
	"": true, // unknown (not in cache yet) reads as idle at rest
}

// Shell is a single drawer terminal.
//
// Name/RepoPath/Command/CreatedAt are immutable after creation (set once). The
// mutable fields — status, exitCode, tmux, tmuxName — are guarded by mu because
// the status worker (RefreshStatus / Restart) and the render thread (Status /
// ExitInfo / Tmux) touch them concurrently.
type Shell struct {
	ID        string
	Name      string // user-facing tab label ("shell", "dev", "logs")
	RepoPath  string // checkout root this shell is scoped to
	Command   string // command run at start ("" = bare interactive shell)
	CreatedAt time.Time

	mu       sync.Mutex
	status   Status
	exitCode string // set when status == StatusExited (from tmux PaneDeadInfo)
	lastCmd  string // latest foreground command line the shell ran (drives the tab label)
	tmuxName string
	tmux     *tmux.Session
}

// New creates a shell handle with a fresh tmux session targeting repoPath.
// command may be empty for a bare interactive shell.
func New(name, repoPath, command string) *Shell {
	ts := tmux.NewSessionWithPrefix(ShellPrefix, name, repoPath)
	return &Shell{
		ID:        generateID(),
		Name:      name,
		RepoPath:  repoPath,
		Command:   command,
		CreatedAt: time.Now(),
		status:    StatusIdle,
		tmuxName:  ts.Name,
		tmux:      ts,
	}
}

// FromRow reconstructs a Shell from a persisted row, reconnecting to its tmux
// session. It does not check liveness — the worker derives status (a dead
// session simply renders as exited on the first refresh).
func FromRow(row *session.ShellRow) *Shell {
	return &Shell{
		ID:        row.ID,
		Name:      row.Name,
		RepoPath:  row.RepoPath,
		Command:   row.Command,
		CreatedAt: row.CreatedAt,
		status:    StatusIdle,
		tmuxName:  row.TmuxName,
		tmux:      tmux.ReconnectSession(row.TmuxName, row.Name, row.RepoPath),
	}
}

// ToRow returns the persisted form.
func (s *Shell) ToRow() *session.ShellRow {
	s.mu.Lock()
	name := s.tmuxName
	s.mu.Unlock()
	return &session.ShellRow{
		ID:        s.ID,
		Name:      s.Name,
		RepoPath:  s.RepoPath,
		Command:   s.Command,
		TmuxName:  name,
		CreatedAt: s.CreatedAt,
	}
}

// Start launches the shell in tmux. No env is passed (no FLEET_INSTANCE_ID), so
// no agent hooks ever fire for a shell.
func (s *Shell) Start() error {
	return s.Tmux().Start(s.Command)
}

// Restart kills the (possibly dead) tmux session and relaunches the shell in a
// fresh one, reusing the same name/command. Updates the tmux name; the caller
// persists it via UpdateShellTmuxName(s.TmuxName()).
func (s *Shell) Restart() error {
	old := s.Tmux()
	if old.Exists() {
		_ = old.Kill()
	}
	ts := tmux.NewSessionWithPrefix(ShellPrefix, s.Name, s.RepoPath)
	if err := ts.Start(s.Command); err != nil {
		return err
	}
	s.mu.Lock()
	s.tmux = ts
	s.tmuxName = ts.Name
	s.exitCode = ""
	s.status = StatusIdle
	s.mu.Unlock()
	return nil
}

// Kill terminates the shell's tmux session.
func (s *Shell) Kill() error { return s.Tmux().Kill() }

// Tmux returns the underlying tmux session (for attach + capture).
func (s *Shell) Tmux() *tmux.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tmux
}

// TmuxName returns the current tmux session name.
func (s *Shell) TmuxName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tmuxName
}

// Status returns the last-derived status without recomputing.
func (s *Shell) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ExitInfo returns the exit code/signal string when the shell has exited, else "".
func (s *Shell) ExitInfo() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// SetLastCommand records the latest foreground command line the shell ran. Empty
// input is ignored so the last real command persists while the shell sits idle.
// Called off the render thread (status worker).
func (s *Shell) SetLastCommand(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	s.mu.Lock()
	s.lastCmd = cmd
	s.mu.Unlock()
}

// DisplayName is the user-facing tab label: the latest command the shell ran,
// falling back to the creation Name ("shell", "shell 2", …) before it has run
// anything. Immutable Name is the stable identity; DisplayName is cosmetic.
func (s *Shell) DisplayName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCmd != "" {
		return s.lastCmd
	}
	return s.Name
}

// RefreshStatus recomputes the shell's status from the tmux caches and returns
// it. Cache-only except for one bounded PaneDeadInfo call on the transition into
// exited (to capture the exit code). Called off the render thread (status
// worker), which always refreshes the tmux session cache immediately before this
// pass, so a just-Started shell is already cached here.
func (s *Shell) RefreshStatus() Status {
	// One locked snapshot: a concurrent Restart swaps tmux+tmuxName together, so
	// reading them in separate lock acquisitions could mix sessions.
	s.mu.Lock()
	ts := s.tmux
	name := s.tmuxName
	prevExit := s.exitCode
	s.mu.Unlock()

	exists := ts.Exists()
	dead := exists && ts.IsPaneDead()

	var st Status
	if !exists {
		// An absent session (e.g. after a tmux server restart) has no live pane;
		// DeriveStatus would read it as idle. Treat it as exited so the drawer
		// offers Enter→restart instead of stranding a dead "idle" tab.
		st = StatusExited
	} else {
		st = DeriveStatus(dead, tmux.PaneCurrentCommand(name))
	}

	// Capture the exit code once, on the transition into exited, and only when the
	// pane is actually dead (an absent session has no pane to query). PaneDeadInfo
	// is a fresh `tmux list-panes` exec, so re-running it every cycle for every
	// exited shell would spawn N subprocesses ~2×/s; cache it after first capture.
	// Restart clears exitCode, so a relaunched-then-re-exited shell re-captures.
	exit := prevExit
	switch {
	case st != StatusExited:
		exit = ""
	case dead && exit == "":
		if _, exitStatus, exitSignal, ok := ts.PaneDeadInfo(); ok {
			switch {
			case exitStatus != "":
				exit = exitStatus
			case exitSignal != "":
				exit = "sig " + exitSignal
			}
		}
	}

	s.mu.Lock()
	s.status = st
	s.exitCode = exit
	s.mu.Unlock()
	return st
}

// IsShellCommand reports whether paneCmd is an interactive shell sitting at its
// prompt — i.e. nothing is running in the pane's foreground.
//
// Unlike the idleCommands lookup it wraps, "" (not in the cache yet) is NOT a
// shell: callers use this to decide whether typing into a pane is safe, and
// "unknown" must not read as "definitely a shell prompt".
func IsShellCommand(paneCmd string) bool {
	return paneCmd != "" && idleCommands[paneCmd]
}

// DeriveStatus computes a shell's status from its tmux pane state. Pure.
func DeriveStatus(dead bool, paneCmd string) Status {
	if dead {
		return StatusExited
	}
	if idleCommands[paneCmd] {
		return StatusIdle
	}
	return StatusRunning
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%d", b, time.Now().Unix())
}
