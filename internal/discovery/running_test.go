package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunningClaudeKeepsOnlyOutsideTerminalSessions(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	live := os.Getpid()
	write := func(name string, p claudePidFile) {
		data, _ := json.Marshal(p)
		if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := func(id string) claudePidFile {
		return claudePidFile{PID: live, SessionID: id, Cwd: cwd, Name: id, Kind: "interactive", Entrypoint: "cli", UpdatedAt: 1}
	}

	write("keep", base("keep"))
	dead := base("dead")
	dead.PID = 1 << 22 // above Linux's pid_max and macOS's 99999
	write("dead", dead)
	inFleet := base("in-fleet")
	inFleet.Tmux = "fleet_proj_1234:@1.%1"
	write("in-fleet", inFleet)
	bg := base("bg")
	bg.Kind = "bg"
	write("bg", bg)
	ide := base("ide")
	ide.Entrypoint = "claude-vscode"
	write("ide", ide)
	write("known", base("known"))
	gone := base("gone")
	gone.Cwd = filepath.Join(cwd, "deleted")
	write("gone", gone)
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := RunningClaude(home, map[string]bool{"known": true})
	if len(got) != 1 || got[0].ClaudeSessionID != "keep" {
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.ClaudeSessionID)
		}
		t.Fatalf("RunningClaude = %v, want only [keep]", ids)
	}
	if got[0].Path != cwd || got[0].Where == "" {
		t.Errorf("keep: path %q where %q", got[0].Path, got[0].Where)
	}
	if want := fmt.Sprintf("pid %d", live); !strings.Contains(got[0].Where, want) {
		t.Errorf("Where %q does not name %q", got[0].Where, want)
	}
}
