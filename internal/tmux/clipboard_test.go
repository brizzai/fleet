package tmux

import "testing"

// clipboardCopyCommandFor must route by what can actually reach a clipboard:
// a tool on PATH whose display server isn't running is worse than no tool,
// because it swallows the copy that the OSC 52 fallback would have delivered.
func TestClipboardCopyCommandFor(t *testing.T) {
	has := func(bins ...string) func(string) bool {
		set := make(map[string]bool, len(bins))
		for _, b := range bins {
			set[b] = true
		}
		return func(bin string) bool { return set[bin] }
	}
	env := func(pairs map[string]string) func(string) string {
		return func(k string) string { return pairs[k] }
	}
	allTools := has("wl-copy", "xclip", "xsel")
	wayland := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"} // XWayland exports both
	x11 := map[string]string{"DISPLAY": ":0"}
	waylandOnly := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	headless := map[string]string{}

	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		hasTool func(string) bool
		want    string
	}{
		{"darwin always pbcopy", "darwin", headless, has(), "pbcopy"},
		{"wayland prefers wl-copy", "linux", wayland, allTools, "wl-copy"},
		{"x11 with wl-clipboard installed skips wl-copy", "linux", x11, allTools, "xclip -selection clipboard -in >/dev/null"},
		{"x11 falls back to xsel", "linux", x11, has("wl-copy", "xsel"), "xsel --clipboard --input"},
		{"wayland without wl-copy uses x11 tools via XWayland", "linux", wayland, has("xclip"), "xclip -selection clipboard -in >/dev/null"},
		{"wayland-only display can't use x11 tools", "linux", waylandOnly, has("xclip", "xsel"), ""},
		{"headless with every tool installed: OSC 52", "linux", headless, allTools, ""},
		{"x11 with no tools: OSC 52", "linux", x11, has(), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipboardCopyCommandFor(tt.goos, env(tt.env), tt.hasTool)
			if got != tt.want {
				t.Errorf("clipboardCopyCommandFor(%q, %v) = %q, want %q", tt.goos, tt.env, got, tt.want)
			}
		})
	}
}

// parseTmuxShowEnvironment must read the *server's* environment table, not
// fleet's own — a headless-started server (e.g. via the systemd unit) has no
// display vars in its table even if fleet itself was launched from one.
func TestParseTmuxShowEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		varName string
		out     string
		want    string
	}{
		{"set", "WAYLAND_DISPLAY", "WAYLAND_DISPLAY=wayland-0\n", "wayland-0"},
		{"set no trailing newline", "DISPLAY", "DISPLAY=:0", ":0"},
		{"unset/removed", "WAYLAND_DISPLAY", "-WAYLAND_DISPLAY\n", ""},
		{"empty output", "DISPLAY", "", ""},
		{"value containing an equals sign", "DISPLAY", "DISPLAY=:0=extra\n", ":0=extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTmuxShowEnvironment(tt.varName, tt.out); got != tt.want {
				t.Errorf("parseTmuxShowEnvironment(%q, %q) = %q, want %q", tt.varName, tt.out, got, tt.want)
			}
		})
	}
}
