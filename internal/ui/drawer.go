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
	// tmux -C + PTY happens off the Update goroutine). The handler installs it only
	// if the requested target + size are still current, else closes the reader.
	shellStreamReadyMsg struct {
		target string
		w, h   int
		term   *vterm.Terminal
		reader *tmux.OutputReader
		err    error
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
	first := h.drawerMode == drawerHidden
	if first {
		h.drawerRepo = h.resolveCurrentRepo()
		h.drawerActiveTab = 0
		h.actionLog.Add("open terminal drawer", h.drawerRepo, true)
	}
	h.drawerMode = drawerTyping
	h.drawerCloseArmed = false
	h.drawerTarget = 1
	cmds := []tea.Cmd{h.drawerAnimTick(), h.drawerTypeTick()}
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
	shells := h.shellsForActiveRepo()
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
		reader, err := tmux.NewOutputReader(target, w, ht, func(b []byte) {
			term.Write(b)
			if h.shellWake.CompareAndSwap(false, true) {
				h.send(shellOutputMsg{})
			}
		})
		h.send(shellStreamReadyMsg{target: target, w: w, h: ht, term: term, reader: reader, err: err})
	}()
}

// applyShellStream installs (or discards) the result of an async attach. Runs on
// the Update goroutine. It discards the reader if the attach errored or the drawer
// moved on (closed, switched shell, resized) while the fork was in flight.
func (h *Home) applyShellStream(msg shellStreamReadyMsg) {
	if h.shellStreamPending == msg.target {
		h.shellStreamPending = ""
	}
	sh := h.activeShell()
	wanted := msg.err == nil &&
		h.drawerMode != drawerHidden &&
		sh != nil && sh.TmuxName() == msg.target && sh.Status() != shell.StatusExited &&
		msg.w == h.drawerInnerW && msg.h == h.drawerInnerH
	if !wanted {
		if msg.err != nil {
			debuglog.Logger.Error("drawer: attach output reader", "target", msg.target, "err", msg.err)
		}
		if msg.reader != nil {
			go msg.reader.Close() // stale or failed — reap off the Update goroutine
		}
		return // a later sync will re-attach with the current target/size
	}
	h.teardownShellStream() // drop any prior stream (closes its reader off-thread)
	h.shellTerm = msg.term
	h.shellReader = msg.reader
	h.shellStreamTarget = msg.target
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
	if !sh.Tmux().Exists() {
		h.setError(fmt.Errorf("shell exited — press Enter to restart"))
		return nil
	}
	h.isAttaching.Store(true)
	// Detach the drawer's control reader first — synchronously: a full-screen
	// attach is another client on the pane, and tmux sizes a shared window to its
	// smallest client, so the small drawer-sized reader must be gone before the
	// attach starts. The stream re-attaches on the next tick after Ctrl+Q returns.
	h.teardownShellStreamSync()
	h.actionLog.Add("attach shell", sh.Name, true)
	return tea.Exec(attachCmd{session: sh.Tmux()}, func(err error) tea.Msg {
		h.isAttaching.Store(false)
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
	switch msg.String() {
	case "`":
		return h, h.closeDrawer()
	case "ctrl+t":
		return h, h.createShell("") // becomes the active tab via shellCreateResultMsg
	case "ctrl+pgup":
		return h, h.switchTab(-1)
	case "ctrl+pgdown":
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
			cell := ansi.Strip(ansi.Cut(line, cursorX, cursorX+1))
			if cell == "" {
				cell = " "
			}
			body[cursorY] = ansi.Truncate(line, cursorX, "") + cur.Render(cell) + ansi.TruncateLeft(line, cursorX+1, "")
		}
	}

	return RenderBorderedPanelFull(
		strings.Join(body, "\n"),
		h.drawerTitle(),
		h.drawerModeLabel(),
		h.drawerCwdLabel(),
		width, curH, true, /* accent: drawer is focused while visible */
	)
}

// drawerTitle is the top-border-left: the tab chips.
func (h *Home) drawerTitle() string {
	shells := h.shellsForActiveRepo()
	active := h.clampTab(len(shells))
	parts := []string{lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Terminal")}
	for i, sh := range shells {
		name := sh.Name
		if sh.Status() == shell.StatusExited {
			if c := sh.ExitInfo(); c != "" {
				name += "(" + c + ")"
			}
		}
		chip := drawerDot(sh.Status()) + " "
		if i == active {
			chip += lipgloss.NewStyle().Foreground(ColorText).Bold(true).Render(name)
		} else {
			chip += lipgloss.NewStyle().Foreground(ColorTextDim).Render(name)
		}
		parts = append(parts, chip)
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(ColorTextDim).Render("+"))
	return strings.Join(parts, "  ")
}

// drawerModeLabel is the loud top-border-right indicator: the live target, or
// a close confirmation for a running shell.
func (h *Home) drawerModeLabel() string {
	name := "shell"
	if sh := h.activeShell(); sh != nil {
		name = sh.Name
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
