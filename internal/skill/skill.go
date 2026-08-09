// Package skill installs fleet's agent skill: the SKILL.md that teaches a
// coding agent how to drive fleet from a shell (`fleet wt`, `fleet send`).
//
// The skill is opt-in — nothing here runs on TUI launch, unlike hook injection.
// Hooks are load-bearing (fleet can't read status without them); a skill is a
// convenience, and it costs its description in the context of *every* session
// in every repo, including ones that have nothing to do with fleet. That is the
// user's call to make, so it stays behind `fleet skill install`.
package skill

import (
	"bytes"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/brizzai/fleet/internal/hooks"
)

// Content is the skill fleet installs, compiled into the binary so a fresh
// `fleet skill install` always writes the copy that matches this version.
//
//go:embed SKILL.md
var Content []byte

// Name is the skill's directory name. The Agent Skills format requires it to
// equal the `name` in SKILL.md's frontmatter, which TestFrontmatterMatchesName
// pins.
const Name = "fleet"

// Agent is a coding agent that reads the Agent Skills format. fleet writes into
// two roots, not four, because the agents overlap:
//
//	~/.claude/skills  Claude Code (also read by OpenCode and Cursor)
//	~/.agents/skills  Codex, Cursor, OpenCode
//
// Codex reads only `.agents/skills` and Claude Code reads only `.claude/skills`,
// so neither root alone covers both. Cursor and OpenCode read both, and are
// mapped to `.agents/skills` so that either one, installed on its own without
// Claude Code, still lands somewhere it reads.
type Agent struct {
	// Name is the value accepted by `fleet skill install -agent`.
	Name string
	// binary is looked up on PATH to detect the agent. Empty when the agent
	// ships no CLI.
	binary string
	// configDir is a second detection signal: Cursor installs its `cursor`
	// shell command only on request, so a Cursor user commonly has ~/.cursor
	// and no binary on PATH.
	configDir func() string
	// root returns the skills directory fleet writes into for this agent.
	root func() string
}

// Path returns the SKILL.md this agent reads.
func (a Agent) Path() string {
	return filepath.Join(a.root(), Name, "SKILL.md")
}

// Detected reports whether this agent looks present on this machine.
func (a Agent) Detected() bool {
	if a.binary != "" {
		if _, err := exec.LookPath(a.binary); err == nil {
			return true
		}
	}
	if dir := a.configDir(); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return home
}

func claudeSkillsRoot() string { return filepath.Join(hooks.GetClaudeConfigDir(), "skills") }
func agentsSkillsRoot() string { return filepath.Join(homeDir(), ".agents", "skills") }

// agents is the full table, in the order results are reported.
var agents = []Agent{
	{Name: "claude", binary: "claude", configDir: hooks.GetClaudeConfigDir, root: claudeSkillsRoot},
	{Name: "codex", binary: "codex", configDir: hooks.GetCodexConfigDir, root: agentsSkillsRoot},
	{Name: "cursor", binary: "cursor", configDir: func() string { return filepath.Join(homeDir(), ".cursor") }, root: agentsSkillsRoot},
	{Name: "opencode", binary: "opencode", configDir: hooks.GetOpenCodeConfigDir, root: agentsSkillsRoot},
}

// Agents returns every agent fleet can install the skill for.
func Agents() []Agent { return append([]Agent(nil), agents...) }

// Detected returns the agents present on this machine.
func Detected() []Agent {
	var found []Agent
	for _, a := range agents {
		if a.Detected() {
			found = append(found, a)
		}
	}
	return found
}

// Find returns the agent with the given name.
func Find(name string) (Agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// AnyInstalled reports whether the skill is present for any agent. Used by the
// TUI to decide whether to offer the install tip; it runs once at startup, not
// on the Update loop.
func AnyInstalled() bool {
	for _, a := range agents {
		if _, err := os.Stat(a.Path()); err == nil {
			return true
		}
	}
	return false
}

// Outcome is what happened to one agent's copy of the skill.
type Outcome string

const (
	// Written means the file was created or updated.
	Written Outcome = "written"
	// Unchanged means the file already held this version's content.
	Unchanged Outcome = "unchanged"
	// Installed means present and matching this binary's copy (status only).
	Installed Outcome = "installed"
	// Outdated means present but written by a different fleet version (status only).
	Outdated Outcome = "outdated"
	// Absent means there is no skill file to report or remove.
	Absent Outcome = "absent"
	// Removed means the file was deleted.
	Removed Outcome = "removed"
	// Skipped means the agent wasn't selected — it isn't installed here.
	Skipped Outcome = "skipped"
	// Failed means the filesystem operation errored; see Result.Err.
	Failed Outcome = "failed"
)

// Result is the per-agent report for one operation.
type Result struct {
	Agent   string
	Path    string
	Outcome Outcome
	Err     error
}

// Install writes the skill for each selected agent, and reports Skipped for the
// rest. Agents sharing a root share one write: the file is written once and
// both report the same path, rather than the second write racing the first.
//
// Never prompts and never reads stdin, so an agent can install the skill for
// itself in one non-interactive call.
func Install(selected []Agent) []Result {
	return run(selected, writeSkill)
}

// Uninstall removes the skill for each selected agent.
func Uninstall(selected []Agent) []Result {
	return run(selected, removeSkill)
}

// Status reports where the skill currently is, for every agent, whether or not
// that agent is installed here — a skill left behind by an uninstalled agent is
// exactly what someone running `status` is looking for.
func Status() []Result {
	return run(agents, statusOf)
}

// run applies op to each selected agent, deduplicating by path so a shared root
// is touched once. Agents not in selected are reported as Skipped.
func run(selected []Agent, op func(path string) Result) []Result {
	chosen := make(map[string]bool, len(selected))
	for _, a := range selected {
		chosen[a.Name] = true
	}

	done := make(map[string]Result)
	results := make([]Result, 0, len(agents))
	for _, a := range agents {
		if !chosen[a.Name] {
			results = append(results, Result{Agent: a.Name, Path: a.Path(), Outcome: Skipped})
			continue
		}
		path := a.Path()
		r, seen := done[path]
		if !seen {
			r = op(path)
			done[path] = r
		}
		r.Agent = a.Name
		results = append(results, r)
	}
	return results
}

func writeSkill(path string) Result {
	r := Result{Path: path}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, Content) {
		r.Outcome = Unchanged
		return r
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	// Write via a temp file in the same directory. Claude Code watches skill
	// directories for live changes, so a partially written SKILL.md can be read
	// mid-write; rename makes the swap atomic.
	tmp, err := os.CreateTemp(dir, "SKILL.md.*")
	if err != nil {
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(Content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	r.Outcome = Written
	return r
}

func removeSkill(path string) Result {
	if _, err := os.Stat(path); err != nil {
		return Result{Path: path, Outcome: Absent}
	}
	if err := os.Remove(path); err != nil {
		return Result{Path: path, Outcome: Failed, Err: err}
	}
	// Drop the now-empty `fleet/` directory. Fails harmlessly if the user kept
	// other files beside the skill, which is the outcome we want either way.
	_ = os.Remove(filepath.Dir(path))
	return Result{Path: path, Outcome: Removed}
}

func statusOf(path string) Result {
	existing, err := os.ReadFile(path)
	if err != nil {
		return Result{Path: path, Outcome: Absent}
	}
	if bytes.Equal(existing, Content) {
		return Result{Path: path, Outcome: Installed}
	}
	return Result{Path: path, Outcome: Outdated}
}
