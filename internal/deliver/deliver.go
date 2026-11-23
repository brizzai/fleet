// Package deliver types a message into an agent session's pane with every
// guard `fleet send` grew, so the CLI and the TUI share one implementation.
//
// The pane is a *typing* surface, not an API, and each guard here exists
// because typing into the wrong state did something worse than failing:
// a pane back at a shell runs the message as a command, a menu discards it and
// confirms the highlighted option, an agent mid-startup swallows it. Deliver
// refuses those states with a typed *Refusal, then checks the outcome on screen
// rather than predicting readiness.
//
// Nothing here prints. Callers (a one-shot CLI process, a tea.Cmd goroutine)
// render the Result and the error themselves.
package deliver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// Refusal reasons. Every error Deliver returns for a refused or failed send is
// a *Refusal whose Is matches exactly one of these, so callers branch with
// errors.Is and never on the message text.
var (
	// ErrAccountGone: a suspended Claude session is pinned to an account that
	// is no longer configured; waking it would bill the ambient login instead.
	ErrAccountGone = errors.New("pinned Claude account is no longer configured")
	// ErrSuspendedWakeFailed: the session was suspended and relaunching it with
	// the message failed. Cause carries the launch error.
	ErrSuspendedWakeFailed = errors.New("failed to wake suspended session")
	// ErrNoTmux: the session's tmux session is gone.
	ErrNoTmux = errors.New("no live tmux session")
	// ErrPaneDead: the agent exited and remain-on-exit kept its dead pane.
	ErrPaneDead = errors.New("agent process has exited")
	// ErrAtShellPrompt: the pane is back at a shell, where the message would
	// run as a command. Never bypassed by Options.Force.
	ErrAtShellPrompt = errors.New("pane is back at a shell prompt")
	// ErrAgentRunning: Options.RefuseRunning was set and the agent is mid-turn.
	ErrAgentRunning = errors.New("agent is running")
	// ErrWaitingPrompt: the agent is waiting on a menu, which would discard the
	// text and confirm the highlighted option on Enter. Bypassed by Force.
	ErrWaitingPrompt = errors.New("agent is waiting on a prompt")
	// ErrPasteFailed: tmux refused the paste. Cause carries its error.
	ErrPasteFailed = errors.New("failed to paste into the pane")
	// ErrNotConfirmed: the paste went through but the message never showed up
	// in the pane — swallowed while the agent was still painting its TUI.
	ErrNotConfirmed = errors.New("message was not delivered")
)

// Refusal is the error Deliver returns. Error() is the CLI's wording (it names
// `fleet restart` and `-force`, and `fleet send` prints it verbatim); Short()
// is a one-line form for the TUI, which has keys rather than commands.
type Refusal struct {
	Reason  error      // one of the Err* sentinels above
	Title   string     // session title
	ID      string     // fleet session id
	Agent   agent.Type // the session's agent
	Status  string     // session status at the time of the check (ErrNoTmux)
	PaneCmd string     // the pane's foreground command (ErrAtShellPrompt)
	Account string     // the pinned account (ErrAccountGone)
	Cause   error      // underlying error (ErrSuspendedWakeFailed, ErrPasteFailed)
}

// Is matches the refusal's reason, so errors.Is(err, deliver.ErrPaneDead) works.
func (r *Refusal) Is(target error) bool { return target == r.Reason }

// Unwrap exposes the underlying launch or tmux error, if any.
func (r *Refusal) Unwrap() error { return r.Cause }

// Forceable reports whether Options.Force would have let the send through.
func (r *Refusal) Forceable() bool { return r.Reason == ErrWaitingPrompt }

