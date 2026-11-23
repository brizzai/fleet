package deliver

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/session"
)

// fakePane scripts what the pane shows. fresh is consumed one read at a time,
// the last entry repeating, so a test can model a message landing late.
type fakePane struct {
	exists, dead bool
	screen       string
	fresh        []string
	pasteErr     error
	pasted       []string
}

func (p *fakePane) Exists() bool                 { return p.exists }
func (p *fakePane) IsPaneDead() bool             { return p.dead }
func (p *fakePane) CapturePane() (string, error) { return p.screen, nil }
func (p *fakePane) CapturePaneFresh() (string, error) {
	if len(p.fresh) == 0 {
		return "", nil
	}
	out := p.fresh[0]
	if len(p.fresh) > 1 {
		p.fresh = p.fresh[1:]
	}
	return out, nil
}
func (p *fakePane) PasteAndSubmit(text string) error {
	if p.pasteErr != nil {
		return p.pasteErr
	}
	p.pasted = append(p.pasted, text)
	return nil
}

// world is one scenario: the session, its pane, and what the side effects saw.
type world struct {
	s        *session.Session
	pane     *fakePane
	paneCmds []string // successive foreground commands, last repeating
	accErr   error
	wakeErr  error
	woke     int
	wakeMsg  string
}

func newWorld(ag agent.Type, status session.Status) *world {
	return &world{
		s: &session.Session{
			ID: "1373c4f5-1792494994", Title: "fix-242", Agent: ag, Status: status,
			TmuxSessionName: "fleet_old", ClaudeSessionID: "conv-1",
		},
		pane:     &fakePane{exists: true, fresh: []string{"> run the tests please"}},
		paneCmds: []string{"claude"},
	}
}

func (w *world) deps() Deps {
	return Deps{
		Pane: func(*session.Session) Pane { return w.pane },
		PaneCommand: func(string) string {
			out := w.paneCmds[0]
			if len(w.paneCmds) > 1 {
				w.paneCmds = w.paneCmds[1:]
			}
			return out
		},
		AccountGone: func(*session.Session) error { return w.accErr },
		Wake: func(s *session.Session) error {
			w.woke++
			w.wakeMsg = s.InitialPrompt
			if w.wakeErr != nil {
				return w.wakeErr
			}
			s.TmuxSessionName = "fleet_new"
			s.SetStatus(session.StatusStarting)
			return nil
		},
	}
}

var fast = Options{
	StartupGrace: 30 * time.Millisecond, StartupPoll: time.Millisecond,
	DeliveryGrace: 30 * time.Millisecond, DeliveryPoll: time.Millisecond,
}

func (w *world) run(t *testing.T, msg string, opts Options) (Result, error) {
	t.Helper()
	opts.StartupGrace, opts.StartupPoll = fast.StartupGrace, fast.StartupPoll
	opts.DeliveryGrace, opts.DeliveryPoll = fast.DeliveryGrace, fast.DeliveryPoll
	return Deliver(context.Background(), w.deps(), w.s, msg, opts)
}

func waitingScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../session/testdata/copilot/permission.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const msg = "run the tests please"

