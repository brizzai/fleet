package agent

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := map[string]Type{
		"claude":   Claude,
		"codex":    Codex,
		"opencode": OpenCode,
		"":         Claude, // empty → default
		"unknown":  Claude, // unrecognized → default
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLaunchCmd(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		opts LaunchOpts
		want string
	}{
		{"claude new", Claude, LaunchOpts{}, "claude"},
		{"claude resume", Claude, LaunchOpts{ResumeID: "abc"}, "claude --resume abc"},
		{"claude fork", Claude, LaunchOpts{ForkID: "abc"}, "claude --resume abc --fork-session"},
		{"claude fork wins over resume", Claude, LaunchOpts{ResumeID: "r", ForkID: "f"}, "claude --resume f --fork-session"},
		{"codex new", Codex, LaunchOpts{}, "codex"},
		{"codex resume", Codex, LaunchOpts{ResumeID: "abc"}, "codex resume abc"},
		{"codex fork", Codex, LaunchOpts{ForkID: "abc"}, "codex fork abc"},
		{"codex fork wins over resume", Codex, LaunchOpts{ResumeID: "r", ForkID: "f"}, "codex fork f"},
		{"opencode new", OpenCode, LaunchOpts{}, "opencode"},
		{"opencode resume", OpenCode, LaunchOpts{ResumeID: "abc"}, "opencode --session abc"},
		{"opencode fork", OpenCode, LaunchOpts{ForkID: "abc"}, "opencode --session abc --fork"},
		{"opencode fork wins over resume", OpenCode, LaunchOpts{ResumeID: "r", ForkID: "f"}, "opencode --session f --fork"},
		// A prompt is the agent's own argument, behind a separator that ends
		// option parsing — `--` for Claude/Codex, `--prompt=` for OpenCode,
		// whose positional is a project path instead.
		{"claude prompt", Claude, LaunchOpts{Prompt: "fix it"}, `claude -- "$FLEET_INITIAL_PROMPT"`},
		{"codex prompt", Codex, LaunchOpts{Prompt: "fix it"}, `codex -- "$FLEET_INITIAL_PROMPT"`},
		{"opencode prompt", OpenCode, LaunchOpts{Prompt: "fix it"}, `opencode --prompt="$FLEET_INITIAL_PROMPT"`},
		{"claude resume with prompt", Claude, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `claude --resume abc -- "$FLEET_INITIAL_PROMPT"`},
		{"codex resume with prompt", Codex, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `codex resume abc -- "$FLEET_INITIAL_PROMPT"`},
		{"opencode resume with prompt", OpenCode, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `opencode --session abc --prompt="$FLEET_INITIAL_PROMPT"`},
		{"claude fork with prompt", Claude, LaunchOpts{ForkID: "abc", Prompt: "go on"}, `claude --resume abc --fork-session -- "$FLEET_INITIAL_PROMPT"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.typ.BuildLaunchCmd(c.opts); got != c.want {
				t.Errorf("BuildLaunchCmd() = %q, want %q", got, c.want)
			}
		})
	}
}

// The launch command is typed into the pane's shell, so any prompt text
// embedded in it would be parsed by that shell — `$(...)` would execute and a
// newline would end the command early. The text must travel in the environment
// instead, leaving only the expansion in the command.
func TestBuildLaunchCmdNeverEmbedsPromptText(t *testing.T) {
	nasty := "fix $(touch /tmp/pwned) `id` \"quoted\" 'single'\nsecond line"
	for _, typ := range []Type{Claude, Codex, OpenCode} {
		cmd := typ.BuildLaunchCmd(LaunchOpts{Prompt: nasty})
		for _, leak := range []string{"touch", "$(", "`", "\n", "second line"} {
			if strings.Contains(cmd, leak) {
				t.Errorf("%s: launch command leaked %q from the prompt: %q", typ, leak, cmd)
			}
		}
		if !strings.Contains(cmd, `"$`+PromptEnvVar+`"`) {
			t.Errorf("%s: launch command should reference the prompt env var, got %q", typ, cmd)
		}
	}
}

// Quoting the expansion satisfies the *shell*; the agent's argv parser is a
// second parser that reads a leading dash as an option and exits, stranding a
// live session at a shell prompt with the message gone. `fleet send` promises a
// message may start with a dash, so the separator has to hold that promise all
// the way to argv.
func TestBuildLaunchCmdSeparatesPromptFromOptions(t *testing.T) {
	cases := map[Type]string{
		Claude:   `-- "$FLEET_INITIAL_PROMPT"`,
		Codex:    `-- "$FLEET_INITIAL_PROMPT"`,
		OpenCode: `--prompt="$FLEET_INITIAL_PROMPT"`, // `--prompt <val>` makes yargs read the value as an option
	}
	for typ, want := range cases {
		cmd := typ.BuildLaunchCmd(LaunchOpts{Prompt: "--force is not a flag here"})
		if !strings.HasSuffix(cmd, want) {
			t.Errorf("%s: launch command = %q, want it to end with %q", typ, cmd, want)
		}
	}
}

func TestBinaryAndDisplayName(t *testing.T) {
	if Claude.Binary() != "claude" || Codex.Binary() != "codex" || OpenCode.Binary() != "opencode" {
		t.Errorf("unexpected Binary(): claude=%q codex=%q opencode=%q", Claude.Binary(), Codex.Binary(), OpenCode.Binary())
	}
	if Claude.DisplayName() != "Claude" || Codex.DisplayName() != "Codex" || OpenCode.DisplayName() != "OpenCode" {
		t.Errorf("unexpected DisplayName(): claude=%q codex=%q opencode=%q", Claude.DisplayName(), Codex.DisplayName(), OpenCode.DisplayName())
	}
}

