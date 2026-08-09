package ui

// Terminal drawer: a collapsible panel of plain non-agent shells (dev servers,
// log tails, scratch commands) scoped to the selected repo/worktree. See
// internal/shell.
//
// In the dual layout it splits the right column — preview on top, terminal
// below; the session list is untouched. In single/stacked layouts it falls
// back to a full-width band at the bottom.
//
// It renders as a bordered panel in fleet's panel vocabulary (accent border =
// focused), with tabs inset in the top border. It is always-typing — there is
// no menu mode: keystrokes go straight to the active shell, and a small set of
// Ctrl chords drive the chrome (terminal-native, single-step). Esc passes
// through to the shell (vim/less).
//
//	`  (sidebar)   → open + start typing immediately (auto-creates a shell if none)
//	   TYPING      → keys → shell. Chords reserved by fleet (never sent on):
//	                 ⌃T new shell · ⌃W close (twice if running) ·
//	                 ⌃PgUp/⌃PgDn switch tab · ⌃G full-screen attach · ` close.
//	                 An exited shell restarts on ⏎.
//
// The body is a live virtual-terminal emulator (internal/vterm) fed by a tmux
// control-mode reader that streams the active shell's pane %output — byte- and
// cursor-accurate, event-driven (no capture-pane polling). The reader sizes the
// pane to the drawer so wrap points match; rendering happens on the View thread.

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/shell"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/vterm"
)

type drawerMode int

const (
	drawerHidden drawerMode = iota // not open
	drawerTyping                   // open, keystrokes → active shell
)

const (
	drawerMaxBodyRows    = 14                     // body cap when open (also clamped so panels keep ≥3 rows)
	drawerMinBodyRows    = 3                      // never smaller than this when open
	drawerMinPreviewRows = 5                      // in the dual split, leave the preview at least this many rows
	drawerMinTermRows    = 12                     // refuse to open the drawer below this total terminal height
	drawerSlideStep      = 0.2                    // slide progress per animation frame
	drawerSyncInterval   = 120 * time.Millisecond // backstop: re-attach/resize the live stream while open
)

// drawerVisible reports whether the drawer should be drawn (open or sliding).
func (h *Home) drawerVisible() bool {
	return h.drawerMode != drawerHidden || h.drawerProgress > 0.001
}

// drawerHasFocus reports whether the drawer owns the keyboard.
func (h *Home) drawerHasFocus() bool {
	return h.drawerMode == drawerTyping
}

// --- messages ---

type (
	drawerAnimTickMsg    struct{}
	drawerTypeTickMsg    struct{}
	shellOutputMsg       struct{} // the active shell's reader pushed new bytes; coalesced render wake
	shellCreateResultMsg struct {
		sh  *shell.Shell
		err error
	}
	shellRestartMsg struct {
		id       string
		tmuxName string
		err      error
	}
	// shellStreamReadyMsg carries the result of an async stream attach (the fork of
	// tmux -C + PTY happens off the Update goroutine). The handler installs it if
	// the drawer still wants that shell, else closes the reader.
	shellStreamReadyMsg struct {
		target string
		w, h   int
		term   *vterm.Terminal
		reader *tmux.OutputReader
		err    error
	}
	// shellReseedMsg carries a capture-pane re-seed for a still-blank emulator
	// (control mode never replays a static screen). Captured off the Update
	// goroutine; applied only if the emulator is still blank for the same target.
	shellReseedMsg struct {
		target string
		seed   []byte
	}
)

// --- slide animation ---

func (h *Home) drawerAnimTick() tea.Cmd {
	return tea.Tick(22*time.Millisecond, func(time.Time) tea.Msg { return drawerAnimTickMsg{} })
}

// drawerStep advances the slide one frame; returns true while still animating.
func (h *Home) drawerStep() bool {
	switch {
	case h.drawerProgress < h.drawerTarget:
		h.drawerProgress += drawerSlideStep
		if h.drawerProgress >= h.drawerTarget {
			h.drawerProgress = h.drawerTarget
			return false
		}
		return true
	case h.drawerProgress > h.drawerTarget:
		h.drawerProgress -= drawerSlideStep
		if h.drawerProgress <= h.drawerTarget {
			h.drawerProgress = h.drawerTarget
			return false
		}
		return true
	}
	return false
}