// Error is the `fleet send` wording, byte-for-byte.
func (r *Refusal) Error() string {
	id := hexID(r.ID)
	switch r.Reason {
	case ErrAccountGone:
		return fmt.Sprintf("session %q is pinned to Claude account %s, which is no longer configured — relaunching it "+
			"would run on your default login instead. In the fleet TUI, press r on it (fleet moves it to a usable "+
			"account) or use . → Move to Account…", r.Title, r.Account)
	case ErrSuspendedWakeFailed:
		return fmt.Sprintf("failed to wake suspended session %q: %v", r.Title, r.Cause)
	case ErrNoTmux:
		return fmt.Sprintf("session %q has no live tmux session (status %s) — restart it with fleet restart %s "+
			"(or r in the TUI)", r.Title, r.Status, id)
	case ErrPaneDead:
		return fmt.Sprintf("the %s process in session %q has exited — restart it with fleet restart %s (or r in the TUI)",
			r.Agent.DisplayName(), r.Title, id)
	case ErrAtShellPrompt:
		return fmt.Sprintf("the %s process in session %q has exited — its pane is back at a %s prompt, "+
			"where the message would run as a shell command.\nRestart it with fleet restart %s (or r in the TUI)",
			r.Agent.DisplayName(), r.Title, r.PaneCmd, id)
	case ErrAgentRunning:
		return fmt.Sprintf("session %q is still working — send once it has finished its turn", r.Title)
	case ErrWaitingPrompt:
		return fmt.Sprintf("session %q is waiting on a prompt — a message typed into a menu is "+
			"discarded and its Enter confirms the highlighted option.\nAnswer it first (attach, "+
			"or Y to approve), or pass -force to send anyway", r.Title)
	case ErrPasteFailed:
		return fmt.Sprintf("failed to send to session %q: %v", r.Title, r.Cause)
	case ErrNotConfirmed:
		return fmt.Sprintf("the message was not delivered — %q was still starting and its input "+
			"wasn't ready yet.\nNothing reached the agent; try again in a moment", r.Title)
	}
	if r.Cause != nil {
		return fmt.Sprintf("%v: %v", r.Reason, r.Cause)
	}
	return fmt.Sprint(r.Reason)
}

// Short is a single line for the TUI: no CLI commands, keys instead of flags.
func (r *Refusal) Short() string {
	name := r.Agent.DisplayName()
	switch r.Reason {
	case ErrAccountGone:
		return fmt.Sprintf("pinned to %s, which is no longer configured; press r to move it to a usable account", r.Account)
	case ErrSuspendedWakeFailed:
		return fmt.Sprintf("couldn't wake the suspended session: %v", r.Cause)
	case ErrNoTmux:
		return "the session isn't running; press r to restart it"
	case ErrPaneDead:
		return name + " has exited; press r to restart it"
	case ErrAtShellPrompt:
		return fmt.Sprintf("%s has exited (pane is at a %s prompt); press r to restart it", name, r.PaneCmd)
	case ErrAgentRunning:
		return name + " is still working; send once it finishes"
	case ErrWaitingPrompt:
		return name + " is waiting on a prompt; answer it first"
	case ErrPasteFailed:
		return fmt.Sprintf("couldn't paste into the pane: %v", r.Cause)
	case ErrNotConfirmed:
		return "not delivered: the agent's input wasn't ready yet, try again"
	}
	return r.Error()
}

// Pane is the slice of *tmux.Session Deliver types through.
type Pane interface {
	Exists() bool
	IsPaneDead() bool
	CapturePane() (string, error)
	CapturePaneFresh() (string, error)
	PasteAndSubmit(text string) error
}

// Deps are Deliver's side effects. Every nil field takes the production
// default, so the CLI passes Deps{} and tests replace what they fake.
type Deps struct {
	// Pane returns the session's pane. Default: s.GetTmuxSession().
	Pane func(s *session.Session) Pane
	// PaneCommand reads the pane's foreground command afresh. Default:
	// tmux.RefreshSessionCache + tmux.PaneCurrentCommand (the cache is a
	// snapshot, and a one-shot CLI has no tick repopulating it).
	PaneCommand func(tmuxSession string) string
	// AccountGone refuses to wake a session pinned to a removed account.
	// Default: AccountGoneErr against the configured accounts. The TUI, which
	// heals a pin on relaunch rather than refusing, may replace it.
	AccountGone func(s *session.Session) error
	// Wake relaunches a suspended session; s.InitialPrompt already holds the
	// message. Default: s.Restart().
	Wake func(s *session.Session) error
}

