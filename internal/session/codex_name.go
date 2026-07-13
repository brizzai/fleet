package session

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	_ "modernc.org/sqlite"
)

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
			debuglog.Logger.Debug("codex name: query threads", "thread", threadID, "err", err)
		}
		return ""
	}

	if title == "" || title == firstUserMessage {
		return ""
	}
	return title
}
