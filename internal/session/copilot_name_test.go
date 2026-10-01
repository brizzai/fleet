package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCopilotWorkspaceName(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		// Shapes copied from real session-state/<id>/workspace.yaml files.
		"generated":   {"id: x\nclient_name: github/cli\nname: Create Text File with Shell\nuser_named: false\n", "Create Text File with Shell"},
		"renamed":     {"id: x\nname: probe-name-x\nuser_named: true\n", "probe-name-x"},
		"none yet":    {"id: x\nuser_named: false\nsummary_count: 0\n", ""},
		"yaml-quoted": {"name: 'Fix: the \"login\" bug'\n", `Fix: the "login" bug`},
		"too long":    {"name: " + strings.Repeat("a", copilotTitleMaxRunes+1) + "\n", ""},
		"not yaml":    {"name: [unclosed\n", ""},
	}
	for name, c := range cases {
		if got := parseCopilotWorkspaceName([]byte(c.yaml)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestReadCopilotSessionNameUsesCopilotHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	dir := filepath.Join(home, "session-state", "sid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("name: Refactor auth\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadAgentSessionName("copilot", "sid", "/any"); got != "Refactor auth" {
		t.Fatalf("got %q", got)
	}
	if got := ReadCopilotSessionName("missing"); got != "" {
		t.Fatalf("missing session: got %q", got)
	}
}
