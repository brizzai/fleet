package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/charmbracelet/x/ansi"
)

// The `A` dialog picks the agent AND, for a user running several Claude
// subscriptions, the account the session authenticates as. Getting the account
// wrong bills work to the wrong subscription, so several of these assert a
// refusal rather than a behaviour.

func createRows() []accountPickerRow {
	now := time.Now()
	return []accountPickerRow{
		{label: "Auto — good@x.com", usage: claudeaccount.Usage{FiveHourPct: 12, FetchedAt: now}, enabled: true},
		{email: "good@x.com", label: "good@x.com", usage: claudeaccount.Usage{FiveHourPct: 12, FetchedAt: now}, enabled: true},
		{email: "dead@x.com", label: "dead@x.com", enabled: false, note: "logged out"},
		{email: "nope@x.com", label: "nope@x.com", enabled: false, note: "not allowed here"},
	}
}

func showCreate(t *testing.T, rows []accountPickerRow) *SessionCreateDialog {
	t.Helper()
	d := NewSessionCreateDialog()
	d.SetSize(100, 40)
	d.Show("/repo/api", "api", agent.Claude, rows)
	return d
}

// tap sends one key the way the app does and keeps the returned dialog, so a
// test reads as a sequence of presses.
func tap(d **SessionCreateDialog, key string) tea.Cmd {
	code := rune(0)
	switch key {
	case "enter":
		code = tea.KeyEnter
	case "up":
		code = tea.KeyUp
	case "down":
		code = tea.KeyDown
	case "left":
		code = tea.KeyLeft
	case "right":
		code = tea.KeyRight
	default:
		code = []rune(key)[0]
	}
	next, cmd := (*d).Update(tea.KeyPressMsg{Code: code, Text: key})
	*d = next
	return cmd
}

// boxHeight counts the dialog's own rows. View() lipgloss.Places the box in the
// full viewport, so measuring the returned string measures the terminal — a
// box that grew a row would still come back the same height, and the assertion
// would pass vacuously.
func boxHeight(view string) int {
	n := 0
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.ContainsAny(line, "\u256d\u2502\u2570") {
			n++
		}
	}
	return n
}

func created(t *testing.T, cmd tea.Cmd) sessionCreateMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("enter produced no command — the dialog refused to create")
	}
	msg, ok := cmd().(sessionCreateMsg)
	if !ok {
		t.Fatalf("expected a sessionCreateMsg, got %T", cmd())
	}
	return msg
}

// Auto leads the cycle and is preselected, so `A` then enter behaves exactly as
// it did before this row existed: an empty account means "let account_strategy
// pick", resolved in handleSessionCreate.
func TestCreateDialogDefaultsToAuto(t *testing.T) {
	d := showCreate(t, createRows())
	msg := created(t, tap(&d, "enter"))
	if msg.account != "" {
		t.Errorf("Auto must send an empty account so the strategy still decides, got %q", msg.account)
	}
	if msg.agent != agent.Claude {
		t.Errorf("agent = %q, want claude", msg.agent)
	}
}

func TestCreateDialogEnterCarriesThePickedAccount(t *testing.T) {
	d := showCreate(t, createRows())
	tap(&d, "down")  // focus the Account row
	tap(&d, "right") // Auto -> good@x.com
	if msg := created(t, tap(&d, "enter")); msg.account != "good@x.com" {
		t.Errorf("account = %q, want good@x.com", msg.account)
	}
}

// A cycler can only honour "a disabled option is shown with its reason" by
// letting you land on one — so enter has to refuse there rather than open a
// session on an account that cannot answer.
func TestCreateDialogRefusesAnAccountThatCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps int
		note  string
	}{
		{"logged out", 2, "logged out"},
		{"disallowed by the origin", 3, "not allowed here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := showCreate(t, createRows())
			tap(&d, "down")
			for range tc.steps {
				tap(&d, "right")
			}
			if cmd := tap(&d, "enter"); cmd != nil {
				t.Error("enter created a session on an account that cannot run it")
			}
			if !d.IsVisible() {
				t.Error("the dialog closed on a refused enter — nothing left for the user to correct")
			}
			if view := ansi.Strip(d.View()); !strings.Contains(view, tc.note) {
				t.Errorf("the row must say why it can't be picked; %q not found in:\n%s", tc.note, view)
			}
		})
	}
}

// submitBlocker is consulted by both the enter handler and the footer, so the
// key can never act on a state the footer calls unready, and can never refuse
// while the footer claims enter creates.
func TestCreateDialogFooterAndEnterAgree(t *testing.T) {
	rows := createRows()
	for i := range rows {
		d := showCreate(t, rows)
		tap(&d, "down")
		for range i {
			tap(&d, "right")
		}
		blocked := d.submitBlocker() != ""
		saysCreate := strings.Contains(ansi.Strip(d.View()), "⏎ create")
		if blocked == saysCreate {
			t.Errorf("option %d (%s): blocked=%v but the footer offers ⏎ create=%v",
				i, rows[i].label, blocked, saysCreate)
		}
		if fired := tap(&d, "enter") != nil; fired == blocked {
			t.Errorf("option %d (%s): enter fired=%v while blocked=%v",
				i, rows[i].label, fired, blocked)
		}
	}
}

