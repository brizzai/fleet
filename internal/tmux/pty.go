//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const terminalStyleReset = "\x1b]8;;\x1b\\\x1b[0m\x1b[24m\x1b[39m\x1b[49m"

// ctrlQSequences enumerates every byte pattern a terminal may emit for Ctrl+Q.
// The bare control byte is the common case; the CSI variants appear when the
// terminal has switched to CSI-u or xterm's modifyOtherKeys reporting (tmux
// enables this via `extended-keys on`, which oh-my-tmux turns on for
// iTerm2/mintty/xterm).
var ctrlQSequences = [][]byte{
	{17},                     // plain Ctrl+Q (ASCII DC1)
	[]byte("\x1b[113;5u"),    // CSI-u / libtickit / kitty keyboard
	[]byte("\x1b[27;5;113~"), // xterm modifyOtherKeys
}

// findCtrlQ returns the byte offset of the earliest Ctrl+Q sequence in buf
// and the length of that sequence. Returns (-1, 0) when none is present.
func findCtrlQ(buf []byte) (int, int) {
	idx, length := -1, 0
	for _, seq := range ctrlQSequences {
		i := bytes.Index(buf, seq)
		if i < 0 {
			continue
		}
		if idx < 0 || i < idx {
			idx = i
			length = len(seq)
		}
	}
	return idx, length
}

// clientEnv returns the parent environment with tmux's own client variables
// stripped. A tmux client refuses to attach to a session on the server it is
// already inside — "sessions should be nested with care, unset $TMUX to force"
// — so a fleet launched from within tmux would hand that variable to every
// attach and Enter would do nothing at all. The error goes to the child's
// stderr, which here is the PTY we immediately put into raw mode, so it never
// reaches the TUI or the log: a silent dead key on fleet's primary action.
// Unsetting is exactly what tmux's message asks for. TMUX_PANE goes with it —
// it names a pane on the outer server and means nothing to the new client.
func clientEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Attach attaches to the tmux session with full PTY support.
// Ctrl+Q detaches and returns to the caller. Ctrl+b d also works (tmux native).
func (s *Session) Attach(ctx context.Context) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start tmux attach with PTY.
	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-t", s.Name)
	cmd.Env = clientEnv()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer ptmx.Close()

	// Save terminal state and set raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Handle window resize signals.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	sigwinchDone := make(chan struct{})
	defer func() {
		signal.Stop(sigwinch)
		close(sigwinchDone)
	}()

	var wg sync.WaitGroup

	// SIGWINCH handler.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sigwinchDone:
				return
			case _, ok := <-sigwinch:
				if !ok {
					return
				}
				if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, ws)
				}
			}
		}
	}()
	// Initial resize.
	sigwinch <- syscall.SIGWINCH

	detachCh := make(chan struct{})
	ioErrors := make(chan error, 2)
	startTime := time.Now()
	const controlSeqTimeout = 50 * time.Millisecond
	outputDone := make(chan struct{})

	// Goroutine: copy PTY output to stdout.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(outputDone)
		_, err := io.Copy(os.Stdout, ptmx)
		if err != nil && err != io.EOF {
			select {
			case ioErrors <- fmt.Errorf("PTY read error: %w", err):
			default:
			}
		}
	}()

	// Goroutine: read stdin, intercept Ctrl+Q (raw ASCII 17 or CSI-u / modifyOtherKeys encodings), forward rest to PTY.
	wg.Add(1)
	go func() {
		defer wg.Done()
		forwardStdinToPTY(ptmx, startTime, controlSeqTimeout, detachCh, cancel, ioErrors)
	}()

	// Wait for command to finish.
	cmdDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmdDone <- cmd.Wait()
	}()

	cleanupAttach := func() {
		cancel()
		_ = ptmx.Close()
		select {
		case <-outputDone:
		case <-time.After(20 * time.Millisecond):
		}
		_, _ = os.Stdout.WriteString(terminalStyleReset)
	}

	// Wait for detach or command completion.
	attachErr := waitForDetach(ctx, detachCh, cmdDone)
	cleanupAttach()
	return attachErr
}

// forwardStdinToPTY reads stdin, intercepts Ctrl+Q for detach, forwards everything else to the PTY.
func forwardStdinToPTY(ptmx *os.File, startTime time.Time, controlSeqTimeout time.Duration, detachCh chan struct{}, cancel context.CancelFunc, ioErrors chan<- error) {
	buf := make([]byte, 32)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			if err != io.EOF {
				select {
				case ioErrors <- fmt.Errorf("stdin read error: %w", err):
				default:
				}
			}
			return
		}

		// Discard initial terminal control sequences.
		if time.Since(startTime) < controlSeqTimeout {
			continue
		}

		// Check for Ctrl+Q in any encoding the terminal might use.
		if idx, _ := findCtrlQ(buf[:n]); idx >= 0 {
			if idx > 0 {
				if _, werr := ptmx.Write(buf[:idx]); werr != nil {
					select {
					case ioErrors <- fmt.Errorf("PTY write error: %w", werr):
					default:
					}
					return
				}
			}
			close(detachCh)
			cancel()
			return
		}

		// Forward input to PTY.
		if _, werr := ptmx.Write(buf[:n]); werr != nil {
			select {
			case ioErrors <- fmt.Errorf("PTY write error: %w", werr):
			default:
			}
			return
		}
	}
}

// waitForDetach waits for detach signal, command completion, or context cancellation.
func waitForDetach(ctx context.Context, detachCh <-chan struct{}, cmdDone <-chan error) error {
	select {
	case <-detachCh:
		return nil
	case err := <-cmdDone:
		return classifyExitError(ctx, err)
	case <-ctx.Done():
		return nil
	}
}

// classifyExitError returns nil for expected exit codes (0, 1) or context cancellation.
func classifyExitError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
			return nil
		}
	}
	return err
}
