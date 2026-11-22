package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// antigravityHookKey names fleet's entry in Antigravity's hooks.json. The file
// is a map of named hooks shared by everything the user installs, so fleet owns
// exactly one key in it and never touches the others.
const antigravityHookKey = "fleet-status"

// antigravityHookTimeoutSec bounds one hook run. The handler takes
// milliseconds; Antigravity's default of 30s would only matter if it hung.
const antigravityHookTimeoutSec = 10

// antigravityCommand is one command hook. Antigravity rejects the whole file if
// a command entry lacks "command" — in either position.
type antigravityCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// antigravityMatcher is a tool event's entry: a tool-name regex and its hooks.
type antigravityMatcher struct {
	Matcher string               `json:"matcher"`
	Hooks   []antigravityCommand `json:"hooks"`
}

// antigravityFleetHooks is fleet's entry. The two shapes are not
// interchangeable (Antigravity 1.5.14, checked with `agy plugin validate`):
// an event with no tool takes command entries directly, and nesting them under
// "hooks" without a matcher makes the loader skip the WHOLE file — every hook
// the user has, not just fleet's — with nothing but a line in cli.log. A tool
// event takes matcher entries; a flat entry there runs nothing.
//
// Deliberately absent:
//   - PreToolUse: a PreToolUse hook that fails BLOCKS the tool call (measured:
//     a missing binary made agy refuse `cat`). A moved or deleted fleet binary
//     would then break tool use in every Antigravity session on the machine.
//     PreInvocation, PostToolUse and Stop fail open — the error is logged and
//     the agent carries on.
//   - SessionStart: Antigravity's own /hooks manager does not know the event
//     and writes it back as null whenever the user edits a hook there.
//     PreInvocation already marks a new conversation.
//   - PostInvocation: it adds nothing PreInvocation and Stop don't say.
type antigravityFleetHooks struct {
	// Enabled is the user's own switch from /hooks. Nil means on; fleet keeps a
	// false it finds rather than overriding the choice.
	Enabled       *bool                `json:"enabled,omitempty"`
	PreInvocation []antigravityCommand `json:"PreInvocation"`
	PostToolUse   []antigravityMatcher `json:"PostToolUse"`
	Stop          []antigravityCommand `json:"Stop"`
}

// GetAntigravityConfigDir returns the directory holding Antigravity's global
// hooks.json (~/.gemini/config). Antigravity documents no override for it.
// Hooks in ~/.gemini/antigravity-cli/settings.json are never loaded, so that
// is not where they go.
func GetAntigravityConfigDir() string {
	return filepath.Join(geminiHome(), "config")
}

// AntigravityAppDataDir is the CLI's own state (~/.gemini/antigravity-cli):
// conversation annotations, transcripts, logs.
func AntigravityAppDataDir() string {
	return filepath.Join(geminiHome(), "antigravity-cli")
}

func geminiHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".gemini")
	}
	return filepath.Join(home, ".gemini")
}

func antigravityCommandFor(event string) antigravityCommand {
	return antigravityCommand{
		Type:    "command",
		Command: nonFleetSessionGuard + GetHookCommand() + " --agent antigravity --event " + event,
		Timeout: antigravityHookTimeoutSec,
	}
}

// wantAntigravityHooks is fleet's entry as it should be, carrying the user's
// enabled switch over from what is there now.
func wantAntigravityHooks(enabled *bool) antigravityFleetHooks {
	return antigravityFleetHooks{
		Enabled:       enabled,
		PreInvocation: []antigravityCommand{antigravityCommandFor("PreInvocation")},
		PostToolUse:   []antigravityMatcher{{Matcher: ".*", Hooks: []antigravityCommand{antigravityCommandFor("PostToolUse")}}},
		Stop:          []antigravityCommand{antigravityCommandFor("Stop")},
	}
}

// antigravityHooksDoc is hooks.json decoded only as far as fleet needs: every
// other key stays raw so it is written back exactly as it was.
type antigravityHooksDoc struct {
	path string
	mode os.FileMode
	keys map[string]json.RawMessage
}

func readAntigravityHooks(configDir string) (*antigravityHooksDoc, error) {
	path := filepath.Join(configDir, "hooks.json")
	// Write through a symlinked hooks.json (dotfiles setups) rather than
	// renaming a regular file over the link.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	doc := &antigravityHooksDoc{path: path, mode: 0o644, keys: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read antigravity hooks.json: %w", err)
	}
	if info, err := os.Stat(path); err == nil {
		doc.mode = info.Mode().Perm()
	}
	// Antigravity accepts a UTF-8 BOM (Windows editors write one).
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return doc, nil
	}
	// A file that doesn't parse is the user's to fix: rewriting it would
	// destroy whatever they were in the middle of writing.
	if err := json.Unmarshal(data, &doc.keys); err != nil {
		return nil, fmt.Errorf("parse antigravity hooks.json: %w", err)
	}
	if doc.keys == nil { // the file held `null`
		doc.keys = map[string]json.RawMessage{}
	}
	return doc, nil
}

