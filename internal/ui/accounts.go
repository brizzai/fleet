package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// accountsMode is which pane of the dialog owns the keyboard.
type accountsMode int

const (
	accountsList         accountsMode = iota // the account table
	accountsPaste                            // manual token entry (the fallback path)
	accountsConfirmRm                        // "really remove?" on the highlighted row
	accountsWaitingToken                     // validating a token we just captured
	accountsRename                           // typing a display label for the highlighted row
)

// AccountsDialog manages the set of Claude subscriptions fleet can launch under.
//
// Accounts are self-naming: the email and plan come from `claude auth status`
// with the token applied, never from the user, so a label can't be wrong and
// nothing has to be typed but the token itself.
type AccountsDialog struct {
	visible bool
	width   int
	height  int

	mode     accountsMode
	accounts []claudeaccount.Account
	usage    map[string]claudeaccount.Usage
	cursor   int
	input    textinput.Model

	// defaultAccount mirrors the manual-strategy default so the list can mark
	// it; changing it here writes through to config.
	defaultAccount string
	manualStrategy bool

	err    string
	notice string
}

// NewAccountsDialog creates the dialog.
func NewAccountsDialog() *AccountsDialog {
	ti := textinput.New()
	ti.Placeholder = "sk-ant-oat01-…"
	ti.CharLimit = 200
	ti.SetWidth(46)
	return &AccountsDialog{input: ti}
}

// Show opens the dialog over the current account set.
func (d *AccountsDialog) Show(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, defaultAccount string, manual bool) {
	d.visible = true
	d.mode = accountsList
	d.accounts = accounts
	d.usage = usage
	d.defaultAccount = defaultAccount
	d.manualStrategy = manual
	d.err = ""
	d.notice = ""
	if d.cursor >= len(accounts) {
		d.cursor = 0
	}
}

// Refresh re-reads the account set after a mutation, keeping the dialog open.
func (d *AccountsDialog) Refresh(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, defaultAccount string) {
	d.accounts = accounts
	d.usage = usage
	d.defaultAccount = defaultAccount
	if d.cursor >= len(accounts) {
		d.cursor = max(0, len(accounts)-1)
	}
}