func (h *Home) drawerTypeTick() tea.Cmd {
	return tea.Tick(drawerSyncInterval, func(time.Time) tea.Msg { return drawerTypeTickMsg{} })
}

func easeInOut(t float64) float64 {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return t * t * (3 - 2*t)
}

// --- mode transitions ---

// openDrawerTyping is the `-from-sidebar action: open the drawer and start
// typing immediately. Auto-creates a shell if the scope repo has none, so
// typing always has a target. No-op while in split focus mode.
func (h *Home) openDrawerTyping() tea.Cmd {
	if h.focusMode {
		return nil
	}
	if h.drawerMode == drawerHidden && h.height < drawerMinTermRows {
		h.setError(fmt.Errorf("terminal too short for the drawer — resize taller"))
		return nil
	}
	h.cfg.NoteFeatureUsed(tipDrawerID, tipLearnedThreshold) // retire the discovery tip once they know it
	first := h.drawerMode == drawerHidden
	if first {
		h.drawerRepo = h.resolveCurrentRepo()
		h.drawerActiveTab = 0
		h.actionLog.Add("open terminal drawer", h.drawerRepo, true)
	}
	h.drawerMode = drawerTyping
	h.drawerCloseArmed = false
	h.drawerTarget = 1
	// The anim tick self-terminates when the slide settles, so it's fine to start
	// per open. The type tick is a permanent 120ms loop, so start at most one —
	// otherwise fast open→close→open toggling accumulates leaked loops (the old
	// loop is still pending when the reopen would start another).
	cmds := []tea.Cmd{h.drawerAnimTick()}
	if !h.drawerTickRunning {
		h.drawerTickRunning = true
		cmds = append(cmds, h.drawerTypeTick())
	}
	if h.activeShell() == nil && first {
		cmds = append(cmds, h.createShell("")) // nothing to type into yet → make one
	}
	// The live stream attaches on the first anim/type tick (once renderDrawer has
	// recorded the body size).
	return tea.Batch(cmds...)
}

// closeDrawer slides the drawer shut and returns focus to the sidebar.
func (h *Home) closeDrawer() tea.Cmd {
	h.drawerMode = drawerHidden
	h.drawerCloseArmed = false
	h.drawerTarget = 0
	h.teardownShellStream() // detach the live reader; the slide-out still animates
	return h.drawerAnimTick()
}

// --- active shell helpers ---

func (h *Home) clampTab(n int) int {
	if n <= 0 {
		return 0
	}
	idx := h.drawerActiveTab
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}

func (h *Home) activeShell() *shell.Shell {
	return h.activeShellIn(h.shellsForActiveRepo())
}

// activeShellIn returns the active tab's shell from an already-computed slice,
// so render-path callers don't each re-filter h.shells.
func (h *Home) activeShellIn(shells []*shell.Shell) *shell.Shell {
	if len(shells) == 0 {
		return nil
	}
	return shells[h.clampTab(len(shells))]
}

func (h *Home) switchTab(delta int) tea.Cmd {
	shells := h.shellsForActiveRepo()
	n := len(shells)
	if n == 0 {
		return nil
	}
	h.drawerActiveTab = ((h.clampTab(n)+delta)%n + n) % n
	h.drawerCloseArmed = false
	h.syncShellStream() // repoint the live stream to the newly-active shell
	return nil
}

// --- live terminal stream (active shell) ---

