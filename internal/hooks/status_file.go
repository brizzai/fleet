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
}

// WriteStatusFile atomically writes a status file to the hooks directory.
//
// The temp file is uniquely named. Hook handlers are one-shot processes and an
// agent can fire two hooks at once (Codex requests permission per concurrent
// tool call), so a shared `<id>.json.tmp` had two processes writing the same
// path: whoever renamed second failed with ENOENT, and an interleaved write
// could be renamed into place as truncated JSON. Racing writers now each rename
// their own file; last one wins, which is fine — they report the same event.
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
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		os.Remove(tmpPath)
		return err
	}

	filePath := filepath.Join(hooksDir, instanceID+".json")
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
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
