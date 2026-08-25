package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/naming"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

const addUsage = "Usage: fleet add [path] [flags]"

// errMissingAddArgs is returned for a bare `fleet add`. The path is optional
// once any flag is given, but the bare word stays a usage error: it was one
// before, and a mistyped `fleet add` in the wrong terminal tab should not
// launch an agent in whatever directory that tab happened to be in.
var errMissingAddArgs = errors.New("nothing to do")

// addOpts holds the parsed `fleet add` invocation. Empty fields mean "unset";
// their defaults depend on the cwd and the user's config, which parsing can't
// see.
type addOpts struct {
	path      string
	agentName string
	account   string
	// prompt is the raw flag value, which may still be "-" for stdin. Reading
	// stdin is I/O, and parsing stays pure — runAdd resolves it.
	prompt string
	model  string
	effort string
}

// addFlagSet builds the `fleet add` flag set, binding into o. The flag package
// is kept silent for the reason worktreeFlagSet documents.
func addFlagSet(o *addOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&o.agentName, "agent", "", "agent to run: claude, codex, or opencode (default: default_agent config)")
	fs.StringVar(&o.account, "account", "", "Claude account email to run as (default: chosen by account_strategy config)")
	fs.StringVar(&o.prompt, "prompt", "", "first message for the agent, which it starts working on (use - to read stdin)")
	fs.StringVar(&o.prompt, "p", "", "shorthand for -prompt")
	fs.StringVar(&o.model, "model", "", "model to launch on, e.g. opus or anthropic/claude-sonnet-5 (default: the agent's own)")
	fs.StringVar(&o.effort, "effort", "", "reasoning effort to launch at, e.g. high or xhigh (default: the agent's own)")
	return fs
}

// printAddUsage writes the usage line and flag defaults to w.
func printAddUsage(w io.Writer) {
	fmt.Fprintln(w, addUsage)
	var discard addOpts
	fs := addFlagSet(&discard)
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// parseAddArgs parses and validates the `fleet add` flags. Kept pure (no
// config, no filesystem) so the argument rules are testable.
func parseAddArgs(args []string) (addOpts, error) {
	var o addOpts
	if len(args) == 0 {
		return o, errMissingAddArgs
	}

	fs := addFlagSet(&o)
	// Parse in a loop, peeling off one positional at a time, so flags work on
	// either side of the path — same as `fleet worktree`, and for the same
	// reason: a path is not free-form prose, so nothing is lost by letting the
	// flag parser see the whole line. (`fleet send` is the deliberate exception;
	// its message can start with a dash.)
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return o, err
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			break
		}
		positional = append(positional, remaining[0])
		rest = remaining[1:]
	}

	if len(positional) > 1 {
		return o, fmt.Errorf("unexpected argument %q — expected a single path", positional[1])
	}
	if len(positional) == 1 {
		o.path = strings.TrimSpace(positional[0])
	}

	// fs.Visit is the only thing separating `-p ''` from -p not given: both
	// leave the value empty.
	var promptSet bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt" || f.Name == "p" {
			promptSet = true
		}
	})
	// An explicitly empty prompt is almost always a command substitution that
	// failed — `-p "$(gh issue view 999)"` on a missing issue. Silently starting
	// a session with no prompt would look like the flag isn't wired up.
	if promptSet && strings.TrimSpace(o.prompt) == "" {
		return o, fmt.Errorf("-prompt was empty")
	}
	o.prompt = strings.TrimSpace(o.prompt)

	// agent.Parse falls back to Claude for anything it doesn't recognize, so a
	// typo would silently launch the wrong agent. Reject it here instead.
	if o.agentName != "" {
		switch agent.Type(o.agentName) {
		case agent.Claude, agent.Codex, agent.OpenCode:
		default:
			return o, fmt.Errorf("unknown agent %q — expected claude, codex, or opencode", o.agentName)
		}
	}
	// The account token is a claude.ai credential; the other agents never read
	// it, so accepting the flag here would silently do nothing.
	if o.account != "" && o.agentName != "" && agent.Type(o.agentName) != agent.Claude {
		return o, fmt.Errorf("--account only applies to claude sessions (got --agent %s)", o.agentName)
	}

	if err := validateLaunchOverrides(o.model, o.effort); err != nil {
		return o, err
	}
	return o, nil
}

