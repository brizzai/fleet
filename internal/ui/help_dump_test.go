package ui

import (
	"os"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHelpDump(t *testing.T) {
	if os.Getenv("READER_DUMP") == "" {
		t.Skip()
	}
	d := newDemoReader(t, 116, 30)
	d.Update(keyOf("?"))
	os.Stdout.WriteString("\n" + ansi.Strip(d.View()) + "\n")
}
