package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/proc"
	"github.com/brizzai/fleet/internal/session"
)

type statusSnapshotMsg struct {
	path string
	err  error
}

// reportSnapshotMsg delivers a capture taken for the bug-report dialog. It is a
// distinct type from statusSnapshotMsg so the `D` key's handler (which only
// toasts a path) can't be reached by a report capture, and vice versa.
type reportSnapshotMsg struct{ snap snapshotResult }

// snapshotResult is everything a capture produced: the on-disk dir plus the
// in-memory payload the bug-report dialog renders and files. meta is the exact
// map written to snapshot.json — read named fields out of it, never marshal it
// wholesale, since it embeds the user's verbatim prompt under
// hook.file_contents.user_prompt.
type snapshotResult struct {
	path      string
	meta      map[string]any
	paneClean string
	debugTail string
	err       error
}

// workerHeartbeat carries the status worker's liveness stamps into a snapshot so
// "is the worker wedged?" is answerable at a glance, without log archaeology.
type workerHeartbeat struct {
	LastCycleAt  time.Time // last completed statusWorkerCycle (zero if none yet)
	CycleStartAt time.Time // in-flight cycle start (zero when idle)
}

// captureStatusSnapshot freezes a session's status evidence to disk and returns
// it in memory.
//
// copyTranscript gates only the frozen copy of the Claude conversation JSONL —
// 0.5–2.7 MB per capture in practice. The `D` key wants it, for offline replay
// of the exact transcript. The bug-report path does not: the transcript's
// diagnostic value is the claude_log block, which readClaudeLogStat derives in a
// streaming pass either way, and a report capture fires on every `!` press.
func captureStatusSnapshot(s *session.Session, sessionID string, hb workerHeartbeat, copyTranscript bool) snapshotResult {
	ts := s.GetTmuxSession()
	if ts == nil {
		return snapshotResult{err: fmt.Errorf("no tmux session")}
	}

	// 1. Fresh pane capture with ANSI.
	rawPane, err := ts.CapturePaneFresh()
	if err != nil {
		return snapshotResult{err: fmt.Errorf("pane capture: %w", err)}
	}

	// 2. Session state snapshot.
	snap := s.SnapshotData(rawPane)

	// 3. Read hook file.
	hookFilePath := filepath.Join(hooks.GetHooksDir(), sessionID+".json")
	hookFileContent, _ := os.ReadFile(hookFilePath)
	hookFileInfo, _ := os.Stat(hookFilePath)

	// 4. Filtered debug log tail.
	debugTail := readFilteredDebugLog(sessionID, 100)

	// 5. Create output directory.
	now := time.Now()
	safeTitle := sanitizeForPath(snap.Title)
	dirName := fmt.Sprintf("%s_%s", now.Format("2006-01-02T15-04-05"), safeTitle)
	home, _ := os.UserHomeDir()
	snapshotDir := filepath.Join(home, ".config", "fleet", "snapshots", dirName)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return snapshotResult{err: fmt.Errorf("mkdir: %w", err)}
	}

	// 6. Freeze the Claude conversation transcript + derive its activity signal.
	// The JSONL is appended on real conversation events, so its recency tells us
	// whether the agent resumed past a stale "waiting" hook — the signal pane
	// scraping misses. Best-effort: Codex sessions have no Claude transcript.
	var claudeLog map[string]any
	if jsonlPath := session.ClaudeTranscriptPath(snap.ClaudeSessionID, snap.ProjectPath); jsonlPath != "" {
		if st := readClaudeLogStat(jsonlPath); st != nil {
			claudeLog = buildClaudeLogBlock(st, snap.HookUpdatedAt, now)
			if copyTranscript {
				_, _ = copyFileStreaming(jsonlPath, filepath.Join(snapshotDir, "claude_session.jsonl"))
			}
		}
	}

	// 6b. Freeze Claude Code's own per-process status file
	// (~/.claude/sessions/<pid>.json), located by sessionId. It carries Claude's
	// first-party busy/idle/shell flag — a Running-vs-Idle corroborator the hook,
	// pane, and transcript all lack. Best-effort; keyed by pid, so we prefer a
	// live process over a stale dead one.
	var claudeStatus map[string]any
	if css := findClaudeSessionStatus(snap.ClaudeSessionID); css != nil {
		claudeStatus = buildClaudeStatusBlock(css, now)
		_ = os.WriteFile(filepath.Join(snapshotDir, "claude_session_status.json"), css.raw, 0644)
	}

	// 7. Write files.
	paneClean := session.StripANSI(rawPane)
	_ = os.WriteFile(filepath.Join(snapshotDir, "pane_raw.txt"), []byte(rawPane), 0644)
	_ = os.WriteFile(filepath.Join(snapshotDir, "pane_clean.txt"), []byte(paneClean), 0644)
	_ = os.WriteFile(filepath.Join(snapshotDir, "debug_tail.txt"), []byte(debugTail), 0644)
	// All goroutine stacks — pins exactly what the status worker is blocked on
	// when its heartbeat (worker block in snapshot.json) shows it stalled.
	writeGoroutineDump(filepath.Join(snapshotDir, "goroutines.txt"))

	meta := buildSnapshotJSON(snap, hookFileContent, hookFileInfo, now, claudeLog, claudeStatus, hb)
	jsonData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(snapshotDir, "snapshot.json"), jsonData, 0644)

	debuglog.Logger.Info("status snapshot captured", "dir", snapshotDir, "session", sessionID)

	return snapshotResult{
		path:      snapshotDir,
		meta:      meta,
		paneClean: paneClean,
		debugTail: debugTail,
	}
}