// The guards' order is the contract: each one is checked only once every
// earlier one has passed, so the refusal names the most fundamental problem.
func TestRefusalOrdering(t *testing.T) {
	gone := &Refusal{Reason: ErrAccountGone}
	cases := []struct {
		name  string
		setup func(w *world)
		opts  Options
		want  error
	}{
		{"suspended on a removed account is refused before any wake", func(w *world) {
			w.s.Status = session.StatusSuspended
			w.accErr = gone
			w.pane.exists = false
		}, Options{}, ErrAccountGone},
		{"suspended wake failure", func(w *world) {
			w.s.Status = session.StatusSuspended
			w.wakeErr = errors.New("tmux new-session: boom")
		}, Options{}, ErrSuspendedWakeFailed},
		{"no tmux beats a dead pane and a shell", func(w *world) {
			w.pane.exists, w.pane.dead = false, true
			w.paneCmds = []string{"zsh"}
		}, Options{}, ErrNoTmux},
		{"dead pane beats a shell", func(w *world) {
			w.pane.dead = true
			w.paneCmds = []string{"zsh"}
		}, Options{}, ErrPaneDead},
		{"shell prompt beats running and waiting", func(w *world) {
			w.paneCmds = []string{"zsh"}
			w.s.Status = session.StatusRunning
			w.pane.screen = waitingScreen(t)
		}, Options{RefuseRunning: true}, ErrAtShellPrompt},
		{"shell prompt is never forceable", func(w *world) {
			w.paneCmds = []string{"zsh"}
		}, Options{Force: true}, ErrAtShellPrompt},
		{"running beats waiting when refused", func(w *world) {
			w.s.Status = session.StatusRunning
			w.pane.screen = waitingScreen(t)
		}, Options{RefuseRunning: true, Force: true}, ErrAgentRunning},
		{"waiting prompt", func(w *world) {
			w.pane.screen = waitingScreen(t)
		}, Options{}, ErrWaitingPrompt},
		{"paste failure", func(w *world) {
			w.pane.pasteErr = errors.New("load-buffer failed")
		}, Options{}, ErrPasteFailed},
		{"message never shows up", func(w *world) {
			w.pane.fresh = []string{"✻ Welcome to Claude Code"}
		}, Options{}, ErrNotConfirmed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(agent.Copilot, session.StatusIdle)
			c.setup(w)
			_, err := w.run(t, msg, c.opts)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("err %T is not a *Refusal", err)
			}
			if r.Forceable() != (c.want == ErrWaitingPrompt) {
				t.Errorf("Forceable = %v", r.Forceable())
			}
			if r.Short() == "" || strings.Contains(r.Short(), "fleet restart") || strings.Contains(r.Short(), "-force") {
				t.Errorf("Short() = %q, want a TUI line without CLI commands", r.Short())
			}
			// Nothing that refused before the paste may have typed anything.
			if c.want != ErrNotConfirmed && len(w.pane.pasted) != 0 {
				t.Errorf("pasted %q despite refusing", w.pane.pasted)
			}
		})
	}
}

func TestWakeSuspended(t *testing.T) {
	w := newWorld(agent.Claude, session.StatusSuspended)
	w.pane.exists = false // its tmux is gone; a wake must not look for it
	res, err := w.run(t, msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Woke || res.Verified || w.woke != 1 {
		t.Errorf("res = %+v, woke %d times", res, w.woke)
	}
	if w.wakeMsg != msg {
		t.Errorf("wake launched with InitialPrompt %q, want the message", w.wakeMsg)
	}
	if res.PrevTmuxSession != "fleet_old" || res.PrevClaudeSessionID != "conv-1" {
		t.Errorf("prev = %q/%q", res.PrevTmuxSession, res.PrevClaudeSessionID)
	}
	if len(w.pane.pasted) != 0 {
		t.Errorf("a wake pasted %q; the message rides the launch", w.pane.pasted)
	}
}

// A failed wake must not leave the message armed for a later, unrelated
// relaunch: the TUI's r would otherwise submit it with nobody asking.
func TestFailedWakeDisarmsThePrompt(t *testing.T) {
	cause := errors.New("boom")
	w := newWorld(agent.Claude, session.StatusSuspended)
	w.wakeErr = cause
	_, err := w.run(t, msg, Options{})
	if !errors.Is(err, ErrSuspendedWakeFailed) || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the reason and its cause", err)
	}
	if w.s.InitialPrompt != "" {
		t.Errorf("InitialPrompt = %q after a failed wake", w.s.InitialPrompt)
	}
}