// Options tune one delivery.
type Options struct {
	// Force sends even though the agent is waiting on a prompt. It never
	// bypasses the shell-prompt guard: there is no version of that a user wants.
	Force bool
	// RefuseRunning refuses while the session's status is running instead of
	// typing into the agent mid-turn. `fleet send` leaves it off.
	RefuseRunning bool

	// Zero values take the defaults below.
	StartupGrace  time.Duration // DefaultStartupGrace
	StartupPoll   time.Duration // DefaultStartupPoll
	DeliveryGrace time.Duration // DefaultDeliveryGrace
	DeliveryPoll  time.Duration // DefaultDeliveryPoll
}

// DefaultStartupGrace bounds how long Deliver waits for a pane to stop looking
// like a bare shell. Generous because the cost is paid only on the
// genuinely-exited path, which is already an error, while under-waiting
// produces a *wrong* error on a healthy session: measured start times ranged
// from 0.6s to 7.4s, dominated by shell rc and the agent's own boot.
const DefaultStartupGrace = 10 * time.Second

// DefaultStartupPoll is the gap between pane reads during that grace period.
const DefaultStartupPoll = 150 * time.Millisecond

// DefaultDeliveryGrace bounds how long Deliver waits for the message to show up
// in the pane, and DefaultDeliveryPoll is the gap between those reads.
const (
	DefaultDeliveryGrace = 2 * time.Second
	DefaultDeliveryPoll  = 250 * time.Millisecond
)

// Result describes a delivery that went through (and, on some errors, what had
// already happened before it failed).
type Result struct {
	// Woke: the session was suspended and was relaunched with the message as its
	// launch prompt. The caller must persist the relaunch — see PersistRelaunch,
	// which takes PrevTmuxSession and PrevClaudeSessionID.
	Woke bool
	// The session's tmux session name and conversation id before the wake.
	PrevTmuxSession     string
	PrevClaudeSessionID string
	// Verified: the message was seen in the pane after the paste. False on a
	// wake (the message rides the launch, not a paste) and for a message too
	// short to identify, where the check is skipped rather than guessed.
	Verified bool
	// PromptUndetectable: the agent has no structural prompt detector
	// (OpenCode), so the waiting guard could not run. If the agent was in fact
	// on a menu, the message was discarded and its Enter confirmed the
	// highlighted option. Set before the paste, so it is reported on errors too.
	PromptUndetectable bool
}

