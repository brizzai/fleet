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
//   - The feature follows the person, not the repo: a repo that names no team
//     tracks the teams the connected user belongs to. A repo can name its own
//     team(s) instead (.fleet.json, .fleet.local.json, .linear.toml) or turn
//     Linear off entirely ({"linear": {"enabled": false}}).
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
	"slices"
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
// In order: a repo that switched Linear off tracks nothing; a repo that names
// its own team(s) tracks exactly those; anything else tracks the teams the
// connected user is a member of.
//
// The last step is the default because teams belong to people rather than to
// repos — one repo can carry tickets from several teams, and one team works
// across many repos — and because naming a team in every repo was the step
// people missed: connected, nothing happened, and nothing said why (#373). It
// is the user's MEMBERSHIPS, never every team in the workspace, which can be
// dozens and would put strangers' tickets under every branch field.
//
// Free and non-blocking — one config read and a cache read, no network —
// because the dialog paths call it while a frame is being built. Before the
// account reading lands (or after Disconnect) the default is empty, so an
// unconfigured repo is quiet rather than wrong.
func TeamKeys(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	teams, disabled, jiraProjects := workspace.LinearRepo(repoPath)
	if disabled {
		return nil
	}
	if len(teams) > 0 {
		return teams
	}
	if key := linearTOMLTeam(repoPath); key != "" {
		return []string{key}
	}
	// A defaulted key never outranks one the repo names explicitly. The
	// registry breaks ties Linear-first, so without this a repo naming Jira
	// project OPS, used by someone on a Linear team also keyed OPS, would send
	// OPS-42 to Linear — a repo that claimed nothing from Linear before
	// defaults existed.
	return slices.DeleteFunc(memberKeys(), func(k string) bool {
		return slices.Contains(jiraProjects, k)
	})
}

// linearTOMLTeam reads team_id out of a .linear.toml, or "".
func linearTOMLTeam(repoPath string) string {
	data, err := readFileLimited(filepath.Join(repoPath, linearConfigFile), 64<<10)
	if err != nil {
		return ""
	}
	m := teamIDRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return strings.ToUpper(string(m[1]))
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
