package tmux

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/creack/pty"
)

// isOctalDigit reports whether b is an ASCII octal digit (0-7).
func isOctalDigit(b byte) bool { return b >= '0' && b <= '7' }

// decodeControlOutput reverses tmux control-mode's %output escaping. tmux
// escapes every byte < 0x20 and the backslash itself as a backslash followed by
// exactly three octal digits (e.g. ESC -> `\033`, `\` -> `\134`, CRLF ->
// `\015\012`); every other byte (including 0x7f and UTF-8 continuation bytes)
// passes through raw. A trailing backslash without three octal digits is left
// literal rather than dropped.
func decodeControlOutput(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+3 < len(s) && isOctalDigit(s[i+1]) && isOctalDigit(s[i+2]) && isOctalDigit(s[i+3]) {
			out = append(out, byte((int(s[i+1]-'0')<<6)|(int(s[i+2]-'0')<<3)|int(s[i+3]-'0')))
			i += 4
			continue
		}
		out = append(out, s[i])
		i++
	}
	return out
}

// parseControlOutput returns the decoded pane bytes if line is a control-mode
// `%output %<pane> <data>` notification, else ok=false. Notifications are
// guaranteed never to appear inside a command's %begin/%end block, so a simple
// prefix check is sufficient — command replies (which we ignore) never start
// with "%output ".
func parseControlOutput(line string) (payload []byte, ok bool) {
	const prefix = "%output "
	rest, found := strings.CutPrefix(line, prefix)
	if !found {
		return nil, false
	}
	// rest == "%<paneid> <data>"; data is everything after the first space.
	// Spaces inside data are >= 0x20 so tmux leaves them raw — split once only.
	_, data, found := strings.Cut(rest, " ")
	if !found {
		return nil, false
	}
	return decodeControlOutput(data), true
}

// OutputReader attaches a tmux control-mode client to a target session and
// streams its panes' live output (octal-decoded) to a callback until Close.
// It attaches to the *target* session with output enabled (unlike ControlClient,
// which attaches to a private hidden session and suppresses output). The attached
// client carries a size, which — since fleet's shell sessions are otherwise
// headless — sets the pane geometry; the caller sizes it to the drawer via
// NewOutputReader's w/h and Resize. Keystrokes are NOT sent over this connection:
// the drawer forwards input through the shared ControlClient (ui.getControlClient).
type OutputReader struct {
	mu     sync.Mutex
	ptmx   *os.File
	cmd    *exec.Cmd
	target string
	closed bool
	failed atomic.Bool // read loop ended unexpectedly (not via Close) — caller should re-attach
}

// CapturePaneANSI returns targetSession's active pane as its current visible
// screen, with SGR (color) escapes. Control mode replays nothing on attach — it
// streams only post-attach %output — so a freshly attached OutputReader renders a
// blank emulator until new output arrives. Seeding the emulator with this makes
// the existing screen (e.g. the shell's prompt) visible immediately. Uncached and
// fresh (unlike Session.CapturePane), and takes a bare session name so the drawer
// can call it off-thread without a *Session.
func CapturePaneANSI(targetSession string) []byte {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", targetSession).Output()
	if err != nil {
		debuglog.Logger.Debug("tmux capture-pane (drawer seed) failed", "target", targetSession, "err", err)
		return nil
	}
	return out
}

// NewOutputReader attaches a control client to targetSession at size w×h and
// invokes onData with each decoded %output chunk on a background goroutine until
// Close. onData runs on the reader goroutine, so it must be cheap / non-blocking
// (e.g. write into a mutex-guarded emulator).
func NewOutputReader(targetSession string, w, h int, onData func([]byte)) (*OutputReader, error) {
	cmd := exec.Command("tmux", "-C", "attach", "-t", targetSession)
	ptmx, err := pty.StartWithSize(cmd, winsize(w, h))
	if err != nil {
		return nil, fmt.Errorf("tmux control attach failed: %w", err)
	}
	r := &OutputReader{ptmx: ptmx, cmd: cmd, target: targetSession}
	go r.readLoop(onData)
	debuglog.Logger.Info("tmux output reader attached", "target", targetSession, "size", fmt.Sprintf("%dx%d", w, h))
	return r, nil
}

func (r *OutputReader) readLoop(onData func([]byte)) {
	sc := bufio.NewScanner(r.ptmx)
	// %output lines can be a full-screen redraw; allow a generous max token.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if payload, ok := parseControlOutput(sc.Text()); ok {
			onData(payload)
		}
	}
	// A scan error after Close (PTY closed underneath us) is expected; only log
	// an unexpected end of the live stream.
	if err := sc.Err(); err != nil && !r.IsClosed() {
		debuglog.Logger.Debug("tmux output reader scan ended", "target", r.target, "err", err)
	}
	// The loop ended while we never called Close — e.g. bufio.ErrTooLong on an
	// oversized single %output line (>8 MiB with no real newline), or the control
	// process exited. Flag it failed so the caller re-attaches instead of freezing
	// on the last frame.
	if !r.IsClosed() {
		r.failed.Store(true)
	}
}

// Failed reports whether the read loop ended unexpectedly (without Close) — e.g.
// an oversized %output line tripped bufio.ErrTooLong or the control process died.
// The drawer treats a failed reader like a detached one and re-attaches.
func (r *OutputReader) Failed() bool { return r.failed.Load() }

// Resize re-sizes the control client (and thus the headless pane) to w×h by
// resizing the PTY, which delivers SIGWINCH to tmux. Keep this in lockstep with
// the emulator's Resize or wrap points diverge.
func (r *OutputReader) Resize(w, h int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("output reader closed")
	}
	return pty.Setsize(r.ptmx, winsize(w, h))
}

// IsClosed reports whether the reader has been closed.
func (r *OutputReader) IsClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Close detaches the control client (the target session keeps running) and
// stops the read goroutine by closing the PTY.
func (r *OutputReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	_ = r.ptmx.Close()
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.cmd.Wait()
	debuglog.Logger.Info("tmux output reader closed", "target", r.target)
}

// winsize builds a pty.Winsize, clamping to uint16 range.
func winsize(w, h int) *pty.Winsize {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w > 0xffff {
		w = 0xffff
	}
	if h > 0xffff {
		h = 0xffff
	}
	return &pty.Winsize{Cols: uint16(w), Rows: uint16(h)} //nolint:gosec // clamped above
}
