package session

import (
	"os"
	"testing"
)

// No test may write a crash dump: the writer lands in the real
// ~/.config/fleet/crashes, and its tmux capture with an empty target grabs
// whatever pane the developer has active.
func TestMain(m *testing.M) {
	crashDumpWriter = func(string, string, string, string, string) {}
	os.Exit(m.Run())
}
