package agent

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]Type{
		"claude":  Claude,
		"codex":   Codex,
		"":        Claude, // empty → default
		"unknown": Claude, // unrecognized → default
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.typ.BuildLaunchCmd(c.opts); got != c.want {
				t.Errorf("BuildLaunchCmd() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBinaryAndDisplayName(t *testing.T) {
	if Claude.Binary() != "claude" || Codex.Binary() != "codex" {
		t.Errorf("unexpected Binary(): claude=%q codex=%q", Claude.Binary(), Codex.Binary())
	}
	if Claude.DisplayName() != "Claude" || Codex.DisplayName() != "Codex" {
		t.Errorf("unexpected DisplayName(): claude=%q codex=%q", Claude.DisplayName(), Codex.DisplayName())
	}
}
