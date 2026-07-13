package proc

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// findHolders is the Linux discovery backend for FindHolders: a native /proc
// walk, no subprocesses. For each pid it resolves cwd, exe, and every open fd
// symlink, with a /proc/<pid>/maps scan as a last resort for file-backed
// mappings (a daemon exec'd from a since-deleted worktree binary). Pids owned
// by other users fail with EPERM on readlink and are skipped — the same
// blindness non-root lsof has on macOS, and irrelevant here: the leftover
// daemons we hunt were spawned by the user's own session.
//
// Semantics note: unlike macOS, an open fd on Linux never blocks rmdir — the
// point of this walk is not dodging EBUSY but killing leftover dev daemons
// (watchers, dev servers) before the worktree is removed, so they don't keep
// respawning files or hold node_modules recreating "Directory not empty".
func findHolders(absDir string, extra map[string]bool) ([]Holder, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	prefix := absDir + string(filepath.Separator)

	var holders []Holder
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue // non-pid /proc entry (self also never counts)
		}
		if !pidHoldsDir(pid, absDir, prefix) {
			continue
		}
		cmd := commandName(pid)
		if excluded(cmd, extra) {
			continue
		}
		holders = append(holders, Holder{PID: pid, Command: cmd})
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].PID < holders[j].PID })
	return holders, nil
}

// pidHoldsDir reports whether pid holds absDir open via cwd, exe, an open fd,
// or a file-backed mapping under it.
func pidHoldsDir(pid int, absDir, prefix string) bool {
	base := "/proc/" + strconv.Itoa(pid)
	for _, link := range [...]string{base + "/cwd", base + "/exe"} {
		if target, err := os.Readlink(link); err == nil && pathUnder(target, absDir, prefix) {
			return true
		}
	}
	fds, err := os.ReadDir(base + "/fd")
	if err != nil {
		return false // EPERM (other user) or the pid raced away — either way, not ours to kill
	}
	for _, fd := range fds {
		if target, err := os.Readlink(base + "/fd/" + fd.Name()); err == nil && pathUnder(target, absDir, prefix) {
			return true
		}
	}
	// maps last: it's the only per-pid *read* (not readlink) and rarely the
	// sole hit, so most processes never pay for it.
	if data, err := os.ReadFile(base + "/maps"); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			// Mapping lines end in the pathname column (absolute path or blank).
			if i := strings.IndexByte(line, '/'); i != -1 && pathUnder(line[i:], absDir, prefix) {
				return true
			}
		}
	}
	return false
}

// pathUnder reports whether p is absDir itself or lies below it. Deleted-file
// links ("path (deleted)") still count: the daemon holding them is the one we
// came for.
func pathUnder(p, absDir, prefix string) bool {
	p = strings.TrimSuffix(p, " (deleted)")
	return p == absDir || strings.HasPrefix(p, prefix)
}

// commandName returns pid's command for display and exclusion matching:
// argv[0]'s full string from cmdline (so normalizeCmd can basename a path like
// /usr/local/bin/code), falling back to comm — which the kernel truncates to
// 15 bytes — for zombies and kernel-adjacent processes with an empty cmdline.
func commandName(pid int) string {
	base := "/proc/" + strconv.Itoa(pid)
	if data, err := os.ReadFile(base + "/cmdline"); err == nil {
		if argv0, _, _ := strings.Cut(string(data), "\x00"); argv0 != "" {
			return argv0
		}
	}
	data, err := os.ReadFile(base + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// foregroundCommands is the Linux discovery backend for ForegroundCommands.
// Where the darwin path snapshots the whole process table with one ps call,
// here each wanted shell costs two tiny /proc reads: the shell's stat for its
// tty's foreground process group (tpgid), then the group leader's stat to
// confirm lineage. Same contract as darwin: the leader must be a direct child
// of the shell, so an idle shell — its own foreground group — yields nothing.
func foregroundCommands(want map[int]bool) map[int]string {
	out := make(map[int]string)
	for shellPID := range want {
		shell, err := readStat(shellPID)
		if err != nil {
			continue
		}
		leaderPID := shell.tpgid
		// Idle shell: the tty's foreground group is the shell's own group.
		if leaderPID <= 0 || leaderPID == shellPID || leaderPID == shell.pgrp {
			continue
		}
		leader, err := readStat(leaderPID)
		if err != nil || leader.ppid != shellPID {
			continue
		}
		if cmd := commandLine(leaderPID); cmd != "" {
			out[shellPID] = cmd
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// commandLine returns pid's full argv as a single space-joined string,
// mirroring the `command=` column darwin gets from ps.
func commandLine(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	s := strings.TrimRight(string(data), "\x00")
	return strings.ReplaceAll(s, "\x00", " ")
}

// procStat is the subset of /proc/<pid>/stat this package needs.
type procStat struct {
	ppid  int
	pgrp  int
	tpgid int
}

func readStat(pid int) (procStat, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	return parseStat(string(data))
}

// parseStat extracts ppid, pgrp, and tpgid from a stat(5) line. Field 2 (comm)
// is parenthesized and may itself contain spaces and parens — e.g. a process
// named "a) b" — so the only safe anchor is the *last* ')'; everything after
// it is space-separated: state ppid pgrp session tty_nr tpgid …
func parseStat(line string) (procStat, error) {
	i := strings.LastIndexByte(line, ')')
	if i == -1 || i+2 > len(line) {
		return procStat{}, strconv.ErrSyntax
	}
	fields := strings.Fields(line[i+1:])
	if len(fields) < 6 {
		return procStat{}, strconv.ErrSyntax
	}
	ppid, e1 := strconv.Atoi(fields[1])
	pgrp, e2 := strconv.Atoi(fields[2])
	tpgid, e3 := strconv.Atoi(fields[5])
	if e1 != nil || e2 != nil || e3 != nil {
		return procStat{}, strconv.ErrSyntax
	}
	return procStat{ppid: ppid, pgrp: pgrp, tpgid: tpgid}, nil
}