// validateLaunchOverrides checks the model and effort values both commands
// accept. They are embedded in the command string that is typed into the pane's
// shell, so an unvalidated value would be executed by it — see
// agent.ValidateLaunchValue.
func validateLaunchOverrides(model, effort string) error {
	if model != "" {
		if err := agent.ValidateLaunchValue("model", model); err != nil {
			return err
		}
	}
	if effort != "" {
		if err := agent.ValidateLaunchValue("effort", effort); err != nil {
			return err
		}
	}
	return nil
}

func runAdd(args []string) {
	opts, err := parseAddArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printAddUsage(os.Stdout) // `-h` is a request, not a failure
			return
		}
		if !errors.Is(err, errMissingAddArgs) {
			fmt.Fprintln(os.Stderr, err)
		}
		// A bad flag or a bare `fleet add` is a usage error — show the flags. A
		// bad *value* isn't: the message already says what to fix.
		if errors.Is(err, errMissingAddArgs) || strings.HasPrefix(err.Error(), "flag ") {
			printAddUsage(os.Stderr)
		}
		os.Exit(1)
	}

	// Resolve `-p -` before anything is created: a pipe that produced nothing
	// should fail while the session still doesn't exist.
	prompt, err := readStdinArg(opts.prompt, os.Stdin, "prompt")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Migrate a legacy `brizz-code` config dir before anything below creates
	// `~/.config/fleet/` — debuglog.Init does, and session.Open creates
	// state.db. Once state.db exists, migrateConfigDir permanently bails out
	// ("both dirs have state.db") and still writes its marker, silently
	// stranding the user's sessions, pins and slot bindings. runTUI and
	// runWorktree do this for the same reason and in the same order.
	migration.Run()

	// Session.Start logs at Info level, and debuglog's fallback logger writes
	// to stderr — without Init those lines land on the user's terminal instead
	// of ~/.config/fleet/debug.log, on top of this command's own output.
	debuglog.Init()
	defer debuglog.Close()

	if err := tmux.IsTmuxAvailable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The path is optional: with flags given and no positional, the session
	// goes in the current directory, matching `fleet worktree --path`.
	path := opts.path
	if path == "" {
		path, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to resolve the current directory: %v\n", err)
			os.Exit(1)
		}
	}
	path = expandPath(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Invalid directory: %s\n", path)
		os.Exit(1)
	}

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	cfg := config.Load()
	accounts := claudeaccount.Load()
	session.SetAccountConfigDirFunc(accounts.ConfigDirFor)

	ag := resolveLaunchAgent(cfg, opts.agentName, true)
	account := resolveLaunchAccount(cfg, accounts, path, ag, opts.account)

	// A prompt names the work, and the work is what the user is looking for in
	// the sidebar — so it titles the session, the same heuristic the TUI's own
	// auto-naming falls back to. TitleGenerated is set alongside it exactly as
	// the TUI does, which keeps the agent's own title free to override this
	// later (that path checks only ManuallyRenamed) while stopping the worker
	// from regenerating the identical title from FirstPrompt.
	title := session.TitleFromPath(path)
	titleGenerated := false
	if prompt != "" {
		if t := naming.GenerateTitle(prompt); t != "" {
			title, titleGenerated = t, true
		}
	}

	s := session.NewSession(title, path)
	s.TitleGenerated = titleGenerated
	launchSession(s, ag, launchOverrides{
		account: account,
		prompt:  prompt,
		model:   opts.model,
		effort:  opts.effort,
	}, storage, launchNotes{})

	fmt.Printf("Created %s session '%s' (%s) in %s\n", ag.DisplayName(), title, s.ID, path)
	// Echo the prompt back: with `-p -` the text came from a pipe the user never
	// saw, and this is the only confirmation that what arrived is what they meant.
	if prompt != "" {
		fmt.Printf("Working on: %s\n", summarizeText(prompt, 60))
	}
}
