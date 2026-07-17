package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeTmux drops a fake `tmux` executable into dir that logs every
// invocation to logPath and answers `show-options -sv <name>` from the
// FAKE_EXTKEYS_VAL / FAKE_TERMFEAT_VAL env vars. set-option calls are recorded
// (not executed), so tests can assert exactly which options EnsureExtendedKeys
// tried to change. fleet is macOS-only, so /bin/sh is always present.
func writeFakeTmux(t *testing.T, dir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
if [ "$1" = "show-options" ]; then
  for last in "$@"; do :; done
  case "$last" in
    extended-keys)     printf '%s\n' "$FAKE_EXTKEYS_VAL" ;;
    terminal-features) printf '%s\n' "$FAKE_TERMFEAT_VAL" ;;
  esac
fi
exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

func TestEnsureExtendedKeys(t *testing.T) {
	tests := []struct {
		name       string
		optOut     bool   // FLEET_NO_EXTENDED_KEYS truthy
		extKeys    string // fake `show-options -sv extended-keys` output
		termFeat   string // fake `show-options -sv terminal-features` output
		wantSetOn  bool   // expect `set-option -s extended-keys on`
		wantAppend bool   // expect `set-option -sa terminal-features xterm*:extkeys`
		wantNoCall bool   // expect tmux never invoked at all
	}{
		{
			name:       "opt-out short-circuits before any tmux call",
			optOut:     true,
			wantNoCall: true,
		},
		{
			name:     "already on with feature present is a no-op",
			extKeys:  "on",
			termFeat: "xterm-256color:RGB,xterm*:extkeys",
		},
		{
			name:     "already always is respected",
			extKeys:  "always",
			termFeat: "xterm*:extkeys",
		},
		{
			// isolate the extended-keys branch: feature already present.
			name:      "default off enables extended-keys",
			extKeys:   "off",
			termFeat:  "xterm*:extkeys",
			wantSetOn: true,
		},
		{
			name:      "empty (unset) value enables extended-keys",
			extKeys:   "",
			termFeat:  "xterm*:extkeys",
			wantSetOn: true,
		},
		{
			// isolate the terminal-features branch: extended-keys already on.
			name:       "missing feature is appended",
			extKeys:    "on",
			termFeat:   "",
			wantAppend: true,
		},
		{
			// the exact-entry guard: an extkeys feature for a different pattern
			// must NOT suppress adding xterm*:extkeys.
			name:       "extkeys on another pattern still appends xterm entry",
			extKeys:    "on",
			termFeat:   "screen*:extkeys",
			wantAppend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "calls.log")
			writeFakeTmux(t, dir, logPath)

			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_EXTKEYS_VAL", tt.extKeys)
			t.Setenv("FAKE_TERMFEAT_VAL", tt.termFeat)
			if tt.optOut {
				t.Setenv("FLEET_NO_EXTENDED_KEYS", "1")
			} else {
				t.Setenv("FLEET_NO_EXTENDED_KEYS", "")
			}

			EnsureExtendedKeys()

			log := readFileOrEmpty(t, logPath)

			if tt.wantNoCall {
				if strings.TrimSpace(log) != "" {
					t.Fatalf("expected no tmux invocations, got:\n%s", log)
				}
				return
			}

			gotSetOn := strings.Contains(log, "set-option -s extended-keys on")
			if gotSetOn != tt.wantSetOn {
				t.Errorf("set extended-keys on = %v, want %v\nlog:\n%s", gotSetOn, tt.wantSetOn, log)
			}

			gotAppend := strings.Contains(log, "set-option -sa terminal-features xterm*:extkeys")
			if gotAppend != tt.wantAppend {
				t.Errorf("append xterm*:extkeys = %v, want %v\nlog:\n%s", gotAppend, tt.wantAppend, log)
			}
		})
	}
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read log: %v", err)
	}
	return string(b)
}
