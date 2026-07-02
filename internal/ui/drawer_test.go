package ui

import "testing"

// drawerSeedBytes turns capture-pane output into emulator-safe seed bytes:
// a clear+home prefix, trailing blank lines dropped (so a prompt doesn't scroll
// off a short drawer) and bare "\n" → "\r\n" (so lines don't stairstep).
func TestDrawerSeedBytes(t *testing.T) {
	const clearHome = "\x1b[2J\x1b[H"
	cases := []struct {
		name string
		in   string
		want string // non-empty results are prefixed with clearHome
	}{
		{"empty", "", ""},
		{"only blanks", "\n\n\n", ""},
		{"single line no newline", "$ ", clearHome + "$ "},
		{"crlf join", "line1\nline2", clearHome + "line1\r\nline2"},
		{"trailing blanks trimmed", "prompt\n\n\n", clearHome + "prompt"},
		{"blank lines kept between content", "a\n\nb\n\n", clearHome + "a\r\n\r\nb"},
		{"trailing spaces count as blank", "prompt\n   \n", clearHome + "prompt"},
	}
	for _, c := range cases {
		if got := string(drawerSeedBytes([]byte(c.in))); got != c.want {
			t.Errorf("%s: drawerSeedBytes(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
