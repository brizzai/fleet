// Package agent describes the coding agents fleet can launch in a session
// (Claude Code, OpenAI Codex, and OpenCode) and owns the per-agent divergence:
// the binary name, display name, and the launch command (including resume/fork
// forms).
package agent

import "fmt"

// Type identifies which coding agent a session runs.
type Type string

const (
	Claude   Type = "claude"
	Codex    Type = "codex"
	OpenCode Type = "opencode"

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
	default:
		return "Claude"
	}
}

// String implements fmt.Stringer.
func (t Type) String() string { return string(t) }

// PromptEnvVar names the tmux session environment variable that carries a
// session's first prompt. The launch command references the variable rather
// than embedding the text, because the command string is *typed into a shell*
// (tmux send-keys), so an embedded prompt would be parsed by that shell: a
// prompt containing `$(...)`, a backtick or a newline would be executed rather
// than sent to the agent. tmux sets the variable with no shell involved and the
// pane's `"$FLEET_INITIAL_PROMPT"` expands to exactly one argument, whatever
// it contains.
const PromptEnvVar = "FLEET_INITIAL_PROMPT"

// promptRef is how the launch command reads the prompt. Double-quoted so a
// multi-word or multi-line prompt stays a single argv element.
const promptRef = `"$` + PromptEnvVar + `"`

// promptArg is the prompt argument as each agent must receive it.
//
// Quoting the expansion protects the prompt from the *shell*; it does nothing
// about the agent's own argv parser, which is a second parser with its own
// opinion about a leading dash. A message may legitimately start with one —
// `fleet send` guarantees exactly that by refusing to parse flags after the
// selector — and every agent rejects it as an unknown option and *exits*,
// leaving a live session parked at a shell prompt with the message gone:
//
//	claude "--fix this"            → error: unknown option '--fix this'
//	codex "--fix this"             → error: unexpected argument … tip: use '-- …'
//	opencode --prompt "--fix this" → prints usage and exits
//
// `--` ends option parsing for the two agents that take the prompt as a
// positional (verified against Claude 2.1 / Codex 0.5x). OpenCode's positional
// is a project path, so its prompt rides --prompt and needs the `=` form: with
// a space, yargs reads the next word as a fresh option instead of the value.
func (t Type) promptArg() string {
	if t == OpenCode {
		return "--prompt=" + promptRef
	}
	return "-- " + promptRef
}

// LaunchOpts carries the per-session details that shape the launch command.
type LaunchOpts struct {
	// ResumeID resumes an existing agent conversation when set (and ForkID is empty).
	ResumeID string
	// ForkID starts a new conversation forked from an existing one when set.
	ForkID string
	// Prompt, when non-empty, makes the agent open on that first message and
	// start working on it. Only its emptiness is read here — the text itself
	// travels out-of-band in PromptEnvVar (see above).
	Prompt string
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
// An initial prompt appends the agent's own prompt argument to any of those —
// `-- <prompt>` for Claude and Codex, `--prompt=<prompt>` for OpenCode (see
// promptArg for why the separator matters). All three accept it alongside
// resume/fork, so a resumed conversation can be handed a message too.
func (t Type) BuildLaunchCmd(o LaunchOpts) string {
	var cmd string
	switch t {
	case Codex:
		switch {
		case o.ForkID != "":
			cmd = fmt.Sprintf("codex fork %s", o.ForkID)
		case o.ResumeID != "":
			cmd = fmt.Sprintf("codex resume %s", o.ResumeID)
		default:
			cmd = "codex"
		}

	case OpenCode:
		switch {
		case o.ForkID != "":
			cmd = fmt.Sprintf("opencode --session %s --fork", o.ForkID)
		case o.ResumeID != "":
			cmd = fmt.Sprintf("opencode --session %s", o.ResumeID)
		default:
			cmd = "opencode"
		}

	default: // Claude
		cmd = "claude"
		if o.ForkID != "" {
			cmd += fmt.Sprintf(" --resume %s --fork-session", o.ForkID)
		} else if o.ResumeID != "" {
			cmd += fmt.Sprintf(" --resume %s", o.ResumeID)
		}
	}

	if o.Prompt != "" {
		cmd += " " + t.promptArg()
	}
	return cmd
}