func buildSnapshotJSON(snap session.StatusSnapshot, hookFileRaw []byte, hookFileInfo os.FileInfo, now time.Time, claudeLog, claudeStatus map[string]any, hb workerHeartbeat) map[string]any {
	m := map[string]any{
		"captured_at": now.Format(time.RFC3339Nano),
		"session": map[string]any{
			"id":                snap.ID,
			"title":             snap.Title,
			"project_path":      snap.ProjectPath,
			"tmux_session":      snap.TmuxSessionName,
			"claude_session_id": snap.ClaudeSessionID,
			"status":            string(snap.Status),
			"acknowledged":      snap.Acknowledged,
		},
	}
	if claudeLog != nil {
		m["claude_log"] = claudeLog
	}
	if claudeStatus != nil {
		m["claude_status"] = claudeStatus
	}

	// Worker liveness. `stalled: true` (or a large last_cycle_ago) means the
	// status worker wedged — the on-screen status is frozen, not mis-detected.
	// goroutines.txt then shows exactly where it's blocked.
	worker := map[string]any{}
	if hb.LastCycleAt.IsZero() {
		worker["last_cycle_at"] = nil
	} else {
		worker["last_cycle_at"] = hb.LastCycleAt.Format(time.RFC3339Nano)
		worker["last_cycle_ago"] = fmtSnapshotAge(hb.LastCycleAt, now)
		worker["stalled"] = now.Sub(hb.LastCycleAt) > workerStallThreshold
	}
	if !hb.CycleStartAt.IsZero() {
		worker["cycle_in_flight_for"] = fmtSnapshotAge(hb.CycleStartAt, now)
	}
	m["worker"] = worker

	hookMap := map[string]any{
		"status":        snap.HookStatus,
		"updated_at":    snap.HookUpdatedAt.Format(time.RFC3339),
		"age":           fmtSnapshotAge(snap.HookUpdatedAt, now),
		"overridden_at": fmtSnapshotAge(snap.HookOverriddenAt, now),
	}
	if len(hookFileRaw) > 0 {
		var parsed any
		if json.Unmarshal(hookFileRaw, &parsed) == nil {
			hookMap["file_contents"] = parsed
		}
	}
	if hookFileInfo != nil {
		hookMap["file_mod_time"] = hookFileInfo.ModTime().Format(time.RFC3339)
	}
	m["hook"] = hookMap

	m["content"] = map[string]any{
		"hash":            snap.LastContentHash,
		"last_change_at":  snap.LastContentChangeAt.Format(time.RFC3339),
		"last_change_ago": fmtSnapshotAge(snap.LastContentChangeAt, now),
	}

	paneDetected := string(snap.DetectedPaneStatus)
	tuiShows := string(snap.Status)
	m["detection"] = map[string]any{
		"pane_detected": paneDetected,
		"tui_shows":     tuiShows,
		"mismatch":      paneDetected != "" && paneDetected != tuiShows,
	}

	return m
}

