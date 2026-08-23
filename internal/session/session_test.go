package session

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

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
		{"whimsical pattern", "· Clauding… (53s · ↓ 749 tokens)\n", StatusRunning},
		// Shape A, glyph-less. Kept because a frame can capture with the animated glyph
		// cell blank or clipped, and because it is what master matched — the token
		// counter stays a valid alternative to the glyph, not a thing replaced by it.
		{"whimsical token counter without glyph", "Clauding… (53s · ↓ 749 tokens)\n", StatusRunning},
		// Shape A must stay on master's exact terms: master required no ellipsis, so
		// hoisting shape B's ellipsis gate above it would silently narrow this line out
		// of detection — and an ellipsis is as droppable as the glyph cell A covers for.
		{"whimsical token counter without ellipsis", "Clauding (53s · ↓ 749 tokens)\n", StatusRunning},
		// Shape B. spinnerChars covers only part of the rotating glyph set — `·` (U+00B7)
		// and `✻` (U+273B) are absent — so with no token counter these matched nothing
		// and read as finished mid-turn.
		{"whimsical thinking without token counter", "· Improvising… (51s · almost done thinking with xhigh effort)\n", StatusRunning},
		{"whimsical bare duration", "✻ Marinating… (33s)\n", StatusRunning},
		// Shape B's gate, one case per clause. Each of these satisfies every other
		// condition (trailing `)`, an `…`, a duration after it), so each fails if its
		// clause is dropped — the previous version of this guard ended in `e` and so
		// never reached the gate at all.
		{"prose with duration but no glyph not matched", "we cut cold start… (2s)\n❯\n", StatusFinished},
		{"accented prose not matched", "Éclair… (2s)\n❯\n", StatusFinished},
		{"lowercase prose behind a glyph not matched", "· quoted example… (2s)\n❯\n", StatusFinished},
		// Isolates the non-ASCII clause. Claude writes its replies in markdown, so a
		// bullet or blockquote closing on a duration is an ordinary line for it to emit —
		// and each of these clears every other condition, including capitalisation.
		{"markdown bullet not matched", "- Ran the suite… (12s)\n❯\n", StatusFinished},
		{"markdown star bullet not matched", "* Marinating… (33s)\n❯\n", StatusFinished},
		{"blockquote not matched", "> Marinating… (33s)\n❯\n", StatusFinished},
		{"heading not matched", "# Marinating… (33s)\n❯\n", StatusFinished},
		{"numbered list item not matched", "1 Compiling… (2s)\n❯\n", StatusFinished},
		// Isolates the trailing-`)` clause: glyph, capitalised word, ellipsis and
		// duration all present, but the line continues past the parenthesis.
		{"activity shape trailing into prose not matched", "· Marinating… (33s) and then some prose\n❯\n", StatusFinished},
		// Isolates shape B's duration clause — the only thing separating an activity
		// line from any glyph-prefixed capitalised note that closes on a parenthetical.
		{"parenthetical without a duration not matched", "· Note… (see below)\n❯\n", StatusFinished},
		// Isolates the whitespace clause: a real glyph, but fused to the word.
		{"glyph fused to word not matched", "·Marinating… (33s)\n❯\n", StatusFinished},
		// Isolates the `waiting for` clause: glyph-prefixed and capitalised, so only
		// that guard rejects it. The boxed rendering below is caught by `│` instead.
		{"unboxed team-waiting line not matched", "· Waiting for team lead approval… (2m 13s)\n❯ \n⏵⏵\n", StatusFinished},
		// A bash tool row ticking under a prompt must not outrank it: detectRunning runs
		// before detectWaiting, so matching this would hide a live permission menu.
		{"bash tool gutter row not matched", "⎿  Running… (9s · timeout 5m)\n❯\n", StatusFinished},
		{"team waiting box with counter not matched", "│ ✢ Waiting for team lead approval… (2m 13s)\n❯ \n⏵⏵\n", StatusWaiting},
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
		// Footer position guard (PR #269 review): a session discussing permission
		// prompts prints a numbered menu and the words "Esc to cancel" in ordinary
		// prose. Both are real conversation content sitting above the input box, so
		// the footer is nowhere near the bottom and must not complete the menu.
		{"menu quoted in prose not matched", "The dialog renders options like:\n  ❯ 1. Yes\n  2. No, tell Claude what to do differently\n\nThe footer (n to add notes + Esc to cancel) identifies it.\n\n✻ Worked for 59s\n\n❯ \n⏵⏵\n", StatusFinished},
		{"exitplanmode footer quoted in prose not matched", "We discussed the plan menu:\n  ❯ 1. Yes, and use auto mode\n  2. No, refine\n\nIts footer says shift+tab to approve with this feedback.\n\n✻ Worked for 12s\n\n❯ \n⏵⏵\n", StatusFinished},
		{"askuserquestion footer quoted verbatim in prose not matched", "⏺ The footer we match renders as:\n\n  Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel\n\n  That is the whole hint list.\n\n✻ Worked for 12s\n\n❯ \n⏵⏵\n", StatusFinished},
		// The same footer text, actually at the bottom, still registers.
		{"askuserquestion footer at bottom matched", "Round 3: Architecture\n☒ Stack  ☐ Process model\n\nEnter to select · ↑/↓ to navigate · n to add notes · Esc to cancel\n", StatusWaiting},
		// Delimiter widening (PR #269 review): the footer is this variant's sole
		// signal, so a rendering change must not silently stop firing.
		{"footer separated by wide gaps matched", "Which approach?\n\nEnter to select   n to add notes   Esc to cancel\n", StatusWaiting},
		// Documented bound, not an oversight: the check is per-line, so a footer that
		// WRAPS onto two lines (narrow pane) is not matched. Widening the delimiter
		// cannot fix that — the hints are no longer on one line to be separated.
		// Pinned so the limit is a known quantity rather than a surprise.
		{"footer hints split across lines not matched (wrap bound)", "Which approach?\n\nn to add notes\nEsc to cancel\n", ""},
		// Single spaces are inside a hint, not between hints — widening must not
		// shred "n to add notes" into words and match on fragments.
		{"single-spaced prose not matched", "the footer has n to add notes and Esc to cancel in it\n❯\n⏵⏵\n", StatusFinished},
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

