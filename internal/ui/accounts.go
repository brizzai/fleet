package ui

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	"github.com/charmbracelet/x/ansi"
)

// accountsMode is which pane of the dialog owns the keyboard.
type accountsMode int

const (
	accountsList         accountsMode = iota // the account table
	accountsConfirmRm                        // "really remove?" on the highlighted row
	accountsWaitingLogin                     // a browser login is in flight
	accountsRename                           // typing a display label for the highlighted row
)

// AccountsDialog manages the set of Claude subscriptions fleet can launch under.
//
// Adding one runs a real `claude` login in its own config directory, so the
// account arrives already named: `claude auth status` reports the email, the
// organization and the plan. Nothing here has to guess, and no credential
// passes through fleet — the login lands in that directory's own Keychain item.
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
	ti.CharLimit = 200
	ti.SetWidth(46)
	return &AccountsDialog{input: ti}
}

// Placeholders for the shared text input. One box serves both the paste and
// rename modes, so the placeholder has to be set on entry — otherwise the
// rename prompt asks for a name while showing a token as the example.
const (
	namePlaceholder = "personal, work, you@example.com"
)

// beginInput focuses the shared text box for a mode, seeding its value and
// placeholder together so the two can't disagree.
func (d *AccountsDialog) beginInput(mode accountsMode, value, placeholder string) {
	d.mode = mode
	d.input.SetValue(value)
	d.input.Placeholder = placeholder
	d.input.CursorEnd()
	d.input.Focus()
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
	// A refresh only happens once an operation has finished, so the in-flight
	// state is over by definition. Without this the dialog sits on "Checking
	// the token…" forever after a *successful* add — the account is saved and
	// the UI still says it is working.
	if d.mode == accountsWaitingLogin {
		d.mode = accountsList
		d.notice = ""
	}
}

// Added puts the dialog back on the list with the new account selected.
//
// No rename prompt: the login reports its own email, so the row is already
// readable. Renaming stays available on `r` for anyone who wants "work" rather
// than an address.
func (d *AccountsDialog) Added(email string) {
	d.mode = accountsList
	d.err = ""
	d.notice = ""
	for i, a := range d.accounts {
		if a.Email == email {
			d.cursor = i
			break
		}
	}
}