// syncShellStream keeps the live emulator stream attached to the active shell at
// the drawer's current body size. It's called from the drawer ticks (and on tab
// switch / shell create / restart / close) and is cheap when nothing changed.
// Runs on the Update goroutine — the only writer of the stream fields. The actual
// attach (tmux -C + PTY fork) is dispatched off-thread via startShellStreamAsync.
func (h *Home) syncShellStream() {
	if h.drawerMode == drawerHidden {
		h.teardownShellStream()
		return
	}
	w, ht := h.drawerInnerW, h.drawerInnerH
	if w < 1 || ht < 1 {
		return // the drawer hasn't rendered yet, so we don't know the body size
	}
	sh := h.activeShell()
	if sh == nil {
		h.teardownShellStream()
		return
	}
	// An exited shell has no live session to attach to — drop the reader but keep
	// the emulator's last frame on screen until it's restarted or switched away.
	if sh.Status() == shell.StatusExited {
		if h.shellReader != nil {
			r := h.shellReader
			go r.Close() // off the Update goroutine
			h.shellReader = nil
		}
		h.shellStreamPending = ""
		return
	}
	target := sh.TmuxName()
	// (Re)attach when there's no live stream for this target, or the current
	// reader's loop died unexpectedly (e.g. an oversized %output line — #14).
	healthy := h.shellTerm != nil && h.shellStreamTarget == target &&
		h.shellReader != nil && !h.shellReader.Failed()
	if !healthy {
		h.startShellStreamAsync(target, w, ht)
		return
	}
	// Same shell: keep the pane + emulator sized to the drawer body.
	if cw, ch := h.shellTerm.Size(); cw != w || ch != ht {
		h.shellTerm.Resize(w, ht)
		if h.shellReader != nil {
			_ = h.shellReader.Resize(w, ht)
		}
	}
	h.maybeReseedBlank(target)
}

// maybeReseedBlank re-captures the pane into the emulator while it is still
// blank. tmux control mode never replays a static screen, so a shell whose
// prompt rendered before (or in a gap around) attach shows nothing until it next
// writes — this backstop (driven by the ~120ms drawer tick) fills it in. The
// capture runs off the Update goroutine; the result is applied in the
// shellReseedMsg handler, which re-checks blank + target before writing.
func (h *Home) maybeReseedBlank(target string) {
	if h.shellReseedPending || h.shellTerm == nil {
		return
	}
	if strings.TrimSpace(h.shellTerm.Render()) != "" {
		return // already has content (live stream or a prior seed)
	}
	h.shellReseedPending = true
	go func() {
		h.send(shellReseedMsg{target: target, seed: drawerSeedBytes(tmux.CapturePaneANSI(target))})
	}()
}

// startShellStreamAsync builds the emulator + control reader OFF the Update
// goroutine (NewOutputReader forks `tmux -C attach` + a PTY — too heavy for
// Update) and posts shellStreamReadyMsg. A pending guard dedups dispatches while
// one is in flight for the same target; the existing stream (if any) keeps
// rendering until the new one is installed by applyShellStream.
func (h *Home) startShellStreamAsync(target string, w, ht int) {
	if h.shellStreamPending == target {
		return // already attaching to this target
	}
	h.shellStreamPending = target
	go func() {
		term := vterm.New(w, ht)
		// Seed the emulator with the pane's current screen BEFORE attaching the
		// live reader — control mode replays nothing on attach, so otherwise the
		// drawer shows a blank body (no prompt) until the next output. Done here
		// (before the reader's goroutine starts) so there's no write race on term.
		if seed := drawerSeedBytes(tmux.CapturePaneANSI(target)); len(seed) > 0 {
			term.Write(seed)
		}
		reader, err := tmux.NewOutputReader(target, w, ht, func(b []byte) {
			term.Write(b)
			if h.shellWake.CompareAndSwap(false, true) {
				h.send(shellOutputMsg{})
			}
		})
		h.send(shellStreamReadyMsg{target: target, w: w, h: ht, term: term, reader: reader, err: err})
	}()
}

// drawerSeedBytes prepares capture-pane output for writing into a fresh emulator:
// trailing blank lines are dropped (else the prompt scrolls off the top of a
// short drawer), and bare "\n" becomes "\r\n" so lines don't stairstep (the
// emulator treats "\n" as line-feed only, no carriage return).
func drawerSeedBytes(capture []byte) []byte {
	if len(capture) == 0 {
		return nil
	}
	lines := strings.Split(string(capture), "\n")
	end := len(lines)
	for end > 0 && strings.TrimRight(lines[end-1], " ") == "" {
		end--
	}
	if end == 0 {
		return nil
	}
	// Clear + home first so a re-seed into a (possibly dirty) emulator reproduces
	// the screen from the top-left, cursor landing after the last line.
	return append([]byte("\x1b[2J\x1b[H"), []byte(strings.Join(lines[:end], "\r\n"))...)
}