// The prompt must reach the pane through the environment, never through the
// launch command: tmux sets env values with no shell involved, while the
// command string is typed into one. These two halves are what make an
// arbitrary prompt safe, so they are asserted together.
func TestInitialPromptTravelsInEnvNotCommand(t *testing.T) {
	nasty := "fix $(touch /tmp/pwned) `id`\nsecond line"
	s := &Session{ID: "abc-123", Agent: "claude", InitialPrompt: nasty}

	cmd := s.buildAgentCmd()
	if strings.Contains(cmd, "touch") || strings.Contains(cmd, "\n") {
		t.Errorf("launch command leaked prompt text: %q", cmd)
	}
	if !strings.Contains(cmd, "$FLEET_INITIAL_PROMPT") {
		t.Errorf("launch command should expand the prompt env var, got %q", cmd)
	}

	var got string
	found := false
	for _, e := range s.sessionEnv() {
		if v, ok := strings.CutPrefix(e, "FLEET_INITIAL_PROMPT="); ok {
			got, found = v, true
		}
	}
	if !found {
		t.Fatalf("sessionEnv() carried no prompt: %q", s.sessionEnv())
	}
	// Verbatim: any escaping here would reach the agent as literal backslashes.
	if got != nasty {
		t.Errorf("prompt env = %q, want %q", got, nasty)
	}
}

// A session with no prompt must launch exactly as it did before the feature
// existed — no stray argument, no empty env var that would make the agent open
// on a blank message.
func TestNoInitialPromptLeavesLaunchUnchanged(t *testing.T) {
	for _, ag := range []agent.Type{agent.Claude, agent.Codex, agent.OpenCode} {
		s := &Session{ID: "abc-123", Agent: ag}
		if cmd := s.buildAgentCmd(); strings.Contains(cmd, "FLEET_INITIAL_PROMPT") {
			t.Errorf("%s: promptless launch command mentions the prompt var: %q", ag, cmd)
		}
		for _, e := range s.sessionEnv() {
			if strings.HasPrefix(e, "FLEET_INITIAL_PROMPT=") {
				t.Errorf("%s: promptless session set %q", ag, e)
			}
		}
	}
}

// The one-shot rule has to hold on every path that hands the agent a command
// line, not just Start: each of them builds the launch command and env from the
// same two functions, so a path that forgets to consume the prompt silently
// re-submits the original task. This asserts the clear itself, which is what
// those paths call — the launches need real tmux and can't run here.
func TestInitialPromptIsOneShot(t *testing.T) {
	s := &Session{ID: "abc-123", Agent: agent.Claude, InitialPrompt: "do the thing"}

	s.mu.Lock()
	s.consumeInitialPromptLocked()
	s.mu.Unlock()

	if s.InitialPrompt != "" {
		t.Fatalf("InitialPrompt = %q, want it consumed", s.InitialPrompt)
	}
	// A second launch must now look exactly like a promptless one, or `r` on a
	// finished session would ask the agent to redo its first task.
	if cmd := s.buildAgentCmd(); strings.Contains(cmd, "FLEET_INITIAL_PROMPT") {
		t.Errorf("relaunch command still carries the prompt: %q", cmd)
	}
	for _, e := range s.sessionEnv() {
		if strings.HasPrefix(e, "FLEET_INITIAL_PROMPT=") {
			t.Errorf("relaunch env still carries the prompt: %q", e)
		}
	}
}

