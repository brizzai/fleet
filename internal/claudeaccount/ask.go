package claudeaccount

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// ErrClaudeMissing means the CLI is not installed. A Codex-only or OpenCode-only
// user has no Claude at all, and a feature that leans on one has to degrade
// rather than report a failure they cannot act on.
var ErrClaudeMissing = errors.New("claude is not installed")

// Ask runs one non-interactive Claude call on a given account and returns what
// it said.
//
// This is fleet asking a question of its own, rather than launching a session
// for the user, so it goes through `claude -p` rather than tmux: no pane, no
// hooks, no status, nothing in the sidebar. It authenticates exactly as every
// other session does — CLAUDE_CONFIG_DIR pointing at that account's own
// claude.ai login — so it needs no API key and spends the subscription the user
// already has.
//
// instruction rides in argv and input rides on STDIN, and the split is
// deliberate: argv is world-readable through ps, so fleet's own words may go
// there and other people's code may not. It is also the only way to pass a
// changeset at all — a 300KB argument is not something to hand a shell.
func Ask(ctx context.Context, dir, instruction, input string) (string, error) {
	if _, err := exec.LookPath(claudeBinary); err != nil {
		return "", ErrClaudeMissing
	}

	cmd := exec.CommandContext(ctx, claudeBinary, "-p", instruction)
	cmd.Stdin = strings.NewReader(input)
	if dir != "" {
		// Empty means the ambient login, which is the right answer when fleet
		// manages no accounts at all: the user has one Claude and it works.
		cmd.Env = configDirEnv(dir)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out")
		}
		out, errOut := strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())
		debuglog.Logger.Debug("claude ask failed", "err", err, "stdout", out, "stderr", errOut)
		return "", fmt.Errorf("claude: %s", askFailureReason(out, errOut, err))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// askFailureReason works out what actually went wrong, and STDOUT is where it
// looks first.
//
// Measured, not assumed: `claude -p` on a spent subscription exits 1, prints
// "You've hit your session limit · resets 3:50pm" on stdout, and puts only
// warnings on stderr — a settings.json permission rule it dislikes, a note
// about the shell's working directory. Reading stderr first therefore reported
// a permission rule as the cause of a quota refusal, which is a false statement
// about the user's setup and sends them to fix a file that was never the
// problem. `claude auth status` has the same shape (see parseAuthStatus): the
// answer is on stdout even when the exit code is 1.
//
// Truncated because this reaches a one-line toast in a footer, and claude's
// warnings run to a full paragraph.
func askFailureReason(stdout, stderr string, runErr error) string {
	for _, s := range []string{stdout, stderr} {
		if line := firstUsefulLine(s); line != "" {
			return trimTo(line, askReasonMax)
		}
	}
	return runErr.Error()
}

// askReasonMax is what fits a footer without pushing the keys off it.
const askReasonMax = 90

// firstUsefulLine skips the notes claude prints on its way to the real answer.
func firstUsefulLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isClaudeNote(line) {
			continue
		}
		return line
	}
	return ""
}

// isClaudeNote recognises claude's own advisories, which are printed alongside
// a failure rather than being one. A settings file it dislikes is a real thing
// to tell the user about — but not while they are asking why their tour is
// missing, and not as the reason it is.
func isClaudeNote(line string) bool {
	return strings.HasPrefix(line, "Permission allow rule") ||
		strings.HasPrefix(line, "Shell cwd was reset") ||
		strings.HasPrefix(line, "Warning: no stdin data received")
}

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
