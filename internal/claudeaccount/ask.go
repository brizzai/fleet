package claudeaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// ErrClaudeMissing means the CLI is not installed. A Codex-only or OpenCode-only
// user has no Claude at all, and a feature that leans on one has to degrade
// rather than report a failure they cannot act on.
var ErrClaudeMissing = errors.New("claude is not installed")

// ErrAskTimeout is a call that ran out of its own time budget (as opposed to
// the caller cancelling it, which returns context.Canceled).
var ErrAskTimeout = errors.New("claude timed out")

// AskOptions shapes one non-interactive `claude -p` call. Every flag here was
// checked against `claude --help` (Claude Code 2.1.327).
type AskOptions struct {
	// ConfigDir is the account's CLAUDE_CONFIG_DIR; "" is the ambient login.
	ConfigDir string
	// Dir is the working directory claude runs in (and its tools read from).
	Dir string
	// Instruction rides in argv — fleet's own words only (see Ask).
	Instruction string
	// Input rides on stdin — anything that is not fleet's own words.
	Input string

	Model        string // --model; "" = claude's default
	OutputFormat string // --output-format: "", "text", "json"
	// Verbose passes --verbose. With OutputFormat "json" it turns the single
	// result object into a JSON array of every event — init, each assistant
	// message with its tool_use blocks, and the result last — which is the
	// only place claude reports which tools the call ran.
	Verbose bool
	// AllowedTools / DisallowedTools are passed one argv element per entry,
	// so a rule with a space in it ("Bash(git log:*)") stays one rule.
	AllowedTools    []string
	DisallowedTools []string
	// RestrictTools passes --tools, which decides which built-in tools EXIST
	// for the call, as opposed to the allow/deny lists, which only decide which
	// ones run without a prompt. Tools is the set (one comma-joined argv
	// element); an empty Tools with RestrictTools set means no tools at all.
	// A CLI too old to know the flag gets the call without it (see
	// toolRestrictionSupported).
	RestrictTools bool
	Tools         []string
	// StrictMCPConfig passes --strict-mcp-config with no --mcp-config: the
	// call loads no MCP server at all, whatever the user's settings name.
	StrictMCPConfig bool
	// NoSessionPersistence keeps the call out of ~/.claude/projects, so it
	// never shows up in /resume or in fleet's own transcript scans.
	NoSessionPersistence bool
	// Timeout bounds the call on top of ctx; 0 means ctx alone.
	Timeout time.Duration
}

// askCall is what the exec seam receives: the whole command, already built.
type askCall struct {
	Name  string
	Args  []string
	Env   []string // nil = inherit
	Dir   string
	Stdin string
}

// Seams for tests. Nothing in fleet's tests may run the real claude: it
// spends the user's quota and depends on their login.
var (
	askLookPath = exec.LookPath
	askExec     = execAsk
	askHelpText = execAskHelp
)

// askHelpTimeout bounds the one `claude --help` a process runs to learn which
// flags its CLI has.
const askHelpTimeout = 10 * time.Second

