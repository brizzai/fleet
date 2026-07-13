// Package proc inspects and terminates processes that hold a filesystem path
// open. macOS-only: it shells out to lsof. It exists to clear a git worktree of
// leftover dev-stack daemons (process-compose, air, vite/node, …) that detached
// from the session's tmux pane and would otherwise pin the directory, making
// `git worktree remove` fail with "Directory not empty".
package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Holder is a process holding a directory open — via its cwd or an open file
// under it.
type Holder struct {
	PID     int
	Command string
}

// neverKill is the set of process names we must never terminate even when they
// hold the worktree: editors and their helpers, language servers, shells,
// terminal multiplexers, fleet itself, and a few system tools. These commonly
// have a worktree as cwd or hold read handles, but killing them would disrupt
// the user's editor/shell rather than the session's dev stack. Open *read*
// handles don't block directory removal on macOS anyway, so sparing them is
// safe.
var neverKill = map[string]bool{
	"code": true, "electron": true, "cursor": true, "windsurf": true,
	"nvim": true, "vim": true, "emacs": true, "nano": true, "subl": true,
	"idea": true, "goland": true, "webstorm": true, "pycharm": true,
	"clion": true, "rider": true, "zed": true, "phpstorm": true,
	"rubymine": true, "datagrip": true, "rustrover": true,
	"gopls": true, "terraform-ls": true, "rust-analyzer": true,
	"zsh": true, "bash": true, "fish": true, "sh": true,
	"tmux": true, "fleet": true, "ssh": true, "sshd": true,
	"git": true, "gpg-agent": true,
}

// FindHolders returns the processes (deduped by PID) that hold dir open,
// excluding the never-kill set and the caller-supplied extraExclude (e.g. the
// configured editor). macOS-only.
func FindHolders(dir string, extraExclude []string) ([]Holder, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)

	extra := make(map[string]bool, len(extraExclude))
	for _, e := range extraExclude {
		if n := normalizeCmd(e); n != "" {
			extra[n] = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// -F p/c/n: machine-readable records (pid, command, name). -w silences
	// warnings; -n/-P skip DNS/port lookups (the slow parts). No `+D` — that
	// would stat the whole tree; instead we enumerate every open file once and
	// prefix-match in Go (~0.5s system-wide). lsof exits non-zero when some
	// files can't be examined even on an otherwise-good run, so we parse
	// whatever it produced and only surface the error when it returned nothing.
	out, err := exec.CommandContext(ctx, "lsof", "-Fpcn", "-w", "-n", "-P").Output()
	if len(out) == 0 && err != nil {
		return nil, err
	}
	return parseHolders(string(out), abs, os.Getpid(), extra), nil
}

// parseHolders is the pure core of FindHolders: it turns lsof -Fpcn output into
// the killable holder set. Split out so it can be unit-tested without lsof.
func parseHolders(lsofOut, absDir string, self int, extra map[string]bool) []Holder {
	prefix := absDir + string(filepath.Separator)

	type acc struct {
		cmd string
		hit bool
	}
	procs := make(map[int]*acc)
	curPID := 0
	for line := range strings.SplitSeq(lsofOut, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			curPID, _ = strconv.Atoi(line[1:])
		case 'c':
			if curPID != 0 {
				if procs[curPID] == nil {
					procs[curPID] = &acc{}
				}
				procs[curPID].cmd = line[1:]
			}
		case 'n':
			if curPID == 0 || curPID == self {
				continue
			}
			name := line[1:]
			if name == absDir || strings.HasPrefix(name, prefix) {
				if procs[curPID] == nil {
					procs[curPID] = &acc{}
				}
				procs[curPID].hit = true
			}
		}
	}

	var holders []Holder
	for pid, a := range procs {
		if !a.hit || excluded(a.cmd, extra) {
			continue
		}
		holders = append(holders, Holder{PID: pid, Command: a.cmd})
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].PID < holders[j].PID })
	return holders
}

