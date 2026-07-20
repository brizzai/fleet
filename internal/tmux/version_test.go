package tmux

import "testing"

func TestParseTmuxVersion(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{"plain release", "tmux 3.4", 3, 4, true},
		{"patch-letter suffix", "tmux 3.3a", 3, 3, true},
		// The server probe (`display-message -p '#{version}'`) reports the
		// bare version with no "tmux " prefix.
		{"server format variable", "3.4\n", 3, 4, true},
		{"server format variable with patch letter", "3.2a", 3, 2, true},
		{"ubuntu 22.04 LTS build", "tmux 3.2a", 3, 2, true},
		{"trailing newline", "tmux 3.4\n", 3, 4, true},
		{"rc suffix", "tmux 3.4-rc2", 3, 4, true},
		{"two-digit major", "tmux 10.1", 10, 1, true},
		{"dev next build unparseable", "tmux next-3.5", 0, 0, false},
		{"dev master build unparseable", "tmux master", 0, 0, false},
		{"no dot", "tmux 3", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"garbage", "not tmux at all", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, ok := parseTmuxVersion(tt.in)
			if major != tt.wantMajor || minor != tt.wantMinor || ok != tt.wantOK {
				t.Errorf("parseTmuxVersion(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tt.in, major, minor, ok, tt.wantMajor, tt.wantMinor, tt.wantOK)
			}
		})
	}
}

// allow-passthrough arrived in tmux 3.3; older servers abort the whole batched
// set-option command on the unknown option, so the boundary matters.
func TestAllowPassthroughVersionGate(t *testing.T) {
	tests := []struct {
		name  string
		major int
		minor int
		ok    bool
		want  bool
	}{
		{"3.2 lacks it", 3, 2, true, false},
		{"3.3 has it", 3, 3, true, true},
		{"3.4 has it", 3, 4, true, true},
		{"4.0 has it", 4, 0, true, true},
		{"2.9 lacks it", 2, 9, true, false},
		{"unparseable dev build assumed modern", 0, 0, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowPassthroughSupported(tt.major, tt.minor, tt.ok); got != tt.want {
				t.Errorf("allowPassthroughSupported(%d, %d, %v) = %v, want %v",
					tt.major, tt.minor, tt.ok, got, tt.want)
			}
		})
	}
}