// fleetEntry decodes fleet's own key. ok is false when there is none.
func (d *antigravityHooksDoc) fleetEntry() (entry antigravityFleetHooks, ok bool) {
	raw, ok := d.keys[antigravityHookKey]
	if !ok {
		return entry, false
	}
	// A rewrite by Antigravity's /hooks manager adds null events and keeps
	// "enabled"; both decode cleanly into the struct, so an entry it merely
	// reformatted compares equal below and is not rewritten.
	if err := json.Unmarshal(raw, &entry); err != nil {
		return antigravityFleetHooks{}, false
	}
	return entry, true
}

func (d *antigravityHooksDoc) write() error {
	data, err := json.MarshalIndent(d.keys, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(d.path, append(data, '\n'), d.mode)
}

// InjectAntigravityHooks makes fleet's entry in configDir/hooks.json current,
// leaving every other key as it is. Returns true if the file was written.
func InjectAntigravityHooks(configDir string) (bool, error) {
	doc, err := readAntigravityHooks(configDir)
	if err != nil {
		return false, err
	}
	have, ok := doc.fleetEntry()
	want := wantAntigravityHooks(have.Enabled)
	if ok && sameAntigravityHooks(have, want) {
		return false, nil
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return false, fmt.Errorf("marshal antigravity hooks: %w", err)
	}
	doc.keys[antigravityHookKey] = raw
	if err := doc.write(); err != nil {
		debuglog.Logger.Error("antigravity hooks: write failed", "path", doc.path, "err", err)
		return false, err
	}
	debuglog.Logger.Info("antigravity hooks injected", "path", doc.path)
	return true, nil
}

func sameAntigravityHooks(a, b antigravityFleetHooks) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// RemoveAntigravityHooks deletes fleet's entry. Returns true if there was one.
func RemoveAntigravityHooks(configDir string) (bool, error) {
	doc, err := readAntigravityHooks(configDir)
	if err != nil {
		return false, err
	}
	if _, ok := doc.keys[antigravityHookKey]; !ok {
		return false, nil
	}
	delete(doc.keys, antigravityHookKey)
	return true, doc.write()
}

// RepairAntigravityHooks restores fleet's entry when it has gone missing or
// names a fleet binary that no longer exists. Returns true when it wrote.
//
// Going missing is routine, not hypothetical: Antigravity's /hooks manager
// writes hooks.json back from the copy it loaded when that session started, so
// editing any hook there erases an entry fleet installed after the session
// launched (measured). An entry the user switched off stays off.
//
// A missing config dir means Antigravity isn't set up here, and nothing is
// written: this never creates ~/.gemini for someone who doesn't use it.
func RepairAntigravityHooks(configDir string) (bool, error) {
	if _, err := os.Stat(configDir); err != nil {
		return false, nil
	}
	doc, err := readAntigravityHooks(configDir)
	if err != nil {
		return false, err
	}
	entry, ok := doc.fleetEntry()
	if !ok {
		debuglog.Logger.Warn("antigravity hooks: fleet's entry is missing, restoring it", "path", doc.path)
		return InjectAntigravityHooks(configDir)
	}
	if entry.Enabled != nil && !*entry.Enabled {
		return false, nil
	}
	if antigravityHookBinaryMissing(entry) {
		return InjectAntigravityHooks(configDir)
	}
	return false, nil
}

func antigravityHookBinaryMissing(entry antigravityFleetHooks) bool {
	commands := append(append([]antigravityCommand{}, entry.PreInvocation...), entry.Stop...)
	for _, m := range entry.PostToolUse {
		commands = append(commands, m.Hooks...)
	}
	for _, c := range commands {
		bin := fleetHookBinary(strings.TrimPrefix(c.Command, nonFleetSessionGuard))
		if bin == "" || !filepath.IsAbs(bin) {
			continue
		}
		// Strictly "not there", as in hookBinaryMissing.
		if _, err := os.Stat(bin); errors.Is(err, fs.ErrNotExist) {
			debuglog.Logger.Warn("antigravity hooks: repairing a hook command that no longer exists", "binary", bin)
			return true
		}
	}
	return false
}

// AntigravityHookState reports fleet's entry for diagnostics: "installed",
// "disabled" (switched off in /hooks), "stale" (present but not current),
// "missing", or "unreadable".
func AntigravityHookState(configDir string) string {
	doc, err := readAntigravityHooks(configDir)
	if err != nil {
		return "unreadable"
	}
	entry, ok := doc.fleetEntry()
	switch {
	case !ok:
		return "missing"
	case entry.Enabled != nil && !*entry.Enabled:
		return "disabled"
	case !sameAntigravityHooks(entry, wantAntigravityHooks(entry.Enabled)):
		return "stale"
	}
	return "installed"
}
