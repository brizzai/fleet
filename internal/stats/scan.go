package stats

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// staleTurnAfter is how long a transcript must sit untouched before a turn
// with no turn_duration line (an Esc-interrupted one, or the last turn of a
// crashed session) is closed at its last assistant timestamp. Younger than
// that, it is probably still running and the next scan picks it up whole.
const staleTurnAfter = time.Hour

// defaultRoots lists every directory whose children are Claude project dirs:
// the ambient ~/.claude and each fleet-managed account's config dir. The
// account dirs symlink projects/ back to ~/.claude, so the same file shows up
// more than once; findTranscripts keeps one per Claude id.
func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	roots := []string{filepath.Join(home, ".claude", "projects")}
	acc, _ := filepath.Glob(filepath.Join(home, ".config", "fleet", "accounts", "*", "projects"))
	return append(roots, acc...)
}

type scanFile struct {
	path      string // realpath — opened, never stored
	key       string // fileKey(path): what stats.db stores instead
	sessionID string // fleet session id
	size      int64
	mtime     time.Time
}

// Scan backfills agent turns from the Claude transcripts of sessions fleet
// has noted — fleet sessions only, never other claude usage on the machine.
// Incremental: each file resumes from the byte offset the last scan
// committed. Newest files first, so recent ranges fill before old ones.
// Safe to call concurrently (calls serialize); honours ctx cancellation
// between lines.
func (s *Store) Scan(ctx context.Context, progress func(Progress)) error {
	if s == nil {
		return nil
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if progress == nil {
		progress = func(Progress) {}
	}

	wanted, err := s.claudeIDs()
	if err != nil {
		return err
	}
	files := s.findTranscripts(ctx, wanted)
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })

	p := Progress{Total: len(files)}
	progress(p)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		turns, tokens, err := s.scanFile(ctx, f)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// One unreadable file must not stop the rest.
		}
		p.Done++
		p.Turns += turns
		p.Tokens += tokens
		progress(p)
	}
	p.Finished = true
	progress(p)
	return nil
}

// claudeIDs maps every Claude session id a noted Claude session ever had to
// its fleet session id.
func (s *Store) claudeIDs() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT c.claude_session_id, c.session_id FROM claude_ids c
		LEFT JOIN sessions s ON s.id = c.session_id
		WHERE COALESCE(s.agent, '') IN ('', 'claude')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var cid, sid string
		if err := rows.Scan(&cid, &sid); err != nil {
			return nil, err
		}
		out[cid] = sid
	}
	return out, rows.Err()
}

// fileKey is how stats.db names a transcript: a hash of its realpath, since the
// path itself spells out the home dir and the project's full cwd.
func fileKey(realpath string) string {
	sum := sha256.Sum256([]byte(realpath))
	return hex.EncodeToString(sum[:16])
}

// findTranscripts returns one file per wanted Claude session id. A fork into
// another cwd stages a copy of the parent's transcript under the parent's id
// in the fork's project dir, so an id can have two files; the largest is the
// live one (the copy stops growing at the fork), and scanning both would count
// the parent's history twice. Symlinked account dirs resolve to the same
// realpath and collapse the same way.
func (s *Store) findTranscripts(ctx context.Context, wanted map[string]string) []scanFile {
	if len(wanted) == 0 {
		return nil
	}
	best := map[string]scanFile{} // claude id → chosen file
	for _, root := range s.roots() {
		projects, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, pd := range projects {
			if ctx.Err() != nil {
				return nil // Scan reports the cancellation
			}
			dir := filepath.Join(root, pd.Name())
			entries, err := os.ReadDir(dir) // follows a symlinked dir
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if !strings.HasSuffix(name, ".jsonl") {
					continue
				}
				cid := strings.TrimSuffix(name, ".jsonl")
				sid, ok := wanted[cid]
				if !ok {
					continue
				}
				real, err := filepath.EvalSymlinks(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				st, err := os.Stat(real)
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
				if cur, ok := best[cid]; ok && cur.size >= st.Size() {
					continue
				}
				best[cid] = scanFile{path: real, key: fileKey(real), sessionID: sid, size: st.Size(), mtime: st.ModTime()}
			}
		}
	}
	out := make([]scanFile, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	return out
}

// turnRow is one agent turn as stored in the turns table.
type turnRow struct {
	offset                         int64
	startMs, durMs                 int64
	model                          string
	in, out, cacheRead, cw5m, cw1h int64
}

func (t turnRow) tokens() int64 { return t.in + t.out + t.cacheRead + t.cw5m + t.cw1h }

// scanFile reads one transcript from its committed offset and stores the
// turns it completes. Returns the number of turns and tokens added.
func (s *Store) scanFile(ctx context.Context, f scanFile) (int, int64, error) {
	var size, mtimeMs, offset int64
	err := s.db.QueryRow(`SELECT size, mtime_ms, offset FROM scan_state WHERE path = ?`, f.key).Scan(&size, &mtimeMs, &offset)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		offset = 0
	case err != nil:
		return 0, 0, err
	case size == f.size && mtimeMs == f.mtime.UnixMilli():
		return 0, 0, nil // untouched since last scan
	}
	rescan := f.size < offset // shrunk: rewritten, start over
	if rescan {
		offset = 0
	}

	fh, err := os.Open(f.path)
	if err != nil {
		return 0, 0, err
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		return 0, 0, err
	}
	stale := s.now().Sub(f.mtime) > staleTurnAfter
	turns, safe, err := parseTranscript(ctx, fh, offset, stale)
	if err != nil {
		return 0, 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	if rescan {
		if _, err := tx.Exec(`DELETE FROM turns WHERE file_key = ?`, f.key); err != nil {
			return 0, 0, err
		}
	}
	var tokens int64
	for _, t := range turns {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO turns(file_key, line_offset, session_id, start_ms, dur_ms, model,
			input_tokens, output_tokens, cache_read, cache_write, cache_write_1h) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			f.key, t.offset, f.sessionID, t.startMs, t.durMs, t.model, t.in, t.out, t.cacheRead, t.cw5m, t.cw1h); err != nil {
			return 0, 0, err
		}
		tokens += t.tokens()
	}
	// Store the size/mtime only when the whole file was consumed; otherwise
	// the next scan must look again even if nothing changed on disk.
	stSize, stMtime := f.size, f.mtime.UnixMilli()
	if safe < f.size {
		stSize = -1
	}
	if _, err := tx.Exec(`INSERT INTO scan_state(path, size, mtime_ms, offset) VALUES(?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime_ms = excluded.mtime_ms, offset = excluded.offset`,
		f.key, stSize, stMtime, safe); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(turns), tokens, nil
}