func (d *AccountsDialog) Hide()           { d.visible = false }
func (d *AccountsDialog) IsVisible() bool { return d.visible }
func (d *AccountsDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// SetBusy puts the dialog into its validating state while a captured token is
// checked against `claude auth status`.
func (d *AccountsDialog) SetBusy(notice string) {
	d.mode = accountsWaitingToken
	d.notice = notice
	d.err = ""
}

// SetError returns the dialog to a usable state after a failed add, dropping
// the user into the paste field — the fallback for a capture that missed.
func (d *AccountsDialog) SetError(err error, offerPaste bool) {
	d.err = claudeaccount.Redact(err.Error())
	d.notice = ""
	if offerPaste {
		d.mode = accountsPaste
		d.input.SetValue("")
		d.input.Focus()
		return
	}
	d.mode = accountsList
}

// Update handles key events.
func (d *AccountsDialog) Update(msg tea.Msg) (*AccountsDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch d.mode {
	case accountsWaitingToken:
		// Only escape works while a validation is in flight; acting on a
		// half-added account would be acting on a state the dialog can't show.
		if keyMsg.String() == "esc" {
			d.mode = accountsList
			d.notice = ""
		}
		return d, nil

	case accountsPaste:
		switch keyMsg.String() {
		case "esc":
			d.mode = accountsList
			d.input.Blur()
			d.err = ""
			return d, nil
		case "enter":
			token := strings.TrimSpace(d.input.Value())
			if token == "" {
				d.err = "paste the token printed by `claude setup-token`"
				return d, nil
			}
			// Accept a whole pasted line, not just a bare token — people paste
			// the surrounding output at least as often as the token alone.
			if tok, found := claudeaccount.ExtractToken(token); found {
				token = tok
			}
			d.input.Blur()
			d.SetBusy("Checking the token…")
			return d, func() tea.Msg { return accountValidateMsg{token: token} }
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return d, cmd

	case accountsRename:
		switch keyMsg.String() {
		case "esc":
			d.mode = accountsList
			d.input.Blur()
			return d, nil
		case "enter":
			email, label := d.selectedEmail(), strings.TrimSpace(d.input.Value())
			d.mode = accountsList
			d.input.Blur()
			if email == "" {
				return d, nil
			}
			return d, func() tea.Msg { return accountRenameMsg{email: email, label: label} }
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return d, cmd

	case accountsConfirmRm:
		switch keyMsg.String() {
		case "y":
			email := d.selectedEmail()
			d.mode = accountsList
			if email == "" {
				return d, nil
			}
			return d, func() tea.Msg { return accountRemoveMsg{email: email} }
		case "n", "esc":
			d.mode = accountsList
		}
		return d, nil
	}

	// List mode.
	switch keyMsg.String() {
	case "esc", "q":
		d.Hide()
	case "j", "down":
		if d.cursor < len(d.accounts)-1 {
			d.cursor++
		}
	case "k", "up":
		if d.cursor > 0 {
			d.cursor--
		}
	case "a":
		d.err = ""
		return d, func() tea.Msg { return accountSetupTokenMsg{} }
	case "p":
		// The explicit paste path, for anyone who already has a token.
		d.err = ""
		d.mode = accountsPaste
		d.input.SetValue("")
		d.input.Focus()
	case "r":
		// An account the API declined to identify gets a fingerprint name, so
		// renaming has to be reachable or those rows stay unreadable forever.
		if a, ok := d.selected(); ok {
			d.mode = accountsRename
			d.input.SetValue(a.Label)
			d.input.CursorEnd()
			d.input.Focus()
		}
	case "d":
		if len(d.accounts) > 0 {
			d.mode = accountsConfirmRm
		}
	case "J":
		if email := d.selectedEmail(); email != "" && d.cursor < len(d.accounts)-1 {
			d.cursor++
			return d, func() tea.Msg { return accountReorderMsg{email: email, delta: 1} }
		}
	case "K":
		if email := d.selectedEmail(); email != "" && d.cursor > 0 {
			d.cursor--
			return d, func() tea.Msg { return accountReorderMsg{email: email, delta: -1} }
		}
	case "enter":
		if email := d.selectedEmail(); email != "" {
			return d, func() tea.Msg { return accountSetDefaultMsg{email: email} }
		}
	}
	return d, nil
}

func (d *AccountsDialog) selectedEmail() string {
	a, ok := d.selected()
	if !ok {
		return ""
	}
	return a.Email
}

func (d *AccountsDialog) selected() (claudeaccount.Account, bool) {
	if d.cursor < 0 || d.cursor >= len(d.accounts) {
		return claudeaccount.Account{}, false
	}
	return d.accounts[d.cursor], true
}

// accountsDialogWidth is fixed so the box doesn't resize as usage percentages
// arrive or a longer email is added mid-session.
const accountsDialogWidth = 66

// View renders the dialog.
func (d *AccountsDialog) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	errStyle := lipgloss.NewStyle().Foreground(ColorRed)

	inner := accountsDialogWidth - 4

	var b strings.Builder
	b.WriteString(titleStyle.Render("Claude Accounts"))
	b.WriteString("\n\n")

	switch d.mode {
	case accountsPaste:
		b.WriteString(dimStyle.Render("Run `claude setup-token` in a terminal, then paste it here.") + "\n")
		b.WriteString(dimStyle.Render("The token lasts a year and is stored outside config.json.") + "\n\n")
		b.WriteString(d.input.View() + "\n")

	case accountsRename:
		b.WriteString(dimStyle.Render("Name for "+d.selectedEmail()) + "\n\n")
		b.WriteString(d.input.View() + "\n")
		b.WriteString(dimStyle.Render("Leave empty to clear the label.") + "\n")

	case accountsWaitingToken:
		b.WriteString(d.notice + "\n")
		b.WriteString(dimStyle.Render("Checking the token with Anthropic…") + "\n")

	default:
		if len(d.accounts) == 0 {
			b.WriteString(dimStyle.Render("No accounts yet — fleet uses whichever account you're") + "\n")
			b.WriteString(dimStyle.Render("logged into. Add a second to spread work across both.") + "\n")
		}
		for i, a := range d.accounts {
			b.WriteString(d.renderRow(i, a, inner) + "\n")
		}
		if d.mode == accountsConfirmRm {
			b.WriteString("\n" + errStyle.Render(fmt.Sprintf("Remove %s?  y / n", d.selectedEmail())) + "\n")
		}
	}

	if d.err != "" {
		b.WriteString("\n" + errStyle.Render("✕ "+d.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", inner)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(d.footer()))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(min(accountsDialogWidth, max(34, d.width-4)))

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(b.String()))
}

func (d *AccountsDialog) footer() string {
	switch d.mode {
	case accountsPaste:
		return "⏎ add   esc back"
	case accountsRename:
		return "⏎ rename   esc back"
	case accountsWaitingToken:
		return "esc cancel"
	case accountsConfirmRm:
		return "y remove   n cancel"
	}
	if d.manualStrategy {
		return "a add   p paste   r rename   d remove   J/K order   ⏎ default   esc close"
	}
	// Enter only means something under the manual strategy, so it is not
	// advertised when the strategy would ignore it.
	return "a add   p paste   r rename   d remove   J/K order   esc close"
}

// renderRow draws one account: order marker, name, plan, and quota when known.
func (d *AccountsDialog) renderRow(i int, a claudeaccount.Account, width int) string {
	selected := i == d.cursor && d.mode != accountsPaste
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	marker := "  "
	if d.manualStrategy && a.Email == d.defaultAccount {
		marker = "★ "
	}

	right := dimStyle.Render("not checked yet")
	if u, ok := d.usage[a.Email]; ok && u.Known() {
		right = renderQuotaCell(u)
	} else if ok && u.Err != nil {
		right = dimStyle.Render("unreadable")
	}

	name := a.Name()
	if a.Plan != "" {
		name += dimStyle.Render(" · " + a.Plan)
	}

	left := marker + name
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	row := left + strings.Repeat(" ", pad) + right

	if selected {
		return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("▸") + row
	}
	return " " + row
}

// renderQuotaCell renders the 5-hour window as "42% · resets 15:04", or the
// reset time alone once the window is spent — at that point the percentage
// tells you nothing you can act on and the clock is the whole answer.
func renderQuotaCell(u claudeaccount.Usage) string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	if u.Exhausted(time.Now()) {
		s := "spent"
		if !u.FiveHourReset.IsZero() {
			s += " · back " + u.FiveHourReset.Local().Format("15:04")
		}
		return lipgloss.NewStyle().Foreground(ColorRed).Render(s)
	}
	return quotaStyle(u.FiveHourPct).Render(fmt.Sprintf("%d%%", u.FiveHourPct)) +
		dimStyle.Render(" used")
}

// quotaStyle colours a utilization figure by how much headroom is left.
func quotaStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 85:
		return lipgloss.NewStyle().Foreground(ColorRed)
	case pct >= 60:
		return lipgloss.NewStyle().Foreground(ColorYellow)
	default:
		return lipgloss.NewStyle().Foreground(ColorGreen)
	}
}

// accountSetupPrefix names the throwaway tmux session that runs
// `claude setup-token`.
//
// Deliberately not a `fleet_` prefix: tmux.ListSessions enumerates agent
// sessions by that prefix, and a login pane must never be mistaken for one.
// Same reasoning as the drawer's `fleetsh_`.
const accountSetupPrefix = "fleetauth_"

const (
	// setupTokenPollInterval is how often the watcher checks the pane for the
	// token. Fast enough to feel immediate, slow enough that capture-pane on an
	// attached session costs nothing.
	setupTokenPollInterval = 750 * time.Millisecond
	// setupTokenWatchWindow bounds the watcher so an abandoned login can't
	// leave a goroutine polling for the life of the process. Generous: the
	// browser flow can involve signing in and pasting a code back.
	setupTokenWatchWindow = 10 * time.Minute
)

// openAccountsDialog shows the account manager.
func (h *Home) openAccountsDialog() tea.Cmd {
	h.accountsDialog.Show(
		h.accounts.List(),
		h.accountUsageSnapshot(),
		h.cfg.DefaultAccount,
		h.cfg.GetAccountStrategy() == claudeaccount.StrategyManual,
	)
	return nil
}

// runSetupToken hands the user a real terminal running `claude setup-token`,
// then reads the token off the pane when they come back.
//
// It has to be a live, attached pane rather than a captured command: the
// browser flow sometimes asks for a code to be pasted back, which a
// non-interactive capture could never satisfy.
func (h *Home) runSetupToken() tea.Cmd {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	ts := tmux.NewSessionWithPrefix(accountSetupPrefix, "setup-token", home)
	debuglog.Logger.Info("starting claude setup-token", "tmux", ts.Name, "cwd", home)

	// Ctrl+Q is the fallback, not the plan — the watcher below returns the user
	// automatically once the token lands. The hint stays because the watcher
	// can miss (an aborted flow prints no token at all).
	//
	// Strip any inherited token so setup-token authenticates the browser
	// session rather than the account fleet happens to be holding.
	const cmd = `claude setup-token; printf '\n\033[1;35m  ✻ Waiting for the token… (Ctrl+Q returns to fleet)\033[0m\n'`
	if err := ts.Start(cmd, "CLAUDE_CODE_OAUTH_TOKEN="); err != nil {
		debuglog.Logger.Error("could not start setup-token session", "tmux", ts.Name, "err", err)
		return func() tea.Msg {
			return accountValidatedMsg{capture: true, err: fmt.Errorf("could not start `claude setup-token`: %w", err)}
		}
	}

	h.isAttaching.Store(true)
	h.attachStartedAt.Store(time.Now().UnixNano())
	h.actionLog.Add("add claude account", "", true)

	// Watch the pane and pull the user back the moment the token appears, so
	// finishing the browser flow is the last thing they have to do. The token
	// is the final thing setup-token prints, so seeing it means the flow is
	// complete — there is no earlier state this could cut short.
	//
	// Runs alongside tea.Exec, which blocks until the attached client goes
	// away; detaching from here is what makes it return.
	captured := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(setupTokenWatchWindow)
		for time.Now().Before(deadline) {
			time.Sleep(setupTokenPollInterval)
			pane, err := ts.CapturePaneJoined()
			if err != nil {
				return // session gone: the user quit the shell themselves
			}
			tok, ok := claudeaccount.ExtractToken(pane)
			if !ok {
				continue
			}
			captured <- tok
			debuglog.Logger.Info("token appeared; returning from the setup pane", "tmux", ts.Name)
			if err := ts.DetachClient(); err != nil {
				debuglog.Logger.Error("could not auto-detach setup pane", "tmux", ts.Name, "err", err)
			}
			return
		}
		debuglog.Logger.Warn("setup-token watch window expired", "tmux", ts.Name, "window", setupTokenWatchWindow)
	}()

	return tea.Exec(attachCmd{session: ts}, func(execErr error) tea.Msg {
		h.isAttaching.Store(false)
		h.attachStartedAt.Store(0)

		// Prefer what the watcher saw. On a manual Ctrl+Q it has usually not
		// fired yet, so fall through to reading the pane directly.
		select {
		case tok := <-captured:
			if killErr := ts.Kill(); killErr != nil {
				debuglog.Logger.Error("failed to kill setup-token session", "err", killErr)
			}
			debuglog.Logger.Info("captured token from setup-token pane",
				"tmux", ts.Name, "token_len", len(tok), "via", "watcher")
			return accountValidateMsg{token: tok}
		default:
		}

		// Joined, not CapturePaneFresh: the token is ~108 characters and the
		// shell prompt eats columns before it, so on any realistic pane width
		// the unjoined capture returns it split across physical lines and the
		// match stops at the wrap. That truncated string then validates as a
		// bogus token, which is exactly what it is.
		pane, capErr := ts.CapturePaneJoined()
		if killErr := ts.Kill(); killErr != nil {
			debuglog.Logger.Error("failed to kill setup-token session", "err", killErr)
		}
		if capErr != nil {
			debuglog.Logger.Error("could not capture setup-token pane", "tmux", ts.Name, "err", capErr)
			return accountValidatedMsg{capture: true, err: fmt.Errorf("could not read the setup-token output: %w", capErr)}
		}
		token, found := claudeaccount.ExtractToken(pane)
		if !found {
			// Size and line count only — never the pane. A partial run can still
			// hold a token, and debug.log is what bug reports paste. An empty
			// capture means they quit early; a large one means the token
			// scrolled off or the format changed.
			debuglog.Logger.Warn("no token in setup-token output",
				"tmux", ts.Name, "pane_bytes", len(pane),
				"pane_lines", strings.Count(pane, "\n"), "exec_err", execErr)
			return accountValidatedMsg{capture: true, err: fmt.Errorf("no token found in the setup-token output — paste it instead")}
		}
		debuglog.Logger.Info("captured token from setup-token pane",
			"tmux", ts.Name, "token_len", len(token))
		return accountValidateMsg{token: token}
	})
}

