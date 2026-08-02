package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StatusFile is the JSON format written to ~/.config/fleet/hooks/{session_id}.json.
type StatusFile struct {
	Status      string `json:"status"`
	SessionID   string `json:"session_id,omitempty"`
	Event       string `json:"event"`
	Timestamp   int64  `json:"ts"`
	UserPrompt  string `json:"user_prompt,omitempty"`
	PromptCount int    `json:"prompt_count,omitempty"`
	// Reason is forwarded from Claude Code's SessionEnd hook payload
	// ("clear", "logout", "prompt_input_exit", "other"). "other" combined with
	// no exit info typically means the process was killed externally.
	Reason string `json:"reason,omitempty"`
	// AgentPID is the process that fired this hook — the agent itself, since a
	// hook command runs as its direct child (verified: a hook's getppid() is the
	// `claude` process). It is what lets the status pipeline ask whether the
	// conversation that owns a session is still running, which separates a
	// session-id rotation (owner gone) from a nested agent that inherited
	// FLEET_INSTANCE_ID (owner alive) — a distinction no amount of transcript
	// reading can make. Omitted by handlers older than this field; readers must
	// treat 0 as "unknown", not "dead".
	//
	// getppid() only names the agent because the installed hook command is a
	// SINGLE SIMPLE COMMAND: Claude and Codex run GetHookCommand()'s
	// `'<path>' hook-handler --fleet-hook` through a shell, which execs rather
	// than forks, and OpenCode spawns the binary directly with no shell at all.
	// A compound command (`;`, `&&`, `|`, a redirection) makes the shell fork,
	// and getppid() would then record that shell — a process that exits
	// immediately, which conversationSucceeds reads as "the owner is gone" for
	// every foreign session id it is ever asked about. Only reachable by hand-
	// editing settings.json today, but keep the installed command simple: the
	// failure is a silently inverted ownership gate, not a missing signal.
	AgentPID int `json:"agent_pid,omitempty"`
}

// WriteStatusFile atomically writes a status file to the hooks directory.
//
// The temp file is uniquely named. Hook handlers are one-shot processes and an
// agent can fire two hooks at once (Codex requests permission per concurrent
// tool call), so a shared `<id>.json.tmp` had two processes writing the same
// path: whoever renamed second failed with ENOENT, and an interleaved write
// could be renamed into place as truncated JSON.
//
// Racing writers now each rename their own file, so every write lands intact and
// the last one wins. That does not order them: two concurrent handlers can carry
// *different* events (a PermissionRequest→waiting racing a Stop→finished), and
// nothing sequences the renames — Timestamp is Unix seconds, too coarse to break
// the tie. A stale event can therefore win and stand until the next hook. That
// was equally true of the shared-tmp scheme; don't build on an ordering nobody
// enforces.
func WriteStatusFile(hooksDir, instanceID string, sf *StatusFile) error {
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(sf)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(hooksDir, instanceID+".*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, filepath.Join(hooksDir, instanceID+".json"))
}

// ReadStatusFile reads and parses a status file.
func ReadStatusFile(path string) (*StatusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var sf StatusFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return &sf, nil
}