// transcriptLine is the handful of fields the scanner reads. Message content
// is never decoded into anything: only ids, model names and token counts.
type transcriptLine struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	DurationMs  int64  `json:"durationMs"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	pfTurnDuration = []byte(`"subtype":"turn_duration"`)
	pfAssistant    = []byte(`"type":"assistant"`)
	pfUser         = []byte(`"type":"user"`)
	pfProgress     = []byte(`"type":"progress"`)
	pfHuman        = []byte(`"kind":"human"`)
	pfOrigin       = []byte(`"origin":`)
	pfToolResult   = []byte(`"toolUseResult"`)
	pfInterrupted  = []byte(`"interruptedMessageId"`)
	pfCompact      = []byte(`"isCompactSummary":true`)
	pfTimestamp    = []byte(`"timestamp":"`)
)

// idleGap splits a turn: a stretch this long with no transcript line at all
// (no reply, no tool result, no sub-agent line) is the turn parked on a
// permission prompt or stalled, not an agent working. Claude's own
// turn_duration counts it — measured here at 70h for one turn — so without
// the split a single forgotten prompt would outweigh a month of real work.
const idleGap = 30 * time.Minute

// maxSegments bounds how many rows one turn may split into; each row's key is
// the closing line's offset plus its index, which must stay inside that line.
const maxSegments = 32

// turnAcc accumulates one in-flight turn.
type turnAcc struct {
	humanMs, firstMs, lastMs int64
	lastOffset               int64
	has                      bool    // saw a (non-synthetic) assistant reply
	acts                     []int64 // every activity timestamp seen in the turn
	row                      turnRow
	ids                      map[string]bool
}

func (a *turnAcc) reset() { *a = turnAcc{} }

func (a *turnAcc) pending() bool { return a.has || a.humanMs != 0 }

// segments closes the turn over [start,end], split wherever activity went
// quiet for longer than idleGap. Tokens ride on the last segment.
func (a *turnAcc) segments(offset, start, end int64) []turnRow {
	if end < start {
		return nil
	}
	pts := make([]int64, 0, len(a.acts)+2)
	pts = append(pts, start)
	for _, t := range a.acts {
		if t > start && t < end {
			pts = append(pts, t)
		}
	}
	pts = append(pts, end)
	sort.Slice(pts, func(i, j int) bool { return pts[i] < pts[j] })
	gap := idleGap.Milliseconds()
	var out []turnRow
	segStart := pts[0]
	for i := 1; i < len(pts); i++ {
		if pts[i]-pts[i-1] > gap && len(out) < maxSegments-1 {
			out = append(out, turnRow{startMs: segStart, durMs: pts[i-1] - segStart})
			segStart = pts[i]
		}
	}
	last := a.row
	last.startMs, last.durMs = segStart, end-segStart
	out = append(out, last)
	kept := out[:0]
	for i, r := range out {
		if r.durMs > 0 || i == len(out)-1 {
			r.offset = offset + int64(len(kept))
			kept = append(kept, r)
		}
	}
	return kept
}

// interrupted closes a turn that never got a turn_duration line (Esc, or a
// crash), spanning from its prompt to its last assistant reply.
func (a *turnAcc) interrupted(offset int64) []turnRow {
	start := a.humanMs
	if start == 0 || start > a.firstMs {
		start = a.firstMs
	}
	if start == 0 || a.lastMs < start {
		return nil
	}
	return a.segments(offset, start, a.lastMs)
}