func TestDelivered(t *testing.T) {
	t.Run("verified once the message shows up, even late", func(t *testing.T) {
		w := newWorld(agent.Claude, session.StatusIdle)
		w.pane.fresh = []string{"", "starting…", "> run   the\n  tests please"}
		res, err := w.run(t, msg, Options{})
		if err != nil || !res.Verified || res.Woke {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if len(w.pane.pasted) != 1 || w.pane.pasted[0] != msg {
			t.Errorf("pasted %q, want the message exactly once", w.pane.pasted)
		}
	})
	t.Run("a collapsed multi-line paste counts", func(t *testing.T) {
		w := newWorld(agent.Claude, session.StatusIdle)
		w.pane.fresh = []string{"> [Pasted text #1 +4 lines]"}
		if res, err := w.run(t, "line one of many\nline two\n", Options{}); err != nil || !res.Verified {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("too short to identify is sent unverified", func(t *testing.T) {
		w := newWorld(agent.Claude, session.StatusIdle)
		w.pane.fresh = []string{"nothing"}
		if res, err := w.run(t, "yes", Options{}); err != nil || res.Verified {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("a running agent is typed into unless refused", func(t *testing.T) {
		w := newWorld(agent.Claude, session.StatusRunning)
		if _, err := w.run(t, msg, Options{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("force sends past a waiting prompt", func(t *testing.T) {
		w := newWorld(agent.Copilot, session.StatusWaiting)
		w.pane.screen = waitingScreen(t)
		if _, err := w.run(t, msg, Options{Force: true}); err != nil {
			t.Fatal(err)
		}
	})
	// Measured start times run to 7.4s; reading the pane once misreported a
	// session still sourcing its rc as one whose agent had exited.
	t.Run("a pane still at its shell during startup is waited for", func(t *testing.T) {
		w := newWorld(agent.Claude, session.StatusStarting)
		w.paneCmds = []string{"zsh", "zsh", "", "claude"}
		if _, err := w.run(t, msg, Options{}); err != nil {
			t.Fatal(err)
		}
	})
}

// OpenCode has no prompt detector, so the waiting guard can't run; the caller
// must be told rather than handed a guarantee it doesn't have.
func TestPromptUndetectable(t *testing.T) {
	w := newWorld(agent.OpenCode, session.StatusIdle)
	if res, err := w.run(t, msg, Options{}); err != nil || !res.PromptUndetectable {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	w = newWorld(agent.OpenCode, session.StatusIdle)
	if res, err := w.run(t, msg, Options{Force: true}); err != nil || res.PromptUndetectable {
		t.Fatalf("with force: res = %+v, err = %v", res, err)
	}
	// Reported on a failure after the check too, since the paste may have landed.
	w = newWorld(agent.OpenCode, session.StatusIdle)
	w.pane.fresh = []string{"nothing"}
	if res, err := w.run(t, msg, Options{}); !errors.Is(err, ErrNotConfirmed) || !res.PromptUndetectable {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestContextCancelStopsTheStartupWait(t *testing.T) {
	w := newWorld(agent.Claude, session.StatusIdle)
	w.paneCmds = []string{"zsh"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Deliver(ctx, w.deps(), w.s, msg, Options{StartupGrace: time.Hour, StartupPoll: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// `fleet send` prints Error() verbatim, so its wording is interface.
func TestRefusalErrorTextIsFleetSends(t *testing.T) {
	base := Refusal{Title: "fix-242", ID: "1373c4f5-1792494994", Agent: agent.Claude}
	with := func(f func(r *Refusal)) *Refusal { r := base; f(&r); return &r }
	cases := []struct {
		r    *Refusal
		want string
	}{
		{with(func(r *Refusal) { r.Reason, r.Account = ErrAccountGone, "gone@example.com" }),
			`session "fix-242" is pinned to Claude account gone@example.com, which is no longer configured — relaunching it would run on your default login instead. In the fleet TUI, press r on it (fleet moves it to a usable account) or use . → Move to Account…`},
		{with(func(r *Refusal) { r.Reason, r.Cause = ErrSuspendedWakeFailed, errors.New("boom") }),
			`failed to wake suspended session "fix-242": boom`},
		{with(func(r *Refusal) { r.Reason, r.Status = ErrNoTmux, "error" }),
			`session "fix-242" has no live tmux session (status error) — restart it with fleet restart 1373c4f5 (or r in the TUI)`},
		{with(func(r *Refusal) { r.Reason = ErrPaneDead }),
			`the Claude process in session "fix-242" has exited — restart it with fleet restart 1373c4f5 (or r in the TUI)`},
		{with(func(r *Refusal) { r.Reason, r.PaneCmd = ErrAtShellPrompt, "zsh" }),
			"the Claude process in session \"fix-242\" has exited — its pane is back at a zsh prompt, where the message would run as a shell command.\nRestart it with fleet restart 1373c4f5 (or r in the TUI)"},
		{with(func(r *Refusal) { r.Reason = ErrWaitingPrompt }),
			"session \"fix-242\" is waiting on a prompt — a message typed into a menu is discarded and its Enter confirms the highlighted option.\nAnswer it first (attach, or Y to approve), or pass -force to send anyway"},
		{with(func(r *Refusal) { r.Reason, r.Cause = ErrPasteFailed, errors.New("boom") }),
			`failed to send to session "fix-242": boom`},
		{with(func(r *Refusal) { r.Reason = ErrNotConfirmed }),
			"the message was not delivered — \"fix-242\" was still starting and its input wasn't ready yet.\nNothing reached the agent; try again in a moment"},
	}
	for _, c := range cases {
		if got := c.r.Error(); got != c.want {
			t.Errorf("%v:\n got %q\nwant %q", c.r.Reason, got, c.want)
		}
	}
}

// A relaunch on a removed account falls back to the ambient login, billing a
// different subscription with nothing on screen saying so.
func TestAccountGoneErr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AccountGoneErr(agent.Claude, "gone@example.com", "t"); err != nil {
		t.Errorf("no accounts configured: %v", err)
	}
	store := claudeaccount.Load()
	store.Upsert(claudeaccount.Account{Email: "work@example.com", ConfigDir: t.TempDir()})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := AccountGoneErr(agent.Claude, "gone@example.com", "t"); !errors.Is(err, ErrAccountGone) {
		t.Errorf("removed account: err = %v, want ErrAccountGone", err)
	}
	for _, c := range []struct {
		ag      agent.Type
		account string
	}{{agent.Claude, "work@example.com"}, {agent.Claude, ""}, {agent.Codex, "gone@example.com"}} {
		if err := AccountGoneErr(c.ag, c.account, "t"); err != nil {
			t.Errorf("%s/%q: %v", c.ag, c.account, err)
		}
	}
}

type fakeStore struct{ tmux, status, cid []string }

func (f *fakeStore) UpdateTmuxSession(_, v string) error     { f.tmux = append(f.tmux, v); return nil }
func (f *fakeStore) UpdateStatus(_, v string) error          { f.status = append(f.status, v); return nil }
func (f *fakeStore) UpdateClaudeSessionID(_, v string) error { f.cid = append(f.cid, v); return nil }

// A one-shot process that wakes a session must persist the new tmux session,
// the status, and any conversation id the relaunch healed — the TUI's worker
// isn't there to do it afterwards.
func TestPersistRelaunch(t *testing.T) {
	s := &session.Session{ID: "x", TmuxSessionName: "fleet_new", Status: session.StatusStarting, ClaudeSessionID: "healed"}
	st := &fakeStore{}
	PersistRelaunch(st, "x", "fleet_old", "stale", s)
	if len(st.tmux) != 1 || st.tmux[0] != "fleet_new" || len(st.status) != 1 || st.status[0] != "starting" ||
		len(st.cid) != 1 || st.cid[0] != "healed" {
		t.Errorf("persisted %+v", st)
	}
	st = &fakeStore{}
	PersistRelaunch(st, "x", "fleet_new", "healed", s)
	if len(st.tmux) != 0 || len(st.cid) != 0 || len(st.status) != 1 {
		t.Errorf("unchanged fields rewritten: %+v", st)
	}
}

func TestDeliveryNeedle(t *testing.T) {
	t.Run("uses the first line only", func(t *testing.T) {
		if got := DeliveryNeedle("run the tests\nthen push"); got != "runthetests" {
			t.Errorf("got %q", got)
		}
	})

	// The pane wraps long lines and indents continuations, so the message's own
	// spacing is not what lands on screen.
	t.Run("squashes whitespace so wrapping can't break the match", func(t *testing.T) {
		if got := DeliveryNeedle("run   the\ttests"); got != "runthetests" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("caps the length", func(t *testing.T) {
		got := DeliveryNeedle(strings.Repeat("a", 100))
		if len([]rune(got)) != 24 {
			t.Errorf("got %d runes, want 24", len([]rune(got)))
		}
	})

	// Too short to identify: "y" would match almost any pane, and a coincidental
	// hit reported as delivery is worse than not checking.
	t.Run("declines a message too short to identify", func(t *testing.T) {
		for _, msg := range []string{"y", "ok", "", "   ", "yes!"} {
			if got := DeliveryNeedle(msg); got != "" {
				t.Errorf("message %q: got needle %q, want none", msg, got)
			}
		}
	})
}
