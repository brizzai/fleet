package stats

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "projects")
	if err := os.MkdirAll(filepath.Join(root, "-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.roots = func() []string { return []string{root} }
	t.Cleanup(func() { s.Close() })
	return s, root
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func humanLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"secret prompt"},"timestamp":%q,"origin":{"kind":"human"},"isSidechain":false}`+"\n", ts(at))
}

func toolResultLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]},"timestamp":%q,"toolUseResult":{"stdout":"x"},"isSidechain":false}`+"\n", ts(at))
}

func assistantLine(at time.Time, id, model string, in, out, cr, cw int64, sidechain bool) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":%v,"timestamp":%q,"message":{"id":%q,"model":%q,"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":%d}}}}`+"\n",
		sidechain, ts(at), id, model, in, out, cr, cw, cw)
}

func durationLine(end time.Time, dur time.Duration, sidechain bool) string {
	return fmt.Sprintf(`{"type":"system","subtype":"turn_duration","durationMs":%d,"timestamp":%q,"isSidechain":%v}`+"\n", dur.Milliseconds(), ts(end), sidechain)
}

func noteClaude(s *Store, id, cid, repo string) {
	s.NoteSession(SessionMeta{ID: id, ClaudeSessionID: cid, Agent: "claude", Repo: repo, CreatedAt: time.Now()})
	s.Flush(time.Second)
}

func countRows(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	s.RecordStatus("a-1", "running", "waiting", time.Now())
	s.RecordAttach("a-1", time.Now(), time.Second)
	s.RecordAction(ActionSpaceJump)
	s.NoteSession(SessionMeta{ID: "a-1"})
	s.Flush(time.Millisecond)
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Report(Range7d, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recap(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordActionRejectsNonEnums(t *testing.T) {
	s, _ := openTest(t)
	for _, a := range []string{"space_jump", "Space Jump", "/Users/me/secret", "fix the login bug", "", "space_jump2", "space_jump"} {
		s.RecordAction(a)
	}
	s.Flush(time.Second)
	if n := countRows(t, s, `SELECT COUNT(*) FROM actions`); n != 1 {
		t.Fatalf("want 1 action row, got %d", n)
	}
	if n := countRows(t, s, `SELECT n FROM actions WHERE action = 'space_jump'`); n != 2 {
		t.Fatalf("want space_jump counted twice, got %d", n)
	}
}

func TestRecorderDoesNotBlockAndSurvivesClose(t *testing.T) {
	s, _ := openTest(t)
	start := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				s.RecordStatus(fmt.Sprintf("s%d-1", g), "running", "waiting", time.Now())
				s.RecordAction(ActionSpaceJump)
			}
		}(g)
	}
	wg.Wait()
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("recording 80k events took %v; the recorder must not block", el)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// After Close every call is a silent no-op, never a panic on a closed chan.
	s.RecordAction(ActionFork)
	s.RecordStatus("x-1", "a", "b", time.Now())
	s.Flush(time.Millisecond)
}