// Deliver hands msg to s, waking it first if it is suspended.
//
// Guards run in this order, each refusing with a *Refusal:
//
//  1. suspended → ErrAccountGone, else wake with msg as InitialPrompt
//     (ErrSuspendedWakeFailed); done — nothing is pasted.
//  2. ErrNoTmux, 3. ErrPaneDead, 4. ErrAtShellPrompt (after a startup grace),
//  5. ErrAgentRunning (only with RefuseRunning), 6. ErrWaitingPrompt (unless
//     Force), then paste (ErrPasteFailed) and confirm (ErrNotConfirmed).
//
// Blocks for up to StartupGrace + DeliveryGrace; call it off the UI goroutine.
// A cancelled ctx returns ctx.Err() from whichever wait it interrupted.
func Deliver(ctx context.Context, d Deps, s *session.Session, msg string, opts Options) (Result, error) {
	d = d.withDefaults()
	opts = opts.withDefaults()
	var res Result

	refuse := func(reason error) *Refusal {
		return &Refusal{Reason: reason, Title: s.Title, ID: s.ID, Agent: s.Agent, Account: s.Account}
	}

	// A suspended session has no tmux to type into — fleet killed it to free
	// memory. Its conversation survives in ClaudeSessionID, so wake it with the
	// message as the launch prompt: the agent resumes already working on it,
	// which is the same one-shot path `fleet worktree -p` uses.
	if s.GetStatus() == session.StatusSuspended {
		if err := d.AccountGone(s); err != nil {
			return res, err
		}
		res.PrevTmuxSession = s.TmuxSessionName
		res.PrevClaudeSessionID = s.GetClaudeSessionID()
		s.InitialPrompt = msg
		if err := d.Wake(s); err != nil {
			// A launch keeps InitialPrompt on failure so a retry carries it. This
			// message was not delivered, so it must not ride a later, unrelated
			// relaunch (the TUI's r) either; the caller retries with it explicitly.
			s.InitialPrompt = ""
			r := refuse(ErrSuspendedWakeFailed)
			r.Cause = err
			return res, r
		}
		res.Woke = true
		return res, nil
	}

	ts := d.Pane(s)
	if !ts.Exists() {
		r := refuse(ErrNoTmux)
		r.Status = string(s.GetStatus())
		return res, r
	}
	// remain-on-exit keeps a dead pane on screen, so an exited agent looks
	// identical to a live one from the outside. Typing into it does nothing.
	if ts.IsPaneDead() {
		return res, refuse(ErrPaneDead)
	}
	// A live tmux session is not proof an agent is listening: the pane runs a
	// shell and the agent is a process inside it, so `/exit` (or a crash) drops
	// the pane back to a prompt with tmux none the wiser. Text pasted there is a
	// command line and the Enter runs it. Deliberately not bypassable with
	// Force: there is no version of this the user wanted.
	paneCmd, atShell, err := waitForAgent(ctx, d.PaneCommand, s.TmuxSessionName, opts.StartupGrace, opts.StartupPoll)
	if err != nil {
		return res, err
	}
	if atShell {
		r := refuse(ErrAtShellPrompt)
		r.PaneCmd = paneCmd
		return res, r
	}

	if opts.RefuseRunning && s.GetStatus() == session.StatusRunning {
		return res, refuse(ErrAgentRunning)
	}

	if !opts.Force {
		if pane, err := ts.CapturePane(); err == nil && session.PaneIndicatesWaiting(s.Agent, pane) {
			return res, refuse(ErrWaitingPrompt)
		}
		// The check above can't see an OpenCode prompt (no structural detector
		// for its UI), so for that agent the guard silently withdraws at the
		// moment it matters most. Refusing outright would be worse — OpenCode
		// defaults to allow-all, so every send would need Force and the flag
		// would become reflex — but proceeding without saying so leaves the user
		// with a guarantee they don't have. The caller says so.
		res.PromptUndetectable = s.Agent == agent.OpenCode
	}

	if err := ts.PasteAndSubmit(msg); err != nil {
		r := refuse(ErrPasteFailed)
		r.Cause = err
		return res, r
	}
	// A pane whose agent is *running* is not necessarily an agent that is
	// *listening*: for a couple of seconds after the process appears, the TUI is
	// still painting and anything written to the pty is discarded. Measured at
	// ~2.4s for Claude, and no signal distinguishes it — the process name has
	// already flipped, and the pane content sits static mid-startup, so waiting
	// for it to settle fires ~2s too early.
	//
	// So don't predict readiness, check the outcome: the message either shows up
	// in the pane or it didn't arrive. Deliberately no auto-retry — a re-paste on
	// a false negative would submit the message twice.
	checked, ok, err := confirmDelivered(ctx, ts, msg, opts.DeliveryGrace, opts.DeliveryPoll)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, refuse(ErrNotConfirmed)
	}
	res.Verified = checked
	return res, nil
}

func (d Deps) withDefaults() Deps {
	if d.Pane == nil {
		d.Pane = func(s *session.Session) Pane { return s.GetTmuxSession() }
	}
	if d.PaneCommand == nil {
		d.PaneCommand = freshPaneCommand
	}
	if d.AccountGone == nil {
		d.AccountGone = func(s *session.Session) error { return AccountGoneErr(s.Agent, s.Account, s.Title) }
	}
	if d.Wake == nil {
		d.Wake = func(s *session.Session) error { return s.Restart() }
	}
	return d
}

