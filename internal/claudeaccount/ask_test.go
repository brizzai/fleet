package claudeaccount

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClaude swaps the exec seam for the duration of a test. Nothing here may
// run the real claude: it spends the user's quota.
func fakeClaude(t *testing.T, fn func(ctx context.Context, c askCall) ([]byte, []byte, error)) *[]askCall {
	t.Helper()
	var calls []askCall
	prevExec, prevLook := askExec, askLookPath
	askExec = func(ctx context.Context, c askCall) ([]byte, []byte, error) {
		calls = append(calls, c)
		return fn(ctx, c)
	}
	askLookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	t.Cleanup(func() { askExec, askLookPath = prevExec, prevLook })
	return &calls
}

func TestAskWithBuildsTheCall(t *testing.T) {
	t.Setenv("FLEET_INSTANCE_ID", "parent-session")
	t.Setenv("FLEET_INITIAL_PROMPT", "do the parent's task")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-inherited")
	calls := fakeClaude(t, func(context.Context, askCall) ([]byte, []byte, error) {
		return []byte("  {\"type\":\"result\"}\n"), nil, nil
	})
	out, err := AskWith(context.Background(), AskOptions{
		ConfigDir:            "/acct/1",
		Dir:                  "/wt",
		Instruction:          "review it",
		Input:                "DIFF",
		Model:                "sonnet",
		OutputFormat:         "json",
		Verbose:              true,
		AllowedTools:         []string{"Read", "Bash(git log:*)"},
		DisallowedTools:      []string{"Edit", "Write"},
		NoSessionPersistence: true,
	})
	if err != nil || out != `{"type":"result"}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	c := (*calls)[0]
	want := []string{"-p", "review it", "--model", "sonnet", "--output-format", "json", "--verbose",
		"--allowedTools", "Read", "Bash(git log:*)", "--disallowedTools", "Edit", "Write",
		"--no-session-persistence"}
	if !slices.Equal(c.Args, want) {
		t.Errorf("args = %q\nwant   %q", c.Args, want)
	}
	if c.Name != "claude" || c.Dir != "/wt" || c.Stdin != "DIFF" {
		t.Errorf("call = %+v", c)
	}
	if !slices.Contains(c.Env, "CLAUDE_CONFIG_DIR=/acct/1") {
		t.Error("account config dir not set")
	}
	for _, kv := range c.Env {
		for _, bad := range []string{"FLEET_INSTANCE_ID=", "FLEET_INITIAL_PROMPT=", "ANTHROPIC_API_KEY="} {
			if strings.HasPrefix(kv, bad) {
				t.Errorf("env carries %s — a reviewer's hooks would write into the parent session / bill the wrong login", kv)
			}
		}
	}
}

// The instruction must come straight after -p: the tool flags are variadic
// and would swallow it as a tool name.
func TestAskArgsInstructionBeforeVariadics(t *testing.T) {
	args := askArgs(AskOptions{Instruction: "go", AllowedTools: []string{"Read"}, DisallowedTools: []string{"Edit"}})
	if args[0] != "-p" || args[1] != "go" {
		t.Errorf("args = %q", args)
	}
	// Each variadic list is closed by another flag or the end of argv.
	if args := askArgs(AskOptions{Instruction: "-x"}); args[1] != " -x" {
		t.Errorf("a dash-led instruction would parse as an option: %q", args)
	}
	if args := askArgs(AskOptions{Instruction: "plain"}); len(args) != 2 {
		t.Errorf("zero options added flags: %q", args)
	}
}

func TestAskAmbientKeepsLoginButDropsFleetVars(t *testing.T) {
	t.Setenv("FLEET_INSTANCE_ID", "x")
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient")
	env := askEnv("")
	if !slices.Contains(env, "CLAUDE_CONFIG_DIR=/ambient") {
		t.Error("ambient call lost the inherited config dir")
	}
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "FLEET_INSTANCE_ID=") }) {
		t.Error("ambient call kept FLEET_INSTANCE_ID")
	}
}

func TestAskTimeout(t *testing.T) {
	fakeClaude(t, func(ctx context.Context, _ askCall) ([]byte, []byte, error) {
		<-ctx.Done()
		return nil, nil, errors.New("signal: killed")
	})
	_, err := AskWith(context.Background(), AskOptions{Instruction: "x", Timeout: 20 * time.Millisecond})
	if !errors.Is(err, ErrAskTimeout) {
		t.Fatalf("err = %v, want ErrAskTimeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AskWith(ctx, AskOptions{Instruction: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v, want context.Canceled (not a timeout)", err)
	}
}

func TestAskMissingBinary(t *testing.T) {
	fakeClaude(t, nil)
	askLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := Ask(context.Background(), "", "x", ""); !errors.Is(err, ErrClaudeMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestAskFailureFromJSONEnvelope(t *testing.T) {
	fakeClaude(t, func(context.Context, askCall) ([]byte, []byte, error) {
		return []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You've hit your session limit · resets 3:50pm"}`),
			[]byte("Shell cwd was reset to /tmp"), errors.New("exit status 1")
	})
	_, err := AskWith(context.Background(), AskOptions{Instruction: "x", OutputFormat: "json"})
	if err == nil || err.Error() != "claude: You've hit your session limit · resets 3:50pm" {
		t.Fatalf("err = %v, want the envelope's result as the reason", err)
	}
}

