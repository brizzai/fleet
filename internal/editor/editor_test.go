package editor

import "testing"

func TestAppForIn(t *testing.T) {
	installed := []string{
		"Visual Studio Code",
		"GoLand",
		"PyCharm Community Edition",
		"IntelliJ IDEA Ultimate",
	}

	tests := []struct {
		name    string
		cmd     string
		wantApp string
		wantOK  bool
	}{
		{"exact bundle name", "goland", "GoLand", true},
		{"edition suffix still matches", "pycharm", "PyCharm Community Edition", true},
		{"multi-word prefix with edition", "idea", "IntelliJ IDEA Ultimate", true},
		{"command differs from bundle name", "code", "Visual Studio Code", true},
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
	t.Run("editor on PATH is exec'd directly", func(t *testing.T) {
		cmd, err := Command("sh", "/repo")
		if err != nil {
			t.Fatalf("Command: %v", err)
		}
		if got, want := cmd.Args[len(cmd.Args)-1], "/repo"; got != want {
			t.Errorf("path arg = %q, want %q", got, want)
		}
	})

	t.Run("flags are preserved before the path", func(t *testing.T) {
		cmd, err := Command("sh -c", "/repo")
		if err != nil {
			t.Fatalf("Command: %v", err)
		}
		if len(cmd.Args) != 3 || cmd.Args[1] != "-c" || cmd.Args[2] != "/repo" {
			t.Errorf("args = %v, want [sh -c /repo]", cmd.Args)
		}
	})

	t.Run("empty spec", func(t *testing.T) {
		if _, err := Command("   ", "/repo"); err == nil {
			t.Error("want error for an empty editor spec, got nil")
		}
	})

	t.Run("no launcher and no app", func(t *testing.T) {
		if _, err := Command("definitely-not-an-editor", "/repo"); err == nil {
			t.Error("want error when the editor is neither on PATH nor installed, got nil")
		}
	})
}

func TestAvailableIsNeverEmpty(t *testing.T) {
	// The settings cycler indexes into this list; an empty one would panic.
	if got := Available(); len(got) == 0 {
		t.Error("Available() is empty; the settings cycler would divide by zero")
	}
}
