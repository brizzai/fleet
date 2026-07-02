package ui

import "testing"

// drawerSeedBytes turns capture-pane output into emulator-safe seed bytes:
// trailing blank lines dropped (so a prompt doesn't scroll off a short drawer)
// and bare "\n" → "\r\n" (so lines don't stairstep).
func TestDrawerSeedBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"only blanks", "\n\n\n", ""},
		{"single line no newline", "$ ", "$ "},
		{"crlf join", "line1\nline2", "line1\r\nline2"},
		{"trailing blanks trimmed", "prompt\n\n\n", "prompt"},
		{"blank lines kept between content", "a\n\nb\n\n", "a\r\n\r\nb"},
		{"trailing spaces count as blank", "prompt\n   \n", "prompt"},
	}
	for _, c := range cases {
		if got := string(drawerSeedBytes([]byte(c.in))); got != c.want {
			t.Errorf("%s: drawerSeedBytes(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
