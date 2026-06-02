package session

import "testing"

func TestCodexPaneWaiting(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "request_user_input question menu",
			content: "› ask me now\n\n  Question 1/1 (1 unanswered)\n  Which option?\n\n  › 1. A\n    2. B\n\n  tab to add notes | enter to submit answer | esc to interrupt\n",
			want:    true,
		},
		{
			name:    "command approval menu (esc to cancel)",
			content: "• Running touch x\n  Would you like to run the following command?\n  $ touch x\n  › 1. Yes, proceed (y)\n    2. No\n  Press enter to confirm or esc to cancel\n",
			want:    true,
		},
		{
			name:    "plan approval menu (esc to go back)",
			content: "• Proposed Plan\n  Implement this plan?\n  › 1. Yes, implement this plan\n    2. Yes, clear context and implement\n    3. No, stay in Plan mode\n  Press enter to confirm or esc to go back\n",
			want:    true,
		},
		{
			name:    "idle prompt is not waiting",
			content: "› Use /skills to list available skills\n\n  gpt-5.5 high · ~/code/proj\n",
			want:    false,
		},
		{
			name:    "running turn is not waiting",
			content: "• Working on it...\n  esc to interrupt\n",
			want:    false,
		},
		{
			name:    "empty pane",
			content: "",
			want:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexPaneWaiting(c.content); got != c.want {
				t.Errorf("codexPaneWaiting() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCodexPaneRunning(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			// Regression: the stuck-idle bug. Codex is actively working but no
			// hook fired; the pane is the only running signal.
			name:    "working footer with timer",
			content: "› sleep 10\n\n• Sleeping for 10 seconds.\n\n• Working (5s • esc to interrupt) · 1 background terminal running · /ps to view · /stop to close\n",
			want:    true,
		},
		{
			name:    "simple working footer",
			content: "• Working on it...\n  esc to interrupt\n",
			want:    true,
		},
		{
			name:    "idle prompt is not running",
			content: "› Use /skills to list available skills\n\n  gpt-5.5 high · ~/code/proj\n",
			want:    false,
		},
		{
			// "esc to interrupt" also appears in the request_user_input menu —
			// that's waiting, not running. Must not be a false positive.
			name:    "request_user_input menu is not running",
			content: "› ask me now\n\n  Question 1/1 (1 unanswered)\n  Which option?\n\n  › 1. A\n    2. B\n\n  tab to add notes | enter to submit answer | esc to interrupt\n",
			want:    false,
		},
		{
			name:    "approval menu is not running",
			content: "• Running touch x\n  Would you like to run the following command?\n  $ touch x\n  › 1. Yes, proceed (y)\n    2. No\n  Press enter to confirm or esc to cancel\n",
			want:    false,
		},
		{
			name:    "empty pane",
			content: "",
			want:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexPaneRunning(c.content); got != c.want {
				t.Errorf("codexPaneRunning() = %v, want %v", got, c.want)
			}
		})
	}
}