// validateAccount identifies a token off the Update goroutine. Claude is
// shelled out to here, so this must never run inline.
func (h *Home) validateAccount(token string, fromCapture bool) tea.Cmd {
	return func() tea.Msg {
		acct, err := claudeaccount.Validate(context.Background(), token)
		return accountValidatedMsg{account: acct, capture: fromCapture, err: err}
	}
}

// handleAccountValidated stores a newly identified account, or reports why it
// could not be added.
func (h *Home) handleAccountValidated(msg accountValidatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Error("account validation failed", "err", claudeaccount.Redact(msg.err.Error()))
		// A capture that missed drops into the paste box, which is the whole
		// point of having one; a paste that failed stays put with the reason.
		h.accountsDialog.SetError(msg.err, msg.capture)
		return h, nil
	}
	h.accounts.Upsert(msg.account)
	return h, h.persistAccounts("Added " + msg.account.Name())
}

// maybePollAccountUsage refreshes each account's quota, throttled per account.
//
// Runs on the worker goroutine: it makes a network call per account, and the
// endpoint 429s persistently if polled faster than MinPollInterval — once
// tripped, backing off does not clear it quickly, so the throttle is a floor
// and not a target.
//
// Failures are recorded rather than raised. Quota is an optimization: with none
// of it readable, least-used degrades to waterfall order and everything still
// works, so a dead endpoint must never surface as an error the user has to
// dismiss.
func (h *Home) maybePollAccountUsage() {
	accounts := h.accounts.List()
	if len(accounts) == 0 {
		return
	}

	now := time.Now()
	current := h.accountUsageSnapshot()
	next := make(map[string]claudeaccount.Usage, len(accounts))
	changed := false

	for _, a := range accounts {
		prev, had := current[a.Email]
		if had {
			next[a.Email] = prev
		}
		// A scope refusal is permanent for this token — a `claude setup-token`
		// credential will never be allowed to read quota. Retrying it on a
		// timer would fill the log with an answer that cannot change.
		if had && errors.Is(prev.Err, claudeaccount.ErrQuotaScope) {
			continue
		}
		// FetchedAt advances on failure too, so a broken endpoint is retried at
		// the same slow cadence instead of on every cycle.
		if had && now.Sub(prev.FetchedAt) < claudeaccount.MinPollInterval {
			continue
		}

		u, err := claudeaccount.FetchUsage(context.Background(), a.Token)
		if err != nil {
			if errors.Is(err, claudeaccount.ErrQuotaScope) {
				// Said once, then never again for this account.
				debuglog.Logger.Info("quota unavailable for this account; assignment falls back to configured order",
					"account", a.Email)
			} else {
				// Warn, not Error: quota is an optimization and a dead endpoint
				// is survivable — but it silently changes which account gets
				// picked, so it must be visible in the log.
				debuglog.Logger.Warn("account usage poll failed",
					"account", a.Email, "err", claudeaccount.Redact(err.Error()))
			}
			prev.Err = err
			prev.FetchedAt = now
			next[a.Email] = prev
			changed = true
			continue
		}
		debuglog.Logger.Info("account usage polled",
			"account", a.Email,
			"five_hour_pct", u.FiveHourPct, "five_hour_reset", u.FiveHourReset.Format(time.RFC3339),
			"seven_day_pct", u.SevenDayPct,
			"exhausted", u.Exhausted(now))
		next[a.Email] = u
		changed = true
	}

	if !changed {
		return
	}
	h.accountUsage.Store(&next)
}

