// Package sqliteutil builds DSNs for fleet's SQLite databases (state.db,
// stats.db) with modernc.org/sqlite.
package sqliteutil

import (
	"net/url"
	"strings"
)

// DSN returns a `file:` DSN for path that applies each pragma on EVERY
// connection the pool opens.
//
// Pragmas must ride the DSN, never a `db.Exec("PRAGMA ...")` after Open:
// busy_timeout and foreign_keys are per-connection settings, and database/sql
// hands Exec one pooled connection, so every connection opened later would
// start with busy_timeout=0 and foreign keys off. That is what produced
// "database is locked (SQLITE_BUSY)" from a single fleet instance.
//
// pragmas are written as modernc expects them, e.g. "busy_timeout(5000)".
// txlock, when set, is passed as _txlock (deferred / immediate / exclusive).
func DSN(path string, pragmas []string, txlock string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if txlock != "" {
		q.Set("_txlock", txlock)
	}
	return "file:" + pathEscaper.Replace(path) + "?" + q.Encode()
}

// pathEscaper percent-encodes the characters SQLite's URI parser treats
// specially in a `file:` path — `%` (escape), `?` (query) and `#` (fragment) —
// so a home directory containing one opens the right file. SQLite decodes %HH
// in the path; everything else passes through as-is.
var pathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
