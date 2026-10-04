package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// copilotHookEvents lists the Copilot CLI hook events fleet subscribes to.
//
// Deliberately absent:
//   - permissionRequest: Copilot treats a failing permissionRequest hook as a
//     DENY, so a moved or deleted fleet binary would refuse every tool call in
//     every Copilot session on the machine. It also fires for auto-approved
//     tools, so it was never a "waiting" signal; notification is.
//   - sessionStart: it fires lazily, after the first userPromptSubmitted, so a
//     status from it would flip the first turn to finished while it runs.
//   - subagentStop, errorOccurred: a subagent finishing is not the session
//     finishing, and a tool error is usually recovered from.
var copilotHookEvents = []string{
	"userPromptSubmitted",
	"postToolUse",
	"postToolUseFailure",
	"notification",
	"agentStop",
	"sessionEnd",
}

// copilotHooksFile is fleet's own file in Copilot's user hooks dir. Copilot
// loads every hooks/*.json there, so fleet never merges into a file it doesn't
// own: install writes this file whole, uninstall deletes it.
const copilotHooksFile = "fleet.json"

// GetCopilotConfigDir returns Copilot CLI's home ($COPILOT_HOME or ~/.copilot).
func GetCopilotConfigDir() string {
	if dir := os.Getenv("COPILOT_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".copilot")
	}
	return filepath.Join(home, ".copilot")
}

// CopilotSessionDir is where Copilot keeps one conversation's state
// (events.jsonl, workspace.yaml). Only top-level conversations get one.
func CopilotSessionDir(sessionID string) string {
	return filepath.Join(GetCopilotConfigDir(), "session-state", sessionID)
}

type copilotHookEntry struct {
	Type       string `json:"type"`
	Bash       string `json:"bash"`
	TimeoutSec int    `json:"timeoutSec"`
}

type copilotHooksDoc struct {
	Version int                           `json:"version"`
	Hooks   map[string][]copilotHookEntry `json:"hooks"`
}

// copilotNonFleetGuard ends the hook in the shell for a Copilot session fleet
// didn't launch. Copilot blocks on every hook, for every session on the
// machine, so this saves each of those a fleet process start (~9ms).
const copilotNonFleetGuard = `[ -n "$FLEET_INSTANCE_ID" ] || exit 0; exec `

// copilotHooksJSON renders fleet's hooks file. Copilot's payload carries no
// event name (notification aside), so each command names its own event.
func copilotHooksJSON() ([]byte, error) {
	doc := copilotHooksDoc{Version: 1, Hooks: map[string][]copilotHookEntry{}}
	for _, ev := range copilotHookEvents {
		doc.Hooks[ev] = []copilotHookEntry{{
			Type:       "command",
			Bash:       copilotNonFleetGuard + GetHookCommand() + " --agent copilot --event " + ev,
			TimeoutSec: 10,
		}}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// InjectCopilotHooks writes fleet's hooks file into configDir/hooks. Returns
// true if the file was written (changed).
func InjectCopilotHooks(configDir string) (bool, error) {
	data, err := copilotHooksJSON()
	if err != nil {
		return false, fmt.Errorf("marshal copilot hooks: %w", err)
	}
	path := filepath.Join(configDir, "hooks", copilotHooksFile)
	if orig, err := os.ReadFile(path); err == nil && bytes.Equal(orig, data) {
		return false, nil
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		debuglog.Logger.Error("copilot hooks: write failed", "path", path, "err", err)
		return false, err
	}
	debuglog.Logger.Info("copilot hooks injected", "path", path)
	return true, nil
}

// RemoveCopilotHooks deletes fleet's hooks file. Returns true if one was removed.
func RemoveCopilotHooks(configDir string) (bool, error) {
	err := os.Remove(filepath.Join(configDir, "hooks", copilotHooksFile))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// RepairCopilotHooks rewrites fleet's hooks file when the binary it names no
// longer exists — the deleted-worktree build hookBinaryMissing describes. A
// missing file is left alone, so this never installs hooks for a user without
// Copilot. Returns true when it wrote.
func RepairCopilotHooks(configDir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "hooks", copilotHooksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var doc copilotHooksDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse copilot hooks file: %w", err)
	}
	for _, entries := range doc.Hooks {
		for _, e := range entries {
			bin := fleetHookBinary(strings.TrimPrefix(e.Bash, copilotNonFleetGuard))
			if bin == "" || !filepath.IsAbs(bin) {
				continue
			}
			// Strictly "not there", as in hookBinaryMissing.
			if _, err := os.Stat(bin); errors.Is(err, fs.ErrNotExist) {
				debuglog.Logger.Warn("copilot hooks: repairing a hook command that no longer exists", "binary", bin)
				return InjectCopilotHooks(configDir)
			}
		}
	}
	return false, nil
}

// CopilotHooksInstalled reports whether fleet's hooks file is present and current.
func CopilotHooksInstalled(configDir string) bool {
	want, err := copilotHooksJSON()
	if err != nil {
		return false
	}
	got, err := os.ReadFile(filepath.Join(configDir, "hooks", copilotHooksFile))
	return err == nil && bytes.Equal(got, want)
}

// EnsureCopilotDirTrust adds projectPath to trustedFolders in Copilot's
// config.json, so a session there doesn't open on the "Do you trust the files
// in this folder?" menu — behind which no hook fires at all.
//
// config.json, not settings.json: Copilot migrates a settings.json entry into
// config.json and drops it when config.json already has the key. The file is
// re-serialized: keys come out sorted and comments other than the leading `//`
// header are dropped. Copilot owns the file and rewrites it itself, so only
// the values matter, and those are kept. An entry is matched exactly:
// whether Copilot honours a trusted parent is not something fleet can check.
func EnsureCopilotDirTrust(configDir, projectPath string) error {
	if projectPath == "" {
		return nil
	}
	// File lock first: a stuck holder then costs concurrent seeds one wait in
	// parallel, not one each in turn. Can't deadlock — whoever holds the mutex
	// already has the flock or gave up on it.
	defer lockCopilotTrust(configDir)()
	copilotTrustMu.Lock()
	defer copilotTrustMu.Unlock()
	if real, err := filepath.EvalSymlinks(projectPath); err == nil {
		projectPath = real
	}
	path := filepath.Join(configDir, "config.json")
	// Write through a symlinked config.json (dotfiles setups) rather than
	// renaming a regular file over the link.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}

	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read copilot config.json: %w", err)
	}
	root := map[string]json.RawMessage{}
	if body := bytes.TrimSpace(stripJSONComments(orig)); len(body) > 0 {
		if err := json.Unmarshal(body, &root); err != nil {
			return fmt.Errorf("parse copilot config.json (refusing to rewrite it): %w", err)
		}
	}
	var trusted []string
	if raw, ok := root["trustedFolders"]; ok {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return fmt.Errorf("parse copilot trustedFolders (refusing to rewrite it): %w", err)
		}
	}
	if slices.Contains(trusted, projectPath) {
		return nil
	}
	trusted = append(trusted, projectPath)
	if root["trustedFolders"], err = marshalJSON(trusted, ""); err != nil {
		return err
	}
	body, err := marshalJSON(root, "  ")
	if err != nil {
		return err
	}
	out := append(leadingComments(orig), body...)

	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := writeFileAtomic(path, out, mode); err != nil {
		return err
	}
	debuglog.Logger.Info("copilot dir trust seeded", "path", projectPath)
	return nil
}

