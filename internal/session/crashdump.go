package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
)

// triggerCrashDump captures forensic state for a session that just died and
// writes it to ~/.config/fleet/crashes/. Called once per death — guarded by
// the deathRecorded flag so subsequent UpdateStatus ticks are no-ops.
//
// The dump itself runs in a goroutine so the worker can move on to other
// sessions. Captures:
//   - tmux pane_dead/exit-status/exit-signal (only present with remain-on-exit on)
//   - last 200 lines of pane content (raw ANSI, copy-paste-friendly), with
//     fallback to the most recent pane capture cached by tmux.Session — vital
//     when the tmux session is GC'd before the dump runs (pane was the only
//     window, claude was the only command, exit closes the lot)
//   - hook status file (SessionEnd reason if Claude reported one)
//   - last 6 perfwatch heartbeats from debug.log (~30s of system context)
//
// reason is a fleet-side label ("tmux_gone", "pane_dead", "hook_dead") that
// indicates which detection path fired.
func (s *Session) triggerCrashDump(reason string) {
	s.mu.Lock()
	if s.deathRecorded {
		s.mu.Unlock()
		return
	}
	s.deathRecorded = true
	tmuxName := s.TmuxSessionName
	id := s.ID
	title := s.Title
	s.mu.Unlock()

	// Snapshot the cached pane content NOW (before the goroutine fires) — by
	// the time the goroutine runs, tmux may already have GC'd the session and
	// the cache reflects the moment of death.
	var cachedPane string
	if s.tmuxSession != nil {
		cachedPane = s.tmuxSession.CachedPane()
	}

	go writeCrashDump(id, title, tmuxName, reason, cachedPane)
}

func writeCrashDump(sessionID, title, tmuxName, reason, cachedPane string) {
	defer func() {
		if r := recover(); r != nil {
			debuglog.Logger.Error("crashdump: panic", "id", sessionID, "recover", r)
		}
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		debuglog.Logger.Error("crashdump: UserHomeDir failed", "err", err)
		return
	}
	dir := filepath.Join(home, ".config", "fleet", "crashes")
	if err := os.MkdirAll(dir, 0755); err != nil {
		debuglog.Logger.Error("crashdump: mkdir failed", "dir", dir, "err", err)
		return
	}

	now := time.Now()
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.txt", sessionID, now.Format("20060102-150405")))

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "=== fleet session crash dump ===\n")
	fmt.Fprintf(&buf, "session_id:     %s\n", sessionID)
	fmt.Fprintf(&buf, "title:          %s\n", title)
	fmt.Fprintf(&buf, "tmux_session:   %s\n", tmuxName)
	fmt.Fprintf(&buf, "detected_via:   %s\n", reason)
	fmt.Fprintf(&buf, "captured_at:    %s\n", now.Format(time.RFC3339))

	// Tmux exit info — only meaningful with remain-on-exit on.
	if dead, status, signal, ok := tmuxPaneDeadInfo(tmuxName); ok {
		fmt.Fprintf(&buf, "tmux_dead:      %t\n", dead)
		fmt.Fprintf(&buf, "exit_status:    %s\n", emptyDash(status))
		fmt.Fprintf(&buf, "exit_signal:    %s%s\n", emptyDash(signal), signalNote(signal))
	} else {
		fmt.Fprintf(&buf, "tmux_dead:      tmux session no longer exists (GC'd before dump)\n")
	}

	// Hook file — Claude Code's SessionEnd payload.
	if hf, err := readHookFile(home, sessionID); err == nil {
		fmt.Fprintf(&buf, "hook_event:     %s\n", hf.Event)
		fmt.Fprintf(&buf, "hook_reason:    %s\n", emptyDash(hf.Reason))
		fmt.Fprintf(&buf, "hook_status:    %s\n", hf.Status)
		fmt.Fprintf(&buf, "hook_ts:        %s\n", time.Unix(hf.Timestamp, 0).Format(time.RFC3339))
	} else {
		fmt.Fprintf(&buf, "hook_file:      not readable (%v)\n", err)
	}

	// Last 200 lines of pane (raw ANSI preserved — paste straight into a
	// terminal to view colored, or strip with `sed 's/\x1b\[[0-9;]*m//g'`).
	// Try live capture first (most recent), fall back to fleet's cached capture
	// (last successful poll before the death) if tmux session is already gone.
	fmt.Fprintf(&buf, "\n=== pane (last 200 lines, raw ANSI) ===\n")
	if pane, err := capturePaneRaw(tmuxName, 200); err == nil {
		buf.WriteString(pane)
		if !strings.HasSuffix(pane, "\n") {
			buf.WriteByte('\n')
		}
	} else if cachedPane != "" {
		fmt.Fprintf(&buf, "(live capture failed: %v — falling back to fleet's cached pane from last successful poll)\n", err)
		buf.WriteString(cachedPane)
		if !strings.HasSuffix(cachedPane, "\n") {
			buf.WriteByte('\n')
		}
	} else {
		fmt.Fprintf(&buf, "(capture failed: %v; no cached content available)\n", err)
	}

	// Last 6 perfwatch heartbeats from debug.log (~30s of system context).
	fmt.Fprintf(&buf, "\n=== recent perfwatch heartbeats (system context) ===\n")
	if hb := tailPerfwatchHeartbeats(6); hb != "" {
		buf.WriteString(hb)
	} else {
		fmt.Fprintf(&buf, "(no heartbeats found — perfwatch may be disabled; relaunch with FLEET_DEBUG=1)\n")
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		debuglog.Logger.Error("crashdump: write failed", "path", path, "err", err)
		return
	}
	debuglog.Logger.Warn("crashdump: written", "path", path, "id", sessionID, "reason", reason)
}