func TestNoteSessionStoresBaseNameOnly(t *testing.T) {
	s, _ := openTest(t)
	s.NoteSession(SessionMeta{ID: "abc-1", ClaudeSessionID: "c1", Agent: "claude", Repo: "/Users/me/code/client-secret/fleet"})
	s.NoteSession(SessionMeta{ID: "abc-1", ClaudeSessionID: "c2"}) // /clear rotation
	s.Flush(time.Second)
	var repo, cid string
	if err := s.db.QueryRow(`SELECT repo, claude_session_id FROM sessions WHERE id='abc-1'`).Scan(&repo, &cid); err != nil {
		t.Fatal(err)
	}
	if repo != "fleet" || cid != "c2" {
		t.Fatalf("repo=%q cid=%q", repo, cid)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM claude_ids WHERE session_id='abc-1'`); n != 2 {
		t.Fatalf("both claude ids must be kept, got %d", n)
	}
}

func TestSweepConcurrency(t *testing.T) {
	per := map[string][]ival{
		"a": mergeIvals([]ival{{0, 10}, {5, 15}}), // own overlap merges → [0,15)
		"b": {{5, 20}},
		"c": {{8, 9}},
		"d": {{20, 25}}, // back-to-back with b: not overlap
	}
	sw := sweep(per, 0, 40, 4)
	if sw.busy != 25 || sw.integral != 36 {
		t.Fatalf("busy=%d integral=%d", sw.busy, sw.integral)
	}
	if sw.peak != 3 || sw.peakAt != 8 {
		t.Fatalf("peak=%d at %d", sw.peak, sw.peakAt)
	}
	if sw.byLevel[1] != 15 || sw.byLevel[2] != 9 || sw.byLevel[3] != 1 {
		t.Fatalf("levels=%v", sw.byLevel)
	}
	// Bucket 0 = [0,10): a 10 + b 5 + c 1 = 16 / 10.
	if math.Abs(sw.mean[0]-1.6) > 1e-9 || sw.peakB[0] != 3 {
		t.Fatalf("bucket0 mean=%v peak=%d", sw.mean[0], sw.peakB[0])
	}
	if sw.mean[3] != 0 || sw.peakB[3] != 0 {
		t.Fatalf("empty bucket must be zero: %v %d", sw.mean[3], sw.peakB[3])
	}
}

func TestScanIncrementalDedupAndSidechain(t *testing.T) {
	s, root := openTest(t)
	s.now = func() time.Time { return time.Now().Add(-time.Hour) } // nothing is stale
	noteClaude(s, "sess-1", "cid1", "fleet")
	path := filepath.Join(root, "-proj", "cid1.jsonl")
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)

	var b strings.Builder
	b.WriteString(humanLine(t0))
	// One API message split over two lines: usage counted once.
	b.WriteString(assistantLine(t0.Add(time.Second), "m1", "claude-opus-5[1m]", 10, 100, 1000, 50, false))
	b.WriteString(assistantLine(t0.Add(2*time.Second), "m1", "claude-opus-5[1m]", 10, 100, 1000, 50, false))
	b.WriteString(toolResultLine(t0.Add(3 * time.Second)))
	b.WriteString(assistantLine(t0.Add(4*time.Second), "sub", "claude-haiku-4-5", 999, 999, 0, 0, true))
	b.WriteString(durationLine(t0.Add(9*time.Second), time.Hour, true)) // sidechain: ignored
	b.WriteString(assistantLine(t0.Add(5*time.Second), "m2", "claude-opus-5", 1, 20, 0, 0, false))
	b.WriteString(durationLine(t0.Add(10*time.Second), 10*time.Second, false))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// A transcript for a session fleet never launched is not read.
	if err := os.WriteFile(filepath.Join(root, "-proj", "other.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var last Progress
	if err := s.Scan(context.Background(), func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if !last.Finished || last.Total != 1 || last.Turns != 1 {
		t.Fatalf("progress %+v", last)
	}
	var in, out, cr, cw1, start, dur int64
	var model, sid string
	if err := s.db.QueryRow(`SELECT input_tokens, output_tokens, cache_read, cache_write_1h, start_ms, dur_ms, model, session_id FROM turns`).
		Scan(&in, &out, &cr, &cw1, &start, &dur, &model, &sid); err != nil {
		t.Fatal(err)
	}
	if in != 11 || out != 120 || cr != 1000 || cw1 != 50 || model != "claude-opus-5" || sid != "sess-1" {
		t.Fatalf("in=%d out=%d cr=%d cw1=%d model=%q sid=%q", in, out, cr, cw1, model, sid)
	}
	if start != t0.UnixMilli() || dur != 10000 {
		t.Fatalf("start=%d dur=%d", start, dur)
	}

	// Append a second turn plus a partial line still being written.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	t1 := t0.Add(time.Minute)
	f.WriteString(humanLine(t1))
	f.WriteString(assistantLine(t1.Add(time.Second), "m3", "claude-opus-5", 5, 5, 0, 0, false))
	f.WriteString(durationLine(t1.Add(30*time.Second), 30*time.Second, false))
	f.WriteString(`{"type":"user","timest`)
	f.Close()
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 2 {
		t.Fatalf("want 2 turns after append, got %d", n)
	}
	// Idempotent: nothing new, nothing duplicated.
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 2 {
		t.Fatalf("rescan duplicated turns: %d", n)
	}

	// Shrink (rewritten file): start over from byte 0.
	var small strings.Builder
	small.WriteString(humanLine(t0))
	small.WriteString(assistantLine(t0.Add(time.Second), "m9", "claude-sonnet-5", 1, 1, 0, 0, false))
	small.WriteString(durationLine(t0.Add(5*time.Second), 5*time.Second, false))
	if err := os.WriteFile(path, []byte(small.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 1 {
		t.Fatalf("want 1 turn after shrink, got %d", n)
	}
	// Deleting the transcript never deletes its turns: the cache is the archive.
	os.Remove(path)
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 1 {
		t.Fatalf("turns must outlive their transcript, got %d", n)
	}
}

func TestScanInterruptedTurnAndSymlinkDedup(t *testing.T) {
	s, root := openTest(t)
	noteClaude(s, "sess-1", "cid1", "fleet")
	// A second root reaching the same files through a symlink (account dirs).
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	s.roots = func() []string { return []string{root, alias} }

	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)
	var b strings.Builder
	b.WriteString(humanLine(t0))
	b.WriteString(assistantLine(t0.Add(20*time.Second), "m1", "claude-opus-5", 1, 1, 0, 0, false))
	// Esc: no turn_duration; the next prompt closes it at its last reply.
	b.WriteString(humanLine(t0.Add(time.Minute)))
	b.WriteString(assistantLine(t0.Add(61*time.Second), "m2", "claude-opus-5", 1, 1, 0, 0, false))
	b.WriteString(durationLine(t0.Add(70*time.Second), 10*time.Second, false))
	// And a trailing turn that never finished.
	b.WriteString(humanLine(t0.Add(2 * time.Minute)))
	b.WriteString(assistantLine(t0.Add(125*time.Second), "m3", "claude-opus-5", 1, 1, 0, 0, false))
	path := filepath.Join(root, "-proj", "cid1.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time { return time.Now().Add(-time.Hour) } // file is fresh
	var last Progress
	if err := s.Scan(context.Background(), func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Total != 1 {
		t.Fatalf("symlinked duplicate must be scanned once, total=%d", last.Total)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 2 {
		t.Fatalf("fresh file: dangling turn must wait, got %d turns", n)
	}
	if n := countRows(t, s, `SELECT dur_ms FROM turns ORDER BY start_ms LIMIT 1`); n != 20000 {
		t.Fatalf("interrupted turn should span prompt→last reply (20s), got %dms", n)
	}

	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) } // now stale
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns`); n != 3 {
		t.Fatalf("stale file: dangling turn closes, got %d turns", n)
	}
}

func TestScanHonoursCancel(t *testing.T) {
	s, root := openTest(t)
	noteClaude(s, "sess-1", "cid1", "")
	os.WriteFile(filepath.Join(root, "-proj", "cid1.jsonl"), []byte(humanLine(time.Now())), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Scan(ctx, nil); err == nil {
		t.Fatal("cancelled scan must return an error")
	}
}

func TestPricing(t *testing.T) {
	cases := []struct {
		model string
		in    float64
		ok    bool
	}{
		{"claude-opus-5", 5, true},
		{"claude-opus-5[1m]", 5, true},
		{"claude-opus-5-5", 4, true},
		{"claude-fable-5-1", 10, true},
		{"claude-opus-4-8", 5, true},
		{"claude-opus-4-1-20250805", 15, true},
		{"claude-sonnet-4-5-20250929", 3, true},
		{"claude-haiku-4-5-20251001", 1, true},
		{"opus", 0, false},
		{"<synthetic>", 0, false},
		{"claude-opus-50", 0, false},
	}
	for _, c := range cases {
		p, ok := priceFor(c.model)
		if ok != c.ok || p.Input != c.in {
			t.Errorf("%s: got %+v ok=%v", c.model, p, ok)
		}
	}
	// 1M in + 1M out + 1M cache read + 1M 1h cache write on Opus 5.
	got, _ := turnCost("claude-opus-5", 1e6, 1e6, 1e6, 0, 1e6)
	if want := 5 + 25 + 0.5 + 10.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("cost=%v want %v", got, want)
	}
	if _, ok := turnCost("mystery-model", 1, 1, 1, 1, 1); ok {
		t.Fatal("unknown model must not be priced")
	}
}

func TestReplyLatencyWithAwayCap(t *testing.T) {
	base := time.Date(2026, 9, 21, 9, 0, 0, 0, time.Local).UnixMilli()
	sec := int64(1000)
	d := &dataset{sessions: map[string]sessionInfo{}}
	for i := int64(0); i < 5; i++ {
		t0 := base + i*3600*sec
		d.status = append(d.status, statusEv{"a", "waiting", t0})
		d.attaches = append(d.attaches, attachEv{"a", t0 + (i+1)*10*sec, 5000})
		d.status = append(d.status, statusEv{"a", "running", t0 + 100*sec}) // after the attach: ignored
	}
	// Away: finished, back 40 minutes later — not a reply.
	d.status = append(d.status, statusEv{"b", "finished", base}, statusEv{"b", "running", base + 40*60*sec})
	// Never resolved: not a reply.
	d.status = append(d.status, statusEv{"c", "waiting", base})
	// A second need-transition before resolution doesn't restart the clock.
	d.status = append(d.status, statusEv{"e", "waiting", base}, statusEv{"e", "finished", base + 5*sec}, statusEv{"e", "running", base + 7*sec})

	rs := d.replies(base, base+24*3600*sec, nil)
	lat := latencies(rs, nil)
	want := []int64{7000, 10000, 20000, 30000, 40000, 50000}
	if fmt.Sprint(lat) != fmt.Sprint(want) {
		t.Fatalf("latencies %v want %v", lat, want)
	}
	if p := percentile(lat, 0.5); p != 20000 {
		t.Fatalf("median %d", p)
	}
	if p := percentile(lat, 0.9); p != 50000 {
		t.Fatalf("p90 %d", p)
	}
}

func TestStreaks(t *testing.T) {
	mk := func(pattern string) []Day {
		var ds []Day
		for _, c := range pattern {
			d := Day{}
			if c == '#' {
				d.Actions = 1
			}
			ds = append(ds, d)
		}
		return ds
	}
	cases := []struct {
		p         string
		cur, best int
	}{
		{"", 0, 0},
		{"..##.###", 3, 3},
		{"..##.###.", 3, 3}, // today empty: streak still alive through yesterday
		{"####..#..", 0, 4},
	}
	for _, c := range cases {
		cur, best := streaks(mk(c.p))
		if cur != c.cur || best != c.best {
			t.Errorf("%q: cur=%d best=%d", c.p, cur, best)
		}
	}
}

func TestReportOnEmptyDB(t *testing.T) {
	s, _ := openTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	for _, r := range Ranges {
		rep, err := s.Report(r, now)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Hero.Peak != 0 || rep.Hero.AgentTime != 0 || len(rep.Records) != 0 || len(rep.FunFacts) != 0 {
			t.Fatalf("%s: empty db produced data: %+v", r.Label(), rep.Hero)
		}
		if len(rep.Days) < 52*7 || len(rep.Days) > 53*7 {
			t.Fatalf("days=%d", len(rep.Days))
		}
		if rep.Days[0].Date.Weekday() != time.Monday || !rep.Days[len(rep.Days)-1].Date.Equal(midnight(now)) {
			t.Fatalf("heatmap must run Monday → today: %v … %v", rep.Days[0].Date, rep.Days[len(rep.Days)-1].Date)
		}
	}
	rc, err := s.Recap(LastFullWeekStart(now))
	if err != nil || len(rc.Cards) != 0 {
		t.Fatalf("empty recap: %v %+v", err, rc)
	}
}

func insertTurn(t *testing.T, s *Store, sid string, key int, start time.Time, dur time.Duration, model string, in, out int64) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO turns(file_key, line_offset, session_id, start_ms, dur_ms, model, input_tokens, output_tokens)
		VALUES('f',?,?,?,?,?,?,?)`, key, sid, start.UnixMilli(), dur.Milliseconds(), model, in, out); err != nil {
		t.Fatal(err)
	}
}

func TestReportAndRecap(t *testing.T) {
	s, _ := openTest(t)
	now := time.Date(2026, 9, 23, 18, 0, 0, 0, time.Local) // Wednesday
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.Local)
	s.NoteSession(SessionMeta{ID: "a-1", Agent: "claude", Repo: "fleet", CreatedAt: day.Add(9 * time.Hour)})
	s.NoteSession(SessionMeta{ID: "b-1", Agent: "claude", Repo: "api", CreatedAt: day.Add(9 * time.Hour)})
	s.NoteSession(SessionMeta{ID: "c-1", Agent: "codex", Repo: "fleet", CreatedAt: day.Add(10 * time.Hour)})
	s.now = func() time.Time { return day.Add(12 * time.Hour) }
	for i := 0; i < 6; i++ {
		s.RecordAction(ActionSpaceJump)
	}
	s.RecordAction(ActionFork)
	for i := 0; i < 5; i++ {
		t0 := day.Add(time.Duration(9+i) * time.Hour)
		s.RecordStatus("a-1", "running", "waiting", t0)
		s.RecordAttach("a-1", t0.Add(30*time.Second), 2*time.Minute)
	}
	s.Flush(time.Second)

	// a: 10:00–12:00, b: 11:00–11:30, c: 11:15–11:20 → peak 3 at 11:15.
	insertTurn(t, s, "a-1", 1, day.Add(10*time.Hour), 2*time.Hour, "claude-opus-5", 1_000_000, 100_000)
	insertTurn(t, s, "b-1", 2, day.Add(11*time.Hour), 30*time.Minute, "claude-opus-5", 0, 0)
	insertTurn(t, s, "c-1", 3, day.Add(11*time.Hour+15*time.Minute), 5*time.Minute, "gpt-5.1-codex", 5, 5)

	rep, err := s.Report(Range7d, now)
	if err != nil {
		t.Fatal(err)
	}
	h := rep.Hero
	if h.Peak != 3 || !h.PeakAt.Equal(day.Add(11*time.Hour+15*time.Minute)) {
		t.Fatalf("peak=%d at %v", h.Peak, h.PeakAt)
	}
	if h.AgentTime != 2*time.Hour+35*time.Minute {
		t.Fatalf("agent time %v", h.AgentTime)
	}
	if want := float64(155) / 120; math.Abs(h.Parallel-want) > 1e-9 {
		t.Fatalf("parallel %v want %v", h.Parallel, want)
	}
	if math.Abs(h.CostUSD-7.5) > 1e-9 { // 1M in × $5 + 100k out × $25; codex unpriced
		t.Fatalf("cost %v", h.CostUSD)
	}
	if h.ReplyN != 5 || h.ReplyMedian != 30*time.Second {
		t.Fatalf("replies n=%d median=%v", h.ReplyN, h.ReplyMedian)
	}
	if math.Abs(rep.ShareSolo+rep.ShareTwo+rep.ShareThreePlus-1) > 1e-9 {
		t.Fatal("shares must sum to 1")
	}
	if len(rep.Activity) == 0 || rep.Activity[0].Action != ActionSpaceJump || rep.Activity[0].Label != "Space jumps" || rep.Activity[0].N != 6 {
		t.Fatalf("activity %+v", rep.Activity)
	}
	if rep.Repos[0].Name != "fleet" || rep.Repos[0].Sessions != 2 {
		t.Fatalf("repos %+v", rep.Repos)
	}
	if rep.Agents[0].Agent != "claude" || rep.Agents[0].Sessions != 2 {
		t.Fatalf("agents %+v", rep.Agents)
	}
	labels := map[string]Record{}
	for _, r := range rep.Records {
		labels[r.Label] = r
	}
	for _, l := range []string{"longest turn", "most at once", "busiest day", "longest attach", "most Space jumps", "most sessions started"} {
		if _, ok := labels[l]; !ok {
			t.Errorf("missing record %q in %+v", l, rep.Records)
		}
	}
	if labels["longest turn"].Value != "2h 00m" || labels["busiest day"].Value != "12 actions" {
		t.Errorf("records %+v", rep.Records)
	}
	if rep.Hero.CostUSD < 7.49 || rep.Hero.CostUSD > 7.51 {
		t.Errorf("cost %v, want $7.50", rep.Hero.CostUSD)
	}
	for _, f := range rep.FunFacts {
		if strings.Contains(f, "$") {
			t.Errorf("the hero already shows the cost; a fun fact must not repeat it: %q", f)
		}
	}
	for _, f := range rep.FunFacts {
		if len(f) > 80 {
			t.Errorf("fun fact too long: %q", f)
		}
	}
	if rep.Streak != 1 || rep.Days[len(rep.Days)-2].Actions != 12 {
		t.Fatalf("streak %d, yesterday %+v", rep.Streak, rep.Days[len(rep.Days)-2])
	}
	if rep.Coverage.Sessions != 3 || rep.Coverage.RecordingSince.IsZero() || rep.Since.IsZero() {
		t.Fatalf("coverage %+v since %v", rep.Coverage, rep.Since)
	}
	if len(rep.Concurrency) != buckets7d {
		t.Fatalf("buckets %d", len(rep.Concurrency))
	}

	rc, err := s.Recap(mondayOf(day))
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Cards) != 5 {
		t.Fatalf("want 5 recap cards, got %d: %+v", len(rc.Cards), rc.Cards)
	}
	if c := rc.Cards[0]; c.Big != "3" || len(c.RepoLines) != 1 || c.Lines[c.RepoLines[0]] != "in api · fleet" {
		t.Errorf("peak card %+v", rc.Cards[0])
	}
	if c := rc.Cards[1]; len(c.RepoLines) != 1 || !strings.Contains(c.Lines[c.RepoLines[0]], "fleet") {
		t.Errorf("hours card must mark its repo line: %+v", c)
	}
	if rc.Cards[4].Big != "6" || rc.Cards[4].Caption != "Space jumps" {
		t.Errorf("activity card %+v", rc.Cards[4])
	}
	if empty, _ := s.Recap(mondayOf(day).AddDate(0, 0, -14)); len(empty.Cards) != 0 {
		t.Errorf("a week with no data must have no cards: %+v", empty.Cards)
	}
}

func TestLastFullWeekStart(t *testing.T) {
	wed := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)
	if got := LastFullWeekStart(wed); !got.Equal(time.Date(2026, 9, 14, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("got %v", got)
	}
	mon := time.Date(2026, 9, 21, 0, 30, 0, 0, time.Local)
	if got := LastFullWeekStart(mon); !got.Equal(time.Date(2026, 9, 14, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("monday: got %v", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		48 * time.Second:                "48s",
		47*time.Minute + 12*time.Second: "47m 12s",
		41*time.Hour + 12*time.Minute:   "41h 12m",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
	if formatUSD(1284.4) != "$1,284" || formatTokens(38_600_000) != "38.6M" {
		t.Error("formatting")
	}
}

func TestParseSplitsIdleGapsAndSkipsSynthetic(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)
	var b strings.Builder
	b.WriteString(humanLine(t0))
	b.WriteString(assistantLine(t0.Add(time.Minute), "m1", "claude-opus-5", 1, 1, 0, 0, false))
	// Parked on a permission prompt for 3 days, then the tool result lands.
	b.WriteString(toolResultLine(t0.Add(72 * time.Hour)))
	b.WriteString(assistantLine(t0.Add(72*time.Hour+time.Minute), "m2", "claude-opus-5", 1, 1, 0, 0, false))
	b.WriteString(durationLine(t0.Add(72*time.Hour+2*time.Minute), 72*time.Hour+2*time.Minute, false))
	// An interrupt marker is not a prompt; a synthetic reply is not activity.
	b.WriteString(humanLine(t0.Add(80 * time.Hour)))
	b.WriteString(assistantLine(t0.Add(80*time.Hour+10*time.Second), "m3", "claude-opus-5", 1, 1, 0, 0, false))
	fmt.Fprintf(&b, `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":%q,"interruptedMessageId":"m3"}`+"\n", ts(t0.Add(80*time.Hour+11*time.Second)))
	b.WriteString(assistantLine(t0.Add(300*time.Hour), "syn", "<synthetic>", 0, 0, 0, 0, false))
	b.WriteString(humanLine(t0.Add(300 * time.Hour)))

	turns, _, err := parseTranscript(context.Background(), strings.NewReader(b.String()), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var durs []time.Duration
	for _, r := range turns {
		durs = append(durs, time.Duration(r.durMs)*time.Millisecond)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 10 * time.Second}
	if fmt.Sprint(durs) != fmt.Sprint(want) {
		t.Fatalf("segments %v want %v", durs, want)
	}
	if turns[0].in != 0 || turns[1].in != 2 {
		t.Fatalf("tokens ride on the last segment: %+v", turns)
	}
	if turns[0].offset == turns[1].offset {
		t.Fatal("segments of one turn need distinct keys")
	}
}

// A fork's transcript repeats its parent's history, and a fork into another cwd
// stages a copy of the parent's transcript under the parent's id. Neither may
// count the parent's turns twice; the fork's first note (which carries the
// parent's Claude id) must not steal the parent's transcript; and stats.db
// never stores a transcript's path.
func TestForkHistoryCountsOnce(t *testing.T) {
	s, root := openTest(t)
	s.now = func() time.Time { return time.Now().Add(24 * time.Hour) } // everything stale
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)
	forkAt := t0.Add(time.Hour)
	s.NoteSession(SessionMeta{ID: "parent-1", ClaudeSessionID: "pcid", Agent: "claude", CreatedAt: t0.Add(-time.Minute)})
	s.Flush(time.Second)
	// The fork's first SessionStart reports the parent's id; later it adopts its own.
	s.NoteSession(SessionMeta{ID: "fork-1", ClaudeSessionID: "pcid", Agent: "claude", CreatedAt: forkAt})
	s.NoteSession(SessionMeta{ID: "fork-1", ClaudeSessionID: "fcid"})
	s.Flush(time.Second)

	parentTurn := humanLine(t0) + assistantLine(t0.Add(time.Second), "m1", "claude-opus-5", 1, 1, 0, 0, false) +
		durationLine(t0.Add(10*time.Minute), 10*time.Minute, false)
	forkTurn := humanLine(forkAt.Add(time.Minute)) + assistantLine(forkAt.Add(61*time.Second), "m2", "claude-opus-5", 1, 1, 0, 0, false) +
		durationLine(forkAt.Add(6*time.Minute), 5*time.Minute, false)
	other := filepath.Join(root, "-other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(root, "-proj", "pcid.jsonl"): parentTurn + humanLine(t0.Add(20*time.Minute)) +
			assistantLine(t0.Add(21*time.Minute), "m3", "claude-opus-5", 1, 1, 0, 0, false) + durationLine(t0.Add(22*time.Minute), 2*time.Minute, false),
		filepath.Join(other, "pcid.jsonl"): parentTurn, // the staged copy, frozen at the fork
		filepath.Join(other, "fcid.jsonl"): parentTurn + forkTurn,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM claude_ids WHERE claude_session_id='pcid' AND session_id='parent-1'`); n != 1 {
		t.Fatal("the fork's first note stole the parent's transcript")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM turns WHERE file_key LIKE '%/%' OR file_key LIKE '%jsonl%'`); n != 0 {
		t.Fatal("turns.file_key must not hold a path")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM scan_state WHERE path LIKE '%/%'`); n != 0 {
		t.Fatal("scan_state.path must not hold a path")
	}
	rep, err := s.Report(RangeAll, t0.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Parent 10m + 2m, fork 5m: the copied 10m counts once, not three times.
	if want := 17 * time.Minute; rep.Hero.AgentTime != want {
		t.Fatalf("agent time %v, want %v", rep.Hero.AgentTime, want)
	}
}

// A comparative fun fact needs enough replies in EACH bucket it compares,
// spread over several days: a first day of recording with a handful of busy
// replies once produced "your replies get quicker when 2+ agents are busy".
func TestReplySpeedFactNeedsEnoughInEachBucket(t *testing.T) {
	day := time.Date(2026, 9, 21, 10, 0, 0, 0, time.Local)
	mk := func(n, days, others int, lat time.Duration) []replySample {
		var out []replySample
		for i := range n {
			at := day.AddDate(0, 0, i%days).Add(time.Duration(i) * time.Minute)
			out = append(out, replySample{lat: lat.Milliseconds(), needAt: at.UnixMilli(), others: others})
		}
		return out
	}
	cases := []struct {
		name    string
		replies []replySample
		want    bool
	}{
		{"both buckets full", append(mk(10, 3, 0, time.Minute), mk(10, 3, 2, 3*time.Minute)...), true},
		{"busy bucket thin", append(mk(30, 3, 0, time.Minute), mk(9, 3, 2, 3*time.Minute)...), false},
		{"calm bucket thin", append(mk(9, 3, 0, time.Minute), mk(30, 3, 2, 3*time.Minute)...), false},
		{"one day only", append(mk(20, 1, 0, time.Minute), mk(20, 1, 2, 3*time.Minute)...), false},
		{"no real difference", append(mk(10, 3, 0, time.Minute), mk(10, 3, 2, time.Minute)...), false},
	}
	for _, c := range cases {
		if got := replySpeedFact(c.replies); (got != "") != c.want {
			t.Errorf("%s: fact %q, want shown=%v", c.name, got, c.want)
		}
	}
	if got := replySpeedFact(append(mk(10, 3, 0, 3*time.Minute), mk(10, 3, 2, time.Minute)...)); !strings.Contains(got, "quicker") {
		t.Errorf("faster when busy: %q", got)
	}
}

// "Busiest between" ranks hours against each other, so a single day's
// session must not crown an hour, nor may a near-tie or an hour whose lead
// comes from one day.
func TestBusiestHourFactNeedsSeveralDays(t *testing.T) {
	day := time.Date(2026, 9, 21, 16, 0, 0, 0, time.Local)
	// span works 16:00–17:30 on each of `days` days: 16:00 holds twice 17:00.
	span := func(days int) map[string][]ival {
		per := map[string][]ival{}
		for i := range days {
			s := day.AddDate(0, 0, i)
			per["a"] = append(per["a"], ival{s.UnixMilli(), s.Add(90 * time.Minute).UnixMilli()})
		}
		return per
	}
	if got := busiestHourFact(span(1)); got != "" {
		t.Errorf("one day of work produced %q", got)
	}
	if got := busiestHourFact(span(3)); !strings.Contains(got, "16:00 and 17:00") {
		t.Errorf("three days of 16:00–17:30 work: %q", got)
	}

	// Near-tie: 16:00 and 17:00 each hold 60m a day, 16:00 a hair more.
	tie := map[string][]ival{}
	for i := range 3 {
		s := day.AddDate(0, 0, i)
		tie["a"] = append(tie["a"], ival{s.UnixMilli(), s.Add(2 * time.Hour).UnixMilli()})
	}
	tie["b"] = []ival{{day.UnixMilli(), day.Add(5 * time.Minute).UnixMilli()}}
	if got := busiestHourFact(tie); got != "" {
		t.Errorf("near-tie between two hours produced %q", got)
	}

	// The winning hour's time all comes from one day: 09:00 on day 0 holds
	// 4h across four agents (a clear lead over 16:00's 3h), while the 16:00
	// work spans three days.
	one := span(3)
	nine := day.Add(-7 * time.Hour)
	for _, k := range []string{"w", "x", "y", "z"} {
		one[k] = []ival{{nine.UnixMilli(), nine.Add(time.Hour).UnixMilli()}}
	}
	if got := busiestHourFact(one); got != "" {
		t.Errorf("a winning hour from a single day produced %q", got)
	}
}

// Facts that compare or rank wait for a week of recording, however many
// samples a first few days pile up; descriptive facts don't wait.
func TestComparativeFactsNeedAWeekOfRecording(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.Local)
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.Local)
	var replies []replySample
	per := map[string][]ival{}
	var attaches []attachEv
	for i := range 3 {
		d := time.Date(day.Year(), day.Month(), day.Day()+i, 16, 0, 0, 0, time.Local)
		per["a"] = append(per["a"], ival{d.UnixMilli(), d.Add(90 * time.Minute).UnixMilli()})
		for k := range 5 {
			at := d.Add(time.Duration(k) * time.Minute)
			replies = append(replies,
				replySample{lat: time.Minute.Milliseconds(), needAt: at.UnixMilli(), others: 0},
				replySample{lat: (3 * time.Minute).Milliseconds(), needAt: at.UnixMilli(), others: 2})
			attaches = append(attaches, attachEv{session: "b", start: at.UnixMilli(), dur: time.Minute.Milliseconds()})
		}
	}
	counts := map[string]int{ActionSpaceJump: 40}
	facts := func(recordingSince time.Time) string {
		d := &dataset{recordingSince: recordingSince.UnixMilli(), attaches: attaches}
		rep := &Report{To: now, Hero: Hero{AgentTime: 4 * time.Hour}}
		return strings.Join(funFacts(d, rep, per, replies, counts, nil, day.UnixMilli()), "\n")
	}
	comparative := []string{"longer", "busiest between", "weren't attached"}

	young := facts(now.AddDate(0, 0, -5)) // 6 recorded days
	for _, c := range comparative {
		if strings.Contains(young, c) {
			t.Errorf("6 days of recording stated a %q fact:\n%s", c, young)
		}
	}
	if !strings.Contains(young, "pressed Space") {
		t.Errorf("a descriptive fact waited for a week:\n%s", young)
	}

	week := facts(now.AddDate(0, 0, -6)) // 7 recorded days
	for _, c := range comparative {
		if !strings.Contains(week, c) {
			t.Errorf("7 days of recording still hid the %q fact:\n%s", c, week)
		}
	}
	if n := recordedDays(0, now); n != 0 {
		t.Errorf("nothing recorded: %d days", n)
	}
}
