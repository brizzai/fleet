package session

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestApplyHookFinished_StaleHookPreservesAck is a regression test for the
// "I attach to a session, exit, and it's still on finished" bug found via
// /debug-status on 2026-05-12.
//
// The activity guard inside applyHookFinished is meant to suppress the
// finished/waiting flicker during Claude's 1-2s post-Stop wrap-up writes.
// It used to fire on ANY fresh tmux activity, even when the underlying hook
// event was minutes old. So after the user attached (which bumps tmux
// activity) and the daemon acked the session into Idle, the next worker
// observation would re-enter the guard, set Status=Running, and clobber
// Acknowledged=false. When the guard expired, the session settled back to
// Finished — silently erasing the user's acknowledgement.
//
// Fix: gate the guard on hookAge (only enter when the Stop event is recent)
// and, even when it fires, only clear Acknowledged when transitioning out
// of Waiting (the legitimate stale-waiting rescue case the guard exists for).
func TestApplyHookFinished_StaleHookPreservesAck(t *testing.T) {
	cases := []struct {
		name           string
		prev           Status
		acked          bool
		hookAge        time.Duration
		paneStatus     Status
		freshActivity  bool
		wantStatus     Status
		wantAcked      bool
	}{
		{
			name:          "user attached + acked; stale hook + fresh activity → preserve idle/ack",
			prev:          StatusIdle,
			acked:         true,
			hookAge:       60 * time.Second, // way past the 5s gate
			paneStatus:    StatusFinished,
			freshActivity: true,
			wantStatus:    StatusIdle,
			wantAcked:     true,
		},
		{
			name:          "unacked finished; stale hook + fresh activity → stay finished, ack untouched",
			prev:          StatusFinished,
			acked:         false,
			hookAge:       60 * time.Second,
			paneStatus:    StatusFinished,
			freshActivity: true,
			wantStatus:    StatusFinished,
			wantAcked:     false,
		},
		{
			name:          "fresh stop event + waiting prev → legitimate stale-waiting rescue clears ack",
			prev:          StatusWaiting,
			acked:         true,
			hookAge:       500 * time.Millisecond, // fresh
			paneStatus:    StatusFinished,
			freshActivity: true,
			wantStatus:    StatusRunning,
			wantAcked:     false, // intentional: user just answered a prompt
		},
		{
			name:          "fresh stop event + idle prev (user attached) → still don't clobber ack",
			prev:          StatusIdle,
			acked:         true,
			hookAge:       500 * time.Millisecond, // fresh, but prev is Idle (post-attach)
			paneStatus:    StatusFinished,
			freshActivity: true,
			wantStatus:    StatusRunning, // guard parks at running for the flicker window
			wantAcked:     true,          // but ack survives so the eventual settle goes to Idle
		},
	}

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{
				Status:       tc.prev,
				Acknowledged: tc.acked,
			}
			if tc.freshActivity {
				now := time.Now().Unix()
				s.activityFn = func() (int64, bool) { return now, true }
			}
			s.applyHookFinished(tc.paneStatus, tc.hookAge, silent)

			if s.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tc.wantStatus)
			}
			if s.Acknowledged != tc.wantAcked {
				t.Errorf("Acknowledged = %v, want %v", s.Acknowledged, tc.wantAcked)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ansi", "hello world", "hello world"},
		{"CSI color", "\x1b[31mred\x1b[0m", "red"},
		{"CSI bold+color", "\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"OSC hyperlink", "\x1b]8;;https://example.com\x07link\x1b]8;;\x07", "link"},
		{"OSC with ST", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "link"},
		// C1 CSI with ESC prefix so fast path doesn't skip (raw 0x9B byte isn't found by ContainsRune).
		{"C1 CSI with ESC", "\x1b[0m\x9B31mred\x9B0m", "red"},
		{"mixed", "\x1b[1mhello\x1b[0m \x1b]8;;url\x07world\x1b]8;;\x07", "hello world"},
		{"empty", "", ""},
		{"no escape fast path", "plain text 123", "plain text 123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.input)
			if got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDetectStatus(t *testing.T) {
	log := slog.Default()

	tests := []struct {
		name    string
		content string
		want    Status
	}{
		{"empty", "", ""},
		{"busy pattern", "some output\nctrl+c to interrupt\n", StatusRunning},
		{"esc busy pattern", "output\nesc to interrupt\n", StatusRunning},
		{"spinner char", "⠋ Working...\n", StatusRunning},
		{"whimsical pattern", "Clauding… (53s · ↓ 749 tokens)\n", StatusRunning},
		{"approval yes allow", "some text\nYes, allow once\n", StatusWaiting},
		{"approval no tell claude", "No, and tell Claude\n❯\n", StatusWaiting},
		{"permission menu 3 options", "output\n❯ 1. Yes\n  2. Yes, during this session\n  3. No\nEsc to cancel · Tab to amend\n", StatusWaiting},
		{"permission menu 2 options", "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n", StatusWaiting},
		{"user numbered list not menu", "❯ 1. issue_type for issues\n2. for issue type that are\n❯\n⏵⏵\n", StatusFinished},
		{"subagent permission prompt", "Read(~/code/foo/bar.ts)\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, during this session\n  3. No\n\nEsc to cancel · Tab to amend\n", StatusWaiting},
		{"team waiting box", "│ ✢  Waiting for team lead approval │\n│ ⏺ @explore │\n│ Permission request sent to team \"my-team\" leader │\n❯ \n⏵⏵\n", StatusWaiting},
		{"team waiting text without box not matched", "Waiting for team lead approval\n❯ \n⏵⏵\n", StatusFinished},
		{"team waiting mid-line box not matched", "caught by the box check (│ + Waiting for team lead on same line)\n❯ \n⏵⏵\n", StatusFinished},
		{"menu line without full structure not matched", "❯ 1. some list item\nother text\n❯\n", StatusFinished},
		{"yn pattern in code not matched", "code with (Y/n) in diff\n❯\n", StatusFinished},
		{"prompt indicator >", "output\n>\n", StatusFinished},
		{"prompt indicator ❯", "❯\n", StatusFinished},
		{"prompt with space", "> \n", StatusFinished},
		{"idle pattern", "⏵⏵\n", StatusFinished},
		{"spinner char mid-line not matched", "⏺ The test checks that ⠋ is gone\n❯\n", StatusFinished},
		{"busy pattern in scrollback not matched", "1. Busy patterns → Running (`ctrl+c to interrupt`, `esc to interrupt`)\nmore text\nmore text\nmore text\nmore text\nmore text\n❯\n", StatusFinished},
		{"no match", "random output text\nmore text\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectStatus(tt.content, log)
			if got != tt.want {
				t.Errorf("detectStatus(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestTitleFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"absolute", "/Users/test/code/myproject", "myproject"},
		{"relative", "code/myproject", "myproject"},
		{"trailing slash", "/Users/test/code/myproject/", "myproject"},
		{"single segment", "myproject", "myproject"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TitleFromPath(tt.path)
			if got != tt.want {
				t.Errorf("TitleFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestGenerateID(t *testing.T) {
	id := generateID()

	// Format: <8hex>-<unix_timestamp>
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-\d+$`, id)
	if err != nil {
		t.Fatalf("regex error: %v", err)
	}
	if !matched {
		t.Errorf("generateID() = %q, does not match expected format <8hex>-<timestamp>", id)
	}

	// Uniqueness check.
	id2 := generateID()
	if id == id2 {
		t.Errorf("generateID() produced duplicate IDs: %q", id)
	}
}

func TestHashContent(t *testing.T) {
	h1 := hashContent("hello")
	h2 := hashContent("hello")
	h3 := hashContent("world")

	if h1 != h2 {
		t.Errorf("hashContent should be deterministic: %q != %q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("hashContent should differ for different inputs")
	}
	if len(h1) != 16 {
		t.Errorf("hashContent should return 16 hex chars, got %d", len(h1))
	}
}

func TestNormalizeForHash(t *testing.T) {
	// Should remove entire lines containing spinner chars.
	input := "⠋ Working on task\n\n\n\nDone"
	result := normalizeForHash(input)
	if strings.Contains(result, "Working on task") {
		t.Errorf("normalizeForHash should remove spinner lines entirely, got: %q", result)
	}

	// Should collapse consecutive blank lines (3+ newlines -> 2 newlines).
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("normalizeForHash should collapse consecutive blank lines, got: %q", result)
	}

	// Should strip right-margin creature animation (20+ spaces → truncate).
	lineWithCreature := "❯ my prompt text" + strings.Repeat(" ", 50) + "( .--. )"
	result = normalizeForHash(lineWithCreature)
	if strings.Contains(result, "( .--. )") {
		t.Errorf("normalizeForHash should strip right-margin content, got: %q", result)
	}
	if !strings.Contains(result, "❯ my prompt text") {
		t.Errorf("normalizeForHash should preserve left content, got: %q", result)
	}

	// Content without long space runs should be unchanged.
	normalLine := "some code with    spaces"
	result = normalizeForHash(normalLine)
	if result != normalLine {
		t.Errorf("normalizeForHash should not strip short space runs, got: %q", result)
	}
}