// writeGoroutineDump writes every goroutine's stack to path. Used by the D-key
// snapshot and the worker-stall watchdog to pin a wedged status worker.
func writeGoroutineDump(path string) {
	buf := make([]byte, 1<<20) // 1 MiB; grow until the full dump fits
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	// 0600: goroutine stacks are user-private (may embed paths/args).
	_ = os.WriteFile(path, buf, 0600)
}

// writeWorkerStallDump is the dev-only watchdog's output: a goroutine dump plus
// a small stall.json, written under the snapshots dir so the debug-status /
// debug-perf skills discover it alongside manual snapshots. Returns the dir, or
// "" on failure.
func writeWorkerStallDump(stalledFor time.Duration, cycleStart time.Time) string {
	now := time.Now()
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".config", "fleet", "snapshots",
		now.Format("2006-01-02T15-04-05")+"_worker-stall")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}

	writeGoroutineDump(filepath.Join(dir, "goroutines.txt"))

	meta := map[string]any{
		"captured_at": now.Format(time.RFC3339Nano),
		"reason":      "status worker stall",
		"stalled_for": stalledFor.Round(time.Millisecond).String(),
	}
	if !cycleStart.IsZero() {
		meta["cycle_in_flight_for"] = now.Sub(cycleStart).Round(time.Millisecond).String()
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "stall.json"), data, 0644)
	return dir
}

func fmtSnapshotAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		return "0s"
	}
	return d.Round(time.Millisecond).String()
}

// claudeLogStat holds activity-recency signals derived from a Claude JSONL
// transcript in a single streaming pass.
type claudeLogStat struct {
	entries         int
	lastEntryAt     time.Time // last timestamped entry (any)
	lastLeadEntryAt time.Time // last non-sidechain entry (excludes sub-agent output)
	recentGaps      []float64 // seconds between the last ~9 timestamped entries
}

