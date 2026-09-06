//go:build !windows

package tmux

import (
	"strings"
	"testing"
)

func TestFindCtrlQ(t *testing.T) {
	tests := []struct {
		name       string
		in         []byte
		wantIdx    int
		wantLength int
	}{
		{"plain byte 17", []byte{17}, 0, 1},
		{"byte 17 after payload", []byte{'h', 'i', 17}, 2, 1},
		{"csi-u kitty format", []byte("\x1b[113;5u"), 0, 8},
		{"csi-u after payload", append([]byte("hi"), []byte("\x1b[113;5u")...), 2, 8},
		{"xterm modifyOtherKeys", []byte("\x1b[27;5;113~"), 0, 11},
		{"modifyOtherKeys after payload", append([]byte("ab"), []byte("\x1b[27;5;113~")...), 2, 11},
		{"not present", []byte("hello"), -1, 0},
		{"empty", []byte{}, -1, 0},
		{"unrelated csi", []byte("\x1b[A"), -1, 0},
		{"earliest wins", append([]byte{17}, []byte("\x1b[113;5u")...), 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIdx, gotLength := findCtrlQ(tt.in)
			if gotIdx != tt.wantIdx || gotLength != tt.wantLength {
				t.Errorf("findCtrlQ(%q) = (%d, %d), want (%d, %d)",
					tt.in, gotIdx, gotLength, tt.wantIdx, tt.wantLength)
			}
		})
	}
}

// A tmux client refuses to attach to a session on the server it is already
// inside, so an attach launched from a fleet running under tmux must not carry
// $TMUX. The failure is silent — the error lands on the PTY, not the TUI — so
// nothing but this test stands between a regression and a dead Enter key.
func TestClientEnvDropsTmuxVars(t *testing.T) {
	t.Setenv("TMUX", "/private/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("FLEET_KEEP_ME", "yes")

	var sawKeeper bool
	for _, kv := range clientEnv() {
		switch {
		case strings.HasPrefix(kv, "TMUX="):
			t.Errorf("clientEnv() kept %q; tmux refuses a nested attach while it is set", kv)
		case strings.HasPrefix(kv, "TMUX_PANE="):
			t.Errorf("clientEnv() kept %q; it names a pane on the outer server", kv)
		case kv == "FLEET_KEEP_ME=yes":
			sawKeeper = true
		}
	}

	// Guard against the opposite bug: an over-eager filter that drops
	// everything would pass every assertion above while breaking the attach.
	if !sawKeeper {
		t.Error("clientEnv() dropped an unrelated variable; it must only strip the tmux ones")
	}
}

// $TMUX names the server AND marks the nesting. clientEnv drops the marker, so
// the socket has to be carried some other way or the attach silently retargets
// the default server — where Exists() never looked and the session is not.
func TestAttachArgsKeepsTheInheritedSocket(t *testing.T) {
	t.Run("inside tmux, -S names the inherited socket", func(t *testing.T) {
		t.Setenv("TMUX", "/private/tmp/tmux-501/work,62039,14")
		got := attachArgs("fleet_demo_abc")
		want := []string{"-S", "/private/tmp/tmux-501/work", "attach-session", "-t", "fleet_demo_abc"}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("attachArgs() = %v, want %v", got, want)
		}
		// tmux parses server options before the command, so a -S appended after
		// attach-session would be read as an argument to it and ignored.
		if got[0] != "-S" {
			t.Errorf("-S must precede the command; got %v", got)
		}
	})

	t.Run("outside tmux, no -S at all", func(t *testing.T) {
		t.Setenv("TMUX", "")
		got := attachArgs("fleet_demo_abc")
		for _, a := range got {
			if a == "-S" {
				t.Fatalf("attachArgs() added -S with no $TMUX: %v", got)
			}
		}
	})
}

func TestSocketFromEnv(t *testing.T) {
	tests := []struct {
		name string
		tmux string
		want string
	}{
		{"standard three-field value", "/tmp/tmux-501/default,123,0", "/tmp/tmux-501/default"},
		{"named socket", "/tmp/tmux-501/work,1,2", "/tmp/tmux-501/work"},
		{"unset", "", ""},
		// tmux has used a bare socket path historically; take the whole value
		// rather than returning nothing and silently retargeting the default.
		{"no comma", "/tmp/tmux-501/default", "/tmp/tmux-501/default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", tt.tmux)
			if got := socketFromEnv(); got != tt.want {
				t.Errorf("socketFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}
