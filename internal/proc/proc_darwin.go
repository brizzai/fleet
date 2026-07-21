package proc

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// findHolders is the darwin discovery backend for FindHolders: one system-wide
// lsof enumeration, parsed by parseHolders (proc.go).
func findHolders(absDir string, extra map[string]bool) ([]Holder, error) {
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
	return parseHolders(string(out), absDir, os.Getpid(), extra), nil
}

// foregroundCommands is the darwin discovery backend for ForegroundCommands:
// one `ps` call regardless of how many pids are passed, parsed by
// parseForeground (proc.go).
func foregroundCommands(want map[int]bool) map[int]string {
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
