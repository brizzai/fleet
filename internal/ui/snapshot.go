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
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
)

type statusSnapshotMsg struct {
	path string
	err  error
}

func captureStatusSnapshot(s *session.Session, sessionID string) statusSnapshotMsg {
	ts := s.GetTmuxSession()
	if ts == nil {
		return statusSnapshotMsg{err: fmt.Errorf("no tmux session")}
	}

	// 1. Fresh pane capture with ANSI.
	rawPane, err := ts.CapturePaneFresh()
	if err != nil {
		return statusSnapshotMsg{err: fmt.Errorf("pane capture: %w", err)}
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
		return statusSnapshotMsg{err: fmt.Errorf("mkdir: %w", err)}
	}

	// 6. Freeze the Claude conversation transcript + derive its activity signal.
	// The JSONL is appended on real conversation events, so its recency tells us
	// whether the agent resumed past a stale "waiting" hook — the signal pane
	// scraping misses. Best-effort: Codex sessions have no Claude transcript.
	var claudeLog map[string]any
	if jsonlPath := session.ClaudeTranscriptPath(snap.ClaudeSessionID, snap.ProjectPath); jsonlPath != "" {
		if st := readClaudeLogStat(jsonlPath); st != nil {
			claudeLog = buildClaudeLogBlock(st, snap.HookUpdatedAt, now)
			_, _ = copyFileStreaming(jsonlPath, filepath.Join(snapshotDir, "claude_session.jsonl"))
		}
	}

	// 7. Write files.
	_ = os.WriteFile(filepath.Join(snapshotDir, "pane_raw.txt"), []byte(rawPane), 0644)
	_ = os.WriteFile(filepath.Join(snapshotDir, "pane_clean.txt"), []byte(session.StripANSI(rawPane)), 0644)
	_ = os.WriteFile(filepath.Join(snapshotDir, "debug_tail.txt"), []byte(debugTail), 0644)

	meta := buildSnapshotJSON(snap, hookFileContent, hookFileInfo, now, claudeLog)
	jsonData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(snapshotDir, "snapshot.json"), jsonData, 0644)

	debuglog.Logger.Info("status snapshot captured", "dir", snapshotDir, "session", sessionID)

	return statusSnapshotMsg{path: snapshotDir}
}

func buildSnapshotJSON(snap session.StatusSnapshot, hookFileRaw []byte, hookFileInfo os.FileInfo, now time.Time, claudeLog map[string]any) map[string]any {
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
