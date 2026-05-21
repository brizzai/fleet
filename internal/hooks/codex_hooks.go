package hooks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// codexHookEvents lists the Codex hook events fleet subscribes to. Field names
// in Codex's payload match Claude's (hook_event_name, session_id, prompt), so
// `fleet hook-handler` and the status pipeline are reused unchanged. Codex has
// no SessionEnd/Notification events — `dead` comes from tmux pane-death.
var codexHookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PermissionRequest",
	"Stop",
}

// GetCodexConfigDir returns the Codex config directory ($CODEX_HOME or ~/.codex).
func GetCodexConfigDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex")
	}
	return filepath.Join(home, ".codex")
}

// InjectCodexHooks merges fleet hook entries into Codex's hooks.json, preserving
// any existing user hooks. Returns true if the file was written (changed).
//
// hooks.json shape: {"hooks": {"<Event>": [ {"hooks": [ {"type","command"} ]} ]}}
// — the same event-map structure as Claude's settings.json["hooks"], so the
// shared merge helpers (mergeHookEvent / claudeHookMatcher / claudeHookEntry)
// are reused. Codex entries omit the Claude-only "async" field.
func InjectCodexHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")

	var root map[string]json.RawMessage
	orig, err := os.ReadFile(hooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			debuglog.Logger.Error("codex hooks: failed to read hooks.json", "path", hooksPath, "err", err)
			return false, fmt.Errorf("read hooks.json: %w", err)
		}
		root = make(map[string]json.RawMessage)
	} else {
		if err := json.Unmarshal(orig, &root); err != nil {
			debuglog.Logger.Error("codex hooks: failed to parse hooks.json", "path", hooksPath, "err", err)
			return false, fmt.Errorf("parse hooks.json: %w", err)
		}
	}

	var events map[string]json.RawMessage
	if raw, ok := root["hooks"]; ok {
		if err := json.Unmarshal(raw, &events); err != nil {
			debuglog.Logger.Error("codex hooks: failed to parse hooks section", "err", err)
			events = make(map[string]json.RawMessage)
		}
	} else {
		events = make(map[string]json.RawMessage)
	}

	for _, event := range codexHookEvents {
		// async=false → omitted via omitempty (Codex hook entries are {type,command}).
		events[event] = mergeHookEvent(events[event], "", false)
	}

	eventsRaw, err := json.Marshal(events)
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	root["hooks"] = eventsRaw

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
	tmpPath := hooksPath + ".tmp"
	if err := os.WriteFile(tmpPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write hooks.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, hooksPath); err != nil {
		os.Remove(tmpPath)
		debuglog.Logger.Error("codex hooks: failed to rename hooks.json.tmp", "err", err)
		return false, fmt.Errorf("rename hooks.json: %w", err)
	}

	debuglog.Logger.Info("codex hooks injected", "path", hooksPath)
	return true, nil
}

// EnsureCodexDirTrust makes sure Codex's config.toml marks projectPath trusted,
// so launching a Codex session there doesn't open with a blocking dir-trust
// prompt. It appends a `[projects."<path>"] trust_level = "trusted"` table if no
// entry for the path exists, leaving the rest of the file untouched. A
// user-set entry (even a non-trusted one) is respected and never overwritten.
func EnsureCodexDirTrust(configDir, projectPath string) error {
	if projectPath == "" {
		return nil
	}
	tomlPath := filepath.Join(configDir, "config.toml")
	header := fmt.Sprintf(`[projects.%q]`, projectPath)

	data, err := os.ReadFile(tomlPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	// Already has a table for this path → respect whatever the user/codex set.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == header {
			return nil
		}
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(header)
	b.WriteString("\ntrust_level = \"trusted\"\n")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(tomlPath, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	debuglog.Logger.Info("codex dir trust seeded", "path", projectPath)
	return nil
}
