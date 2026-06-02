package tmux

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"alphanumeric", "hello123", "hello123"},
		{"with hyphens", "my-session", "my-session"},
		{"with spaces", "my session", "my-session"},
		{"with special chars", "hello@world#123", "hello-world-123"},
		{"leading hyphens", "---hello", "hello"},
		{"trailing hyphens", "hello---", "hello"},
		{"consecutive hyphens collapsed", "hello---world", "hello-world"},
		{"empty string", "", "session"},
		{"all special chars", "@#$%^&", "session"},
		{"uppercase preserved", "MySession", "MySession"},
		{"mixed", "My Cool Session! (v2)", "My-Cool-Session-v2"},
		{"long name truncated", "abcdefghijklmnopqrstuvwxyz12345678901234567890", "abcdefghijklmnopqrstuvwxyz1234"},
		{"exactly 30", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"dots replaced", "my.session.name", "my-session-name"},
		{"underscores replaced", "my_session_name", "my-session-name"},
		{"slash replaced", "path/to/session", "path-to-session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewSessionNameFormat(t *testing.T) {
	s := NewSession("test-session", "/tmp/workdir")

	// Name should start with the prefix.
	if len(s.Name) < len(SessionPrefix) {
		t.Fatalf("session name too short: %q", s.Name)
	}
	prefix := s.Name[:len(SessionPrefix)]
	if prefix != SessionPrefix {
		t.Errorf("session name prefix: got %q, want %q", prefix, SessionPrefix)
	}

	// DisplayName and WorkDir should be preserved.
	if s.DisplayName != "test-session" {
		t.Errorf("DisplayName: got %q, want %q", s.DisplayName, "test-session")
	}
	if s.WorkDir != "/tmp/workdir" {
		t.Errorf("WorkDir: got %q, want %q", s.WorkDir, "/tmp/workdir")
	}
}

func TestReconnectSession(t *testing.T) {
	s := ReconnectSession("fleet_test_abc123", "My Session", "/home/user/project")

	if s.Name != "fleet_test_abc123" {
		t.Errorf("Name: got %q, want %q", s.Name, "fleet_test_abc123")
	}
	if s.DisplayName != "My Session" {
		t.Errorf("DisplayName: got %q, want %q", s.DisplayName, "My Session")
	}
	if s.WorkDir != "/home/user/project" {
		t.Errorf("WorkDir: got %q, want %q", s.WorkDir, "/home/user/project")
	}
}

func TestGenerateShortIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateShortID()
		if len(id) != 8 {
			t.Errorf("generateShortID() returned %q (len %d), expected 8 hex chars", id, len(id))
		}
		if seen[id] {
			t.Errorf("generateShortID() produced duplicate: %q", id)
		}
		seen[id] = true
	}
}

// buildBatch reconstructs the on-the-wire stdout of a batched capture: each
// pane's raw content followed by its "<sentinel>\t<name>\n" marker.
func buildBatch(sentinel string, blocks ...[2]string) string {
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl[1]) // content bytes
		b.WriteString(sentinel + "\t" + bl[0] + "\n")
	}
	return b.String()
}

func TestParseBatchCapture(t *testing.T) {
	const sent = "deadbeefcafef00ddeadbeefcafef00d"

	t.Run("preserves content bytes exactly", func(t *testing.T) {
		contentA := "line1\n\x1b[31mred\x1b[0m\n$ \n" // trailing newline + ANSI + tab-free
		contentB := "prompt with\ttab\nand two lines\n"
		got := parseBatchCapture(buildBatch(sent, [2]string{"sessA", contentA}, [2]string{"sessB", contentB}), sent)
		if got["sessA"] != contentA {
			t.Errorf("sessA content mismatch:\n got %q\nwant %q", got["sessA"], contentA)
		}
		if got["sessB"] != contentB {
			t.Errorf("sessB content mismatch:\n got %q\nwant %q", got["sessB"], contentB)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 entries, got %d", len(got))
		}
	})

	t.Run("empty pane yields empty string entry", func(t *testing.T) {
		got := parseBatchCapture(buildBatch(sent, [2]string{"empty", ""}, [2]string{"full", "x\n"}), sent)
		if v, ok := got["empty"]; !ok || v != "" {
			t.Errorf("expected empty present with \"\", got ok=%v v=%q", ok, v)
		}
		if got["full"] != "x\n" {
			t.Errorf("full content mismatch: %q", got["full"])
		}
	})

	t.Run("mid-chain abort drops the unterminated tail", func(t *testing.T) {
		// sessA terminated cleanly; the chain aborted on sessB, so its capture
		// is partial and no marker follows — sessB and sessC must be absent.
		stdout := buildBatch(sent, [2]string{"sessA", "A-content\n"}) + "partial B output with no marker\n"
		got := parseBatchCapture(stdout, sent)
		if got["sessA"] != "A-content\n" {
			t.Errorf("sessA should survive intact, got %q", got["sessA"])
		}
		if _, ok := got["sessB"]; ok {
			t.Error("sessB must be absent (its marker never emitted)")
		}
		if len(got) != 1 {
			t.Errorf("expected exactly 1 surviving entry, got %d", len(got))
		}
	})

	t.Run("no markers returns empty map", func(t *testing.T) {
		if got := parseBatchCapture("just some output, server died\n", sent); len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})
}

func TestCaptureSentinelEntropy(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s := captureSentinel()
		if len(s) != 32 {
			t.Errorf("captureSentinel() = %q (len %d), want 32 hex chars", s, len(s))
		}
		if seen[s] {
			t.Errorf("captureSentinel() produced duplicate: %q", s)
		}
		seen[s] = true
	}
}