// nextAccountAfter returns the account following email in configured order,
// wrapping. Reports false when there is nowhere else to go.
func nextAccountAfter(accounts []claudeaccount.Account, email string) (claudeaccount.Account, bool) {
	if len(accounts) < 2 {
		return claudeaccount.Account{}, false
	}
	idx := -1
	for i, a := range accounts {
		if a.Email == email {
			idx = i
			break
		}
	}
	// An unknown current account (removed, or a session still on the ambient
	// login) moves to the first configured account rather than nowhere.
	if idx < 0 {
		return accounts[0], true
	}
	return accounts[(idx+1)%len(accounts)], true
}

// moveSelectedAccount moves the selected session to the next account and
// relaunches it there.
//
// Restart rebuilds the environment from sessionEnv and relaunches with
// `--resume <id>`, and ~/.claude/projects is shared across accounts, so the
// conversation survives intact. What does not survive is the prompt cache,
// which is per-account — hence this is an explicit action rather than something
// fleet does on its own.
func (h *Home) moveSelectedAccount() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		return nil
	}
	if agentOf(s) != agent.Claude {
		h.setError(fmt.Errorf("accounts apply to Claude sessions only"))
		return nil
	}
	next, ok := nextAccountAfter(h.accounts.List(), s.Account)
	if !ok {
		h.setError(fmt.Errorf("add a second Claude account to move sessions between them"))
		return nil
	}
	if conflict := claudeaccount.GuardConflictingAuth(); conflict != "" {
		h.setError(fmt.Errorf("%s is set and overrides fleet's account selection — unset it first", conflict))
		return nil
	}

	s.Account = next.Email
	if err := h.storage.UpdateAccount(s.ID, next.Email); err != nil {
		debuglog.Logger.Error("failed to persist session account", "id", s.ID, "err", err)
	}
	h.actionLog.Add("move session account", next.Email, true)
	h.setInfo("Moving to " + next.Name() + " — resuming the conversation there")

	id, title := s.ID, s.Title
	return func() tea.Msg {
		// Always a full Restart, never RespawnClaude: the token is a tmux
		// session-level env var set at creation, so respawning the pane inside
		// the existing session would relaunch under the OLD account.
		if err := s.Restart(); err != nil {
			debuglog.Logger.Error("account move restart failed", "id", id, "title", title, "err", err)
			return sessionRestartMsg{id: id, err: err}
		}
		return sessionRestartMsg{id: id}
	}
}

// agentOf resolves a session's agent, treating the legacy empty value as Claude
// the same way the launch path does.
func agentOf(s *session.Session) agent.Type { return agent.Parse(string(s.Agent)) }

// persistAccounts saves the store, re-points the token resolver and refreshes
// the dialog. Every account mutation goes through here so those three can't
// drift apart.
func (h *Home) persistAccounts(notice string) tea.Cmd {
	if err := h.accounts.Save(); err != nil {
		h.setError(fmt.Errorf("could not save accounts: %w", err))
		return nil
	}
	session.SetAccountTokenFunc(h.accounts.TokenFor)
	h.accountsDialog.Refresh(h.accounts.List(), h.accountUsageSnapshot(), h.cfg.DefaultAccount)
	if notice != "" {
		h.setInfo(notice)
	}
	return nil
}
