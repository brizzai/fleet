package session

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	_ "modernc.org/sqlite"
)

// codexTitleMaxRunes bounds what we'll accept as a Codex title. A `/rename` is a
// handful of words; a seeded first prompt runs to thousands of characters.
const codexTitleMaxRunes = 120

// codexQueryErrOnce keeps a broken-schema warning to one line per launch.
var codexQueryErrOnce sync.Once

// codexStateDBPath returns the Codex state database holding the `threads` table,
// or "" if there isn't one.
//
// Codex versions the file ($CODEX_HOME/state_<N>.sqlite — state_5.sqlite as of
// codex-cli 0.144) and bumps N with the schema, so the highest N wins rather
// than a hardcoded name: a Codex upgrade must not silently strip titles.
func codexStateDBPath() string {
	matches, err := filepath.Glob(filepath.Join(hooks.GetCodexConfigDir(), "state_*.sqlite"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		return codexStateVersion(matches[i]) < codexStateVersion(matches[j])
	})
	return matches[len(matches)-1]
}

// codexStateVersion extracts N from a `state_<N>.sqlite` path (-1 if absent, so
// an unparseable name sorts below every real one).
func codexStateVersion(path string) int {
	name := strings.TrimSuffix(filepath.Base(path), ".sqlite")
	n, err := strconv.Atoi(strings.TrimPrefix(name, "state_"))
	if err != nil {
		return -1
	}
	return n
}

// ReadCodexSessionName reads the title the user set with `/rename` inside a
// Codex thread. Codex persists it only in its state DB (`UPDATE threads SET
// title = ...`) — the rollout JSONL carries no title — so that's what we read,
// read-only, keyed by the thread id fleet already stores as ClaudeSessionID.
//
// Codex *seeds* threads.title with the raw, untruncated first user message, so a
// title still equal to first_user_message is not a rename: returning it would
// swap fleet's short heuristic title for a multi-thousand-char prompt. Only a
// title that has diverged is the user's own, and only that is adopted.
//
// Returns "" whenever there's no title to adopt — including a missing DB, an
// unknown thread, or a schema that has moved on. Like a missing Claude
// transcript, that's the normal case, not an error.
func ReadCodexSessionName(threadID string) string {
	if threadID == "" {
		return ""
	}
	path := codexStateDBPath()
	if path == "" {
		return ""
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		debuglog.Logger.Debug("codex name: open state db", "path", path, "err", err)
		return ""
	}
	defer db.Close()

	var title, firstUserMessage string
	err = db.QueryRow("SELECT title, first_user_message FROM threads WHERE id = ?", threadID).
		Scan(&title, &firstUserMessage)
	if err != nil {
		if err != sql.ErrNoRows {
			// A failing query — as opposed to a missing DB or an unknown thread —
			// is what a Codex schema bump looks like, and it silently kills title
			// pickup. Say so at Warn (Debug is invisible at the default level,
			// including in the log the bug-report flow pastes into issues), but
			// only once: the worker re-reads every 30s per session, and the report
			// carries just the last 100 lines, so spamming it would evict the
			// context that makes this diagnosable.
			codexQueryErrOnce.Do(func() {
				debuglog.Logger.Warn("codex name: query failed — Codex titles won't update (schema change?)",
					"db", path, "err", err)
			})
		}
		return ""
	}

	if title == "" || title == firstUserMessage {
		return ""
	}
	// Belt and braces on the seeded-title check above: equality is only as good
	// as Codex writing the two columns byte-identically, so a future Codex that
	// trims or normalizes `title` alone would defeat it and hand us a prompt.
	// Nothing a person types into `/rename` is this long.
	if utf8.RuneCountInString(title) > codexTitleMaxRunes {
		return ""
	}
	return title
}
