// Package jira reads Jira Cloud issues through Atlassian's REST v3 API.
//
// It is one implementation of ticket.Provider, and it deliberately holds no
// copy of anything internal/ticket already owns: branch naming, the seeded
// prompt, image download, the keychain store and ticket.md itself are shared
// with Linear, so the two trackers cannot drift apart in the parts a user sees.
//
// Cloud only. The v3 API is what serves descriptions as ADF and what exposes
// /rest/api/3/search/jql; Server and Data Center speak v2, return wiki markup,
// and authenticate with a bearer PAT rather than Basic — a second fetch path
// and a second auth story for a product fleet cannot test against. Pointed at a
// Server instance, the connect dialog reports that plainly rather than half
// working.
//
// There is deliberately no `jira` CLI anywhere in here, for the same reason
// there is no `linear` one: TestNoTicketSubprocess allowlists the three OS
// helpers fleet may run, so a new subprocess has to be added deliberately.
package jira

import (
	"strings"

	"github.com/brizzai/fleet/internal/workspace"
)

// kind is Jira's stable id: the config key, the analytics property, and the
// prefix of its keychain service name. Never shown to a user — Name is.
const kind = "jira"

// ProjectKeys returns the Jira project keys this repo tracks, or nil if it
// tracks none.
//
// Nil is the answer that keeps an unrelated repo silent for a connected user,
// so there is deliberately NO fallback to "every project on the site": a real
// Jira site has hundreds, and that would put ticket suggestions under the branch
// field of every repo on the machine.
//
// Free and non-blocking — two small file reads, no network. Unlike Linear there
// is no foreign-config fallback to read: Jira has no per-repo file convention
// that any tool already writes.
func ProjectKeys(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	return workspace.JiraProjectKeys(repoPath)
}

// escapeJQL quotes a user's search term for a JQL string literal.
//
// JQL string literals take backslash escapes, so a term containing a quote
// would otherwise end the literal and turn the rest of what someone typed into
// syntax. Only these two characters need it — everything else inside quotes is
// data, including the reserved words that would break an unquoted term.
func escapeJQL(term string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(term)
}
