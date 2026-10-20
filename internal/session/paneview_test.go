package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

func loadPane(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return string(data)
}

func optionLabels(m *Menu) []string {
	var out []string
	for _, o := range m.Options {
		out = append(out, o.Label)
	}
	return out
}

func TestParsePaneViewMenus(t *testing.T) {
	debuglog.Init()
	tests := []struct {
		fixture  string
		agent    agent.Type
		kind     string
		question string
		labels   []string
		cursor   int
		subject  string
	}{
		{
			fixture: "pane_waiting_permission_3opt.txt", kind: "permission",
			question: "Do you want to create a.json?",
			labels:   []string{"Yes", "Yes, allow all edits in tmp/ during this session (shift+tab)", "No"},
			cursor:   0, subject: "1 hello",
		},
		{
			fixture: "pane_waiting_permission_cursor_opt2.txt", kind: "permission",
			question: "Do you want to proceed?", cursor: 1,
			labels: []string{"Yes", "Yes, allow reading from morebrizzai-brz-env-from-cwd/ during this session", "No"},
		},
		{
			// The command sits in a dashed frame under Claude's auto-mode Tip; the
			// Tip is advice to the person at the keyboard, not context.
			fixture: "pane_waiting_permission_bash.txt", kind: "permission",
			question: "Do you want to proceed?", cursor: 0, subject: "echo fleet-test > note.txt",
			labels: []string{"Yes", "Yes, and always allow access to", "Yes, and switch to auto mode · auto mode handles these prompts for you", "No"},
		},
		{
			fixture: "pane_waiting_exitplanmode_approve.txt", kind: "plan", cursor: 0,
			question: "Claude has written up a plan and is ready to execute. Would you like to proceed?",
			labels:   []string{"Yes, and use auto mode", "Yes, manually approve edits", "No, refine with Ultraplan on Claude Code on the web", "Tell Claude what to change"},
		},
		{
			// Side-by-side preview panel: labels come from the left column only, and
			// the panel's lines never leak into an option's detail.
			fixture: "pane_waiting_askuserquestion_single_preview.txt", kind: "question", cursor: 0,
			question: "How should the renderer handle a sample that is mostly padding?",
			labels:   []string{"Trim at the payload", "Fall back to the record", "Use the raw message from", "Trim, and keep the"},
		},
		{
			// Focus on the question tabs: no option holds the cursor.
			fixture: "pane_waiting_askuserquestion_checkbox_focus.txt", kind: "question", cursor: -1,
			question: "Process architecture — how do the Go core and the UI talk?",
			labels:   []string{"Long-running Go daemon +", "Embed Go core into UI", "Spawn Go subprocess per UI", "Rewrite core in Swift/Rust"},
		},
		{
			// Footer is only "Esc to cancel" — the permission prompt's footer — so
			// the kind comes from the rows themselves.
			fixture: "pane_waiting_askuserquestion_simple.txt", kind: "question", cursor: 0,
			question: "Which color do you prefer?",
			labels:   []string{"Red", "Blue", "Type something.", "Chat about this"},
		},
		{
			// Unnumbered rows; the nearest line above them ("Security guide") is not
			// the question, the line that asks one is.
			fixture: "pane_waiting_trust_folder.txt", kind: "permission", cursor: 0,
			question: "Quick safety check: Is this a project you created or one you trust? (Like your",
			labels:   []string{"No, exit", "Yes, I trust this folder"},
		},
		{
			fixture: "copilot/ask_user.txt", agent: agent.Copilot, kind: "question", cursor: 0,
			question: "Which color should the button be?",
			labels:   []string{"red", "blue", "Other (type your answer)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			a := tt.agent
			if a == "" {
				a = agent.Claude
			}
			m := ParsePaneView(a, loadPane(t, tt.fixture)).Menu
			if m == nil {
				t.Fatal("no menu parsed")
			}
			if m.Kind != tt.kind {
				t.Errorf("kind = %q, want %q", m.Kind, tt.kind)
			}
			if m.Question != tt.question {
				t.Errorf("question = %q, want %q", m.Question, tt.question)
			}
			if got := optionLabels(m); strings.Join(got, "|") != strings.Join(tt.labels, "|") {
				t.Errorf("labels = %q, want %q", got, tt.labels)
			}
			for i, o := range m.Options {
				if o.N != i+1 {
					t.Errorf("option %d numbered %d", i, o.N)
				}
				if strings.ContainsAny(o.Detail, "│┌└") {
					t.Errorf("option %d detail carries a side panel: %q", o.N, o.Detail)
				}
			}
			if m.Cursor != tt.cursor {
				t.Errorf("cursor = %d, want %d", m.Cursor, tt.cursor)
			}
			if tt.subject != "" && m.Subject != tt.subject {
				t.Errorf("subject = %q, want %q", m.Subject, tt.subject)
			}
			for _, c := range m.Context {
				if strings.HasPrefix(c, "Tip:") {
					t.Errorf("context kept Claude's Tip line: %q", c)
				}
			}
		})
	}
}

