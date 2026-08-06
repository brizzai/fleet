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
		// A prompt is the agent's own argument — positional for Claude/Codex,
		// --prompt for OpenCode, whose positional is a project path instead.
		{"claude prompt", Claude, LaunchOpts{Prompt: "fix it"}, `claude "$FLEET_INITIAL_PROMPT"`},
		{"codex prompt", Codex, LaunchOpts{Prompt: "fix it"}, `codex "$FLEET_INITIAL_PROMPT"`},
		{"opencode prompt", OpenCode, LaunchOpts{Prompt: "fix it"}, `opencode --prompt "$FLEET_INITIAL_PROMPT"`},
		{"claude resume with prompt", Claude, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `claude --resume abc "$FLEET_INITIAL_PROMPT"`},
		{"codex resume with prompt", Codex, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `codex resume abc "$FLEET_INITIAL_PROMPT"`},
		{"opencode resume with prompt", OpenCode, LaunchOpts{ResumeID: "abc", Prompt: "go on"}, `opencode --session abc --prompt "$FLEET_INITIAL_PROMPT"`},
		{"claude fork with prompt", Claude, LaunchOpts{ForkID: "abc", Prompt: "go on"}, `claude --resume abc --fork-session "$FLEET_INITIAL_PROMPT"`},
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

func TestBinaryAndDisplayName(t *testing.T) {
	if Claude.Binary() != "claude" || Codex.Binary() != "codex" || OpenCode.Binary() != "opencode" {
		t.Errorf("unexpected Binary(): claude=%q codex=%q opencode=%q", Claude.Binary(), Codex.Binary(), OpenCode.Binary())
	}
	if Claude.DisplayName() != "Claude" || Codex.DisplayName() != "Codex" || OpenCode.DisplayName() != "OpenCode" {
		t.Errorf("unexpected DisplayName(): claude=%q codex=%q opencode=%q", Claude.DisplayName(), Codex.DisplayName(), OpenCode.DisplayName())
	}
}