// trustMu serializes Codex's folder-trust seeds: each is a read-modify-write of
// one config file, and session launches run them on their own goroutines.
var trustMu sync.Mutex

// copilotTrustMu serializes Copilot trust seeds within this process. Separate
// from trustMu so a stuck Copilot lock can never hold up a Codex seed.
var copilotTrustMu sync.Mutex

// copilotTrustLockWait bounds the wait for Copilot's trust lock. Copilot holds
// it for one small file write, so anything longer is a stuck holder.
var copilotTrustLockWait = 2 * time.Second

// lockCopilotTrust takes the lock Copilot itself holds while it edits
// trustedFolders: flock(LOCK_EX) on config.json.trusted-folders.lock beside
// config.json (Copilot 1.0.91, measured). copilotTrustMu only covers this
// process; this also covers other fleet processes (parallel `fleet wt`) and
// Copilot. It must be flock — a POSIX lock doesn't exclude it on Linux — and
// the file is never removed, or two holders could lock two different files.
//
// Best effort: if the lock can't be had within copilotTrustLockWait, or the
// filesystem can't lock at all, the seed goes ahead unlocked — a lost entry
// shows as Copilot's trust menu, a session that never launches doesn't show
// at all. The returned func releases whatever was taken.
func lockCopilotTrust(configDir string) func() {
	noop := func() {}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return noop
	}
	f, err := os.OpenFile(filepath.Join(configDir, "config.json.trusted-folders.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		debuglog.Logger.Warn("copilot trust: lock unavailable, seeding unlocked", "err", err)
		return noop
	}
	deadline := time.Now().Add(copilotTrustLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			debuglog.Logger.Warn("copilot trust: lock not taken, seeding unlocked", "err", err)
			_ = f.Close()
			return noop
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// marshalJSON is json.MarshalIndent without HTML escaping, so a value like
// "a & b" stays as Copilot wrote it rather than becoming "a \u0026 b". The
// result ends in a newline.
func marshalJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// leadingComments returns the `//` comment lines before the JSON body, which
// Copilot writes at the top of config.json.
func leadingComments(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte("\n"))
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("//")) && len(trimmed) > 0 {
			break
		}
		if len(trimmed) > 0 {
			out = append(append(out, trimmed...), '\n')
		}
		data = rest
	}
	return out
}

// stripJSONComments removes `//` and `/* */` comments outside string literals.
// String-aware on purpose: config.json holds "https://github.com", which a
// naive stripper would truncate into invalid JSON.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr, esc := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// writeFileAtomic writes via a uniquely named temp file + rename, so a reader
// (Copilot loads hooks and config at startup) never sees a half-written file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpPath, mode)
	}
	if werr == nil {
		werr = os.Rename(tmpPath, path)
	}
	if werr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", path, werr)
	}
	return nil
}
