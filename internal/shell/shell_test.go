package shell

import "testing"

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		name    string
		dead    bool
		paneCmd string
		want    Status
	}{
		{"dead pane is exited", true, "node", StatusExited},
		{"dead overrides command", true, "zsh", StatusExited},
		{"zsh is idle", false, "zsh", StatusIdle},
		{"login shell is idle", false, "-zsh", StatusIdle},
		{"bash is idle", false, "bash", StatusIdle},
		{"unknown/empty is idle at rest", false, "", StatusIdle},
		{"node is running", false, "node", StatusRunning},
		{"vite is running", false, "vite", StatusRunning},
		{"tail is running", false, "tail", StatusRunning},
	}
	for _, c := range cases {
		if got := DeriveStatus(c.dead, c.paneCmd); got != c.want {
			t.Errorf("%s: DeriveStatus(%v, %q) = %q, want %q", c.name, c.dead, c.paneCmd, got, c.want)
		}
	}
}

func TestShellPrefixNotSessionPrefix(t *testing.T) {
	// "fleetsh_" must NOT be a prefix of an agent-session name check ("fleet_"),
	// or shells would leak into tmux.ListSessions().
	if len(ShellPrefix) < 6 || ShellPrefix[:6] != "fleets" {
		t.Fatalf("unexpected ShellPrefix %q", ShellPrefix)
	}
}
