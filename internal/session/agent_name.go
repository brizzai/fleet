package session

import "github.com/brizzai/fleet/internal/agent"

// ReadAgentSessionName returns the title the agent itself keeps for a session —
// Claude's custom-title/ai-title, or the title a Codex user set with `/rename`.
// Empty means the agent has no title of its own and fleet's prompt heuristic
// should name the session.
func ReadAgentSessionName(a agent.Type, sessionID, projectPath string) string {
	switch a {
	case agent.Codex:
		return ReadCodexSessionName(sessionID)
	case agent.OpenCode:
		// OpenCode titles its sessions too, but fleet doesn't read them yet.
		return ""
	default:
		// Claude, and legacy rows with no agent recorded.
		return ReadClaudeSessionName(sessionID, projectPath)
	}
}
