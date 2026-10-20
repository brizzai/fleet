package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTurnsTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Shapes taken from current Claude Code transcripts.
func human(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"origin":{"kind":"human"},"timestamp":%q,"message":{"role":"user","content":%q}}`, ts, text)
}

func assistantText(ts, text string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, ts, text)
}

func assistantTool(ts string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`, ts)
}

func toolResult(ts, content string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":%q}]},"toolUseResult":{}}`, ts, content)
}

func turnEnd(ts string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"turn_duration","timestamp":%q}`, ts)
}

func interrupt(ts string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`, ts)
}

func TestReadClaudeTurns(t *testing.T) {
	t.Run("prompt, tools, closing reply", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "fix the login bug"),
			assistantText("2026-10-20T10:00:01Z", "Let me look."),
			assistantTool("2026-10-20T10:00:02Z"),
			toolResult("2026-10-20T10:00:03Z", "ok"),
			assistantTool("2026-10-20T10:00:04Z"),
			toolResult("2026-10-20T10:00:05Z", "ok"),
			assistantText("2026-10-20T10:00:06Z", "Fixed: the redirect looped."),
			turnEnd("2026-10-20T10:00:07Z"),
		)
		turns, err := ReadClaudeTurns(path, 3)
		if err != nil || len(turns) != 1 {
			t.Fatalf("turns = %+v, err = %v", turns, err)
		}
		got := turns[0]
		if got.Prompt != "fix the login bug" || got.Tools != 2 || !got.Done || got.Interrupted {
			t.Errorf("turn = %+v", got)
		}
		// Only what follows the last tool call is the reply; "Let me look." is
		// narration on the way.
		if got.Reply != "Fixed: the redirect looped." {
			t.Errorf("reply = %q", got.Reply)
		}
	})

	t.Run("a turn in progress has no reply yet", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "refactor storage"),
			assistantText("2026-10-20T10:00:01Z", "Starting with the schema."),
			assistantTool("2026-10-20T10:00:02Z"),
		)
		turns, _ := ReadClaudeTurns(path, 1)
		if len(turns) != 1 || turns[0].Done || turns[0].Reply != "" || turns[0].Tools != 1 {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("a later prompt closes the turn before it", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "one"),
			assistantText("2026-10-20T10:00:01Z", "first"),
			human("2026-10-20T10:01:00Z", "two"),
			assistantText("2026-10-20T10:01:01Z", "second"),
		)
		turns, _ := ReadClaudeTurns(path, 5)
		if len(turns) != 2 || !turns[0].Done || turns[0].Reply != "first" || turns[1].Prompt != "two" {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("returns the last n, oldest first", func(t *testing.T) {
		var lines []string
		for i := range 5 {
			ts := fmt.Sprintf("2026-10-20T10:0%d:00Z", i)
			lines = append(lines, human(ts, fmt.Sprintf("prompt %d", i)), assistantText(ts, fmt.Sprintf("reply %d", i)))
		}
		turns, _ := ReadClaudeTurns(writeTurnsTranscript(t, lines...), 2)
		if len(turns) != 2 || turns[0].Prompt != "prompt 3" || turns[1].Prompt != "prompt 4" {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("an interrupt ends the turn", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "run the suite"),
			assistantTool("2026-10-20T10:00:01Z"),
			toolResult("2026-10-20T10:00:02Z", "The user doesn't want to proceed with this tool use."),
			interrupt("2026-10-20T10:00:02Z"),
		)
		turns, _ := ReadClaudeTurns(path, 1)
		if len(turns) != 1 || !turns[0].Done || !turns[0].Interrupted {
			t.Errorf("turns = %+v", turns)
		}
	})

	// Newer Claude Code replays earlier messages in "api-request-blob" lines. A
	// replayed interrupt marker belongs to an old turn, not the one running now.
	t.Run("a replayed interrupt is not this turn's", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "run it again"),
			`{"type":"api-request-blob","sessionId":"x","hash":"h","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
			assistantTool("2026-10-20T10:00:01Z"),
		)
		turns, _ := ReadClaudeTurns(path, 1)
		if len(turns) != 1 || turns[0].Done || turns[0].Interrupted {
			t.Errorf("turns = %+v", turns)
		}
	})

	// The words, inside tool output, are escaped in a JSON string and must not
	// read as an interrupt either.
	t.Run("interrupt words in tool output", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "grep for it"),
			assistantTool("2026-10-20T10:00:01Z"),
			toolResult("2026-10-20T10:00:02Z", `"text":"[Request interrupted by user`),
		)
		turns, _ := ReadClaudeTurns(path, 1)
		if len(turns) != 1 || turns[0].Interrupted {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("slash commands read back as the command", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "<command-name>/review</command-name>\n<command-message>review</command-message>\n<command-args>PR 12</command-args>"),
			fmt.Sprintf(`{"type":"user","timestamp":"2026-10-20T10:00:01Z","message":{"role":"user","content":%q}}`, "<local-command-stdout>ok</local-command-stdout>"),
		)
		turns, _ := ReadClaudeTurns(path, 2)
		if len(turns) != 1 || turns[0].Prompt != "/review PR 12" {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("sub-agent lines are not the lead's turns", func(t *testing.T) {
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "lead prompt"),
			`{"type":"user","isSidechain":true,"origin":{"kind":"human"},"timestamp":"2026-10-20T10:00:01Z","message":{"role":"user","content":"sub-agent task"}}`,
			`{"type":"assistant","isSidechain":true,"timestamp":"2026-10-20T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"sub-agent reply"}]}}`,
			assistantText("2026-10-20T10:00:03Z", "lead reply"),
		)
		turns, _ := ReadClaudeTurns(path, 3)
		if len(turns) != 1 || turns[0].Prompt != "lead prompt" || turns[0].Reply != "lead reply" {
			t.Errorf("turns = %+v", turns)
		}
	})

	t.Run("missing file is no turns, not an error", func(t *testing.T) {
		turns, err := ReadClaudeTurns(filepath.Join(t.TempDir(), "nope.jsonl"), 3)
		if err != nil || turns != nil {
			t.Errorf("turns = %v, err = %v", turns, err)
		}
	})

	// The tail window grows until it holds a prompt: a turn whose prompt sits
	// further back than the first read must still come back with its prompt.
	t.Run("window grows past bulky tool output", func(t *testing.T) {
		bulk := toolResult("2026-10-20T10:00:02Z", strings.Repeat("x", 600<<10))
		path := writeTurnsTranscript(t,
			human("2026-10-20T10:00:00Z", "the prompt before the bulk"),
			assistantTool("2026-10-20T10:00:01Z"),
			bulk,
			assistantText("2026-10-20T10:00:03Z", "done"),
		)
		turns, _ := ReadClaudeTurns(path, 1)
		if len(turns) != 1 || turns[0].Prompt != "the prompt before the bulk" || turns[0].Reply != "done" {
			t.Errorf("turns = %+v", turns)
		}
	})
}