// applyShellStream installs (or discards) the result of an async attach. Runs on
// the Update goroutine. It discards the reader if the attach errored or the drawer
// moved on (closed, switched shell, resized) while the fork was in flight.
func (h *Home) applyShellStream(msg shellStreamReadyMsg) {
	if h.shellStreamPending == msg.target {
		h.shellStreamPending = ""
	}
	sh := h.activeShell()
	// Install as long as the drawer still wants THIS shell. Size is deliberately
	// NOT required to match: the drawer resizes every frame as it slides open, so
	// requiring msg.w/h == current would discard the fresh reader mid-animation and
	// re-fork a later one — which then attaches after the shell's prompt has
	// rendered and (control mode never replaying) shows blank. Install now and
	// resize to the current body below.
	wanted := msg.err == nil &&
		h.drawerMode != drawerHidden &&
		sh != nil && sh.TmuxName() == msg.target && sh.Status() != shell.StatusExited
	if !wanted {
		if msg.err != nil {
			debuglog.Logger.Error("drawer: attach output reader", "target", msg.target, "err", msg.err)
		}
		if msg.reader != nil {
			go msg.reader.Close() // stale or failed — reap off the Update goroutine
		}
		return // a later sync will re-attach with the current target
	}
	h.teardownShellStream() // drop any prior stream (closes its reader off-thread)
	h.shellTerm = msg.term
	h.shellReader = msg.reader
	h.shellStreamTarget = msg.target
	// Bring the emulator + pane to the current body size if the drawer resized
	// while the fork was in flight.
	if w, ht := h.drawerInnerW, h.drawerInnerH; w > 0 && ht > 0 {
		if cw, ch := msg.term.Size(); cw != w || ch != ht {
			msg.term.Resize(w, ht)
			_ = msg.reader.Resize(w, ht)
		}
	}
}

// teardownShellStream detaches the live reader — closing it (Kill+Wait) OFF the
// Update goroutine — and drops the emulator. Use teardownShellStreamSync when the
// detach must complete first (full-screen attach).
func (h *Home) teardownShellStream() { h.dropShellStream(true) }

// teardownShellStreamSync detaches and waits for the reader to fully close.
// attachShell needs this: a full-screen takeover shares the pane and tmux sizes
// the window to the smallest client, so the drawer-sized reader must be gone
// before the attach starts. We're leaving the render loop via tea.Exec anyway, so
// a brief synchronous close here is fine.
func (h *Home) teardownShellStreamSync() { h.dropShellStream(false) }

func (h *Home) dropShellStream(async bool) {
	if h.shellReader != nil {
		r := h.shellReader
		if async {
			go r.Close()
		} else {
			r.Close()
		}
		h.shellReader = nil
	}
	h.shellTerm = nil
	h.shellStreamTarget = ""
	h.shellStreamPending = ""
	h.shellReseedPending = false
	h.shellWake.Store(false)
}

// --- lifecycle ---

// createShell spawns a new shell in the drawer's scope repo. command "" = bare $SHELL.
func (h *Home) createShell(command string) tea.Cmd {
	repo := h.drawerScopeRepo()
	if repo == "" {
		h.setError(fmt.Errorf("select a repo or worktree to open a shell"))
		return nil
	}
	name := h.nextShellName(repo, command)
	h.actionLog.Add("new shell", name, true)
	return func() tea.Msg {
		sh := shell.New(name, repo, command)
		if err := sh.Start(); err != nil {
			return shellCreateResultMsg{err: err}
		}
		return shellCreateResultMsg{sh: sh}
	}
}