// The account is a claude.ai subscription credential Codex and OpenCode neither
// read nor need, so a pick made under Claude must not ride into one.
func TestCreateDialogDropsTheAccountForNonClaudeAgents(t *testing.T) {
	d := showCreate(t, createRows())
	tap(&d, "down")
	tap(&d, "right") // pick good@x.com
	tap(&d, "up")    // back to the Agent row
	tap(&d, "right") // Claude -> Codex
	msg := created(t, tap(&d, "enter"))
	if msg.agent != agent.Codex {
		t.Fatalf("agent = %q, want codex", msg.agent)
	}
	if msg.account != "" {
		t.Errorf("a codex session carried account %q — it would be silently ignored at launch", msg.account)
	}
}

// Cycling away from Claude and back must not quietly reset the account to Auto.
func TestCreateDialogKeepsTheAccountAcrossAnAgentCycle(t *testing.T) {
	d := showCreate(t, createRows())
	tap(&d, "down")
	tap(&d, "right")
	tap(&d, "up")
	tap(&d, "right") // Codex
	tap(&d, "left")  // back to Claude
	if msg := created(t, tap(&d, "enter")); msg.account != "good@x.com" {
		t.Errorf("account = %q after cycling the agent away and back, want good@x.com", msg.account)
	}
}

// The highlight may never park where ←→ does nothing: on a non-Claude agent the
// Account row is inert, so focus has to come back on its own.
func TestCreateDialogFocusLeavesTheInertAccountRow(t *testing.T) {
	d := showCreate(t, createRows())
	tap(&d, "down")
	tap(&d, "up")
	tap(&d, "right") // Claude -> Codex, with focus on Agent
	tap(&d, "down")  // must not land on the now-inert Account row
	if d.focus != focusCreateAgent {
		t.Error("focus moved onto the Account row while it was inert on a Codex session")
	}
	if view := ansi.Strip(d.View()); !strings.Contains(view, "Claude only") {
		t.Errorf("the inert row must name the clause that failed:\n%s", view)
	}
	// The account itself must not be shown beside it: "good@x.com Claude only"
	// reads as "this session runs on good@x.com", the opposite of what happens.
	if view := ansi.Strip(d.View()); strings.Contains(view, "good@x.com") {
		t.Errorf("an inert Account row named an account the session will not use:\n%s", view)
	}
}

// Below two accounts every session runs on the same credential, so the row would
// be a constant — the rule previewAccountLabel already applies to the preview
// footer.
func TestCreateDialogHasNoAccountRowBelowTwoAccounts(t *testing.T) {
	d := showCreate(t, nil)
	view := ansi.Strip(d.View())
	if strings.Contains(view, "Account") {
		t.Errorf("an Account row was rendered with no accounts configured:\n%s", view)
	}
	// ↑↓ are dead with one row, so the footer must not advertise them.
	if strings.Contains(view, "↑↓") {
		t.Errorf("the footer advertises ↑↓ with only one row to stand on:\n%s", view)
	}
	tap(&d, "down")
	if d.focus != focusCreateAgent {
		t.Error("focus left the Agent row when it is the only one")
	}
	if msg := created(t, tap(&d, "enter")); msg.account != "" {
		t.Errorf("account = %q, want empty", msg.account)
	}
}

// A dialog is fixed size: the box must not grow or shrink as the highlight
// moves, the agent cycles, or the footer swaps to a refusal. And no line may
// exceed the box — lipgloss v2's Width is border-inclusive, so an over-budget
// line wraps and silently adds a row.
func TestCreateDialogHeightIsStable(t *testing.T) {
	rows := createRows()
	base := showCreate(t, rows)
	want := boxHeight(base.View())

	for _, ag := range agentCycle {
		for _, focus := range []sessionCreateFocus{focusCreateAgent, focusCreateAccount} {
			for i := range rows {
				d := showCreate(t, rows)
				d.agent, d.focus, d.accountIdx = ag, focus, i
				if got := boxHeight(d.View()); got != want {
					t.Errorf("agent=%s focus=%d option=%d: height %d, want %d\n%s",
						ag, focus, i, got, want, ansi.Strip(d.View()))
				}
				for n, line := range strings.Split(d.View(), "\n") {
					if w := lipgloss.Width(line); w > 100 {
						t.Errorf("agent=%s focus=%d option=%d: line %d is %d cols, past the terminal",
							ag, focus, i, n, w)
					}
				}
			}
		}
	}
}

// The box has to survive a terminal narrower than its natural width without
// wrapping a row — a long repo path and a long account name both have to give.
func TestCreateDialogFitsANarrowTerminal(t *testing.T) {
	const narrow = 46
	wide := showCreate(t, createRows())

	d := NewSessionCreateDialog()
	d.SetSize(narrow, 20)
	d.Show("/very/long/path/to/some/repository/checkout", "checkout", agent.Claude, createRows())
	d.focus = focusCreateAccount

	if got, want := boxHeight(d.View()), boxHeight(wide.View()); got != want {
		t.Errorf("a narrow terminal changed the box height: %d lines, want %d\n%s",
			got, want, ansi.Strip(d.View()))
	}
	for n, line := range strings.Split(d.View(), "\n") {
		if w := lipgloss.Width(line); w > narrow {
			t.Errorf("line %d is %d cols wide on a %d-col terminal", n, w, narrow)
		}
	}
}