// Measured against a real spent subscription: `claude -p` exits 1, puts the
// actual reason on STDOUT, and puts only advisories on stderr. Reading stderr
// first reported a settings.json permission rule as the cause of a quota
// refusal — a false statement about the user's setup that sends them to fix a
// file which was never the problem.
func TestAskFailureReasonPrefersStdoutOverWarnings(t *testing.T) {
	stdout := "You've hit your session limit · resets 3:50pm (Asia/Jerusalem)"
	stderr := `Permission allow rule (/Users/x/.config/fleet/accounts/1/settings.json): Bash(find ~/go/pkg/mod -type f) has a wildcard before the rest of the command.
Shell cwd was reset to /Users/x/code/fleet`

	got := askFailureReason(stdout, stderr, errors.New("exit status 1"))
	if !strings.Contains(got, "session limit") {
		t.Errorf("reason = %q, want the quota refusal that actually stopped it", got)
	}
	if strings.Contains(got, "Permission allow rule") {
		t.Error("reported a settings warning as the cause of the failure")
	}
}

// --verbose turns the envelope into an event array; a failure's reason is in
// its last result event, not on the array's first line.
func TestAskFailureReasonReadsAVerboseEventArray(t *testing.T) {
	stdout := `[{"type":"system","subtype":"init"},{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You've hit your session limit · resets 3:50pm"}]`
	if got := askFailureReason(stdout, "", errors.New("exit status 1")); !strings.HasPrefix(got, "You've hit your session limit") {
		t.Errorf("reason = %q, want the result event's text", got)
	}
}

// With nothing on stdout, the advisories are still skipped: they accompany a
// failure rather than being one.
func TestAskFailureReasonSkipsNotesOnStderrToo(t *testing.T) {
	stderr := "Permission allow rule (x): bad\nShell cwd was reset to /tmp\nAPI Error: overloaded"
	if got := askFailureReason("", stderr, errors.New("exit status 1")); got != "API Error: overloaded" {
		t.Errorf("reason = %q, want the real error under the notes", got)
	}
	if got := askFailureReason("", "Shell cwd was reset to /tmp", errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("reason = %q, want the exit status", got)
	}
}

// The reason lands in a one-line footer, and claude's warnings run to a
// paragraph. A wrapped toast pushes the keys off the screen.
func TestAskFailureReasonFitsAFooter(t *testing.T) {
	long := strings.Repeat("something went wrong ", 40)
	got := askFailureReason(long, "", errors.New("exit status 1"))
	if len([]rune(got)) > askReasonMax {
		t.Errorf("reason is %d runes, over the %d cap", len([]rune(got)), askReasonMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated reason does not say it was truncated")
	}
}

// fakeHelp swaps the `claude --help` probe and resets the per-process cache.
func fakeHelp(t *testing.T, help string, err error) *int {
	t.Helper()
	var n int
	prev := askHelpText
	askHelpText = func(context.Context) (string, error) { n++; return help, err }
	resetAskFlags()
	t.Cleanup(func() { askHelpText = prev; resetAskFlags() })
	return &n
}

func TestAskRestrictsToolsAndMCP(t *testing.T) {
	probes := fakeHelp(t, "  --strict-mcp-config   Only use MCP servers…\n  --tools <tools...>   Built-in tools…", nil)
	calls := fakeClaude(t, func(context.Context, askCall) ([]byte, []byte, error) { return []byte("ok"), nil, nil })
	o := AskOptions{Instruction: "go", AllowedTools: []string{"Read"}, RestrictTools: true,
		Tools: []string{"Read", "Grep", "Glob"}, StrictMCPConfig: true}
	for range 2 {
		if _, err := AskWith(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"-p", "go", "--allowedTools", "Read", "--tools", "Read,Grep,Glob", "--strict-mcp-config"}
	if got := (*calls)[0].Args; !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant   %q", got, want)
	}
	if *probes != 1 {
		t.Errorf("--help probed %d times, want once per process", *probes)
	}

	// No tools at all is an explicit empty value, not an absent flag.
	if args := askArgs(AskOptions{Instruction: "x", RestrictTools: true}); !slices.Equal(args, []string{"-p", "x", "--tools", ""}) {
		t.Errorf("no-tools args = %q", args)
	}
}

func TestAskDropsRestrictionOnAnOldCLI(t *testing.T) {
	fakeHelp(t, "  --allowedTools <tools...>\n  --mcp-config <configs...>", nil)
	calls := fakeClaude(t, func(context.Context, askCall) ([]byte, []byte, error) { return []byte("ok"), nil, nil })
	_, err := AskWith(context.Background(), AskOptions{Instruction: "go", DisallowedTools: []string{"Edit"},
		RestrictTools: true, Tools: []string{"Read"}, StrictMCPConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range (*calls)[0].Args {
		if a == "--tools" || a == "--strict-mcp-config" {
			t.Errorf("old CLI got %s, which it would reject: %q", a, (*calls)[0].Args)
		}
	}
	if !slices.Contains((*calls)[0].Args, "--disallowedTools") {
		t.Error("the deny list must survive the downgrade")
	}
}

func TestAskHelpFailureIsNotCached(t *testing.T) {
	probes := fakeHelp(t, "", errors.New("timeout"))
	fakeClaude(t, func(context.Context, askCall) ([]byte, []byte, error) { return []byte("ok"), nil, nil })
	o := AskOptions{Instruction: "go", RestrictTools: true, Tools: []string{"Read"}}
	_, _ = AskWith(context.Background(), o)
	_, _ = AskWith(context.Background(), o)
	if *probes != 2 {
		t.Errorf("a failed probe was cached: %d probes", *probes)
	}
}