// nextShellName derives a tab name: the command's first token, else
// "shell"/"shell 2"/… unique within the repo.
func (h *Home) nextShellName(repo, command string) string {
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	existing := map[string]bool{}
	for _, sh := range h.shells {
		if sh.RepoPath == repo {
			existing[sh.Name] = true
		}
	}
	if !existing["shell"] {
		return "shell"
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("shell %d", i)
		if !existing[n] {
			return n
		}
	}
}

// attachShell does a full PTY takeover of the shell's tmux session (ctrl+q
// returns), reusing the session attach path.
func (h *Home) attachShell(sh *shell.Shell) tea.Cmd {
	if sh == nil {
		return nil
	}
	// Gate on the derived status, not pane existence: Session.Start sets
	// remain-on-exit=on, so an exited shell keeps a dead pane and Tmux().Exists()
	// still returns true — attaching would drop the user into a `[dead]` pane. An
	// exited shell has no live process, so restart it in place (same as Enter).
	if sh.Status() == shell.StatusExited {
		return h.restartShell(sh)
	}
	h.isAttaching.Store(true)
	h.attachStartedAt.Store(time.Now().UnixNano())
	// Detach the drawer's control reader first — synchronously: a full-screen
	// attach is another client on the pane, and tmux sizes a shared window to its
	// smallest client, so the small drawer-sized reader must be gone before the
	// attach starts. The stream re-attaches on the next tick after Ctrl+Q returns.
	h.teardownShellStreamSync()
	h.actionLog.Add("attach shell", sh.Name, true)
	return tea.Exec(attachCmd{session: sh.Tmux()}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
		h.attachStartedAt.Store(0)
		return statusUpdateMsg{} // generic refresh; field unused by the handler
	})
}

// closeShell kills + forgets a shell. No undo (a scratch shell carries no state).
func (h *Home) closeShell(sh *shell.Shell) tea.Cmd {
	if sh == nil {
		return nil
	}
	h.workerMu.Lock()
	out := h.shells[:0]
	for _, s := range h.shells {
		if s.ID != sh.ID {
			out = append(out, s)
		}
	}
	h.shells = out
	h.workerMu.Unlock()
	if err := h.storage.DeleteShell(sh.ID); err != nil {
		debuglog.Logger.Error("storage: DeleteShell", "id", sh.ID, "err", err)
	}
	if n := len(h.shellsForActiveRepo()); h.drawerActiveTab >= n {
		h.drawerActiveTab = n - 1
		if h.drawerActiveTab < 0 {
			h.drawerActiveTab = 0
		}
	}
	// Re-point the live stream to the newly-active tab now — otherwise the killed
	// session keeps streaming and renderDrawer shows a blank body until the next
	// ~120ms tick repoints it.
	h.syncShellStream()
	h.actionLog.Add("close shell", sh.Name, true)
	return func() tea.Msg {
		_ = sh.Kill()
		return nil
	}
}

// restartShell relaunches an exited (or any) shell in place.
func (h *Home) restartShell(sh *shell.Shell) tea.Cmd {
	if sh == nil {
		return nil
	}
	id := sh.ID
	h.actionLog.Add("restart shell", sh.Name, true)
	return func() tea.Msg {
		err := sh.Restart()
		return shellRestartMsg{id: id, tmuxName: sh.TmuxName(), err: err}
	}
}

// --- key handling (drawer-focused) ---

