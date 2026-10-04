package session

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/hooks"
	"gopkg.in/yaml.v3"
)

// copilotTitleMaxRunes bounds what we'll accept as a Copilot title: its own are
// a handful of words, so anything longer is not a title.
const copilotTitleMaxRunes = 120

// ReadCopilotSessionName returns the title Copilot keeps for a session: the
// `name` in session-state/<id>/workspace.yaml. Copilot writes a model-generated
// one after the first turn (like Claude's ai-title) and replaces it on
// `/rename`, so this covers both. "" until it exists.
func ReadCopilotSessionName(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(hooks.CopilotSessionDir(sessionID), "workspace.yaml"))
	if err != nil {
		return ""
	}
	return parseCopilotWorkspaceName(data)
}

func parseCopilotWorkspaceName(data []byte) string {
	var ws struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal(data, &ws) != nil {
		return ""
	}
	name := strings.TrimSpace(ws.Name)
	if strings.ContainsAny(name, "\n\r") || utf8.RuneCountInString(name) > copilotTitleMaxRunes {
		return ""
	}
	return name
}
