package session

import "github.com/brizzai/fleet/internal/agent"

// ReadAgentSessionName returns the title the agent itself keeps for a session —
// Claude's custom-title/ai-title, the title a Codex user set with `/rename`,
// Copilot's own (generated, or set with `/rename`), or OpenCode's
// model-generated (or renamed) title.
// Empty means the agent has no title of its own and fleet's prompt heuristic
// should name the session.
func ReadAgentSessionName(a agent.Type, sessionID, projectPath string) string {
	switch a {
	case agent.Codex:
		return ReadCodexSessionName(sessionID)
	case agent.Copilot:
		return ReadCopilotSessionName(sessionID)
	case agent.OpenCode:
		return ReadOpenCodeSessionName(sessionID)
	default:
		// Claude, and legacy rows with no agent recorded.
		return ReadClaudeSessionName(sessionID, projectPath)
	}
}