// handleTypingKey is the drawer's only key handler: it forwards keystrokes to
// the active shell, reserving a small set of Ctrl chords for the chrome (new /
// close / switch / attach) plus ` to close. There is no menu mode — Esc passes
// through to the shell.
func (h *Home) handleTypingKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Chrome is matched on the US-QWERTY position, so a non-Latin layout can
	// close the drawer it just opened — Russian 'ё' sits on the backtick key, and
	// without this it would open the drawer and then be typed into the shell,
	// leaving no way back out. Only the chrome comparison is remapped: everything
	// forwarded below stays literal, because this is a terminal and what you type
	// has to arrive as typed. Cost: that one letter reaches the shell only via
	// Ctrl+G full attach, which intercepts nothing — the same trade already made
	// for Ctrl+T and Ctrl+W.
	switch normalizeKey(msg).String() {
	case "`":
		return h, h.closeDrawer()
	case "ctrl+t":
		return h, h.createShell("") // becomes the active tab via shellCreateResultMsg
	case "pgup", "ctrl+pgup":
		return h, h.switchTab(-1)
	case "pgdown", "ctrl+pgdown":
		return h, h.switchTab(1)
	case "ctrl+g":
		return h, h.attachShell(h.activeShell())
	}

	sh := h.activeShell()
	if sh == nil {
		return h, nil // no shell to type into (e.g. just closed the last one)
	}

	// ⌃W closes the active shell — twice to confirm a running one.
	if msg.String() == "ctrl+w" {
		if sh.Status() == shell.StatusRunning && !h.drawerCloseArmed {
			h.drawerCloseArmed = true
			return h, nil
		}
		h.drawerCloseArmed = false
		return h, h.closeShell(sh)
	}
	if h.drawerCloseArmed {
		h.drawerCloseArmed = false // any other key disarms the close confirmation
	}

	// An exited shell has no live process to type to — ⏎ restarts it in place.
	if sh.Status() == shell.StatusExited {
		if msg.Code == tea.KeyEnter {
			return h, h.restartShell(sh)
		}
		return h, nil
	}

	cc := h.getControlClient()
	if cc == nil {
		h.setError(fmt.Errorf("type-mode unavailable — Ctrl+G to attach"))
		return h, nil
	}
	forwardKeyToPane(cc, sh.TmuxName(), msg)
	return h, nil // the echoed output streams back via the live reader
}

// forwardKeyToPane sends a keypress to a tmux pane via the control client.
// Plain Ctrl chords (⌃A/⌃E/⌃R/⌃K/⌃C/…) translate to tmux "C-x" so the shell's
// own line-editing keeps working; the drawer's reserved chords are intercepted
// before this is ever reached.
func forwardKeyToPane(cc *tmux.ControlClient, target string, msg tea.KeyPressMsg) {
	switch msg.Code {
	case tea.KeyEnter:
		cc.SendKeys(target, "Enter")
	case tea.KeyBackspace:
		cc.SendKeys(target, "BSpace")
	case tea.KeyTab:
		cc.SendKeys(target, "Tab")
	case tea.KeySpace:
		cc.SendKeys(target, "Space")
	case tea.KeyEsc:
		cc.SendKeys(target, "Escape")
	case tea.KeyUp:
		cc.SendKeys(target, "Up")
	case tea.KeyDown:
		cc.SendKeys(target, "Down")
	case tea.KeyLeft:
		cc.SendKeys(target, "Left")
	case tea.KeyRight:
		cc.SendKeys(target, "Right")
	case tea.KeyHome:
		cc.SendKeys(target, "Home")
	case tea.KeyEnd:
		cc.SendKeys(target, "End")
	case tea.KeyDelete:
		cc.SendKeys(target, "DC")
	case tea.KeyPgUp:
		cc.SendKeys(target, "PageUp")
	case tea.KeyPgDown:
		cc.SendKeys(target, "PageDown")
	default:
		// Plain Ctrl chords → tmux "C-x"; printable text passes through literally.
		if c, ok := ctrlChord(msg.String()); ok {
			cc.SendKeys(target, c)
			return
		}
		if msg.Text != "" {
			cc.SendLiteralKeys(target, msg.Text)
		}
	}
}

// ctrlChord maps a single-letter Ctrl combo ("ctrl+k") to its tmux key name
// ("C-k"). Returns ok=false for anything else.
func ctrlChord(s string) (string, bool) {
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok && len(rest) == 1 {
		return "C-" + rest, true
	}
	return "", false
}

// --- rendering ---

