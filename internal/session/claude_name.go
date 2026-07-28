package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// claudeProjectDirReplacer mirrors Claude Code's per-cwd transcript dir naming.
// Empirically: realpath the cwd, then replace `/`, `.`, `_` with `-`.
var claudeProjectDirReplacer = strings.NewReplacer("/", "-", ".", "-", "_", "-")

// ClaudeProjectDirName returns the directory name Claude Code uses for cwd's
// transcript bag under ~/.claude/projects/. It realpaths cwd (so macOS symlinks
// like /var/... -> /private/var/... encode the same way Claude would), then
// substitutes path separators and the chars Claude treats as separators.
//
// If EvalSymlinks errors (e.g. the dir doesn't exist yet — possible for a
// freshly-created worktree dest), falls back to filepath.Clean so callers can
// build the encoded name before the dir exists.
func ClaudeProjectDirName(cwd string) string {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		resolved = filepath.Clean(cwd)
	}
	return claudeProjectDirReplacer.Replace(resolved)
}

// claudeProjectDir returns the absolute path of Claude's per-cwd transcript dir.
func claudeProjectDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects", ClaudeProjectDirName(cwd)), nil
}

// ClaudeTranscriptPath returns the absolute path of the JSONL conversation
// transcript Claude Code writes for claudeSessionID under projectPath's cwd.
// Returns "" if inputs are empty or the home dir can't be resolved. The file
// may not exist (e.g. Codex sessions have no Claude transcript) — callers should
// treat a missing file as "no transcript".
func ClaudeTranscriptPath(claudeSessionID, projectPath string) string {
	if claudeSessionID == "" || projectPath == "" {
		return ""
	}
	dir, err := claudeProjectDir(projectPath)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, claudeSessionID+".jsonl")
}

// ReadClaudeSessionName reads Claude's best title for a session from its JSONL
// conversation file. Claude writes two kinds of title entries: "custom-title"
// (an explicit /rename inside the session) and "ai-title" (a model-generated
// title that updates as the work evolves). A custom title wins when present;
// otherwise the latest ai-title is returned. Returns empty string if neither
// is found.
func ReadClaudeSessionName(claudeSessionID, projectPath string) string {
	if claudeSessionID == "" || projectPath == "" {
		return ""
	}

	projectDir, err := claudeProjectDir(projectPath)
	if err != nil {
		return ""
	}
	jsonlPath := filepath.Join(projectDir, claudeSessionID+".jsonl")

	var lastCustom, lastAI string
	_ = forEachTranscriptLine(jsonlPath, func(line string) bool {
		// Quick check before JSON parsing. Matches both "custom-title" and
		// "ai-title"; the Type check below filters any incidental hits.
		if !strings.Contains(line, "-title") {
			return true
		}

		var entry struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return true
		}
		switch entry.Type {
		case "custom-title":
			if entry.CustomTitle != "" {
				lastCustom = entry.CustomTitle
			}
		case "ai-title":
			if entry.AITitle != "" {
				lastAI = entry.AITitle
			}
		}
		return true
	})

	if lastCustom != "" {
		return lastCustom
	}
	return deslugify(lastAI)
}

// slugSeparators replaces a slug's word separators with spaces. Case is left
// untouched on purpose.
var slugSeparators = strings.NewReplacer("-", " ", "_", " ")

// deslugify turns a kebab/snake slug into spaced words while preserving each
// character's original case, so Claude's slug-style ai-titles read naturally:
//
//	native-ai-title-integration -> native ai title integration
//	fix-API-client              -> fix API client
//
// It deliberately does NOT capitalize anything (no sentence/title/pascal case),
// so an already-uppercase acronym like "API" survives but a lowercase word
// stays lowercase. Titles that already contain spaces are natural language, not
// slugs, and are returned unchanged.
func deslugify(s string) string {
	if s == "" || strings.Contains(s, " ") {
		return s
	}
	if !strings.ContainsAny(s, "-_") {
		return s
	}
	return slugSeparators.Replace(s)
}

// ErrParentTranscriptMissing is returned by CopyClaudeForkTranscript when the
// parent Claude conversation JSONL doesn't exist at the expected location.
var ErrParentTranscriptMissing = errors.New("parent Claude transcript not found")

