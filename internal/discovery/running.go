package discovery

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/git"
)

// claudePidFile is the part of ~/.claude/sessions/<pid>.json that Claude Code
// writes for every running session, which fleet reads to offer a takeover.
type claudePidFile struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Tmux       string `json:"tmux"`
	UpdatedAt  int64  `json:"updatedAt"` // unix ms
}

// RunningClaude lists interactive Claude Code sessions running outside fleet,
// newest first, ready to resume in a fleet session. known holds the Claude
// session ids fleet already manages. home is the user's home directory.
//
// ponytail: reads only the default ~/.claude, not other accounts' config dirs;
// the pid file is Claude Code's undocumented format, so a change there makes
// this list come back empty rather than wrong; pid reuse after a crash isn't
// checked.
func RunningClaude(home string, known map[string]bool) []Recent {
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	var out []Recent
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p claudePidFile
		if json.Unmarshal(data, &p) != nil || p.PID <= 0 || p.SessionID == "" {
			continue
		}
		// Only a terminal session the user can close and we can resume: not a
		// background job, not an IDE panel, not one already in a fleet pane.
		if p.Kind != "interactive" || p.Entrypoint != "cli" || strings.HasPrefix(p.Tmux, "fleet_") || known[p.SessionID] {
			continue
		}
		if info, err := os.Stat(p.Cwd); err != nil || !info.IsDir() || !processAlive(p.PID) {
			continue
		}
		out = append(out, runningRecent(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out
}

func runningRecent(p claudePidFile) Recent {
	r := Recent{
		Path:            p.Cwd,
		Branch:          git.GetBranchName(p.Cwd),
		ClaudeSessionID: p.SessionID,
		Title:           p.Name,
		LastUsed:        time.UnixMilli(p.UpdatedAt),
		OriginKey:       git.GetOriginKey(p.Cwd),
	}
	if r.Branch == "" {
		r.Branch = filepath.Base(p.Cwd) // not a git repo: show the folder
	}
	if gi, err := os.Stat(filepath.Join(p.Cwd, ".git")); err == nil {
		r.IsWorktree = !gi.IsDir()
	}
	if r.Title == "" {
		r.Title = filepath.Base(p.Cwd)
	}
	where := []string{}
	if p.Status != "" {
		where = append(where, p.Status)
	}
	if tty := processTTY(p.PID); tty != "" {
		where = append(where, tty)
	}
	if p.Tmux != "" {
		where = append(where, "tmux "+p.Tmux)
	}
	where = append(where, "pid "+strconv.Itoa(p.PID))
	r.Where = strings.Join(where, " · ")
	return r
}

// processAlive reports whether pid exists. Signal 0 checks without sending;
// EPERM means it exists but belongs to someone else.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processTTY names the terminal pid runs in (pts/4, ttys003), so the user can
// find the window to close. ps has the same flags on Linux and macOS.
func processTTY(pid int) string {
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(string(out))
	if tty == "?" || tty == "??" {
		return ""
	}
	return tty
}
