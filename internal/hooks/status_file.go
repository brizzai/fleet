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
func WriteStatusFile(hooksDir, instanceID string, sf *StatusFile) error {
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(sf)
	if err != nil {
		return err
	}

	filePath := filepath.Join(hooksDir, instanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
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
