package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeFakeTmux drops a fake `tmux` executable into dir that logs every
// invocation to logPath, answers `show-options -sv <name>` from the
// FAKE_EXTKEYS_VAL / FAKE_TERMFEAT_VAL env vars and `display-message -p
// '#{version}'` from FAKE_TMUX_VERSION. set-option calls are recorded (not
// executed), so tests can assert exactly which options EnsureExtendedKeys tried
// to change. /bin/sh is present on both platforms fleet supports.
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
if [ "$1" = "display-message" ]; then
  printf '%s\n' "$FAKE_TMUX_VERSION"
fi
exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// resetServerVersion clears the memoized tmux version so each subtest gets the
// version its fake tmux reports. serverVersionParts is a sync.Once, so without
// this the first subtest's answer would decide the whole run.
func resetServerVersion(t *testing.T) {
	t.Helper()
	clear := func() {
		versionOnce = sync.Once{}
		versionMajor, versionMinor, versionSuffix, versionOK = 0, 0, "", false
	}
	clear()
	t.Cleanup(clear)
}

func TestEnsureExtendedKeys(t *testing.T) {
	tests := []struct {
		name       string
		optOut     bool   // FLEET_NO_EXTENDED_KEYS truthy
		version    string // fake `tmux -V`; defaults to a safe release
		extKeys    string // fake `show-options -sv extended-keys` output
		termFeat   string // fake `show-options -sv terminal-features` output
		wantSetOn  bool   // expect `set-option -s extended-keys on`
		wantAppend bool   // expect `set-option -sa terminal-features xterm*:extkeys`
		wantNoCall bool   // expect tmux never invoked at all
		wantGated  bool   // expect the version probe, then nothing else
	}{
		{
			name:       "opt-out short-circuits before any tmux call",
			optOut:     true,
			wantNoCall: true,
		},
		{
			// tmux#4146/#4156: 3.5 mis-encodes Shift keys and emits invalid
			// bytes for Alt+Backspace. 3.5a is the fix, and the patch letter is
			// the only thing telling them apart.
			name:      "tmux 3.5 is gated out",
			version:   "tmux 3.5",
			extKeys:   "off",
			wantGated: true,
		},
		{
			name:      "tmux 3.5 rc is gated out too",
			version:   "tmux 3.5-rc1",
			extKeys:   "off",
			wantGated: true,
		},
		{
			name:      "tmux 3.5a is allowed",
			version:   "tmux 3.5a",
			extKeys:   "off",
			termFeat:  "xterm*:extkeys",
			wantSetOn: true,
		},
		{
			// tmux#5031: 3.6.x leaks the outer terminal's raw bytes into the
			// pane on a fast key burst. Fixed in 3.7.
			name:      "tmux 3.6a is gated out",
			version:   "tmux 3.6a",
			extKeys:   "off",
			wantGated: true,
		},
		{
			name:      "tmux 3.7b is allowed",
			version:   "tmux 3.7b",
			extKeys:   "off",
			termFeat:  "xterm*:extkeys",
			wantSetOn: true,
		},
		{
			// A dev build reports something unparseable; assume it's ahead of
			// both bugs rather than degrade it.
			name:      "unparseable dev version is allowed",
			version:   "next-3.8",
			extKeys:   "off",
			termFeat:  "xterm*:extkeys",
			wantSetOn: true,
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
			resetServerVersion(t)

			version := tt.version
			if version == "" {
				version = "tmux 3.5a"
			}

			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_TMUX_VERSION", version)
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

			if tt.wantGated {
				// The version probe is expected; touching the server is not.
				if strings.Contains(log, "show-options") || strings.Contains(log, "set-option") {
					t.Fatalf("expected the version gate to skip all server calls, got:\n%s", log)
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