// readClaudeLogStat streams a Claude JSONL transcript once, extracting how
// recently it was appended. The recency vs. a "waiting" hook's timestamp tells
// us whether the agent already resumed (no resume hook fires on permission-grant
// or AskUserQuestion-answer). Returns nil if the file is unreadable or has no
// timestamped entries. Uses bufio.Reader (not Scanner) to tolerate the very long
// lines big tool results produce.
func readClaudeLogStat(path string) *claudeLogStat {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	st := &claudeLogStat{}
	var recent []time.Time
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		if strings.Contains(line, `"timestamp"`) {
			var e struct {
				Timestamp   string `json:"timestamp"`
				IsSidechain bool   `json:"isSidechain"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) == nil && e.Timestamp != "" {
				if t, perr := time.Parse(time.RFC3339, e.Timestamp); perr == nil {
					st.entries++
					st.lastEntryAt = t
					if !e.IsSidechain {
						st.lastLeadEntryAt = t
					}
					recent = append(recent, t)
					if len(recent) > 9 {
						recent = recent[1:]
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if st.entries == 0 {
		return nil
	}
	for i := 1; i < len(recent); i++ {
		st.recentGaps = append(st.recentGaps, math.Round(recent[i].Sub(recent[i-1]).Seconds()*10)/10)
	}
	return st
}

// buildClaudeLogBlock renders the derived conversation-log signal for snapshot.json.
// advanced_past_hook flags that the transcript moved >2s beyond the hook timestamp —
// i.e. the user acted and the agent resumed, so a lingering "waiting" hook is stale.
// The 2s tolerance absorbs the hook's whole-second ts and the prompt-triggering
// entry that shares the hook's instant.
func buildClaudeLogBlock(st *claudeLogStat, hookUpdatedAt, now time.Time) map[string]any {
	if st == nil {
		return nil
	}
	m := map[string]any{
		"file":           "claude_session.jsonl",
		"entries":        st.entries,
		"last_entry_at":  st.lastEntryAt.UTC().Format(time.RFC3339Nano),
		"last_entry_age": fmtSnapshotAge(st.lastEntryAt, now),
		"recent_gaps_s":  st.recentGaps,
	}
	if !st.lastLeadEntryAt.IsZero() {
		m["last_lead_entry_at"] = st.lastLeadEntryAt.UTC().Format(time.RFC3339Nano)
		if !hookUpdatedAt.IsZero() {
			delta := st.lastLeadEntryAt.Sub(hookUpdatedAt).Seconds()
			m["seconds_past_hook"] = math.Round(delta*10) / 10
			m["advanced_past_hook"] = delta > 2
		}
	}
	return m
}

// claudeSessionStatus mirrors the fields we read from Claude Code's own
// per-process status file (~/.claude/sessions/<pid>.json). status is Claude's
// first-party flag (busy / idle / shell); statusUpdatedAt is epoch-ms. raw holds
// the untouched file bytes so the snapshot can freeze a verbatim copy.
type claudeSessionStatus struct {
	raw             []byte
	alive           bool   // liveness verdict, computed once during the match
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	Name            string `json:"name"`
	Version         string `json:"version"`
}

// findClaudeSessionStatus scans ~/.claude/sessions/*.json for the file(s) whose
// sessionId matches claudeSessionID and returns the best match. The dir is keyed
// by pid, so a session can have a stale dead-process file alongside the live one:
// a live process wins over a dead one, and newest statusUpdatedAt breaks ties.
// Returns nil if nothing matches.
func findClaudeSessionStatus(claudeSessionID string) *claudeSessionStatus {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return findClaudeSessionStatusIn(filepath.Join(home, ".claude", "sessions"), claudeSessionID)
}

// findClaudeSessionStatusIn is findClaudeSessionStatus with the sessions dir
// injected, so the match/liveness/tiebreak logic is testable off a temp dir.
func findClaudeSessionStatusIn(dir, claudeSessionID string) *claudeSessionStatus {
	if claudeSessionID == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var best *claudeSessionStatus
	var bestAlive bool
	for _, p := range matches {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var css claudeSessionStatus
		if json.Unmarshal(raw, &css) != nil || css.SessionID != claudeSessionID {
			continue
		}
		css.raw = raw
		css.alive = proc.Alive(css.PID)
		if best == nil || (css.alive && !bestAlive) ||
			(css.alive == bestAlive && css.StatusUpdatedAt > best.StatusUpdatedAt) {
			pick := css
			best, bestAlive = &pick, css.alive
		}
	}
	return best
}

// buildClaudeStatusBlock renders the Claude session status file signal for
// snapshot.json. status is Claude Code's own busy/idle/shell flag; pid_alive
// distinguishes a live flag from a dead process's last-known value. Trust the
// value while alive, not status_age — the flag is event-driven, not a heartbeat,
// and it stays "idle" through /compact (so it can't explain a compaction stall).
// Caveat: pid_alive is PID-only — a recycled PID (the file's process exited and
// the OS reassigned its PID) can report a dead session's last status as live.
func buildClaudeStatusBlock(css *claudeSessionStatus, now time.Time) map[string]any {
	if css == nil {
		return nil
	}
	m := map[string]any{
		"file":      "claude_session_status.json",
		"pid":       css.PID,
		"pid_alive": css.alive,
		"status":    css.Status,
		"name":      css.Name,
		"version":   css.Version,
	}
	if css.StatusUpdatedAt > 0 {
		t := time.UnixMilli(css.StatusUpdatedAt)
		m["status_updated_at"] = t.UTC().Format(time.RFC3339Nano)
		m["status_age"] = fmtSnapshotAge(t, now)
	}
	return m
}

// copyFileStreaming copies src to dst without loading the whole file into memory
// (Claude transcripts reach tens of MB).
func copyFileStreaming(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, in)
}

func readFilteredDebugLog(sessionID string, maxLines int) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	logPath := filepath.Join(home, ".config", "fleet", "debug.log")
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, sessionID) {
			lines = append(lines, line)
			if len(lines) > maxLines*2 {
				lines = lines[len(lines)-maxLines:]
			}
		}
	}

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

var pathSanitizer = regexp.MustCompile(`[^a-zA-Z0-9-]`)

func sanitizeForPath(title string) string {
	s := strings.ReplaceAll(title, " ", "-")
	s = pathSanitizer.ReplaceAllString(s, "")
	if len(s) > 30 {
		s = s[:30]
	}
	if s == "" {
		s = "session"
	}
	return strings.ToLower(s)
}
