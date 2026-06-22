package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNeutralizeUnsafeWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii untouched", "if err := workflow.Run()", "if err := workflow.Run()"},
		{"box drawing untouched", "├─ │ └─ ╭──╮", "├─ │ └─ ╭──╮"},
		{"arrows untouched", "foo → bar ← baz", "foo → bar ← baz"},
		{"dingbat checkmarks untouched", "✓ done ✗ failed", "✓ done ✗ failed"},
		{"em dash untouched", "pair — only once", "pair — only once"},
		{"hebrew untouched", "שלום עולם", "שלום עולם"},
		{"simple emoji to dots", "ship it 🚀 now", "ship it .. now"},
		{"heart with VS16 to dots", "love ❤️ this", "love .. this"},
		{"zwj family emoji to dots", "team \U0001F468\u200d\U0001F469\u200d\U0001F467 here", "team .. here"},
		{"flag to dots", "lang \U0001F1EE\U0001F1F1 set", "lang .. set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := neutralizeUnsafeWidth(tt.in)
			if got != tt.want {
				t.Errorf("neutralizeUnsafeWidth(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The core invariant: after neutralization, fleet's measured width equals the
// width of the (all-ASCII) result — so no downstream padding/truncation that
// trusts ansi.StringWidth can produce a row that overflows in the terminal.
func TestNeutralizeUnsafeWidthPreservesMeasuredWidth(t *testing.T) {
	for _, in := range []string{
		"ship it 🚀 now",
		"love ❤️ this",
		"team \U0001F468\u200d\U0001F469\u200d\U0001F467 here",
		"\U0001F1EE\U0001F1F1 multi 🎉🎉 emoji",
	} {
		out := neutralizeUnsafeWidth(in)
		if ansi.StringWidth(out) != ansi.StringWidth(in) {
			t.Errorf("width drift for %q: in=%d out=%d", in, ansi.StringWidth(in), ansi.StringWidth(out))
		}
		// Result must be free of the codepoints that caused the disagreement.
		if strings.ContainsFunc(out, isUnsafeWidthRune) {
			t.Errorf("neutralizeUnsafeWidth(%q) left unsafe runes: %q", in, out)
		}
	}
}

// SGR color sequences must survive untouched while an embedded emoji is still
// neutralized — the capture path uses `capture-pane -e`, so content is colored.
func TestNeutralizeUnsafeWidthKeepsAnsiSequences(t *testing.T) {
	in := "\x1b[32m+ added 🚀\x1b[0m"
	want := "\x1b[32m+ added ..\x1b[0m"
	if got := neutralizeUnsafeWidth(in); got != want {
		t.Errorf("neutralizeUnsafeWidth(%q) = %q, want %q", in, got, want)
	}
}
