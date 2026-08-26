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

// luceneReserved is the set Atlassian documents as reserved inside a text
// search: everything Lucene's query parser treats as an operator.
const luceneReserved = `+-&|!(){}[]^~*?:\"`

// escapeJQL quotes a user's search term for a `text ~ "..."` clause.
//
// TWO layers, applied in this order, because the value passes through two
// parsers on the way in:
//
//  1. Lucene, which parses the CONTENTS of the literal for a `~` comparison.
//     Quoting is not enough here — an unescaped `(` or `?` is an operator, not
//     data, and the query is rejected outright.
//  2. The JQL string literal itself, where a backslash or a quote would
//     otherwise end the string early.
//
// The earlier version escaped only for layer 2 and asserted in its own comment
// that "everything else inside quotes is data". That is true of an `=`
// comparison and false of `~` — so typing `fix (login` into the New branch
// field, which fires a search at three runes, produced a 400 that the dialog
// reported as "Jira: unavailable". Half a branch name is not evidence that the
// tracker is down.
func escapeJQL(term string) string {
	var b strings.Builder
	b.Grow(len(term) * 2)
	for _, r := range term {
		if strings.ContainsRune(luceneReserved, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	// Now the JQL literal: every backslash this produced (and every one the
	// user typed) has to survive as a backslash, and a quote has to stay inside
	// the string.
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(b.String())
}