func (d *AccountsDialog) Hide()           { d.visible = false }
func (d *AccountsDialog) IsVisible() bool { return d.visible }
func (d *AccountsDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// SetBusy puts the dialog into its in-flight state while a browser login runs.
func (d *AccountsDialog) SetBusy(notice string) {
	d.mode = accountsWaitingLogin
	d.notice = notice
	d.err = ""
}

// SetError returns the dialog to the list after a failed add, with the reason.
//
// There is no fallback path to offer any more. The old flow could half-succeed
// — a browser login that worked but whose token fleet failed to scrape off the
// pane — so it dropped the user into a paste box. A login either completes in
// its config dir or it doesn't, and `a` retries it.
func (d *AccountsDialog) SetError(err error) {
	d.err = claudeaccount.Redact(err.Error())
	d.notice = ""
	d.mode = accountsList
}

// Update handles key events.
func (d *AccountsDialog) Update(msg tea.Msg) (*AccountsDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch d.mode {
	case accountsWaitingLogin:
		// Only escape works while a login is in flight; acting on a half-added
		// account would be acting on a state the dialog can't show.
		if keyMsg.String() == "esc" {
			d.mode = accountsList
			d.notice = ""
		}
		return d, nil

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
		// Number keys pick a suggestion. Almost every account is "work" or
		// "personal", so the common case should be one keystroke rather than
		// eight — but the box stays focused, so anything else is just typed.
		if n := keyMsg.String(); len(n) == 1 && n[0] >= '1' && n[0] <= '9' {
			if picks := d.nameSuggestions(); int(n[0]-'1') < len(picks) {
				d.input.SetValue(picks[n[0]-'1'])
				d.input.CursorEnd()
				return d, nil
			}
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
		return d, func() tea.Msg { return accountLoginMsg{} }
	case "r":
		// Optional now that rows carry a real email, but a two-account setup
		// reads better as "work" and "personal" than as two addresses.
		if a, ok := d.selected(); ok {
			d.beginInput(accountsRename, a.Label, namePlaceholder)
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

// nameSuggestions are the one-key picks offered in the rename box: the two
// labels that cover almost every two-subscription setup.
func (d *AccountsDialog) nameSuggestions() []string {
	var out []string
	for _, s := range []string{"work", "personal"} {
		if !d.labelTaken(s) {
			out = append(out, s)
		}
	}
	return out
}

// labelTaken reports whether another account already uses this name, so the
// picks never offer a duplicate.
func (d *AccountsDialog) labelTaken(name string) bool {
	sel := d.selectedEmail()
	for _, a := range d.accounts {
		if a.Email != sel && (a.Label == name || a.Email == name) {
			return true
		}
	}
	return false
}

func (d *AccountsDialog) selected() (claudeaccount.Account, bool) {
	if d.cursor < 0 || d.cursor >= len(d.accounts) {
		return claudeaccount.Account{}, false
	}
	return d.accounts[d.cursor], true
}

const (
	// accountsDialogWidth is fixed so the box doesn't resize as usage
	// percentages arrive or a longer name is added mid-session.
	accountsDialogWidth = 72
	// boxChrome is what the border and padding take out of the declared width.
	// Measured, not assumed: lipgloss v2's Width is border-inclusive, so the
	// usable content is Width - 2 border - 4 padding. Getting this wrong by two
	// wrapped the separator, the rows and the footer all at once.
	boxChrome = 6
)

// View renders the dialog.
func (d *AccountsDialog) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	errStyle := lipgloss.NewStyle().Foreground(ColorRed)

	boxW := min(accountsDialogWidth, max(40, d.width-4))
	inner := boxW - boxChrome

	var b strings.Builder
	b.WriteString(titleStyle.Render("Claude Accounts"))
	b.WriteString("\n\n")

	switch d.mode {
	case accountsRename:
		b.WriteString(dimStyle.Render(ansi.Truncate("Name for "+d.selectedName(), inner, "…")) + "\n")
		b.WriteString("\n" + d.input.View() + "\n")
		if picks := d.nameSuggestions(); len(picks) > 0 {
			var row []string
			for i, p := range picks {
				row = append(row, fmt.Sprintf("%d %s", i+1, p))
			}
			b.WriteString(dimStyle.Render(ansi.Truncate(strings.Join(row, "   "), inner, "…")) + "\n")
		}

	case accountsWaitingLogin:
		b.WriteString(d.notice + "\n")
		b.WriteString(dimStyle.Render("Checking the token with Anthropic…") + "\n")

	default:
		if len(d.accounts) == 0 {
			for _, l := range wrapTo(inner, "No accounts yet — fleet uses whichever account you're logged into. Add a second to spread work across both.") {
				b.WriteString(dimStyle.Render(l) + "\n")
			}
		}
		for i, a := range d.accounts {
			b.WriteString(d.renderRow(i, a, inner) + "\n")
		}
		if d.mode == accountsConfirmRm {
			b.WriteString("\n" + errStyle.Render(ansi.Truncate("Remove "+d.selectedName()+"?  y / n", inner, "…")) + "\n")
		}
	}

	if d.err != "" {
		b.WriteString("\n")
		for _, l := range wrapTo(inner, "✕ "+d.err) {
			b.WriteString(errStyle.Render(l) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", inner)))
	b.WriteString("\n")
	// The key hints are one idea per line rather than one long wrapped run:
	// a footer that wraps mid-chord reads as a rendering fault.
	for i, l := range d.footer(inner) {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(dimStyle.Render(l))
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(boxW)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(b.String()))
}

// selectedName is the human-facing name of the highlighted row.
func (d *AccountsDialog) selectedName() string {
	a, ok := d.selected()
	if !ok {
		return ""
	}
	return a.Name()
}

// footer returns the key hints, packed into lines that fit width.
func (d *AccountsDialog) footer(width int) []string {
	var hints []string
	switch d.mode {
	case accountsRename:
		hints = []string{"1-9 pick", "⏎ save", "esc skip"}
	case accountsWaitingLogin:
		hints = []string{"esc cancel"}
	case accountsConfirmRm:
		hints = []string{"y remove", "n cancel"}
	default:
		hints = []string{"a add", "p paste", "r rename", "d remove", "J/K order"}
		// Enter only means something under the manual strategy, so it is not
		// advertised when the strategy would ignore it.
		if d.manualStrategy {
			hints = append(hints, "⏎ default")
		}
		hints = append(hints, "esc close")
	}
	return packHints(hints, width)
}

// packHints lays hints out across as few lines as fit, never splitting one.
func packHints(hints []string, width int) []string {
	const sep = "   "
	var lines []string
	cur := ""
	for _, h := range hints {
		next := h
		if cur != "" {
			next = cur + sep + h
		}
		if cur != "" && lipgloss.Width(next) > width {
			lines = append(lines, cur)
			cur = h
			continue
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// wrapTo breaks text on word boundaries to fit width, so prose in the dialog
// never relies on the box to wrap it (the box wraps mid-word).
func wrapTo(width int, text string) []string {
	if width < 8 {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if cur != "" && lipgloss.Width(next) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// renderRow draws one account: selection marker, name, plan, and quota.
//
// width is the content width the row must not exceed; the name is truncated to
// protect the right-hand cell, since a wrapped row reads as a broken dialog.
func (d *AccountsDialog) renderRow(i int, a claudeaccount.Account, width int) string {
	selected := i == d.cursor
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	// Three lead cells, kept as separate columns rather than one string: the
	// cursor and the default-account star are independent, and a row can carry
	// both. (Slicing them back apart would also cut the multi-byte ★ in half.)
	cursorCol := " "
	if selected {
		cursorCol = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("▸")
	}
	starCol := "  "
	if d.manualStrategy && a.Email == d.defaultAccount {
		starCol = lipgloss.NewStyle().Foreground(ColorAccent).Render("★") + " "
	}
	const leadCells = 3

	// The right-hand cell is for numbers you can act on. An account whose token
	// may never read quota gets nothing rather than a word: "unreadable" reads
	// as a fault, when in fact nothing is wrong — a `claude setup-token`
	// credential simply isn't entitled to the usage endpoint.
	//
	// A rejection is the exception, and it outranks the numbers: the account is
	// out of the rotation entirely, so showing its last-known percentage would
	// present a dead credential as a healthy one with headroom to spare.
	right := ""
	if u, ok := d.usage[a.Email]; ok {
		switch {
		case u.LoggedOut:
			right = lipgloss.NewStyle().Foreground(ColorRed).Render("✕ logged out")
		case u.Known():
			right = renderQuotaCell(u)
		case u.Err != nil:
			right = dimStyle.Render("quota unavailable")
		}
	}

	name := a.Name()
	if a.Plan != "" {
		name += dimStyle.Render(" · " + a.Plan)
	}

	avail := width - leadCells - lipgloss.Width(right)
	if avail > 1 && lipgloss.Width(name) > avail {
		name = ansi.Truncate(name, avail, "…")
	}

	pad := width - leadCells - lipgloss.Width(name) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return cursorCol + starCol + name + strings.Repeat(" ", pad) + right
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
	// accountLoginPollInterval is how often the watcher asks whether the config
	// dir has a login yet. Each check shells out to `claude auth status`, so
	// this is slower than a pane capture would be and still feels immediate.
	accountLoginPollInterval = 1500 * time.Millisecond
	// accountLoginWindow bounds the watcher so an abandoned login can't leave a
	// goroutine polling for the life of the process. Generous: the browser flow
	// can involve signing in and pasting a code back.
	accountLoginWindow = 10 * time.Minute
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

// runAccountLogin hands the user a real terminal running `claude` in a fresh
// config directory, so they can complete /login for another subscription.
//
// It has to be a live, attached pane rather than a captured command: the browser
// flow sometimes asks for a code to be pasted back, which a non-interactive
// capture could never satisfy.
//
// Unlike the setup-token flow this replaced, nothing is scraped off the screen.
// The old watcher matched an `sk-ant-…` token in the pane, which meant fleet
// depended on Claude Code's exact output and could half-fail — a login that
// worked but whose token fleet missed. Here the pane is only a place for the
// user to type; the result is read by asking `claude auth status` whether that
// directory is logged in.
func (h *Home) runAccountLogin() tea.Cmd {
	dir, err := claudeaccount.NewConfigDirPath()
	if err == nil {
		err = claudeaccount.Provision(dir)
	}
	if err != nil {
		debuglog.Logger.Error("could not prepare an account config dir", "err", err)
		return func() tea.Msg {
			return accountLoggedInMsg{err: fmt.Errorf("could not prepare the account directory: %w", err)}
		}
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		home = os.TempDir()
	}
	ts := tmux.NewSessionWithPrefix(accountSetupPrefix, "login", home)
	debuglog.Logger.Info("starting account login", "tmux", ts.Name, "dir", dir)

	// Ctrl+Q is the fallback, not the plan — the watcher below returns the user
	// automatically once the login lands. The hint stays because an abandoned
	// flow never completes and the user needs a way out.
	cmd := `printf '\033[1;35m  ✻ Run /login to add this account, then wait (Ctrl+Q returns to fleet)\033[0m\n\n'; claude`
	// configDirEnv scrubs any inherited credential: fleet is often launched from
	// a fleet session, and an ambient token would outrank this dir's login and
	// log the wrong account in.
	_, env := claudeaccount.LoginCommand(dir)
	if err := ts.Start(cmd, env...); err != nil {
		debuglog.Logger.Error("could not start login session", "tmux", ts.Name, "err", err)
		return func() tea.Msg {
			return accountLoggedInMsg{err: fmt.Errorf("could not start `claude`: %w", err)}
		}
	}

	h.isAttaching.Store(true)
	h.attachStartedAt.Store(time.Now().UnixNano())
	h.actionLog.Add("add claude account", "", true)

	// Watch for the login completing and pull the user back the moment it does,
	// so finishing the browser flow is the last thing they have to do. Runs
	// alongside tea.Exec, which blocks until the attached client goes away;
	// detaching from here is what makes it return.
	identified := make(chan claudeaccount.Identity, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), accountLoginWindow)
		defer cancel()
		id, err := claudeaccount.WaitForLogin(ctx, dir, accountLoginPollInterval)
		if err != nil {
			debuglog.Logger.Warn("account login watch ended without a login",
				"tmux", ts.Name, "window", accountLoginWindow, "err", err)
			return
		}
		identified <- id
		if err := ts.DetachClient(); err != nil {
			debuglog.Logger.Error("could not auto-detach login pane", "tmux", ts.Name, "err", err)
		}
	}()

	return tea.Exec(attachCmd{session: ts}, func(execErr error) tea.Msg {
		h.isAttaching.Store(false)
		h.attachStartedAt.Store(0)
		if killErr := ts.Kill(); killErr != nil {
			debuglog.Logger.Error("failed to kill login session", "err", killErr)
		}

		// Prefer what the watcher saw. On a manual Ctrl+Q it has usually not
		// fired yet, so ask once more directly — the user may well have finished
		// logging in and detached before the next poll came round.
		var id claudeaccount.Identity
		select {
		case id = <-identified:
		default:
			var err error
			id, err = claudeaccount.Identify(context.Background(), dir)
			if err != nil {
				// The dir has no login, so it is litter. Removing it keeps an
				// abandoned add from leaving a directory that Provision would
				// keep refreshing forever.
				if rmErr := claudeaccount.RemoveConfigDir(dir); rmErr != nil {
					debuglog.Logger.Warn("could not remove an abandoned account dir", "dir", dir, "err", rmErr)
				}
				debuglog.Logger.Info("login pane closed without a login", "tmux", ts.Name, "err", err, "exec_err", execErr)
				return accountLoggedInMsg{err: fmt.Errorf("no account was logged in")}
			}
		}

		// Read quota now so the new row shows a number immediately rather than
		// waiting out a poll interval.
		usage, uerr := claudeaccount.FetchUsage(context.Background(), dir)
		if uerr != nil {
			debuglog.Logger.Info("quota unavailable just after login", "err", uerr)
		}
		debuglog.Logger.Info("account logged in", "email", id.Email, "plan", id.Plan, "org", id.OrgUUID)
		return accountLoggedInMsg{account: claudeaccount.WithUsage(claudeaccount.FromIdentity(id, dir), usage)}
	})
}

// handleAccountLoggedIn stores a newly added account, or reports why it failed.
func (h *Home) handleAccountLoggedIn(msg accountLoggedInMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Error("adding an account failed", "err", claudeaccount.Redact(msg.err.Error()))
		h.accountsDialog.SetError(msg.err)
		return h, nil
	}

	// Upsert matches on organization, so logging the same subscription in twice
	// updates the existing account instead of creating a second row for it —
	// and can therefore store under a key that is not msg.account.Email.
	// Everything below indexes by account, so it uses the key Upsert wrote.
	key := h.accounts.Upsert(msg.account)

	current := h.accountUsageSnapshot()
	next := make(map[string]claudeaccount.Usage, len(current)+1)
	maps.Copy(next, current)
	// Replace this account's reading outright rather than merging: a fresh login
	// is exactly how a logged-out account is fixed, and inheriting the old
	// verdict would keep it excluded from selection until the next poll, making
	// the fix look like it didn't work.
	next[key] = msg.account.InitialUsage()
	h.accountUsage.Store(&next)

	stored, ok := h.accounts.Get(key)
	if !ok {
		stored = msg.account
	}
	cmd := h.persistAccounts("Added " + stored.Name())
	// After persistAccounts, so the dialog is looking at the refreshed list.
	h.accountsDialog.Added(key)
	return h, cmd
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
		// Paced on AttemptedAt, not FetchedAt: a failing account must back off
		// at the same slow cadence as a healthy one rather than retrying every
		// cycle, and FetchedAt no longer advances on failure.
		if had && now.Sub(prev.AttemptedAt) < claudeaccount.MinPollInterval {
			continue
		}

		// The probe is the only route, deliberately.
		//
		// /api/oauth/usage is free but needs the `user:profile` scope that
		// `claude setup-token` withholds, so for fleet's accounts it answers
		// 403 — and when rate-limited it answers 429 instead, which is
		// indistinguishable from a transient failure and left accounts with no
		// quota at all. It also only tolerates callers claiming to be
		// claude-code. One mechanism that works for every token type, under
		// fleet's own name, is worth roughly nine tokens a poll.
		u, err := claudeaccount.FetchUsage(context.Background(), a.ConfigDir)
		if err != nil {
			// A missing login is the one failure that carries information.
			// Everything else here means "fleet could not ask", which says
			// nothing about the account; this says it cannot run a session at
			// all, and selection must act on it rather than score it as unknown.
			loggedOut := errors.Is(err, claudeaccount.ErrNotLoggedIn)
			if loggedOut {
				// Error, not Warn: every session assigned this account is about
				// to fall back to the ambient login, and the account keeps
				// looking healthy in the readout until someone reads this line.
				debuglog.Logger.Error("account is logged out; excluding it from new sessions",
					"account", a.Email, "err", claudeaccount.Redact(err.Error()))
			} else {
				// Warn, not Error: quota is an optimization and a dead endpoint
				// is survivable — but it silently changes which account gets
				// picked, so it must be visible in the log.
				debuglog.Logger.Warn("account usage poll failed",
					"account", a.Email, "err", claudeaccount.Redact(err.Error()))
			}
			// Keep whatever numbers we already had — a reading from three
			// minutes ago beats none, and dropping it would pull the account out
			// of the readout and rank it at the neutral midpoint for selection.
			// Only the attempt clock moves.
			prev.Err = err
			prev.AttemptedAt = now
			prev.LoggedOut = loggedOut
			next[a.Email] = prev
			changed = true
			continue
		}

		u.AttemptedAt = now
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

// persistAccounts saves the store, re-points the token resolver and refreshes
// the dialog. Every account mutation goes through here so those three can't
// drift apart.
func (h *Home) persistAccounts(notice string) tea.Cmd {
	if err := h.accounts.Save(); err != nil {
		h.setError(fmt.Errorf("could not save accounts: %w", err))
		return nil
	}
	session.SetAccountConfigDirFunc(h.accounts.ConfigDirFor)
	h.accountsDialog.Refresh(h.accounts.List(), h.accountUsageSnapshot(), h.cfg.DefaultAccount)
	if notice != "" {
		h.setInfo(notice)
	}
	return nil
}

// accountLabel is the human name for an account key, for use in UI text.
//
// A key the store no longer knows must not be shown as if the session were
// running on it. TokenFor returns nothing for an unknown account and
// sessionEnv then sets no variable at all, so such a session is really on the
// ambient login — the label says what is true now, and notes the removal as
// the reason rather than the state.
//
// This happens legitimately: an account re-added after fleet learns its email
// is keyed by that email, not the fingerprint it had before, so sessions
// created under the old key are orphaned by the rename.
func (h *Home) accountLabel(email string) string {
	if email == "" {
		return "your logged-in account"
	}
	if a, ok := h.accounts.Get(email); ok {
		return a.Name()
	}
	return "your logged-in account (its account was removed)"
}

// agentOf resolves a session's agent, treating the legacy empty value as Claude
// the same way the launch path does.
func agentOf(s *session.Session) agent.Type { return agent.Parse(string(s.Agent)) }

// previewAccountLabel names the account for the preview footer, or "" when
// there is nothing worth saying.
//
// Silent until a second account exists: with one (or none) every session runs
// on the same credential, so the label would be a constant — noise on the one
// line the user reads for path and recency.
func (h *Home) previewAccountLabel(s *session.Session) string {
	if s == nil || h.accounts.Len() < 2 || agentOf(s) != agent.Claude {
		return ""
	}
	return h.accountLabel(s.Account)
}