func execAskHelp(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, askHelpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBinary, "--help")
	cmd.WaitDelay = askWaitDelay
	cmd.Env = askEnv("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// askFlags caches, per process, whether the installed claude knows --tools and
// --strict-mcp-config (Claude Code 2.1.327 does). Detected once: the answer
// cannot change under a running process short of a reinstall, and a --help per
// call would double the cost of every short ask.
var askFlags struct {
	mu     sync.Mutex
	known  bool
	ok     bool
	warned bool
}

// toolRestrictionSupported reports whether this claude takes --tools and
// --strict-mcp-config. A failed probe is not cached, so a transient failure
// (a timeout under load) is retried on the next call rather than downgrading
// every later call for the life of the process.
func toolRestrictionSupported(ctx context.Context) bool {
	askFlags.mu.Lock()
	defer askFlags.mu.Unlock()
	if askFlags.known {
		return askFlags.ok
	}
	help, err := askHelpText(ctx)
	if err != nil && help == "" {
		debuglog.Logger.Warn("claude --help failed; tool restriction flags unknown", "err", err)
		return false
	}
	askFlags.known = true
	askFlags.ok = strings.Contains(help, "--tools") && strings.Contains(help, "--strict-mcp-config")
	return askFlags.ok
}

// resetAskFlags forgets the detection (tests).
func resetAskFlags() {
	askFlags.mu.Lock()
	defer askFlags.mu.Unlock()
	askFlags.known, askFlags.ok, askFlags.warned = false, false, false
}

// downgradeUnsupported drops --tools / --strict-mcp-config for a CLI that
// predates them, warning once per process. The allow/deny lists still apply,
// so the call keeps its read-only posture — it just loses the stronger "the
// tool does not exist" guarantee.
func downgradeUnsupported(ctx context.Context, o AskOptions) AskOptions {
	if !o.RestrictTools && !o.StrictMCPConfig {
		return o
	}
	if toolRestrictionSupported(ctx) {
		return o
	}
	askFlags.mu.Lock()
	if !askFlags.warned {
		askFlags.warned = true
		debuglog.Logger.Warn("claude lacks --tools/--strict-mcp-config; running with the allow/deny lists only — upgrade Claude Code")
	}
	askFlags.mu.Unlock()
	o.RestrictTools, o.Tools, o.StrictMCPConfig = false, nil, false
	return o
}

// askWaitDelay bounds Wait after the context kills claude: a tool claude
// spawned can inherit the stdout pipe, and without WaitDelay Wait blocks until
// that grandchild exits too (the FetchBaseRef lesson).
const askWaitDelay = 5 * time.Second

func execAsk(ctx context.Context, c askCall) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = c.Env
	cmd.Dir = c.Dir
	cmd.WaitDelay = askWaitDelay
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// Ask runs one non-interactive Claude call on a given account and returns what
// it said.
//
// This is fleet asking a question of its own, rather than launching a session
// for the user, so it goes through `claude -p` rather than tmux: no pane, no
// hooks, no status, nothing in the sidebar. It authenticates exactly as every
// other session does — CLAUDE_CONFIG_DIR pointing at that account's own
// claude.ai login — so it needs no API key and spends the subscription the user
// already has.
//
// instruction rides in argv and input rides on STDIN, and the split is
// deliberate: argv is world-readable through ps, so fleet's own words may go
// there and other people's code may not. It is also the only way to pass a
// changeset at all — a 300KB argument is not something to hand a shell.
func Ask(ctx context.Context, configDir, instruction, input string) (string, error) {
	return AskWith(ctx, AskOptions{ConfigDir: configDir, Instruction: instruction, Input: input})
}

// AskWith is Ask with every knob. It returns claude's stdout, trimmed. With
// OutputFormat "json" that is the result envelope, which on an exit-0 call can
// still report is_error — reading it is the caller's job.
func AskWith(ctx context.Context, o AskOptions) (string, error) {
	if _, err := askLookPath(claudeBinary); err != nil {
		return "", ErrClaudeMissing
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	o = downgradeUnsupported(ctx, o)

	stdout, stderr, err := askExec(ctx, askCall{
		Name:  claudeBinary,
		Args:  askArgs(o),
		Env:   askEnv(o.ConfigDir),
		Dir:   o.Dir,
		Stdin: o.Input,
	})
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			if o.Timeout > 0 {
				return "", fmt.Errorf("%w after %s", ErrAskTimeout, o.Timeout)
			}
			return "", ErrAskTimeout
		case ctx.Err() != nil:
			return "", ctx.Err()
		}
		out, errOut := strings.TrimSpace(string(stdout)), strings.TrimSpace(string(stderr))
		debuglog.Logger.Debug("claude ask failed", "err", err, "stdout", trimTo(out, 2000), "stderr", trimTo(errOut, 2000))
		return "", fmt.Errorf("claude: %s", askFailureReason(out, errOut, err))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// askArgs builds argv. The instruction sits directly after -p, BEFORE any
// variadic flag: --allowedTools and --disallowedTools take every following
// non-option word, so an instruction placed after them would be read as a
// tool name and the call would run with no prompt at all.
//
// An instruction starting with "-" would itself be read as an option, so it
// gets a leading space — invisible to the model, decisive to the parser.
func askArgs(o AskOptions) []string {
	instr := o.Instruction
	if strings.HasPrefix(instr, "-") {
		instr = " " + instr
	}
	args := []string{"-p", instr}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.OutputFormat != "" {
		args = append(args, "--output-format", o.OutputFormat)
	}
	if o.Verbose {
		args = append(args, "--verbose")
	}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, o.AllowedTools...)
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, o.DisallowedTools...)
	}
	// --tools is variadic too, which is fine here: it comes after the
	// instruction, and its whole value is ONE element ("Read,Grep,Glob", or ""
	// for none), so it cannot swallow anything that follows.
	if o.RestrictTools {
		args = append(args, "--tools", strings.Join(o.Tools, ","))
	}
	if o.StrictMCPConfig {
		args = append(args, "--strict-mcp-config")
	}
	if o.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	return args
}