// renderDrawer draws the drawer as a bordered panel at its current slide
// height. maxOuterH caps the panel's total rows (border included) so it leaves
// room for whatever shares its space — the preview, in the dual split.
func (h *Home) renderDrawer(width, maxOuterH int) string {
	shells := h.shellsForActiveRepo()

	innerWidth := width - 2 // panel borders
	if innerWidth < 1 {
		innerWidth = 1
	}
	// Stable terminal viewport: a real terminal has a fixed size, so the body is
	// the available rows (clamped), not content-fit. Recorded for syncShellStream,
	// which sizes the reader + emulator to match so wrap points line up.
	dispRows := maxOuterH - 2
	if dispRows > drawerMaxBodyRows {
		dispRows = drawerMaxBodyRows
	}
	if h.drawerHeight > 0 && dispRows > h.drawerHeight {
		dispRows = h.drawerHeight // honor the drawer_height config cap
	}
	if dispRows < drawerMinBodyRows {
		dispRows = drawerMinBodyRows
	}
	h.drawerInnerW = innerWidth
	h.drawerInnerH = dispRows

	// Body: the live emulator screen for the active shell, a CTA when the scope
	// repo has no shells, or blank for a frame while the stream attaches.
	var raw []string
	cursorX, cursorY := -1, -1
	switch {
	case len(shells) == 0:
		raw = []string{drawerEmptyCTA()}
	case h.shellTerm != nil && h.shellStreamTarget == shells[h.clampTab(len(shells))].TmuxName():
		raw = strings.Split(strings.TrimRight(stripOSC8(h.shellTerm.Render()), "\n"), "\n")
		cursorX, cursorY = h.shellTerm.Cursor()
	default:
		raw = []string{""}
	}
	// Pad to the full viewport (the emulator is dispRows tall; TrimRight dropped
	// trailing blank rows, so restore them for a stable-height box).
	for len(raw) < dispRows {
		raw = append(raw, "")
	}

	fullH := dispRows + 2 // + top/bottom border
	if fullH > maxOuterH {
		fullH = maxOuterH
	}
	if fullH < 3 {
		fullH = 3 // top border + one row + bottom border
	}

	// Animated current height: grow from a 3-row box to full as the slide opens.
	curH := fullH
	if h.drawerProgress < 1 {
		curH = 3 + int(math.Round(easeInOut(h.drawerProgress)*float64(fullH-3)))
		if curH < 3 {
			curH = 3
		}
		if curH > fullH {
			curH = fullH
		}
	}
	innerNow := curH - 2
	if innerNow < 1 {
		innerNow = 1
	}

	// While the slide is still opening, reveal the top of the viewport; the full
	// screen shows once open.
	body := raw
	if len(body) > innerNow {
		body = body[:innerNow]
	}

	// Block cursor at the emulator's real (x, y). Only overlaid once fully open —
	// the slide crops rows, so the row index would otherwise be off.
	if h.drawerMode == drawerTyping && h.drawerProgress >= 0.999 && cursorY >= 0 && cursorY < len(body) {
		cur := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
		line := body[cursorY]
		lw := lipgloss.Width(line)
		if cursorX < 0 || cursorX >= lw {
			pad := cursorX - lw
			if pad < 0 {
				pad = 0
			}
			body[cursorY] = line + strings.Repeat(" ", pad) + cur.Render(" ")
		} else {
			// The grapheme under the cursor may be double-width (CJK/emoji/wide box
			// drawing). A 1-cell cut of a wide glyph returns "" (it can't fit), which
			// would blank the cursor and leave the glyph in the right-hand remainder
			// — garbling the row. Detect the wide case and slice by the full width.
			cellW := 1
			if ansi.Strip(ansi.Cut(line, cursorX, cursorX+1)) == "" &&
				ansi.StringWidth(ansi.Cut(line, cursorX, cursorX+2)) == 2 {
				cellW = 2
			}
			cell := ansi.Strip(ansi.Cut(line, cursorX, cursorX+cellW))
			if cell == "" {
				cell = " "
			}
			body[cursorY] = ansi.Truncate(line, cursorX, "") + cur.Render(cell) + ansi.TruncateLeft(line, cursorX+cellW, "")
		}
	}

	return RenderBorderedPanelFull(
		strings.Join(body, "\n"),
		h.drawerTitle(shells),
		h.drawerModeLabel(shells),
		h.drawerCwdLabel(),
		width, curH, true, /* accent: drawer is focused while visible */
	)
}

