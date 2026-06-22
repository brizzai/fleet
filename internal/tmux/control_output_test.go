package tmux

import "testing"

func TestDecodeControlOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world 123", "hello world 123"},
		{"esc", `a\033[31mb`, "a\x1b[31mb"},
		{"backslash", `x\134y`, `x\y`},
		{"crlf", `line\015\012`, "line\r\n"},
		{"mixed", `\033]0;title\007`, "\x1b]0;title\x07"},
		{"spaces-raw", `two  spaces`, "two  spaces"},
		{"trailing-backslash-literal", `end\05`, `end\05`},
		{"empty", "", ""},
		{"utf8-passthrough", "caf\xc3\xa9", "caf\xc3\xa9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(decodeControlOutput(c.in)); got != c.want {
				t.Errorf("decodeControlOutput(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseControlOutput(t *testing.T) {
	t.Run("valid output line", func(t *testing.T) {
		payload, ok := parseControlOutput(`%output %1 hello\015\012world`)
		if !ok {
			t.Fatal("expected ok for an output notification line")
		}
		if got := string(payload); got != "hello\r\nworld" {
			t.Errorf("payload = %q, want %q", got, "hello\r\nworld")
		}
	})

	t.Run("output with spaces in data", func(t *testing.T) {
		payload, ok := parseControlOutput(`%output %3 a b c`)
		if !ok || string(payload) != "a b c" {
			t.Errorf("got (%q, %v), want (%q, true)", string(payload), ok, "a b c")
		}
	})

	t.Run("non-output notifications ignored", func(t *testing.T) {
		for _, line := range []string{
			`%begin 1700000000 1 0`,
			`%end 1700000000 1 0`,
			`%layout-change @1 bced,318x58,0,0,1`,
			`%window-pane-changed @1 %1`,
			`%exit`,
			``,
			`%output`,    // no pane id / data
			`%output %1`, // pane id but no data
		} {
			if _, ok := parseControlOutput(line); ok {
				t.Errorf("parseControlOutput(%q) = ok, want not ok", line)
			}
		}
	})
}