// CopyClaudeForkTranscript stages a fork's parent conversation in the
// destination cwd's project dir so `claude --resume <parentID> --fork-session`
// run from destProjectPath finds the transcript. Returns ErrParentTranscriptMissing
// if the parent JSONL isn't where we expect.
//
// Copy is atomic: writes to a temp file in the dest dir then renames, so a
// concurrently-writing parent gives us a point-in-time snapshot. Overwrites
// any existing dest file (idempotent re-dup).
func CopyClaudeForkTranscript(parentSessionID, parentProjectPath, destProjectPath string) error {
	if parentSessionID == "" {
		return errors.New("parent session id is empty")
	}

	srcDir, err := claudeProjectDir(parentProjectPath)
	if err != nil {
		return fmt.Errorf("resolve parent project dir: %w", err)
	}
	dstDir, err := claudeProjectDir(destProjectPath)
	if err != nil {
		return fmt.Errorf("resolve dest project dir: %w", err)
	}
	srcPath := filepath.Join(srcDir, parentSessionID+".jsonl")
	dstPath := filepath.Join(dstDir, parentSessionID+".jsonl")

	src, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w at %s", ErrParentTranscriptMissing, srcPath)
		}
		return fmt.Errorf("open parent transcript: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create dest project dir: %w", err)
	}

	tmp, err := os.CreateTemp(dstDir, parentSessionID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("copy transcript: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		cleanup()
		return fmt.Errorf("rename temp to dest: %w", err)
	}
	return nil
}

// rotationHandoffWindow bounds how far apart a /clear-rotated session's first
// transcript entry and the previous (owner) session's last entry may be while
// still counting as a continuation. A /clear hands off within milliseconds; the
// window absorbs clock/flush skew but stays far tighter than the gap a concurrent
// nested child shows. (Linked rotations — continue/resume/fork — use the
// deterministic uuid signal below instead, which has no time dependence.)
const rotationHandoffWindow = 10 * time.Second

// rotationVerdict is the result of sessionRotationVerdict: whether a foreign Claude
// session_id continues the owner session (adopt), is a separate nested child (reject
// and neg-cache), or can't be decided yet because the transcript hasn't flushed (retry).
type rotationVerdict int

const (
	rotationNo      rotationVerdict = iota // a separate nested child — reject and neg-cache
	rotationYes                            // a confirmed rotation — adopt the new id
	rotationUnknown                        // undecidable this cycle (transcript not flushed) — retry
)

// sessionRotationVerdict reports whether newSessionID is ownerSessionID's conversation
// continued under a fresh id (a Claude session-id rotation) rather than a separate
// nested child claude that merely inherited the same FLEET_INSTANCE_ID.
//
// Claude rotates a session id three ways, and they leave different traces:
//   - continue / resume / fork: the new transcript opens with a
//     `compact_boundary`/`summary` entry whose `logicalParentUuid` points at the
//     prior session's tail uuid. Preserved history means the new file's first
//     timestamp is OLD (minutes-to-days before the handoff), so we match the
//     uuid against the owner transcript instead — deterministic, time-independent,
//     and a nested child can't match (its link, if any, points at its own parent).
//   - /clear: a fresh conversation (first user entry `parentUuid:null`, no link)
//     that hands off instantly, so the new file's first entry lands within
//     rotationHandoffWindow of the owner's last entry.
//   - in-place compaction keeps the same session id, so it never reaches here.
//
// When the proximity fallback can't read a timestamp from either transcript it returns
// rotationUnknown rather than rotationNo: a /clear rotation fires its SessionStart hook
// before the new transcript flushes its first timestamped entry (the file opens with
// timestamp-less mode/permission-mode/file-history-snapshot header entries), so a
// premature rotationNo would get neg-cached and freeze the in-memory hook on the dead
// owner session forever (issue #23). Unknown makes the caller retry next cycle.
func sessionRotationVerdict(projectPath, ownerSessionID, newSessionID string) rotationVerdict {
	if projectPath == "" || ownerSessionID == "" || newSessionID == "" {
		return rotationNo
	}
	newPath := ClaudeTranscriptPath(newSessionID, projectPath)
	ownerPath := ClaudeTranscriptPath(ownerSessionID, projectPath)

	// Primary: deterministic parent link (continue/resume/fork).
	if link := transcriptParentLink(newPath); link != "" && transcriptContainsUUID(ownerPath, link) {
		return rotationYes
	}

	// Fallback: instant timestamp handoff (/clear). Undecidable until both transcripts
	// have a readable timestamp — see the note above on the SessionStart-vs-flush race.
	newFirst := firstTranscriptTimestamp(newPath)
	ownerLast := lastTranscriptTimestamp(ownerPath)
	if newFirst.IsZero() || ownerLast.IsZero() {
		return rotationUnknown
	}
	delta := newFirst.Sub(ownerLast)
	if delta < 0 {
		delta = -delta
	}
	if delta <= rotationHandoffWindow {
		return rotationYes
	}
	return rotationNo
}