// The three agents spell reasoning effort three different ways, and Codex does
// not spell it as a flag at all. Getting one of them wrong launches an agent
// that either ignores the setting or refuses the flag and exits, leaving a live
// pane at a shell prompt.
func TestBuildLaunchCmdModelAndEffortPerAgent(t *testing.T) {
	tests := []struct {
		name string
		ag   Type
		o    LaunchOpts
		want string
	}{
		{"claude model", Claude, LaunchOpts{Model: "opus"}, "claude --model opus"},
		{"claude effort", Claude, LaunchOpts{Effort: "xhigh"}, "claude --effort xhigh"},
		{"claude both", Claude, LaunchOpts{Model: "claude-opus-5", Effort: "high"}, "claude --model claude-opus-5 --effort high"},
		{"codex effort is a config override", Codex, LaunchOpts{Model: "gpt-5.1-codex-max", Effort: "high"}, "codex --model gpt-5.1-codex-max -c model_reasoning_effort=high"},
		{"opencode takes a model but never an effort", OpenCode, LaunchOpts{Model: "anthropic/claude-sonnet-5", Effort: "max"}, "opencode --model anthropic/claude-sonnet-5"},
		{"neither set changes nothing", Claude, LaunchOpts{}, "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ag.BuildLaunchCmd(tt.o); got != tt.want {
				t.Errorf("BuildLaunchCmd() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The prompt argument ends option parsing (`--`), so anything appended after it
// stops being read as a flag and is handed to the agent as prompt text instead.
func TestModelAndEffortPrecedeThePrompt(t *testing.T) {
	for _, ag := range []Type{Claude, Codex, OpenCode} {
		cmd := ag.BuildLaunchCmd(LaunchOpts{Model: "opus", Effort: "high", Prompt: "do the thing"})
		promptAt := strings.Index(cmd, PromptEnvVar)
		if promptAt < 0 {
			t.Fatalf("%s: prompt missing from %q", ag, cmd)
		}
		want := []string{"opus"}
		if ag.SupportsEffort() {
			want = append(want, "high")
		}
		for _, flag := range want {
			if at := strings.Index(cmd, flag); at < 0 || at > promptAt {
				t.Errorf("%s: %q must precede the prompt argument, got %q", ag, flag, cmd)
			}
		}
	}
}

// Model and effort are the only launch values embedded in the command string,
// which tmux send-keys types into the pane's *shell*. A value carrying a shell
// metacharacter would be executed by it, so the validator is the whole defence
// and every real-world value has to survive it.
func TestValidateLaunchValue(t *testing.T) {
	ok := []string{
		"opus", "sonnet", "haiku", "fable",
		"claude-opus-5", "claude-sonnet-5", "gpt-5.1-codex-max",
		"anthropic/claude-sonnet-5", "openai/gpt-5",
		"low", "medium", "high", "xhigh", "max", "ultracode", "minimal",
		"model.v1.2",
	}
	for _, v := range ok {
		if err := ValidateLaunchValue("model", v); err != nil {
			t.Errorf("ValidateLaunchValue(%q) = %v, want nil", v, err)
		}
	}

	bad := []string{
		"", " ", "opus; rm -rf ~", "$(whoami)", "`id`", "a|b", "a&b", "a>b",
		"a b", "a\nb", "-opus", "--model", "a'b", `a"b`, "a$b", "a\\b", "a(b)",
	}
	for _, v := range bad {
		if err := ValidateLaunchValue("model", v); err == nil {
			t.Errorf("ValidateLaunchValue(%q) = nil, want an error", v)
		}
	}
}

// OpenCode has the concept of a reasoning effort but no way to accept one on
// the command fleet launches: `--variant` is declared on the `run` subcommand,
// while fleet runs the default `$0 [project]` command, whose builder declares
// only project/prompt/network options — and OpenCode's root parser is yargs in
// .strict() mode, so an unknown option prints the command list and exits.
//
// fleet shipped `--variant` here briefly. The cost of getting it wrong is not a
// dropped setting: it is a session fleet reports as created, row saved and repo
// pinned, whose pane is a shell prompt with no agent in it.
func TestOpenCodeNeverReceivesAnEffortFlag(t *testing.T) {
	if OpenCode.SupportsEffort() {
		t.Fatal("OpenCode.SupportsEffort() = true, want false")
	}
	for _, ag := range []Type{Claude, Codex} {
		if !ag.SupportsEffort() {
			t.Errorf("%s.SupportsEffort() = false, want true", ag)
		}
	}

	// BuildLaunchCmd guards too, so a caller that forgets to reject the flag
	// drops it rather than launching a command OpenCode refuses outright.
	for _, o := range []LaunchOpts{
		{Effort: "high"},
		{Effort: "max", Model: "anthropic/claude-sonnet-5"},
		{Effort: "minimal", ResumeID: "abc", Prompt: "x"},
	} {
		cmd := OpenCode.BuildLaunchCmd(o)
		for _, banned := range []string{"--variant", "--effort", "model_reasoning_effort", o.Effort} {
			if strings.Contains(cmd, banned) {
				t.Errorf("opencode command %q must not carry %q", cmd, banned)
			}
		}
	}
}
