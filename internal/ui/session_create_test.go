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
	// Both focus states. submitBlocker is focus-independent — the blocked account
	// is still the one that would be used wherever the highlight sits — so the
	// footer has to hold up on the Agent row too. Testing only the Account row is
	// what let the footer ship pointing ←→ at the agent cycler.
	for _, focus := range []sessionCreateFocus{focusCreateAccount, focusCreateAgent} {
		for i := range rows {
			d := showCreate(t, rows)
			tap(&d, "down")
			for range i {
				tap(&d, "right")
			}
			if focus == focusCreateAgent {
				tap(&d, "up")
			}
			if d.focus != focus {
				t.Fatalf("option %d: focus is %d, want %d", i, d.focus, focus)
			}

			blocked := d.submitBlocker() != ""
			view := ansi.Strip(d.View())
			if saysCreate := strings.Contains(view, "⏎ create"); blocked == saysCreate {
				t.Errorf("focus=%d option %d (%s): blocked=%v but the footer offers ⏎ create=%v",
					focus, i, rows[i].label, blocked, saysCreate)
			}
			// The route it names has to work from where the highlight is: on the
			// Agent row ←→ cycles the agent, so "←→ pick another" would be a lie.
			if blocked && focus == focusCreateAgent && !strings.Contains(view, "↓ then ←→") {
				t.Errorf("option %d (%s): the blocked footer points at ←→ while the highlight is on Agent:\n%s",
					i, rows[i].label, view)
			}
			if fired := tap(&d, "enter") != nil; fired == blocked {
				t.Errorf("focus=%d option %d (%s): enter fired=%v while blocked=%v",
					focus, i, rows[i].label, fired, blocked)
			}
		}
	}
}

// A blocked footer must never leave the user without a route back to the row it
// is talking about: it replaces the whole hint, so the hint it replaces it with
// has to be reachable from wherever the highlight sits.
func TestCreateDialogBlockedFooterNamesAReachableKey(t *testing.T) {
	d := showCreate(t, createRows())
	tap(&d, "down")
	tap(&d, "right")
	tap(&d, "right") // dead@x.com, logged out
	if d.submitBlocker() == "" {
		t.Fatal("expected a blocked option")
	}
	if got := ansi.Strip(d.View()); !strings.Contains(got, "logged out — ←→ pick another") {
		t.Errorf("on the Account row the footer should name ←→:\n%s", got)
	}
	tap(&d, "up") // highlight moves to Agent; ←→ now cycles the agent
	if got := ansi.Strip(d.View()); !strings.Contains(got, "logged out — ↓ then ←→ pick another") {
		t.Errorf("on the Agent row the footer must route back to the Account row first:\n%s", got)
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

// `tab` advances the agent on master. Routing it through moveFocus made it do
// nothing at all here — for the majority of users, with no footer hint to signal
// the key had changed meaning. It has to keep advancing the only cycler on
// screen. (↑↓/j/k going inert is fine: they were never bound.)
func TestCreateDialogTabStillCyclesTheAgentBelowTwoAccounts(t *testing.T) {
	d := showCreate(t, nil)
	if d.agent != agent.Claude {
		t.Fatalf("opened on %q, want claude", d.agent)
	}
	tap(&d, "tab")
	if d.agent != agent.Codex {
		t.Errorf("tab left the agent on %q; on master it advanced to codex", d.agent)
	}
	tap(&d, "shift+tab")
	if d.agent != agent.Claude {
		t.Errorf("shift+tab left the agent on %q, want claude", d.agent)
	}
}

// With the Account row present, tab means "next row" — the idiom every other
// multi-field dialog here uses.
func TestCreateDialogTabMovesRowsWithTwoAccounts(t *testing.T) {
	d := showCreate(t, createRows())
	before := d.agent
	tap(&d, "tab")
	if d.focus != focusCreateAccount {
		t.Error("tab did not move to the Account row")
	}
	if d.agent != before {
		t.Errorf("tab changed the agent to %q while moving rows", d.agent)
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

// The value cell's budget has to match what renderRow actually spends, in
// COLUMNS. ◂ and ▸ are 3-byte runes but one column each, so budgeting them with
// len() reserved 4 columns that do not exist: the account label lost that much
// truncation headroom and the row's ▸ stopped short of the box's own width.
func TestCreateDialogRowChromeIsMeasuredInColumns(t *testing.T) {
	d := showCreate(t, createRows())
	const value = "0123456789"
	line := ansi.Strip(d.renderRow("Account", value, 200, true, true))
	if got, want := lipgloss.Width(line), sessionCreateRowChrome+lipgloss.Width(value); got != want {
		t.Errorf("a row is %d columns wide but the budget reserves %d — the value cell is off by %d",
			got, want, want-got)
	}
}

// The consequence the budget bug had on screen: the Account row's trailing ▸
// must reach the same column the box's own width implies, not stop short of it.
func TestCreateDialogAccountArrowReachesTheBoxWidth(t *testing.T) {
	d := showCreate(t, createRows())
	d.focus = focusCreateAccount
	var agentRow, accountRow string
	for _, line := range strings.Split(ansi.Strip(d.View()), "\n") {
		if strings.Contains(line, "Account   ") {
			accountRow = strings.TrimRight(line, " │")
		}
		if strings.Contains(line, "Agent     ") {
			agentRow = strings.TrimRight(line, " │")
		}
	}
	if accountRow == "" || agentRow == "" {
		t.Fatal("could not find both rows")
	}
	// The Account row is padded to a fixed value cell, so it is the wider of the
	// two; it must not exceed the inner width the box budgets.
	inner := sessionCreateWidth - sessionCreateChrome
	if w := lipgloss.Width(strings.TrimLeft(accountRow, " │")); w > inner {
		t.Errorf("the Account row is %d columns, past the %d-column inner width", w, inner)
	}
}