// drawerTitle is the top-border-left: the tab chips. Takes the active-repo shell
// slice (computed once per render by renderDrawer) to avoid re-filtering h.shells.
// The active tab renders as a filled accent pill — the same selected-row
// vocabulary as the sidebar — so the focused shell reads at a glance.
func (h *Home) drawerTitle(shells []*shell.Shell) string {
	active := h.clampTab(len(shells))
	selStyle := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 1)
	parts := []string{lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Terminal")}
	for i, sh := range shells {
		name := truncCmd(sh.DisplayName(), drawerTabNameMax)
		if sh.Status() == shell.StatusExited {
			if c := sh.ExitInfo(); c != "" {
				name += "(" + c + ")"
			}
		}
		if i == active {
			parts = append(parts, drawerDot(sh.Status())+selStyle.Render(name))
		} else {
			parts = append(parts, drawerDot(sh.Status())+" "+lipgloss.NewStyle().Foreground(ColorTextDim).Render(name))
		}
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(ColorTextDim).Render("+"))
	return strings.Join(parts, "  ")
}

// drawerTabNameMax caps a shell tab/chip label — command lines can be long, so
// they're truncated to keep the tab bar and collapsed border readable.
const drawerTabNameMax = 22

// truncCmd shortens a command-line label to max runes, appending an ellipsis.
func truncCmd(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// drawerModeLabel is the loud top-border-right indicator: the live target, or
// a close confirmation for a running shell. Takes the active-repo shell slice to
// avoid re-filtering h.shells (renderDrawer computes it once).
func (h *Home) drawerModeLabel(shells []*shell.Shell) string {
	name := "shell"
	if sh := h.activeShellIn(shells); sh != nil {
		name = truncCmd(sh.DisplayName(), drawerTabNameMax)
	}
	if h.drawerCloseArmed {
		return lipgloss.NewStyle().Foreground(ColorYellow).Bold(true).Render("kill " + name + "? ⌃W again")
	}
	return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("● TYPING → " + name)
}

// drawerCwdLabel is the bottom-border-right cwd hint.
func (h *Home) drawerCwdLabel() string {
	return lipgloss.NewStyle().Foreground(ColorTextDim).Render(shortenPath(h.drawerScopeRepo()))
}

// collapsedShellMax caps how many shell chips ride a panel's bottom border while
// the drawer is closed — a glance-able summary, not the whole list.
const collapsedShellMax = 3

// collapsedShellChips renders the selected repo's shells as a compact
// dot + name summary for a panel's bottom border, shown while the drawer is
// closed. Returns "" when the drawer is open/sliding (the drawer itself shows
// the shells) or the scope repo has no shells. Capped at collapsedShellMax with
// a "+N" overflow marker.
func (h *Home) collapsedShellChips() string {
	if h.drawerVisible() {
		return ""
	}
	shells := h.shellsForActiveRepo()
	if len(shells) == 0 {
		return ""
	}
	nameStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	n := len(shells)
	shown := n
	if shown > collapsedShellMax {
		shown = collapsedShellMax
	}
	parts := make([]string, 0, shown+1)
	for _, sh := range shells[:shown] {
		parts = append(parts, drawerDot(sh.Status())+" "+nameStyle.Render(truncCmd(sh.DisplayName(), drawerTabNameMax)))
	}
	if n > shown {
		parts = append(parts, nameStyle.Render(fmt.Sprintf("+%d", n-shown)))
	}
	return strings.Join(parts, "  ")
}

func drawerEmptyCTA() string {
	return lipgloss.NewStyle().Foreground(ColorTextDim).Render("  starting a shell here…")
}

func drawerDot(st shell.Status) string {
	switch st {
	case shell.StatusRunning:
		return StatusRunningStyle.Render("●")
	case shell.StatusExited:
		return StatusErrorStyle.Render("✕")
	default:
		return StatusIdleStyle.Render("○")
	}
}
