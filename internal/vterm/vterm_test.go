package vterm

import (
	"strings"
	"testing"
)

func TestTerminalWriteRenderDirty(t *testing.T) {
	term := New(20, 5)
	if term.Dirty() {
		t.Error("new terminal should not be dirty")
	}

	term.Write([]byte("hello"))
	if !term.Dirty() {
		t.Error("should be dirty after write")
	}
	out := term.Render()
	if !strings.Contains(out, "hello") {
		t.Errorf("render missing %q: %q", "hello", out)
	}
	if term.Dirty() {
		t.Error("Render should clear dirty")
	}

	// Cursor advances to column 5 after writing 5 cells.
	if x, _ := term.Cursor(); x != 5 {
		t.Errorf("cursor x = %d, want 5", x)
	}
}

func TestStripScreenTitles(t *testing.T) {
	t.Run("inline title dropped, output kept", func(t *testing.T) {
		term := New(40, 5)
		term.Write([]byte("\x1bkecho\x1b\\hey")) // ESC k echo ST  +  "hey"
		got := term.Render()
		if !strings.Contains(got, "hey") {
			t.Errorf("expected output %q kept, got %q", "hey", got)
		}
		if strings.Contains(got, "echo") {
			t.Errorf("expected title %q stripped, got %q", "echo", got)
		}
	})

	t.Run("BEL-terminated title", func(t *testing.T) {
		term := New(40, 5)
		term.Write([]byte("\x1bktitle\x07ok"))
		got := term.Render()
		if strings.Contains(got, "title") || !strings.Contains(got, "ok") {
			t.Errorf("BEL title not stripped: %q", got)
		}
	})

	t.Run("title split across writes", func(t *testing.T) {
		term := New(40, 5)
		term.Write([]byte("\x1bk"))
		term.Write([]byte("some-cmd"))
		term.Write([]byte("\x1b\\done"))
		got := term.Render()
		if strings.Contains(got, "some-cmd") || !strings.Contains(got, "done") {
			t.Errorf("cross-chunk title not stripped: %q", got)
		}
	})

	t.Run("ordinary escapes pass through", func(t *testing.T) {
		term := New(40, 5)
		term.Write([]byte("\x1b[31mRED\x1b[0m"))
		got := term.Render()
		if !strings.Contains(got, "RED") || !strings.Contains(got, "31") {
			t.Errorf("SGR should survive the title filter: %q", got)
		}
	})
}

func TestTerminalColorAndResize(t *testing.T) {
	term := New(20, 5)
	term.Write([]byte("\x1b[31mRED\x1b[0m"))
	if !strings.Contains(term.Render(), "31") {
		t.Error("expected red SGR in render output")
	}

	term.Resize(40, 10)
	if w, h := term.Size(); w != 40 || h != 10 {
		t.Errorf("size = %dx%d, want 40x10", w, h)
	}

	// Empty write is a no-op and must not mark dirty.
	_ = term.Render()
	term.Write(nil)
	if term.Dirty() {
		t.Error("empty write should not mark dirty")
	}
}
