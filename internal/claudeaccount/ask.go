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
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out")
		}
		debuglog.Logger.Debug("claude ask failed", "err", err, "stderr", msg)
		if msg != "" {
			return "", fmt.Errorf("claude: %s", firstLine(msg))
		}
		return "", fmt.Errorf("claude: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
