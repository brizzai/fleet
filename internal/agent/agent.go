// Package agent describes the coding agents fleet can launch in a session
// (Claude Code, OpenAI Codex, OpenCode, and Cursor CLI) and owns the per-agent
// divergence: the binary name, display name, and the launch command (including
// resume/fork forms).
package agent

import "fmt"

// Type identifies which coding agent a session runs.
type Type string

const (
	Claude   Type = "claude"
	Codex    Type = "codex"
	OpenCode Type = "opencode"
	Cursor   Type = "cursor"

	// Default is the agent assumed when none is recorded (legacy sessions, empty config).
	Default = Claude
)

// Parse normalizes a stored/config string into a Type, falling back to Default
// for empty or unrecognized values.
func Parse(s string) Type {
	switch Type(s) {
	case Claude:
		return Claude
	case Codex:
		return Codex
	case OpenCode:
		return OpenCode
	case Cursor:
		return Cursor
	default:
		return Default
	}
}

// Binary returns the executable name to look up on PATH and launch.
func (t Type) Binary() string {
	switch t {
	case Codex:
		return "codex"
	case OpenCode:
		return "opencode"
	case Cursor:
		return "cursor-agent"
	default:
		return "claude"
	}
}

// DisplayName returns the human-facing label for the agent.
func (t Type) DisplayName() string {
	switch t {
	case Codex:
		return "Codex"
	case OpenCode:
		return "OpenCode"
	case Cursor:
		return "Cursor"
	default:
		return "Claude"
	}
}

// String implements fmt.Stringer.
func (t Type) String() string { return string(t) }

// LaunchOpts carries the per-session details that shape the launch command.
type LaunchOpts struct {
	// ResumeID resumes an existing agent conversation when set (and ForkID is empty).
	ResumeID string
	// ForkID starts a new conversation forked from an existing one when set.
	ForkID string
}

// BuildLaunchCmd returns the shell command to run in the session's tmux pane.
//
// Claude:
//
//	claude
//	claude --resume <id>
//	claude --resume <id> --fork-session   (fork)
//
// Codex (directory + hook trust are seeded out-of-band; no flags here):
//
//	codex
//	codex resume <id>
//	codex fork <id>                        (fork)
//
// OpenCode (status plugin is installed out-of-band; --fork is a modifier on
// --session, not a subcommand):
//
//	opencode
//	opencode --session <id>
//	opencode --session <id> --fork         (fork)
//
// Cursor (hooks are seeded out-of-band; no fork primitive — fork-to-worktree
// stays Claude-only):
//
//	cursor-agent
//	cursor-agent --resume <id>
func (t Type) BuildLaunchCmd(o LaunchOpts) string {
	if t == Codex {
		switch {
		case o.ForkID != "":
			return fmt.Sprintf("codex fork %s", o.ForkID)
		case o.ResumeID != "":
			return fmt.Sprintf("codex resume %s", o.ResumeID)
		default:
			return "codex"
		}
	}

	if t == OpenCode {
		switch {
		case o.ForkID != "":
			return fmt.Sprintf("opencode --session %s --fork", o.ForkID)
		case o.ResumeID != "":
			return fmt.Sprintf("opencode --session %s", o.ResumeID)
		default:
			return "opencode"
		}
	}

	// Claude (default).
	cmd := "claude"
	if o.ForkID != "" {
		cmd += fmt.Sprintf(" --resume %s --fork-session", o.ForkID)
	} else if o.ResumeID != "" {
		cmd += fmt.Sprintf(" --resume %s", o.ResumeID)
	}
	return cmd
}
