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
	"time"
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

	f, err := os.Open(jsonlPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max buffer

	var lastCustom, lastAI string
	for scanner.Scan() {
		line := scanner.Text()
		// Quick check before JSON parsing. Matches both "custom-title" and
		// "ai-title"; the Type check below filters any incidental hits.
		if !strings.Contains(line, "-title") {
			continue
		}

		var entry struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
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
	}

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

// rotationHandoffWindow bounds how far apart a rotated session's first transcript
// entry and the previous (owner) session's last entry may be while still counting
// as a continuation. Claude Code's session-id rotations (compaction, /clear,
// continue) hand off within milliseconds; the window absorbs clock/flush skew but
// stays far tighter than the gap a concurrent nested child shows.
const rotationHandoffWindow = 10 * time.Second

// isSessionRotation reports whether newSessionID is ownerSessionID's conversation
// continued under a fresh id (a Claude session-id rotation) rather than a separate
// nested child claude that merely inherited the same FLEET_INSTANCE_ID.
//
// The signal is transcript continuity: a rotation's new transcript begins right
// where the owner's ends, so the new file's first entry and the owner file's last
// entry land within rotationHandoffWindow of each other. A concurrent child starts
// mid-way through the owner's life and the owner keeps appending past it, so the
// two never line up.
//
// When either transcript can't be read we return false — the safe default, since
// wrongly adopting a real nested child would clobber the owner's status and resume
// id.
func isSessionRotation(projectPath, ownerSessionID, newSessionID string) bool {
	if projectPath == "" || ownerSessionID == "" || newSessionID == "" {
		return false
	}
	newFirst := firstTranscriptTimestamp(ClaudeTranscriptPath(newSessionID, projectPath))
	ownerLast := lastTranscriptTimestamp(ClaudeTranscriptPath(ownerSessionID, projectPath))
	if newFirst.IsZero() || ownerLast.IsZero() {
		return false
	}
	delta := newFirst.Sub(ownerLast)
	if delta < 0 {
		delta = -delta
	}
	return delta <= rotationHandoffWindow
}

// firstTranscriptTimestamp returns the timestamp of the earliest timestamped JSONL
// entry in the transcript at path, or the zero time if it's missing/unreadable or
// has no timestamped entries.
func firstTranscriptTimestamp(path string) time.Time { return scanTranscriptTimestamp(path, false) }

// lastTranscriptTimestamp returns the timestamp of the latest timestamped JSONL
// entry in the transcript at path, or the zero time. It scans the whole file;
// transcripts are small and this runs only on the rare session-id-change path.
func lastTranscriptTimestamp(path string) time.Time { return scanTranscriptTimestamp(path, true) }

// scanTranscriptTimestamp walks the JSONL transcript at path and returns the
// first (last=false) or last (last=true) entry timestamp it can parse.
func scanTranscriptTimestamp(path string, last bool) time.Time {
	if path == "" {
		return time.Time{}
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	var result time.Time
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"timestamp"`) {
			continue
		}
		var entry struct {
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Timestamp == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, entry.Timestamp)
		if err != nil {
			continue
		}
		if !last {
			return ts
		}
		result = ts
	}
	return result
}
