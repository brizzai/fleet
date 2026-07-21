package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
)

// cursorHookEvents lists the Cursor CLI hook events fleet subscribes to.
// Cursor's payload field names match Claude's (hook_event_name, session_id,
// prompt on beforeSubmitPrompt), so `fleet hook-handler` is reused unchanged;
// only the event names and hooks.json shape are Cursor-specific.
var cursorHookEvents = []string{
	"sessionStart",
	"beforeSubmitPrompt",
	"beforeShellExecution",
	"afterShellExecution",
	"stop",
	"sessionEnd",
}

// cursorHookEntry represents a single hook entry in Cursor's hooks.json. Unlike
// Claude/Codex's nested {matcher, hooks:[...]} shape, Cursor's schema is flat:
// each event maps directly to an array of entries.
type cursorHookEntry struct {
	Command string `json:"command"`
	Type    string `json:"type,omitempty"`
}

// GetCursorConfigDir returns the Cursor CLI config directory (~/.cursor). No
// env override is documented for Cursor (unlike CODEX_HOME/CLAUDE_CONFIG_DIR).
func GetCursorConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".cursor")
	}
	return filepath.Join(home, ".cursor")
}

// InjectCursorHooks merges fleet hook entries into Cursor's hooks.json,
// preserving any existing user hooks. Returns true if the file was written
// (changed).
//
// hooks.json shape: {"version": 1, "hooks": {"<event>": [ {"command","type"} ]}}
// — flat per event, so the merge is simpler than Claude/Codex's matcher-grouped
// shape and doesn't reuse mergeHookEvent/claudeHookMatcher.
func InjectCursorHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")

	var root map[string]json.RawMessage
	orig, err := os.ReadFile(hooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			debuglog.Logger.Error("cursor hooks: failed to read hooks.json", "path", hooksPath, "err", err)
			return false, fmt.Errorf("read hooks.json: %w", err)
		}
		root = make(map[string]json.RawMessage)
	} else {
		if err := json.Unmarshal(orig, &root); err != nil {
			debuglog.Logger.Error("cursor hooks: failed to parse hooks.json", "path", hooksPath, "err", err)
			return false, fmt.Errorf("parse hooks.json: %w", err)
		}
	}

	var events map[string]json.RawMessage
	if raw, ok := root["hooks"]; ok {
		// Fail closed: emptying `events` here would drop the user's existing hooks
		// on the write below, contradicting this function's preserve-user-hooks
		// contract. Refuse to touch the file when the section is unparseable.
		if err := json.Unmarshal(raw, &events); err != nil {
			debuglog.Logger.Error("cursor hooks: failed to parse hooks section", "err", err)
			return false, fmt.Errorf("parse hooks section (refusing to overwrite user hooks): %w", err)
		}
	} else {
		events = make(map[string]json.RawMessage)
	}

	for _, event := range cursorHookEvents {
		merged, err := mergeCursorHookEvent(events[event])
		if err != nil {
			debuglog.Logger.Error("cursor hooks: failed to parse event entries", "event", event, "err", err)
			return false, fmt.Errorf("parse %q entries (refusing to overwrite user hooks): %w", event, err)
		}
		events[event] = merged
	}

	eventsRaw, err := json.Marshal(events)
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	root["hooks"] = eventsRaw

	versionRaw, err := json.Marshal(1)
	if err != nil {
		return false, fmt.Errorf("marshal version: %w", err)
	}
	root["version"] = versionRaw

	finalData, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal hooks.json: %w", err)
	}

	// Idempotent: skip the write (and the "changed" signal) if nothing changed.
	if bytes.Equal(bytes.TrimSpace(orig), bytes.TrimSpace(finalData)) {
		return false, nil
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	// A uniquely-named temp file (like WriteStatusFile) rather than a fixed
	// "hooks.json.tmp": multiple fleet instances can start concurrently, and a
	// shared tmp path would let one instance's rename race another's, failing
	// with ENOENT or losing a write and leaving Cursor hooks uninstalled.
	tmp, err := os.CreateTemp(configDir, "hooks.*.json.tmp")
	if err != nil {
		return false, fmt.Errorf("create hooks.json.tmp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(finalData); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write hooks.json.tmp: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod hooks.json.tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close hooks.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, hooksPath); err != nil {
		debuglog.Logger.Error("cursor hooks: failed to rename hooks.json.tmp", "err", err)
		return false, fmt.Errorf("rename hooks.json: %w", err)
	}

	debuglog.Logger.Info("cursor hooks injected", "path", hooksPath)
	return true, nil
}

// mergeCursorHookEvent adds fleet's hook to an event's flat entry array,
// preserving any existing (non-fleet) entries and updating the command path
// in place if it changed (e.g. after a rebuild).
//
// Fail closed: an existing, non-empty entry that isn't a parseable
// []cursorHookEntry array is refused rather than silently discarded — treating
// unmarshal failure as "no entries" would clobber whatever the user (or another
// tool) put there, contradicting InjectCursorHooks' preserve-user-hooks contract.
func mergeCursorHookEvent(existing json.RawMessage) (json.RawMessage, error) {
	var entries []cursorHookEntry
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &entries); err != nil {
			return nil, err
		}
	}

	currentCmd := GetHookCommand()

	for i, e := range entries {
		if isFleetHook(e.Command) {
			if e.Command != currentCmd {
				entries[i].Command = currentCmd
			}
			result, err := json.Marshal(entries)
			return result, err
		}
	}

	entries = append(entries, cursorHookEntry{Command: currentCmd, Type: "command"})
	result, err := json.Marshal(entries)
	return result, err
}
