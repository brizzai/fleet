package tmux

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The bytes are the whole feature: an agent reads ESC[O as "the terminal lost
// focus", and reads anything else as text typed at its prompt. A typo here does
// not fail — it silently pastes junk into the message the user just sent.
func TestFocusOutHexIsTheXtermReport(t *testing.T) {
	b, err := hex.DecodeString(strings.Join(focusOutHex, ""))
	if err != nil {
		t.Fatalf("focusOutHex is not valid hex for `send-keys -H`: %v", err)
	}
	if got := string(b); got != "\x1b[O" {
		t.Errorf("focusOutHex decodes to %q, want the xterm focus-out report %q", got, "\x1b[O")
	}
}