func (o Options) withDefaults() Options {
	if o.StartupGrace <= 0 {
		o.StartupGrace = DefaultStartupGrace
	}
	if o.StartupPoll <= 0 {
		o.StartupPoll = DefaultStartupPoll
	}
	if o.DeliveryGrace <= 0 {
		o.DeliveryGrace = DefaultDeliveryGrace
	}
	if o.DeliveryPoll <= 0 {
		o.DeliveryPoll = DefaultDeliveryPoll
	}
	return o
}

// AccountGoneErr refuses to relaunch a Claude session pinned to an account
// that is no longer configured: the launch would fall back to the ambient login
// and silently bill a different subscription. The TUI heals such a pin on r; the
// CLI can't, since which account to move to depends on quota and login state
// that live only in a running TUI. With no accounts configured at all there is
// nothing to heal to, so the TUI launches on the ambient login and so does this.
// Returns a *Refusal matching ErrAccountGone, or nil.
func AccountGoneErr(ag agent.Type, account, title string) error {
	if ag != agent.Claude || account == "" {
		return nil
	}
	accounts := claudeaccount.Load()
	if accounts.Len() == 0 || accounts.ConfigDirFor(account) != "" {
		return nil
	}
	return &Refusal{Reason: ErrAccountGone, Title: title, Agent: ag, Account: account}
}

// Store is the slice of *session.StateDB a relaunch's persistence needs.
type Store interface {
	UpdateTmuxSession(id, tmuxSession string) error
	UpdateStatus(id, status string) error
	UpdateClaudeSessionID(id, claudeSessionID string) error
}

// PersistRelaunch records what a relaunch (Restart, RespawnClaude, a wake)
// changed on s, given the tmux session name and conversation id it had before.
// Failures are logged, not returned: the relaunch already happened.
func PersistRelaunch(st Store, id, prevTmux, prevClaudeID string, s *session.Session) {
	// Restart built a new tmux session; without persisting the new name the
	// row points at a session that no longer exists and the next reader
	// (TUI or CLI) reads the session as dead.
	if s.TmuxSessionName != prevTmux {
		if err := st.UpdateTmuxSession(id, s.TmuxSessionName); err != nil {
			debuglog.Logger.Error("failed to persist tmux session after relaunch", "id", id, "err", err)
		}
	}
	if err := st.UpdateStatus(id, string(s.GetStatus())); err != nil {
		debuglog.Logger.Error("failed to persist status after relaunch", "id", id, "err", err)
	}
	// A relaunch may have *healed* the conversation id on the way in
	// (adoptResolvedLaunchID promotes a resolved id over a stale one — the
	// issue #226 path). The TUI gets away with not persisting that because its
	// hook worker re-persists moments later; a CLI process exits as soon as it
	// is done, so an unsaved heal is recomputed and thrown away on every
	// relaunch and the session stays pinned to a dead conversation. An empty id
	// is `restart -fresh` dropping one, written only now that the launch worked.
	if cid := s.ClaudeSessionID; cid != prevClaudeID {
		if err := st.UpdateClaudeSessionID(id, cid); err != nil {
			debuglog.Logger.Error("failed to persist healed conversation id after relaunch", "id", id, "err", err)
		}
	}
}

// PaneAtShell reads the pane's foreground command once, after refreshing the
// tmux cache, and reports whether it is a shell sitting at its prompt.
func PaneAtShell(tmuxSession string) (paneCmd string, atShell bool) {
	paneCmd = freshPaneCommand(tmuxSession)
	return paneCmd, tmux.IsShellCommand(paneCmd)
}

