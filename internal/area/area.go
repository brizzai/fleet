// Package area implements config-defined "areas": named, directory-based
// slices of the sidebar (e.g. personal vs. work). An area is only a view filter
// held by one fleet process; nothing here touches the database.
package area

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
)

// All is the pseudo-area meaning "no filter".
const All = ""

type prefix struct {
	path string
	area int // index into Matcher.areas
}

// Matcher resolves directories to areas. The zero value (and a Matcher built
// from no areas) is disabled: every query answers "no area".
type Matcher struct {
	areas    []config.Area
	prefixes []prefix
	def      int // index of the default area, -1 when none
}

// New compiles areas. home is what "~" expands to ("" = the user's home).
// Areas with an empty name are ignored, and so are empty paths.
func New(areas []config.Area, home string) *Matcher {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	m := &Matcher{def: -1}
	for _, a := range areas {
		if strings.TrimSpace(a.Name) == "" {
			continue
		}
		idx := len(m.areas)
		m.areas = append(m.areas, a)
		if a.Default && m.def < 0 {
			m.def = idx
		}
		for _, p := range a.Paths {
			p = expand(p, home)
			if p == "" {
				continue
			}
			m.prefixes = append(m.prefixes, prefix{p, idx})
			// Tracked repo roots come from `git rev-parse --show-toplevel`,
			// which resolves symlinks; match the resolved form as well.
			if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
				m.prefixes = append(m.prefixes, prefix{r, idx})
			}
		}
	}
	return m
}

// Enabled reports whether any area is configured.
func (m *Matcher) Enabled() bool { return m != nil && len(m.areas) > 0 }

// Names lists the configured area names in config order.
func (m *Matcher) Names() []string {
	if !m.Enabled() {
		return nil
	}
	out := make([]string, len(m.areas))
	for i, a := range m.areas {
		out[i] = a.Name
	}
	return out
}

// Resolve maps a user-typed name (case-insensitive) to the configured name.
func (m *Matcher) Resolve(name string) (string, bool) {
	if !m.Enabled() {
		return "", false
	}
	for _, a := range m.areas {
		if strings.EqualFold(a.Name, strings.TrimSpace(name)) {
			return a.Name, true
		}
	}
	return "", false
}

// Of returns the area owning a session at path. mainRepo, when non-nil, is
// consulted only if path itself matches no area, to place a linked worktree
// that lives outside its repository's folder with that repository. Paths
// nothing claims belong to the default area; with no default the result is
// All, which every area shows.
func (m *Matcher) Of(path string, mainRepo func() string) string {
	if !m.Enabled() {
		return All
	}
	if i := m.longest(path); i >= 0 {
		return m.areas[i].Name
	}
	if mainRepo != nil {
		if mp := mainRepo(); mp != "" && mp != path {
			if i := m.longest(mp); i >= 0 {
				return m.areas[i].Name
			}
		}
	}
	if m.def >= 0 {
		return m.areas[m.def].Name
	}
	return All
}

// Visible reports whether something in area `of` shows under the current view.
// Unowned items (of == All) show everywhere.
func Visible(current, of string) bool {
	return current == All || of == All || current == of
}

// Next cycles area1 → area2 → … → All → area1.
func (m *Matcher) Next(current string) string {
	names := m.Names()
	if len(names) == 0 {
		return All
	}
	if current == All {
		return names[0]
	}
	for i, n := range names {
		if n == current {
			if i+1 < len(names) {
				return names[i+1]
			}
			return All
		}
	}
	return names[0]
}

// Account returns the account email configured for the area that owns path.
func (m *Matcher) Account(path string, mainRepo func() string) string {
	name := m.Of(path, mainRepo)
	if name == All {
		return ""
	}
	for _, a := range m.areas {
		if a.Name == name {
			return a.Account
		}
	}
	return ""
}

// PickAccount applies an area's account to a launch: it returns email when it
// is a configured, logged-in account the origin's allowlist permits, and false
// otherwise so the caller runs its normal selection.
func PickAccount(email string, accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, allowed []string) (claudeaccount.Account, bool) {
	if email == "" {
		return claudeaccount.Account{}, false
	}
	acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
		Accounts: accounts,
		Usage:    usage,
		Strategy: claudeaccount.StrategyManual,
		Manual:   email,
		Allowed:  allowed,
	})
	// Manual falls through to the automatic modes when the pinned account is
	// absent, disallowed or logged out; only an exact hit counts here.
	if !ok || acct.Email != email {
		return claudeaccount.Account{}, false
	}
	return acct, true
}

// longest returns the area index of the longest prefix containing path, or -1.
func (m *Matcher) longest(path string) int {
	i, _ := m.longestPrefix(path)
	return i
}

// longestPrefix is longest plus the prefix that matched.
func (m *Matcher) longestPrefix(path string) (int, string) {
	path = filepath.Clean(path)
	best, bestLen, bestPre := -1, -1, ""
	for _, p := range m.prefixes {
		if contains(p.path, path) && len(p.path) > bestLen {
			best, bestLen, bestPre = p.area, len(p.path), p.path
		}
	}
	return best, bestPre
}

// Folder returns the area and the folder group of a checkout at path: the first
// directory below the matching area prefix, when the path sits at least two
// levels below it (prefix/Youtube/lastro → "Youtube"). Paths directly in the
// area folder, matching no area prefix, or with no areas configured have no
// group (""). Like Of, it consults mainRepo only when path itself matches no
// prefix, and the default area never groups: it owns paths by absence.
func (m *Matcher) Folder(path string, mainRepo func() string) (areaName, folder string) {
	if !m.Enabled() {
		return "", ""
	}
	path = filepath.Clean(path)
	i, pre := m.longestPrefix(path)
	if i < 0 && mainRepo != nil {
		if mp := mainRepo(); mp != "" {
			path = filepath.Clean(mp)
			i, pre = m.longestPrefix(path)
		}
	}
	if i < 0 {
		return "", ""
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(path, pre), string(filepath.Separator))
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return m.areas[i].Name, ""
	}
	return m.areas[i].Name, parts[0]
}

// contains reports whether path is dir or lies under it, on a directory
// boundary (/a/Trab does not contain /a/Trabalho2).
func contains(dir, path string) bool {
	if path == dir {
		return true
	}
	if dir == string(filepath.Separator) {
		return strings.HasPrefix(path, dir)
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

func expand(p, home string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		p = home
	} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(home, rest)
	}
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}