// A numbered list in the conversation is not a prompt: no menu unless the
// agent's own detector says the pane is waiting.
func TestParsePaneViewNoMenuWhenNotWaiting(t *testing.T) {
	debuglog.Init()
	for _, f := range []string{
		"pane_finished_idle_prompt.txt",
		"pane_finished_permission_mode.txt",
		"pane_running_extended_thinking.txt",
		"pane_finished_prose_mentions_askuserquestion_footer.txt",
	} {
		if m := ParsePaneView(agent.Claude, loadPane(t, f)).Menu; m != nil {
			t.Errorf("%s: parsed a menu %q from a pane that isn't waiting", f, m.Question)
		}
	}
}

const (
	rule = "\x1b[38;5;244m────────────────────────────────────────────────────────────\x1b[39m"
	bar  = "  \x1b[38;5;220m⏵⏵ auto mode on\x1b[38;5;246m (shift+tab to cycle)\x1b[39m"
)

func inputPane(box ...string) string {
	return strings.Join(append(append([]string{"⏺ done.", "", rule}, box...), rule, bar), "\n")
}

func TestParsePaneViewInput(t *testing.T) {
	debuglog.Init()
	tests := []struct {
		name string
		pane string
		want Input
	}{
		{"empty box", inputPane("\x1b[39m❯ \x1b[7m \x1b[0m"), Input{}},
		{"typed draft", inputPane("\x1b[39m❯ hello draft text"), Input{Text: "hello draft text", Kind: InputDraft}},
		{
			// A dim box is Claude's suggestion — the same characters a draft would
			// be, which is exactly the confusion this exists to end.
			"suggestion", inputPane("\x1b[39m❯ \x1b[2mcheck on it when the plan is ready\x1b[0m"),
			Input{Text: "check on it when the plan is ready", Kind: InputSuggestion},
		},
		{
			// The block cursor sits on the first character in reverse video; it is
			// not evidence the text was typed.
			"suggestion under the cursor", inputPane("\x1b[39m❯ \x1b[7mc\x1b[27;2mommit this\x1b[0m"),
			Input{Text: "commit this", Kind: InputSuggestion},
		},
		{
			"multi-line draft", inputPane("\x1b[39m❯ first line", "  second line", "  third"),
			Input{Text: "first line\nsecond line\nthird", Kind: InputDraft},
		},
		{
			// The animated creature at the far right of the prompt line is not input.
			"creature in the margin", inputPane("\x1b[39m❯ \x1b[7m \x1b[0m" + strings.Repeat(" ", 60) + "}~(× .. ×)~{"),
			Input{},
		},
		{
			// An extended colour index of 2 is a colour, not the dim attribute.
			"colour 2 is not dim", inputPane("\x1b[39m❯ \x1b[38;5;2mgreen text\x1b[39m"),
			Input{Text: "green text", Kind: InputDraft},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParsePaneView(agent.Claude, tt.pane).Input; got != tt.want {
				t.Errorf("input = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The other agents' boxes aren't parsed; claiming a draft there would be a guess.
func TestParsePaneViewInputClaudeOnly(t *testing.T) {
	pane := inputPane("\x1b[39m❯ hello draft text")
	for _, a := range []agent.Type{agent.Codex, agent.OpenCode, agent.Copilot} {
		if got := ParsePaneView(a, pane).Input; got.Kind != InputEmpty {
			t.Errorf("%s: input = %+v, want empty", a, got)
		}
	}
}

func TestCleanPaneLines(t *testing.T) {
	pane := strings.Join([]string{
		"",
		"⏺ Reading 1 file… (ctrl+o to expand)",
		"  ⎿  ~/notes.md",
		"",
		"",
		"╭──────────────────────╮",
		"│ boxed text           │",
		"╰──────────────────────╯",
		"✻ Gesticulating… (5m 42s · ↓ 4.2k tokens)",
		rule,
		"\x1b[39m❯ unsent words",
		rule,
		bar,
	}, "\n")
	got := CleanPaneLines(pane)
	want := []string{
		"⏺ Reading 1 file…",
		"  ⎿  ~/notes.md",
		"",
		"  boxed text",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("CleanPaneLines =\n%q\nwant\n%q", got, want)
	}
}

func TestPaneShowsWorking(t *testing.T) {
	debuglog.Init()
	if !PaneShowsWorking(agent.Claude, loadPane(t, "pane_running_extended_thinking.txt")) {
		t.Error("extended-thinking pane should read as working")
	}
	if PaneShowsWorking(agent.Claude, loadPane(t, "pane_finished_idle_prompt.txt")) {
		t.Error("idle prompt should not read as working")
	}
	if PaneShowsWorking(agent.OpenCode, loadPane(t, "pane_running_extended_thinking.txt")) {
		t.Error("OpenCode has no pane detector; it must never claim to see work")
	}
}