// excluded reports whether a process command should be spared from killing.
func excluded(cmd string, extra map[string]bool) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	if c == "" {
		return true
	}
	// Editor helper processes show up as e.g. "Code Helper (Plugin)".
	if neverKill[c] || strings.HasPrefix(c, "code helper") {
		return true
	}
	n := normalizeCmd(cmd)
	return neverKill[n] || extra[n]
}

// normalizeCmd reduces a command string to a lowercase basename of its first
// field, e.g. "/usr/local/bin/code --wait" -> "code".
func normalizeCmd(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, " \t"); i != -1 {
		s = s[:i]
	}
	return strings.ToLower(filepath.Base(s))
}

// ForegroundCommands maps each given pane-shell pid to the command line of the
// process it is currently running — the leader of its tty's foreground process
// group (e.g. a `pane_pid` shell running "npm run dev" reports "npm run dev").
// Background jobs are excluded and a shell sitting at its prompt is absent from
// the result. One `ps` call regardless of how many pids are passed. macOS-only.
func ForegroundCommands(shellPIDs []int) map[int]string {
	if len(shellPIDs) == 0 {
		return nil
	}
	want := make(map[int]bool, len(shellPIDs))
	for _, p := range shellPIDs {
		if p > 0 {
			want[p] = true
		}
	}
	if len(want) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// pid, ppid, tpgid (the tty's foreground process group), then the full argv
	// (command= is last so it may contain spaces).
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,tpgid=,command=").Output()
	if len(out) == 0 || err != nil {
		return nil
	}
	return parseForeground(string(out), want)
}

// parseForeground is the pure core of ForegroundCommands. A shell's tpgid is the
// process group its tty currently gives keyboard input to; the leader of that
// group (pid == tpgid) is a direct child of the shell and the command it is
// running. Reading the leader by tpgid means background jobs (a different group)
// never win, a pipeline resolves to its first stage (the group leader), and an
// idle shell — whose foreground group is the shell itself, not a child — yields
// nothing so its previous command persists.
func parseForeground(psOut string, want map[int]bool) map[int]string {
	type proc struct {
		ppid int
		cmd  string
	}
	byPID := make(map[int]proc)
	fgPgrp := make(map[int]int) // wanted shell pid -> its tty's foreground process group
	for line := range strings.SplitSeq(psOut, "\n") {
		pidStr, rest, ok := cutField(line)
		if !ok {
			continue
		}
		ppidStr, rest, ok := cutField(rest)
		if !ok {
			continue
		}
		tpgidStr, rest, ok := cutField(rest)
		if !ok {
			continue
		}
		pid, e1 := strconv.Atoi(pidStr)
		ppid, e2 := strconv.Atoi(ppidStr)
		tpgid, e3 := strconv.Atoi(tpgidStr)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		byPID[pid] = proc{ppid: ppid, cmd: strings.TrimSpace(rest)}
		if want[pid] {
			fgPgrp[pid] = tpgid
		}
	}

	out := make(map[int]string)
	for shellPID, pgrp := range fgPgrp {
		// The foreground group's leader has pid == pgrp. Require it to be a direct
		// child of the shell — an idle shell is its own foreground group, so its
		// leader is the shell (parented elsewhere) and is correctly skipped.
		if leader, ok := byPID[pgrp]; ok && leader.ppid == shellPID && leader.cmd != "" {
			out[shellPID] = leader.cmd
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cutField splits off the first space-delimited field from s (skipping any
// leading spaces, as ps right-pads numeric columns). ok is false when s has no
// field separator left.
func cutField(s string) (field, rest string, ok bool) {
	return strings.Cut(strings.TrimLeft(s, " "), " ")
}

// Kill sends SIGTERM to each pid, waits up to grace for them to exit, then
// SIGKILLs any survivors. Pids already gone are ignored. macOS-only.
func Kill(pids []int, grace time.Duration) error {
	if len(pids) == 0 {
		return nil
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !anyAlive(pids) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range pids {
		if Alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return nil
}

// Alive probes whether a pid exists using signal 0 (sends no signal). A
// non-positive pid is treated as dead: signal 0 to pid 0/-1 targets a process
// group, which is never what a liveness check wants.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func anyAlive(pids []int) bool { return slices.ContainsFunc(pids, Alive) }