// TestConversationActivePastHookIgnoresQueuedWork covers the bug where a session
// blocked on an unanswered AskUserQuestion dialog reported Running.
//
// Two signals had to fail together to get there, and this is the second one. The pane
// went silent (a side-by-side preview panel pushed the "❯ N." cursor line past
// detectWaiting's window — see the pane_waiting_askuserquestion_single_preview golden),
// which handed the deciding vote to the transcript tiebreaker. The transcript's newest
// entry was:
//
//	{"type":"queue-operation","operation":"enqueue", … "Agent \"…\" finished" …}
//
// a background agent's task-notification being enqueued 4.9s after the PermissionRequest
// hook. It fires *because* the lead is parked, so counting it as progress inverted the
// tiebreaker's own premise — "the LEAD-only transcript stays static while the lead is
// truly blocked" — and pinned the session to running for the whole 120s window.
func TestConversationActivePastHookIgnoresQueuedWork(t *testing.T) {
	debuglog.Init()

	// Anchor on wall-clock: the tiebreaker also requires the last entry to be recent
	// (conversationActiveWindow), so fixed dates would fail on age alone and the test
	// would pass for the wrong reason.
	now := time.Now().UTC()
	hookAt := now.Add(-45 * time.Second)
	stamp := func(d time.Duration) string {
		return hookAt.Add(d).Format(time.RFC3339Nano)
	}
	// Real lead work, a full minute BEFORE the hook — the lead has not moved since.
	lead := []string{
		`{"type":"assistant","timestamp":"` + stamp(-61*time.Second) + `"}`,
		`{"type":"user","timestamp":"` + stamp(-58*time.Second) + `"}`,
	}
	// Past the hook by more than conversationActiveSkew, and recent enough to be inside
	// conversationActiveWindow — exactly the frame that flipped the live session.
	pastHook := stamp(5 * time.Second)

	cases := []struct {
		name string
		tail string
		want bool
	}{
		{
			name: "queued task-notification is not progress",
			tail: `{"type":"queue-operation","operation":"enqueue","timestamp":"` + pastHook + `","content":"<task-notification>Agent finished</task-notification>"}`,
			want: false,
		},
		{
			name: "turn-end bookkeeping is not progress",
			tail: `{"type":"system","subtype":"stop_hook_summary","timestamp":"` + pastHook + `"}`,
			want: false,
		},
		{
			name: "still-running sub-agent is not progress",
			tail: `{"type":"assistant","isSidechain":true,"timestamp":"` + pastHook + `"}`,
			want: false,
		},
		{
			// "attachment" reads like conversation but is overwhelmingly bookkeeping,
			// and "queued_command" is a message typed at a PARKED lead — the same class
			// as the queue-operation above, arriving through a different type.
			name: "queued_command attachment is not progress",
			tail: `{"type":"attachment","attachment":{"type":"queued_command"},"timestamp":"` + pastHook + `"}`,
			want: false,
		},
		{
			// Positive control. Without it every assertion above would also pass on a
			// scanner that had stopped finding anything at all.
			name: "a resumed lead turn IS progress",
			tail: `{"type":"assistant","timestamp":"` + pastHook + `"}`,
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			project := t.TempDir()
			const claudeID = "a9045325-69ee-4103-9cd5-d7cf07c8c9a8"

			dir := filepath.Join(home, ".claude", "projects", ClaudeProjectDirName(project))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := strings.Join(append(append([]string{}, lead...), tc.tail), "\n") + "\n"
			path := filepath.Join(dir, claudeID+".jsonl")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			s := &Session{
				ID:              "test-conv-active",
				Agent:           agent.Claude,
				ClaudeSessionID: claudeID,
				ProjectPath:     project,
				hookUpdatedAt:   hookAt,
			}
			if got := s.conversationActivePastHook(); got != tc.want {
				t.Errorf("conversationActivePastHook() = %v, want %v", got, tc.want)
			}
		})
	}
}