// fleetSessionVars are fleet's own per-session variables, which a fleet
// process launched from inside a fleet session inherits. Leaving
// FLEET_INSTANCE_ID set would let the reviewer's hooks (the account's
// settings.json carries fleet's) write status into that session's file —
// a background review would flip someone's row to running and back. The
// prompt variable is agent.PromptEnvVar, spelled out to avoid the import.
var fleetSessionVars = map[string]bool{
	"FLEET_INSTANCE_ID":    true,
	"FLEET_INITIAL_PROMPT": true,
}

// askEnv is the call's environment: the account's config dir (with inherited
// credentials scrubbed, as configDirEnv does for every helper), or the ambient
// environment — minus fleet's per-session variables either way.
func askEnv(configDir string) []string {
	env := os.Environ()
	if configDir != "" {
		env = configDirEnv(configDir)
	}
	out := env[:0:0]
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && fleetSessionVars[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// askFailureReason works out what actually went wrong, and STDOUT is where it
// looks first.
//
// Measured, not assumed: `claude -p` on a spent subscription exits 1, prints
// "You've hit your session limit · resets 3:50pm" on stdout, and puts only
// warnings on stderr — a settings.json permission rule it dislikes, a note
// about the shell's working directory. Reading stderr first therefore reported
// a permission rule as the cause of a quota refusal, which is a false statement
// about the user's setup and sends them to fix a file that was never the
// problem. `claude auth status` has the same shape (see parseAuthStatus): the
// answer is on stdout even when the exit code is 1. With --output-format json
// the reason is the envelope's "result", so that is unwrapped first.
//
// Truncated because this reaches a one-line toast in a footer, and claude's
// warnings run to a full paragraph.
func askFailureReason(stdout, stderr string, runErr error) string {
	if r := envelopeResult(stdout); r != "" {
		stdout = r
	}
	for _, s := range []string{stdout, stderr} {
		if line := firstUsefulLine(s); line != "" {
			return trimTo(line, askReasonMax)
		}
	}
	return runErr.Error()
}

// envelopeResult returns the "result" text of a --output-format json envelope
// — the single object, or the last result event of a --verbose array — or ""
// when stdout is not one.
func envelopeResult(stdout string) string {
	type envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
	}
	var env envelope
	switch {
	case strings.HasPrefix(stdout, "{"):
		if json.Unmarshal([]byte(stdout), &env) != nil {
			return ""
		}
	case strings.HasPrefix(stdout, "["):
		var events []envelope
		if json.Unmarshal([]byte(stdout), &events) != nil {
			return ""
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "result" {
				env = events[i]
				break
			}
		}
	default:
		return ""
	}
	if env.Type != "result" {
		return ""
	}
	if strings.TrimSpace(env.Result) != "" {
		return env.Result
	}
	return env.Subtype
}

// askReasonMax is what fits a footer without pushing the keys off it.
const askReasonMax = 90

// firstUsefulLine skips the notes claude prints on its way to the real answer.
func firstUsefulLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isClaudeNote(line) {
			continue
		}
		return line
	}
	return ""
}

// isClaudeNote recognises claude's own advisories, which are printed alongside
// a failure rather than being one.
func isClaudeNote(line string) bool {
	return strings.HasPrefix(line, "Permission allow rule") ||
		strings.HasPrefix(line, "Shell cwd was reset") ||
		strings.HasPrefix(line, "Warning: no stdin data received")
}

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
