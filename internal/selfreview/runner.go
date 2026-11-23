package selfreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/claudeaccount"
)

// Runner prepares one review run over a worktree. dir is the worktree: the
// reviewer's working directory, which its read-only tools see.
type Runner interface {
	Review(ctx context.Context, dir string, in Input) (*Run, RunMeta, error)
}

// DefaultModel is the reviewer model when review_model is unset.
const DefaultModel = "opus"

// DefaultTimeout bounds one reviewer call.
const DefaultTimeout = 5 * time.Minute

// ReviewerTools is the reviewer's whole toolset, passed as --tools: the
// built-in tools that exist for the call at all. No shell and no MCP server
// (--strict-mcp-config): the reviewer reads attacker-influenced diffs, and a
// prompt injection can only do what the tools it reaches can do. Even a
// "read-only" git allowance is a shell — `git log` takes --output=<file> and
// `git diff` takes --ext-diff — and fleet already hands over the commit log.
var ReviewerTools = []string{"Read", "Grep", "Glob"}

// ReviewerAllowedTools are the tools the reviewer may use without a prompt
// (there is nobody to answer one under -p). The same set as ReviewerTools,
// kept as an allowlist too so a Claude Code too old for --tools still runs
// read-only.
var ReviewerAllowedTools = []string{"Read", "Grep", "Glob"}

// ReviewerDisallowedTools are refused outright, whatever the user's own
// settings allow: the reviewer reads attacker-influenced content and must not
// be able to change the worktree it is judging.
var ReviewerDisallowedTools = []string{"Edit", "Write", "NotebookEdit"}

// AskFunc is the seam ClaudeRunner calls; claudeaccount.AskWith in production.
type AskFunc func(ctx context.Context, o claudeaccount.AskOptions) (string, error)

// ClaudeRunner reviews with a fresh `claude -p` on one account.
type ClaudeRunner struct {
	Model     string        // "" = DefaultModel
	ConfigDir string        // account CLAUDE_CONFIG_DIR; "" = ambient login
	Timeout   time.Duration // per call; 0 = DefaultTimeout
	Ask       AskFunc       // nil = claudeaccount.AskWith
	now       func() time.Time
}

// repairInstruction asks for the same review reshaped, nothing more. It runs
// with no tools: the content is already in hand.
const repairInstruction = `Stdin holds a code review that was meant to be ONE JSON object but is not valid. Reply with only that object, corrected to this shape, keeping its content. No prose, no code fence.
%s`

// wordingInstruction asks for the same review with only its wording fixed.
// It runs with no tools: everything it needs is on stdin.
const wordingInstruction = `Stdin holds a code review as ONE JSON object, then the problems fleet found in it. Fix ONLY what the problems name: plain words for a person deciding, no code identifiers outside checked items, how and chapters, within the word limits; each decide one yes/no question where yes keeps the code as is; a missing field (summary, recommend, how, a scope stop for intent_gap, chapters covering every file) is added from what the object already says. Keep every id, kind, hunk, path, src, checked item and fact exactly. Reply with only the corrected object in this shape. No prose, no code fence.
%s`

// repairWording runs the wording repair call. ok is false when the call
// failed or its reply could not be read — the caller keeps the original.
func (r ClaudeRunner) repairWording(ctx context.Context, ask AskFunc, base claudeaccount.AskOptions, run *Run, problems []string, meta *RunMeta) (*Run, ReviewerMeta, bool) {
	body, err := json.Marshal(run)
	if err != nil {
		return nil, ReviewerMeta{}, false
	}
	call := base
	call.Instruction = fmt.Sprintf(wordingInstruction, fmt.Sprintf(schemaText, ""))
	call.Input = string(body) + "\n\nPROBLEMS:\n- " + strings.Join(problems, "\n- ") + "\n"
	call.Tools = nil // RestrictTools with no tools: none exist
	call.DisallowedTools = append(append([]string{}, ReviewerDisallowedTools...), "Bash", "Read", "Grep", "Glob")
	out, err := ask(ctx, call)
	if err != nil {
		meta.Warnings = append(meta.Warnings, "wording repair failed: "+err.Error())
		return nil, ReviewerMeta{}, false
	}
	noteEnvelope(meta, out)
	fixed, more, err := Parse([]byte(out))
	if err != nil {
		meta.Warnings = append(meta.Warnings, "wording repair unreadable; kept the original")
		return nil, more, false
	}
	return fixed, more, true
}