// parseTranscript reads JSONL from r (positioned at base) and returns the
// completed turns plus the offset up to which everything is accounted for —
// the start of any turn still in flight. A trailing line without a newline is
// left for next time (it may be mid-write). stale closes a dangling turn.
func parseTranscript(ctx context.Context, r io.Reader, base int64, stale bool) ([]turnRow, int64, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var (
		turns []turnRow
		acc   turnAcc
		pos   = base
		safe  = base
	)
	for n := 0; ; n++ {
		if n&1023 == 0 && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // partial trailing line (or none): not consumed
			}
			return nil, 0, err
		}
		lineStart := pos
		pos += int64(len(line))

		// Every line the scanner cares about passes one substring test before
		// any JSON is decoded; tool-output lines (often MBs) skip decoding.
		switch {
		case bytes.Contains(line, pfTurnDuration):
			var tl transcriptLine
			if json.Unmarshal(line, &tl) != nil || tl.IsSidechain || tl.Type != "system" {
				break
			}
			end, ok := parseTS(tl.Timestamp)
			if !ok || tl.DurationMs < 0 {
				break
			}
			turns = append(turns, acc.segments(lineStart, end-tl.DurationMs, end)...)
			acc.reset()
			safe = pos
			continue
		case bytes.Contains(line, pfAssistant):
			var tl transcriptLine
			if json.Unmarshal(line, &tl) != nil || tl.Type != "assistant" || tl.Message == nil {
				break
			}
			ts, ok := parseTS(tl.Timestamp)
			if !ok || normalizeModel(tl.Message.Model) == "<synthetic>" {
				break // synthetic lines are Claude Code's, not the model's
			}
			if tl.IsSidechain {
				// A sub-agent line: activity, never the lead's tokens.
				if acc.pending() {
					acc.acts = append(acc.acts, ts)
				}
				break
			}
			acc.acts = append(acc.acts, ts)
			if acc.firstMs == 0 {
				acc.firstMs = ts
			}
			acc.lastMs = ts
			acc.lastOffset = lineStart
			acc.has = true
			// One API message is split across a line per content block,
			// each repeating the same usage: count it once.
			id := tl.Message.ID
			if u := tl.Message.Usage; u != nil && (id == "" || !acc.ids[id]) {
				if acc.ids == nil {
					acc.ids = map[string]bool{}
				}
				if id != "" {
					acc.ids[id] = true
				}
				acc.row.in += u.InputTokens
				acc.row.out += u.OutputTokens
				acc.row.cacheRead += u.CacheReadInputTokens
				if cc := u.CacheCreation; cc != nil && cc.Ephemeral5m+cc.Ephemeral1h > 0 {
					acc.row.cw5m += cc.Ephemeral5m
					acc.row.cw1h += cc.Ephemeral1h
				} else {
					acc.row.cw5m += u.CacheCreationInputTokens
				}
				if m := normalizeModel(tl.Message.Model); m != "" {
					acc.row.model = m
				}
			}
		case bytes.Contains(line, pfUser) && isHumanLine(line):
			var tl transcriptLine
			if json.Unmarshal(line, &tl) != nil || tl.IsSidechain || tl.IsMeta || tl.Type != "user" {
				break
			}
			ts, ok := parseTS(tl.Timestamp)
			if !ok {
				break
			}
			if acc.has {
				turns = append(turns, acc.interrupted(lineStart)...)
			}
			acc.reset()
			acc.humanMs = ts
			safe = lineStart
			continue
		case acc.pending() && (bytes.Contains(line, pfUser) || bytes.Contains(line, pfProgress)):
			// Tool results and progress lines: activity only. Their payload
			// can be megabytes, so the timestamp is read without decoding.
			if ts, ok := quickTS(line); ok {
				acc.acts = append(acc.acts, ts)
			}
		}
		if !acc.pending() {
			safe = pos
		}
	}
	if stale {
		if acc.has {
			turns = append(turns, acc.interrupted(acc.lastOffset)...)
		}
		safe = pos // a dangling turn, or a prompt that never got an answer
	}
	return turns, safe, nil
}

// isHumanLine reports whether a user line is a prompt a person typed rather
// than a tool result: origin.kind=human on current Claude Code, and on older
// transcripts (no origin) the absence of a tool result or an interrupt marker.
func isHumanLine(line []byte) bool {
	if bytes.Contains(line, pfOrigin) {
		return bytes.Contains(line, pfHuman)
	}
	return !bytes.Contains(line, pfToolResult) && !bytes.Contains(line, pfInterrupted) && !bytes.Contains(line, pfCompact)
}

// quickTS finds the first top-level-looking "timestamp" without decoding the
// line. Only used as an activity hint, so a rare miss costs nothing.
func quickTS(line []byte) (int64, bool) {
	i := bytes.Index(line, pfTimestamp)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(pfTimestamp):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 || j > 40 {
		return 0, false
	}
	return parseTS(string(rest[:j]))
}

func parseTS(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}
