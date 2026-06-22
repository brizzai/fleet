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
// The body is a periodic off-thread capture of the active shell's pane (never
// captured on the View thread), mirroring the preview pane.

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
)

type drawerMode int

const (
	drawerHidden drawerMode = iota // not open
	drawerTyping                   // open, keystrokes → active shell
)

const (
	drawerMaxBodyRows    = 14                    // body cap when open (also clamped so panels keep ≥3 rows)
	drawerMinBodyRows    = 3                     // never smaller than this when open
	drawerMinPreviewRows = 5                     // in the dual split, leave the preview at least this many rows
	drawerMinTermRows    = 12                    // refuse to open the drawer below this total terminal height
	drawerSlideStep      = 0.2                   // slide progress per animation frame
	drawerHotInterval    = 33 * time.Millisecond // ~30fps capture while typing / output flowing
	drawerHotWindow      = 750 * time.Millisecond
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
	drawerAnimTickMsg struct{}
	drawerTypeTickMsg struct{}
	drawerBodyMsg     struct {
		shellID, content string
		cursorX          int // tmux #{cursor_x}; -1 if unknown
	}
	shellCreateResultMsg struct {
		sh  *shell.Shell
		err error
	}
	shellRestartMsg struct {
		id       string
		tmuxName string
		err      error
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
	return tea.Tick(drawerHotInterval, func(time.Time) tea.Msg { return drawerTypeTickMsg{} })
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
	h.drawerHotUntil = time.Now().Add(drawerHotWindow)
	h.drawerTarget = 1
	cmds := []tea.Cmd{h.drawerAnimTick(), h.drawerTypeTick()}
	if sh := h.activeShell(); sh != nil {
		cmds = append(cmds, h.fetchDrawerBody(sh))
	} else if first {
		cmds = append(cmds, h.createShell("")) // nothing to type into yet → make one
	}
	return tea.Batch(cmds...)
}

// closeDrawer slides the drawer shut and returns focus to the sidebar.
func (h *Home) closeDrawer() tea.Cmd {
	h.drawerMode = drawerHidden
	h.drawerCloseArmed = false
	h.drawerTarget = 0
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
	return h.fetchDrawerBody(h.activeShell())
}

// --- body capture (off-thread) ---

func (h *Home) fetchDrawerBody(sh *shell.Shell) tea.Cmd {
	if sh == nil {
		return nil
	}
	id := sh.ID
	ts := sh.Tmux()
	return func() tea.Msg {
		content, cx, _, err := ts.CaptureWithCursor()
		if err != nil {
			return drawerBodyMsg{shellID: id, content: "", cursorX: -1}
		}
		return drawerBodyMsg{shellID: id, content: content, cursorX: cx}
	}
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
		h.setError(fmt.Errorf("shell exited — press r to restart"))
		return nil
	}
	h.isAttaching.Store(true)
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
	// Stay "hot" so the ~30fps capture keeps refreshing while you type.
	h.drawerHotUntil = time.Now().Add(drawerHotWindow)
	forwardKeyToPane(cc, sh.TmuxName(), msg)
	return h, nil // output refreshes via drawerTypeTick
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

	// Body lines from the active shell's pane capture (trailing blanks trimmed),
	// or a CTA when the scope repo has no shells.
	var raw []string
	if len(shells) == 0 {
		raw = []string{drawerEmptyCTA()}
	} else {
		active := shells[h.clampTab(len(shells))]
		content := ""
		if h.drawerBodyShell == active.ID {
			content = h.drawerBody
		}
		raw = trimTrailingBlank(strings.Split(strings.TrimRight(stripOSC8(content), "\n"), "\n"))
		if len(raw) == 0 {
			raw = []string{""}
		}
	}

	// Inner body height: fit content, clamped to [min,max].
	inner := len(raw)
	if inner < drawerMinBodyRows {
		inner = drawerMinBodyRows
	}
	if inner > drawerMaxBodyRows {
		inner = drawerMaxBodyRows
	}
	if inner < 1 {
		inner = 1
	}
	fullH := inner + 2 // + top/bottom border
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

	body := raw
	if len(body) > innerNow {
		body = body[len(body)-innerNow:]
	}
	// Block cursor at the real tmux cursor column on the prompt line — so it
	// lands correctly even when a shell autosuggestion trails the cursor (rather
	// than after it). Falls back to an end-of-line block when the column is
	// unknown or past the rendered content.
	if h.drawerMode == drawerTyping && len(body) > 0 {
		last := len(body) - 1
		line := body[last]
		cur := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
		cx := h.drawerCursorX
		if cx < 0 || cx >= lipgloss.Width(line) {
			body[last] = line + cur.Render(" ")
		} else {
			cell := ansi.Strip(ansi.Cut(line, cx, cx+1))
			if cell == "" {
				cell = " "
			}
			body[last] = ansi.Truncate(line, cx, "") + cur.Render(cell) + ansi.TruncateLeft(line, cx+1, "")
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

// trimTrailingBlank drops trailing visually-empty lines (a tmux pane capture is
// full-height and mostly blank for a fresh shell).
func trimTrailingBlank(lines []string) []string {
	i := len(lines)
	for i > 0 && strings.TrimSpace(ansi.Strip(lines[i-1])) == "" {
		i--
	}
	return lines[:i]
}
