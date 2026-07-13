package editor

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestAppForIn(t *testing.T) {
	installed := []string{
		"/Applications/Visual Studio Code.app",
		"/Applications/GoLand.app",
		"/Applications/PyCharm Community Edition.app",
		"/Users/x/Applications/JetBrains Toolbox/IntelliJ IDEA Ultimate.app",
	}

	tests := []struct {
		name    string
		cmd     string
		wantApp string
		wantOK  bool
	}{
		{"exact bundle name", "goland", "/Applications/GoLand.app", true},
		{"edition suffix still matches", "pycharm", "/Applications/PyCharm Community Edition.app", true},
		{"toolbox dir, multi-word prefix", "idea", "/Users/x/Applications/JetBrains Toolbox/IntelliJ IDEA Ultimate.app", true},
		{"command differs from bundle name", "code", "/Applications/Visual Studio Code.app", true},
		{"known editor, app not installed", "webstorm", "", false},
		{"terminal editor has no bundle", "vim", "", false},
		{"unknown editor", "notanide", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, ok := appForIn(tt.cmd, installed)
			if ok != tt.wantOK || app != tt.wantApp {
				t.Errorf("appForIn(%q) = (%q, %v), want (%q, %v)", tt.cmd, app, ok, tt.wantApp, tt.wantOK)
			}
		})
	}
}

func TestCommand(t *testing.T) {
	// A terminal editor is on PATH everywhere these tests run, so it exercises
	// the direct-exec branch without depending on which GUI apps are installed.
	t.Run("editor on PATH is exec'd directly and not waited on", func(t *testing.T) {
		cmd, wait, err := command("sh", "/repo")
		if err != nil {
			t.Fatalf("command: %v", err)
		}
		if wait {
			t.Error("wait = true; an editor process must outlive fleet, so it must not be waited on")
		}
		if got, want := cmd.Args[len(cmd.Args)-1], "/repo"; got != want {
			t.Errorf("path arg = %q, want %q", got, want)
		}
	})

	t.Run("flags are preserved before the path", func(t *testing.T) {
		cmd, _, err := command("sh -c", "/repo")
		if err != nil {
			t.Fatalf("command: %v", err)
		}
		if len(cmd.Args) != 3 || cmd.Args[1] != "-c" || cmd.Args[2] != "/repo" {
			t.Errorf("args = %v, want [sh -c /repo]", cmd.Args)
		}
	})

	t.Run("empty spec", func(t *testing.T) {
		if _, _, err := command("   ", "/repo"); err == nil {
			t.Error("want error for an empty editor spec, got nil")
		}
	})

	t.Run("no launcher and no app", func(t *testing.T) {
		_, _, err := command("definitely-not-an-editor", "/repo")
		if err == nil {
			t.Fatal("want error when the editor is neither on PATH nor installed, got nil")
		}
		if !strings.Contains(err.Error(), "no matching app installed") {
			t.Errorf("error = %q, want it to say no app is installed", err)
		}
	})
}

// The `open` fallback must be waited on: Start alone always succeeds (it only
// execs /usr/bin/open), so an unresolvable bundle would fail silently and leak
// an unreaped child.
func TestOpenFallbackIsWaitedOn(t *testing.T) {
	if _, ok := appFor("goland"); !ok {
		t.Skip("no GoLand bundle on this machine; nothing to exercise the open fallback")
	}
	cmd, wait, err := command("goland", "/repo")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !wait {
		t.Error("wait = false; a non-zero `open` exit is the only signal that the bundle didn't resolve")
	}
	if cmd.Args[0] != "open" || cmd.Args[1] != "-a" {
		t.Fatalf("args = %v, want an `open -a` invocation", cmd.Args)
	}
	// The bundle path, not the bare name: LaunchServices should not get a second
	// chance to resolve a name we already resolved on disk.
	if !strings.HasSuffix(cmd.Args[2], ".app") {
		t.Errorf("app arg = %q, want the full bundle path found by the scan", cmd.Args[2])
	}
}

// An installed app skipped only because the spec carried flags must not be
// reported as "not installed" — that sends the user hunting for the wrong fix.
func TestFlagsWithInstalledAppNamesTheRealCause(t *testing.T) {
	if _, ok := appFor("goland"); !ok {
		t.Skip("no GoLand bundle on this machine")
	}
	if _, err := exec.LookPath("goland"); err == nil {
		t.Skip("goland CLI launcher is on PATH here; the fallback branch is unreachable")
	}
	_, _, err := command("goland --dumb-flag", "/repo")
	if err == nil {
		t.Fatal("want an error for flags with no CLI launcher, got nil")
	}
	if strings.Contains(err.Error(), "no matching app installed") {
		t.Errorf("error = %q, but the app IS installed — it was skipped because of the flags", err)
	}
	if !strings.Contains(err.Error(), "CLI launcher") {
		t.Errorf("error = %q, want it to point at the CLI launcher as the fix", err)
	}
}

func TestAvailableIsNeverEmpty(t *testing.T) {
	// The settings cycler indexes into this list; an empty one would divide by zero.
	if got := Available(); len(got) == 0 {
		t.Error("Available() is empty; the settings cycler would divide by zero")
	}
}

// internal/proc seeds its never-kill set from Commands(), so an editor missing
// here is one fleet would SIGKILL off a worktree while the user had it open.
func TestCommandsCoversEveryKnownEditor(t *testing.T) {
	got := Commands()
	if len(got) != len(knownEditors) {
		t.Fatalf("Commands() has %d entries, knownEditors has %d", len(got), len(knownEditors))
	}
	for _, k := range knownEditors {
		if !slices.Contains(got, k.cmd) {
			t.Errorf("Commands() is missing %q; proc would not spare it", k.cmd)
		}
	}
}
