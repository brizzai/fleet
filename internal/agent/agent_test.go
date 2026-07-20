package agent

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]Type{
		"claude":   Claude,
		"codex":    Codex,
		"opencode": OpenCode,
		"cursor":   Cursor,
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
		{"cursor new", Cursor, LaunchOpts{}, "cursor-agent"},
		{"cursor resume", Cursor, LaunchOpts{ResumeID: "abc"}, "cursor-agent --resume abc"},
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
	if Claude.Binary() != "claude" || Codex.Binary() != "codex" || OpenCode.Binary() != "opencode" || Cursor.Binary() != "cursor-agent" {
		t.Errorf("unexpected Binary(): claude=%q codex=%q opencode=%q cursor=%q", Claude.Binary(), Codex.Binary(), OpenCode.Binary(), Cursor.Binary())
	}
	if Claude.DisplayName() != "Claude" || Codex.DisplayName() != "Codex" || OpenCode.DisplayName() != "OpenCode" || Cursor.DisplayName() != "Cursor" {
		t.Errorf("unexpected DisplayName(): claude=%q codex=%q opencode=%q cursor=%q", Claude.DisplayName(), Codex.DisplayName(), OpenCode.DisplayName(), Cursor.DisplayName())
	}
}
