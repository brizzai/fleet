// Package linear reads Linear issues through Linear's own GraphQL API.
//
// It is one implementation of ticket.Provider; everything that was never
// Linear-specific — branch naming, the seeded prompt, image download, the
// keychain store, ticket.md itself — lives in internal/ticket, so this package
// is a client and nothing more.
//
// Design rules, in the order they matter:
//
//   - fleet holds exactly one Linear credential and nothing else. It is stored
//     in the OS keychain where there is one, read at request time, and never
//     written into a session's tmux environment, a log line, or a bug report.
//   - The feature is per-repo and opt-out-by-absence: a repo that names no
//     Linear team (via .fleet.json or .linear.toml) behaves exactly as it did
//     before this package existed, even for a connected user.
//   - Nothing here runs on the Bubble Tea Update goroutine or in the status/git
//     workers. Every call is event-driven and one-shot, which is what keeps it
//     clear of workerStallThreshold's budget. The two methods the UI does call
//     synchronously — Available and Keys — touch no network and no keychain.
//
// There is deliberately no `linear` CLI anywhere in here. An earlier version
// shelled out to one, which cost three version-skew bugs in a single session
// and would not have transferred to Jira.
package linear

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brizzai/fleet/internal/workspace"
)

// linearConfigFile is the `linear` CLI's own per-repo config. fleet does not
// require, write, or depend on that CLI, but reading the team key out of a file
// someone already has costs nothing and makes this zero-touch for them.
//
// Only team_id is read. api_key lives in the same file and is deliberately never
// touched: fleet resolves its own credential and has no business adopting one
// left there for another tool.
const linearConfigFile = ".linear.toml"

var teamIDRe = regexp.MustCompile(`(?m)^\s*team_id\s*=\s*["']([A-Za-z][A-Za-z0-9]*)["']`)

// TeamKeys returns the Linear team keys this repo tracks, or nil if it tracks
// none.
//
// Nil is the answer that keeps an unrelated repo silent for a connected user, so
// there is deliberately NO fallback to "every team in the workspace": that would
// put Linear suggestions under the branch field of every repo on the machine.
//
// Free and non-blocking — two small file reads, no network — because the dialog
// paths call it while a frame is being built.
func TeamKeys(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	if keys := workspace.LinearTeamKeys(repoPath); len(keys) > 0 {
		return keys
	}
	data, err := readFileLimited(filepath.Join(repoPath, linearConfigFile), 64<<10)
	if err != nil {
		return nil
	}
	m := teamIDRe.FindSubmatch(data)
	if m == nil {
		return nil
	}
	return []string{strings.ToUpper(string(m[1]))}
}

// readFileLimited reads at most limit bytes, so a pathological config file
// can't be pulled into memory whole.
func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
