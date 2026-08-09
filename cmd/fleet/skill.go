package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/brizzai/fleet/internal/skill"
)

const skillUsage = "Usage: fleet skill <install|uninstall|status> [flags]"

// skillOpts holds the parsed `fleet skill` invocation. agentSel is empty when
// unset, because the default differs per verb (see resolveAgents).
type skillOpts struct {
	agentSel string
}

// skillFlagSet builds the `fleet skill` flag set. Kept silent for the same
// reason as worktreeFlagSet: flag.ContinueOnError still writes the error and
// runs Usage, which would print everything twice.
func skillFlagSet(o *skillOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet skill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&o.agentSel, "agent", "", "comma-separated agents, or `all` (default: the agents installed on this machine)")
	return fs
}

func printSkillUsage(w io.Writer) {
	fmt.Fprintln(w, skillUsage)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Installs the fleet agent skill, teaching a coding agent how to drive fleet")
	fmt.Fprintln(w, "from a shell. Written to the skill directory each agent reads:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ~/.claude/skills/fleet/  Claude Code")
	fmt.Fprintln(w, "  ~/.agents/skills/fleet/  Codex, Cursor, OpenCode")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Never prompts, so an agent can run `fleet skill install` for itself.")
	fmt.Fprintln(w)
	fs := skillFlagSet(&skillOpts{})
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func runSkill(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, skillUsage)
		os.Exit(1)
	}
	// `fleet skill -h` (no verb) is a help request, not a bad invocation.
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printSkillUsage(os.Stdout)
		return
	}

	verb := args[0]
	var opts skillOpts
	fs := skillFlagSet(&opts)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printSkillUsage(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, "%v\n", err)
		printSkillUsage(os.Stderr)
		os.Exit(1)
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "Unexpected argument: %s\n", rest[0])
		fmt.Fprintln(os.Stderr, skillUsage)
		os.Exit(1)
	}

	switch verb {
	case "install":
		selected, err := resolveAgents(opts.agentSel, skill.Detected())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		if len(selected) == 0 {
			fmt.Fprintln(os.Stderr, "No supported agent found on this machine (looked for claude, codex, cursor, opencode).")
			fmt.Fprintln(os.Stderr, "Run 'fleet skill install -agent all' to install anyway.")
			os.Exit(1)
		}
		results := skill.Install(selected)
		printSkillResults(results)
		if !anyFailed(results) {
			fmt.Println("\nAlready-open agent sessions may need a restart to see it.")
		}
		exitOnFailure(results)
	case "uninstall":
		// Uninstall defaults to every agent, not just the detected ones: the
		// copy worth cleaning up is often the one belonging to an agent the
		// user has since removed. Removal only ever touches files fleet wrote.
		selected, err := resolveAgents(opts.agentSel, skill.Agents())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		results := skill.Uninstall(selected)
		printSkillResults(results)
		exitOnFailure(results)
	case "status":
		results := skill.Status()
		printSkillResults(results)
		for _, r := range results {
			if r.Outcome == skill.Outdated {
				fmt.Println("\nRun 'fleet skill install' to update.")
				break
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown skill subcommand: %s\n", verb)
		fmt.Fprintln(os.Stderr, skillUsage)
		os.Exit(1)
	}
}

// resolveAgents turns the -agent value into the agents to act on. An empty
// value means the verb's default; "all" means every agent fleet knows.
//
// Names are validated explicitly rather than ignored when unrecognized: a typo
// would otherwise silently install for nothing and still report success.
func resolveAgents(sel string, fallback []skill.Agent) ([]skill.Agent, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return fallback, nil
	}
	if sel == "all" {
		return skill.Agents(), nil
	}
	var chosen []skill.Agent
	for name := range strings.SplitSeq(sel, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		a, ok := skill.Find(name)
		if !ok {
			return nil, fmt.Errorf("unknown agent %q: expected one of claude, codex, cursor, opencode, all", name)
		}
		chosen = append(chosen, a)
	}
	if len(chosen) == 0 {
		return nil, errors.New("-agent was given no agent names")
	}
	return chosen, nil
}

// skillOutcomeLine maps an outcome to its marker, label, and the detail column.
func skillOutcomeLine(r skill.Result) (marker, label, detail string) {
	switch r.Outcome {
	case skill.Written:
		return "✓", "installed", tildePath(r.Path)
	case skill.Unchanged, skill.Installed:
		return "✓", "up to date", tildePath(r.Path)
	case skill.Outdated:
		return "!", "outdated", tildePath(r.Path)
	case skill.Removed:
		return "✓", "removed", tildePath(r.Path)
	case skill.Absent:
		return "-", "not installed", tildePath(r.Path)
	case skill.Skipped:
		// An agent is skipped either because it isn't here or because -agent
		// named someone else. Re-check rather than assume: telling a Cursor user
		// that Cursor isn't installed, when they simply passed -agent claude,
		// is a claim about their machine that happens to be false.
		if a, ok := skill.Find(r.Agent); ok && a.Detected() {
			return "-", "skipped", "not selected"
		}
		return "-", "skipped", "not found on this machine"
	case skill.Failed:
		return "✕", "failed", fmt.Sprint(r.Err)
	default:
		// Unreachable unless a new Outcome lands without a line here; "?" makes
		// that visible rather than printing a bare enum value as a status.
		return "?", string(r.Outcome), tildePath(r.Path)
	}
}

func printSkillResults(results []skill.Result) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, r := range results {
		marker, label, detail := skillOutcomeLine(r)
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", marker, r.Agent, label, detail)
	}
	w.Flush()
}

func anyFailed(results []skill.Result) bool {
	for _, r := range results {
		if r.Outcome == skill.Failed {
			return true
		}
	}
	return false
}

func exitOnFailure(results []skill.Result) {
	if anyFailed(results) {
		os.Exit(1)
	}
}

// tildePath shortens a path under the user's home to `~/...`, which is both
// shorter and still pasteable into a shell.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}
