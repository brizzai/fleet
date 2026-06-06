package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/charmbracelet/x/ansi"
)

func TestOverlayCursor_PlainText(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		col     int
		wantSub string // substring that should appear (the reverse-video block)
	}{
		{
			name:    "cursor at start",
			line:    "hello",
			col:     0,
			wantSub: "\x1b[7mh\x1b[27m",
		},
		{
			name:    "cursor in middle",
			line:    "hello",
			col:     2,
			wantSub: "\x1b[7ml\x1b[27m",
		},
		{
			name:    "cursor at last char",
			line:    "hello",
			col:     4,
			wantSub: "\x1b[7mo\x1b[27m",
		},
		{
			name:    "cursor past end appends space",
			line:    "hello",
			col:     5,
			wantSub: "\x1b[7m \x1b[27m",
		},
		{
			name:    "cursor far past end appends space",
			line:    "hi",
			col:     10,
			wantSub: "\x1b[7m \x1b[27m",
		},
		{
			name:    "empty line cursor at 0",
			line:    "",
			col:     0,
			wantSub: "\x1b[7m \x1b[27m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := overlayCursor(tt.line, tt.col)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("overlayCursor(%q, %d) = %q, want substring %q", tt.line, tt.col, got, tt.wantSub)
			}
			// Visible width: cursor at col adds padding + 1 block char when past end.
			gotWidth := ansi.StringWidth(got)
			origWidth := ansi.StringWidth(tt.line)
			if tt.col >= origWidth {
				// Width = col + 1 (padding to cursor position + cursor block).
				wantWidth := tt.col + 1
				if gotWidth != wantWidth {
					t.Errorf("width: got %d, want %d (col %d + 1 for cursor)", gotWidth, wantWidth, tt.col)
				}
			} else {
				if gotWidth != origWidth {
					t.Errorf("width: got %d, want %d (same as original)", gotWidth, origWidth)
				}
			}
		})
	}
}

func TestOverlayCursor_WithANSI(t *testing.T) {
	// Line with ANSI color codes: "\x1b[32mhello\x1b[0m" (green "hello")
	line := "\x1b[32mhello\x1b[0m"

	got := overlayCursor(line, 2)
	// Should contain reverse video marker.
	if !strings.Contains(got, "\x1b[7m") {
		t.Errorf("expected reverse video in output: %q", got)
	}
	// Visible width should be preserved (5 chars).
	if w := ansi.StringWidth(got); w != 5 {
		t.Errorf("width: got %d, want 5", w)
	}
}

func TestOverlayCursor_PreservesContent(t *testing.T) {
	line := "abcdef"
	got := overlayCursor(line, 3)
	// Strip ANSI to get plain text — should still be "abcdef".
	plain := ansi.Strip(got)
	if plain != "abcdef" {
		t.Errorf("plain text should be preserved: got %q, want %q", plain, "abcdef")
	}
}

func TestRenderPreview_CursorSkippedWhenOffScreen(t *testing.T) {
	// Simulate a line that would be truncated to width-2 columns.
	// Cursor at column 50 should be skipped when preview width is 40 (visible=38).
	longLine := strings.Repeat("x", 60)
	cursor := &tmux.CursorPosition{X: 50, Y: 0}

	// Build a minimal session for RenderPreview.
	s := &session.Session{Title: "test", ProjectPath: "/tmp"}
	s.SetStatus(session.StatusRunning)

	result := RenderPreview(s, longLine, nil, 40, 10, true, cursor)
	// The cursor should NOT appear since cursor.X (50) >= width-2 (38).
	if strings.Contains(result, "\x1b[7m") {
		t.Error("cursor overlay should not render when cursor column is past visible width")
	}
}

func TestRenderPreview_CursorRenderedWhenOnScreen(t *testing.T) {
	line := "hello world"
	cursor := &tmux.CursorPosition{X: 3, Y: 0}

	s := &session.Session{Title: "test", ProjectPath: "/tmp"}
	s.SetStatus(session.StatusRunning)

	result := RenderPreview(s, line, nil, 80, 10, true, cursor)
	// The reverse-video cursor should be present.
	if !strings.Contains(result, "\x1b[7m") {
		t.Error("cursor overlay should render when cursor column is within visible width")
	}
}