// WaitForAgentInPane reports whether the pane is sitting at a shell prompt
// with no agent in the foreground, returning the command it settled on. It
// polls for up to DefaultStartupGrace.
//
// It polls rather than reading once, because a single read cannot tell "the
// agent exited" from "the agent hasn't started yet" — and both are common. A
// session is launched by *typing* the agent's command into a fresh pane, so for
// as long as the shell takes to source its rc, pane_current_command is `zsh`.
// That is precisely the window the advertised script runs in:
//
//	gh issue view 242 | fleet wt fix-242 -p -
//	fleet send fix-242 "also update the tests"     # ← lands mid-startup
//
// Reading once made that fail with "the Claude process has exited", which was
// both false and — since this guard is deliberately not forceable — a dead
// end. Waiting costs nothing on a healthy session (the first read after startup
// wins) and delays only the genuinely-exited case, which is already an error.
func WaitForAgentInPane(tmuxSession string) (paneCmd string, atShell bool) {
	paneCmd, atShell, _ = waitForAgent(context.Background(), freshPaneCommand, tmuxSession,
		DefaultStartupGrace, DefaultStartupPoll)
	return paneCmd, atShell
}

func freshPaneCommand(tmuxSession string) string {
	tmux.RefreshSessionCache()
	return tmux.PaneCurrentCommand(tmuxSession)
}

func waitForAgent(ctx context.Context, paneCommand func(string) string, tmuxSession string,
	grace, poll time.Duration) (paneCmd string, atShell bool, err error) {
	deadline := time.Now().Add(grace)
	for {
		paneCmd = paneCommand(tmuxSession)
		if !tmux.IsShellCommand(paneCmd) {
			return paneCmd, false, nil
		}
		if time.Now().After(deadline) {
			return paneCmd, true, nil
		}
		if err := sleep(ctx, poll); err != nil {
			return paneCmd, true, err
		}
	}
}

// confirmDelivered reports whether message visibly reached the pane. checked is
// false when the message is too short to look for, in which case ok is true.
//
// "Visibly reached" is the right bar rather than "was submitted": a message
// sitting in the input box is in front of the user and recoverable, whereas one
// swallowed during startup is gone with nothing on screen to show for it.
func confirmDelivered(ctx context.Context, ts Pane, message string, grace, poll time.Duration) (checked, ok bool, err error) {
	needle := DeliveryNeedle(message)
	if needle == "" {
		return false, true, nil // nothing distinctive to look for; don't invent a failure
	}
	deadline := time.Now().Add(grace)
	for {
		if pane, err := ts.CapturePaneFresh(); err == nil {
			hay := squashSpace(session.StripANSI(pane))
			// Claude collapses a multi-line paste to a "[Pasted text #1 +N lines]"
			// placeholder and Copilot one over 10 lines to "[Paste #1 - 12 lines]",
			// so the text itself is never on screen to match. hay is squashed.
			if strings.Contains(hay, needle) || strings.Contains(hay, "[Pastedtext") || strings.Contains(hay, "[Paste#") {
				return true, true, nil
			}
		}
		if time.Now().After(deadline) {
			return true, false, nil
		}
		if err := sleep(ctx, poll); err != nil {
			return true, false, err
		}
	}
}

// DeliveryNeedle builds what to look for in the pane: the head of the message's
// first line, with whitespace squashed out.
//
// Squashing is what makes the match survive rendering — the pane wraps long
// lines and indents continuations, so the message's own spacing is not what
// ends up on screen. Returns "" for a message too short to identify, where a
// match would be indistinguishable from coincidence; the caller then skips the
// check rather than guessing.
func DeliveryNeedle(message string) string {
	line := message
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	squashed := squashSpace(line)
	r := []rune(squashed)
	if len(r) < 6 {
		return ""
	}
	if len(r) > 24 {
		r = r[:24]
	}
	return string(r)
}

// squashSpace removes all whitespace, so wrapped and re-indented pane text
// still matches the message it came from.
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// hexID is a session id's random half, the part `fleet status` shows and that
// resolves as an id prefix.
func hexID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}