func tmuxPaneDeadInfo(tmuxName string) (dead bool, status, signal string, ok bool) {
	out, err := exec.Command("tmux", "list-panes", "-t", tmuxName+":0.0",
		"-F", "#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}").Output()
	if err != nil {
		return false, "", "", false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) != 3 {
		return false, "", "", false
	}
	return parts[0] == "1", parts[1], parts[2], true
}

func capturePaneRaw(tmuxName string, lines int) (string, error) {
	// -e preserves ANSI; -S -N starts N lines back from the bottom of history.
	out, err := exec.Command("tmux", "capture-pane", "-t", tmuxName, "-p", "-e",
		"-S", fmt.Sprintf("-%d", lines)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func readHookFile(home, sessionID string) (*hooks.StatusFile, error) {
	path := filepath.Join(home, ".config", "fleet", "hooks", sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hf hooks.StatusFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, err
	}
	return &hf, nil
}

// tailPerfwatchHeartbeats returns the last n "perfwatch heartbeat" lines from
// the debug log, oldest first. Returns "" if the log can't be read or no
// heartbeats are present.
func tailPerfwatchHeartbeats(n int) string {
	data, err := os.ReadFile(debuglog.LogPath())
	if err != nil {
		return ""
	}
	// Walk lines from the end, picking the last n that contain "perfwatch heartbeat".
	all := bytes.Split(data, []byte("\n"))
	var picked []string
	for i := len(all) - 1; i >= 0 && len(picked) < n; i-- {
		line := string(all[i])
		if strings.Contains(line, "perfwatch heartbeat") {
			picked = append(picked, line)
		}
	}
	if len(picked) == 0 {
		return ""
	}
	// Reverse to oldest-first.
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return strings.Join(picked, "\n") + "\n"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// signalNote annotates a tmux #{pane_dead_signal} value with what the signal
// number means, when recognized. Returns "" for unknown/empty values.
func signalNote(sig string) string {
	switch strings.TrimSpace(sig) {
	case "":
		return ""
	case "0":
		return " (no signal — clean exit)"
	case "1":
		return " (SIGHUP)"
	case "2":
		return " (SIGINT — Ctrl+C)"
	case "6":
		return " (SIGABRT — panic/abort)"
	case "9":
		return " (SIGKILL — likely OOM/Jetsam or external kill)"
	case "11":
		return " (SIGSEGV — segfault)"
	case "15":
		return " (SIGTERM — graceful kill)"
	}
	return ""
}