// transcriptParentLink returns the logicalParentUuid of the first
// compact_boundary/summary entry in the transcript at path — the link a
// continue/resume/fork rotation records to its parent session's tail uuid — or
// "" if the transcript opens with no such entry (a /clear-fresh or brand-new
// session). Only the first such entry matters: later compact_boundary entries in
// the same file are in-place compactions that link within this file.
func transcriptParentLink(path string) string {
	if path == "" {
		return ""
	}
	var link string
	_ = forEachTranscriptLine(path, func(line string) bool {
		if !strings.Contains(line, "logicalParentUuid") {
			return true
		}
		var entry struct {
			Type              string `json:"type"`
			Subtype           string `json:"subtype"`
			LogicalParentUUID string `json:"logicalParentUuid"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return true
		}
		if entry.LogicalParentUUID != "" && (entry.Subtype == "compact_boundary" || entry.Type == "summary") {
			link = entry.LogicalParentUUID
			return false
		}
		return true
	})
	return link
}

// transcriptWarnInterval throttles the warnings below. They sit on per-cycle paths
// — ReadClaudeSessionName re-reads a transcript on every worker cycle while a
// session is still unnamed (app.go sets recheck=0 inside agentNameFreshPollWindow),
// then ~every 30s — and the conditions they report are sticky: EACCES does not clear
// itself, and an over-cap entry is re-encountered on every scan. Unthrottled, one
// broken transcript writes a line per session per cycle for the life of the process.
// debug.log's last 100 lines are what the bug-report flow pastes into a public issue,
// so that drip would evict the diagnostics these warnings exist to preserve. Same
// shape and interval as rotRejectLogInterval.
const transcriptWarnInterval = time.Minute

// transcriptWarnedAt maps reason+path to the last time that pair was logged.
var transcriptWarnedAt sync.Map

// warnTranscript logs at Warn at most once per transcriptWarnInterval per
// (reason, path).
//
// Warn rather than Debug on purpose: debuglog only enables Debug when FLEET_DEBUG is
// set, so a Debug line would be absent from exactly the bug reports these warnings
// are for. Throttling is what makes Warn affordable on a per-cycle path.
func warnTranscript(reason, path string, args ...any) {
	key := reason + "\x00" + path
	now := time.Now()
	if prev, ok := transcriptWarnedAt.Load(key); ok && now.Sub(prev.(time.Time)) < transcriptWarnInterval {
		return
	}
	transcriptWarnedAt.Store(key, now)
	debuglog.Logger.Warn(reason, append([]any{"path", path}, args...)...)
}

// transcriptLineCap bounds how much of one JSONL line is held in memory. Past it
// the remainder of that line is discarded and the walk continues with the next —
// losing one entry is recoverable (its neighbours are usually seconds away),
// whereas abandoning the walk silently turns the entire rest of the file into
// "doesn't exist", which is the failure this helper exists to prevent.
const transcriptLineCap = 8 << 20 // 8MB

// forEachTranscriptLine calls fn for each non-empty line of the JSONL transcript
// at path, stopping early when fn returns false.
//
// Deliberately not bufio.Scanner. A Scanner has a maximum token size and reports
// an over-long line by ending the loop with ErrTooLong — which reads exactly like
// a clean EOF unless the caller checks scanner.Err(), and none of the callers here
// did. Transcript lines routinely get large: an entry carrying a pasted image or a
// big tool result is one JSON line, and 4 of 554 local transcripts held a line over
// 1MB, the worst of them 2% of the way into the file. The answers that came back
// were not visibly broken, just stale — a timestamp from sixteen days earlier, a
// uuid reported absent — and every consumer treats them as authoritative. That
// silently disabled the between-bursts tiebreaker in conversationActivePastHook and
// biased sessionRotationVerdict toward rejecting genuine rotations.
// Callers that only need a best-effort answer discard the returned error; the
// helper has already logged anything beyond a routine missing file.
func forEachTranscriptLine(path string, fn func(line string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		// A missing transcript is routine — the session may not have written one
		// yet, or belongs to an agent that keeps none. Anything else (permissions,
		// a bad path) leaves callers returning a zero value that reads exactly like
		// "nothing happened", so it must not pass in silence.
		if !errors.Is(err, os.ErrNotExist) {
			warnTranscript("transcript: open failed", path, "err", err)
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)
	var buf []byte
	overCap := false
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Mid-line: accumulate until the cap, then drop the rest of this line.
			if !overCap {
				if len(buf)+len(chunk) > transcriptLineCap {
					overCap, buf = true, nil
					// Say so. A skipped entry leaves a complete-looking walk that is
					// missing one line, and if that line was the last, the callers here
					// return the entry before it — stale, and indistinguishable from a
					// real answer. That is the failure this helper replaced, at a
					// smaller scale, so it must not be the one it reintroduces.
					warnTranscript("transcript: entry over cap, skipped", path,
						"cap_bytes", transcriptLineCap)
				} else {
					buf = append(buf, chunk...)
				}
			}
			continue
		}
		if !overCap {
			buf = append(buf, chunk...)
			if line := strings.TrimRight(string(buf), "\r\n"); line != "" && !fn(line) {
				return nil
			}
		}
		buf, overCap = buf[:0], false
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			// Partial walk. Whatever the caller returns is derived from a prefix of
			// the file, which is precisely the failure this helper exists to end —
			// so say so rather than letting a stale answer look authoritative.
			warnTranscript("transcript: read failed, result is partial", path, "err", err)
			return err
		}
	}
}

// transcriptContainsUUID reports whether any entry in the transcript at path
// carries the given uuid. Used to confirm a child's logicalParentUuid actually
// belongs to the owner transcript (vs a nested child's own parent).
func transcriptContainsUUID(path, uuid string) bool {
	if path == "" || uuid == "" {
		return false
	}
	found := false
	_ = forEachTranscriptLine(path, func(line string) bool {
		if !strings.Contains(line, uuid) {
			return true
		}
		var entry struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.UUID == uuid {
			found = true
			return false
		}
		return true
	})
	return found
}

// firstTranscriptTimestamp returns the timestamp of the earliest timestamped JSONL
// entry in the transcript at path, or the zero time if it's missing/unreadable or
// has no timestamped entries.
func firstTranscriptTimestamp(path string) time.Time {
	return scanTranscriptTimestamp(path, false, false)
}

// lastTranscriptTimestamp returns the timestamp of the latest timestamped JSONL
// entry in the transcript at path, or the zero time. It scans the whole file;
// transcripts are small and this runs only on the rare session-id-change path.
func lastTranscriptTimestamp(path string) time.Time {
	return scanTranscriptTimestamp(path, true, false)
}

// lastLeadTranscriptTimestamp returns the timestamp of the last lead-conversation
// (non-sidechain) entry in the transcript at path, or the zero time if it's
// missing/unreadable or has no such entries. Sub-agent (sidechain) entries are
// skipped so a still-running sub-agent can't mask a finished lead turn. Used as the
// out-of-pane tiebreaker for the between-bursts frame where the pane is
// indistinguishable from finished (see conversationActivePastHook).
func lastLeadTranscriptTimestamp(path string) time.Time {
	return scanTranscriptTimestamp(path, true, true)
}

// scanTranscriptTimestamp walks the JSONL transcript at path and returns the first
// (last=false) or last (last=true) entry timestamp it can parse. When excludeSidechain
// is set, sub-agent (isSidechain:true) entries are skipped.
func scanTranscriptTimestamp(path string, last bool, excludeSidechain bool) time.Time {
	if path == "" {
		return time.Time{}
	}
	var result time.Time
	_ = forEachTranscriptLine(path, func(line string) bool {
		if !strings.Contains(line, `"timestamp"`) {
			return true
		}
		var entry struct {
			Timestamp   string `json:"timestamp"`
			IsSidechain bool   `json:"isSidechain"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Timestamp == "" {
			return true
		}
		if excludeSidechain && entry.IsSidechain {
			return true
		}
		// RFC3339Nano: Claude transcripts stamp millisecond precision (e.g.
		// "...:04.226Z"). The layout also parses entries with no fractional part.
		ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			return true
		}
		result = ts
		return last // first match wins when we only want the earliest entry
	})
	return result
}