// Review builds the prompt, runs claude, parses and enforces the reply. An
// unparseable reply gets exactly one repair call; a second failure is an
// error. A readable reply whose prose breaks Lint's rules gets one wording
// repair call, and the version with fewer problems is kept.
func (r ClaudeRunner) Review(ctx context.Context, dir string, in Input) (*Run, RunMeta, error) {
	model := r.Model
	if model == "" {
		model = DefaultModel
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ask := r.Ask
	if ask == nil {
		ask = claudeaccount.AskWith
	}
	now := r.now
	if now == nil {
		now = time.Now
	}

	meta := RunMeta{Model: model}
	start := now()

	instruction, stdin := BuildPrompt(in)
	base := claudeaccount.AskOptions{
		ConfigDir:    r.ConfigDir,
		Dir:          dir,
		Model:        model,
		OutputFormat: "json",
		// --verbose turns the single result object into the whole event
		// list, which is the only place claude reports the tool calls — and
		// "did the reviewer open any file?" is not answerable without them.
		Verbose:              true,
		NoSessionPersistence: true,
		StrictMCPConfig:      true,
		RestrictTools:        true,
		Timeout:              timeout,
	}

	call := base
	call.Instruction, call.Input = instruction, stdin
	call.Tools = ReviewerTools
	call.AllowedTools, call.DisallowedTools = ReviewerAllowedTools, ReviewerDisallowedTools
	out, err := ask(ctx, call)
	meta.Attempts = 1
	if err != nil {
		meta.Elapsed = now().Sub(start)
		return nil, meta, err
	}
	noteEnvelope(&meta, out)

	run, reviewer, perr := Parse([]byte(out))
	if perr != nil && errors.Is(perr, ErrNoJSON) {
		repair := base
		repair.Instruction = fmt.Sprintf(repairInstruction, fmt.Sprintf(schemaText, ""))
		repair.Input = modelText(out)
		repair.Tools = nil // RestrictTools with no tools: none exist
		repair.DisallowedTools = append(append([]string{}, ReviewerDisallowedTools...), "Bash", "Read", "Grep", "Glob")
		meta.Attempts = 2
		meta.Warnings = append(meta.Warnings, "reply was not valid JSON; asked once to repair it")
		out2, err := ask(ctx, repair)
		if err != nil {
			meta.Elapsed = now().Sub(start)
			return nil, meta, fmt.Errorf("repair: %w", err)
		}
		noteEnvelope(&meta, out2)
		var more ReviewerMeta
		run, more, perr = Parse([]byte(out2))
		reviewer.add(more)
	}
	if perr != nil {
		meta.Elapsed = now().Sub(start)
		return nil, meta, perr
	}

	// Wording: one repair call when the prose breaks the rules a reader
	// depends on (Lint). Whichever version has fewer problems is kept; a
	// failed or unreadable repair keeps the original. Wording never fails a
	// review.
	if problems := Lint(run); len(problems) > 0 {
		meta.Attempts++
		kept := run
		left := len(problems)
		if fixed, more, ok := r.repairWording(ctx, ask, base, run, problems, &meta); ok {
			reviewer.add(more)
			if n := len(Lint(fixed)); n <= left {
				kept, left = fixed, n
			}
		}
		run = kept
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("wording: %d %s, asked once to fix (%d left)",
			len(problems), plural(len(problems), "problem", "problems"), left))
	}

	meta.Warnings = append(meta.Warnings, Enforce(run, in)...)
	// Set after Enforce, like every computed field: the model never writes it.
	reviewer.Relativize(dir)
	run.Reviewer = &reviewer
	meta.Elapsed = now().Sub(start)
	return run, meta, nil
}

// noteEnvelope adds what claude's envelope says about the call to meta.
func noteEnvelope(meta *RunMeta, out string) {
	if env, ok := ParseEnvelope([]byte(out)); ok {
		meta.CostUSD += env.TotalCostUSD
		meta.NumTurns += env.NumTurns
		if env.SessionID != "" {
			meta.SessionID = env.SessionID
		}
	}
}

// modelText is the model's own words out of a reply, for the repair call.
func modelText(out string) string {
	if env, ok := ParseEnvelope([]byte(out)); ok {
		return env.Result
	}
	return out
}
