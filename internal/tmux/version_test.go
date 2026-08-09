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

func TestParseTmuxVersionParts(t *testing.T) {
	tests := []struct {
		in         string
		wantSuffix string
	}{
		{"tmux 3.5", ""},
		{"tmux 3.5a", "a"},
		{"3.5a\n", "a"},
		{"tmux 3.4-rc2", "-rc2"},
		{"tmux 3.6b", "b"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, _, suffix, ok := parseTmuxVersionParts(tt.in)
			if !ok {
				t.Fatalf("parseTmuxVersionParts(%q) failed to parse", tt.in)
			}
			if suffix != tt.wantSuffix {
				t.Errorf("parseTmuxVersionParts(%q) suffix = %q, want %q", tt.in, suffix, tt.wantSuffix)
			}
		})
	}
}

func TestExtendedKeysSafe(t *testing.T) {
	tests := []struct {
		name   string
		in     string // as reported by tmux
		unpars bool   // expect the version to be unparseable
		want   bool
	}{
		{name: "3.2a predates both bugs", in: "tmux 3.2a", want: true},
		{name: "3.4 predates both bugs", in: "tmux 3.4", want: true},
		{name: "3.5 mis-encodes shift keys (tmux#4156)", in: "tmux 3.5", want: false},
		{name: "3.5 rc is not the 3.5a fix", in: "tmux 3.5-rc1", want: false},
		{name: "3.5a carries the fix", in: "tmux 3.5a", want: true},
		{name: "3.6 leaks raw bytes (tmux#5031)", in: "tmux 3.6", want: false},
		{name: "3.6a still leaks", in: "tmux 3.6a", want: false},
		{name: "3.6b still leaks", in: "tmux 3.6b", want: false},
		{name: "3.7 carries the fix", in: "tmux 3.7", want: true},
		{name: "3.7b carries the fix", in: "tmux 3.7b", want: true},
		{name: "a future major is assumed fixed", in: "tmux 4.0", want: true},
		{name: "dev build is assumed modern", in: "tmux next-3.8", unpars: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, suffix, ok := parseTmuxVersionParts(tt.in)
			if ok == tt.unpars {
				t.Fatalf("parseTmuxVersionParts(%q) ok = %v, want %v", tt.in, ok, !tt.unpars)
			}
			if got := extendedKeysSafe(major, minor, suffix, ok); got != tt.want {
				t.Errorf("extendedKeysSafe(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
