package stats

import (
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"time"
)

// awayCap: a reply gap longer than this means you were away, not slow, and
// is left out of the latency figures entirely.
const awayCap = 30 * time.Minute

// Bucket counts for the concurrency curve, per range.
const (
	buckets7d    = 168 // hourly
	bucketsOther = 120
)

type ival struct{ s, e int64 } // [s,e) in unix ms

type turnData struct {
	session               string
	start, end            int64
	model                 string
	in, out, cr, cw5, cw1 int64
}

func (t turnData) tokens() int64 { return t.in + t.out + t.cr + t.cw5 + t.cw1 }

type sessionInfo struct {
	agent, repo string
	created     int64
}

type statusEv struct {
	session, to string
	at          int64
}

type attachEv struct {
	session    string
	start, dur int64
}

// dataset is everything a Report or Recap reads, loaded in one pass.
type dataset struct {
	turns          []turnData
	sessions       map[string]sessionInfo
	status         []statusEv                // sorted by at
	attaches       []attachEv                // sorted by start
	actions        map[string]map[string]int // day key → action → n
	recordingSince int64
	turnsSince     int64
}

// ownTurns restricts a `turns t` query to turns inside their session's own
// lifetime (see load).
const ownTurns = `LEFT JOIN sessions s ON s.id = t.session_id WHERE t.start_ms >= COALESCE(s.created_at_ms, 0)`

func (s *Store) load(fromMs int64) (*dataset, error) {
	d := &dataset{sessions: map[string]sessionInfo{}, actions: map[string]map[string]int{}}

	// A turn that starts before its fleet session was created is history the
	// transcript copied in — a fork's `--fork-session` transcript repeats its
	// parent's turns — not work done in that session, so it never counts.
	// DISTINCT folds a transcript re-read under a second file key.
	rows, err := s.db.Query(`SELECT DISTINCT t.session_id, t.start_ms, t.dur_ms, t.model, t.input_tokens, t.output_tokens,
		t.cache_read, t.cache_write, t.cache_write_1h FROM turns t `+ownTurns+`
		AND t.start_ms + t.dur_ms >= ? ORDER BY t.start_ms`, fromMs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t turnData
		var dur int64
		if err := rows.Scan(&t.session, &t.start, &dur, &t.model, &t.in, &t.out, &t.cr, &t.cw5, &t.cw1); err != nil {
			rows.Close()
			return nil, err
		}
		t.end = t.start + dur
		d.turns = append(d.turns, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(`SELECT id, agent, repo, created_at_ms FROM sessions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var si sessionInfo
		if err := rows.Scan(&id, &si.agent, &si.repo, &si.created); err != nil {
			rows.Close()
			return nil, err
		}
		if si.agent == "" {
			si.agent = "claude"
		}
		d.sessions[id] = si
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT session_id, to_status, at_ms FROM status_events WHERE at_ms >= ? ORDER BY at_ms`, fromMs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e statusEv
		if err := rows.Scan(&e.session, &e.to, &e.at); err != nil {
			rows.Close()
			return nil, err
		}
		d.status = append(d.status, e)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT session_id, started_at_ms, dur_ms FROM attaches WHERE started_at_ms >= ? ORDER BY started_at_ms`, fromMs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a attachEv
		if err := rows.Scan(&a.session, &a.start, &a.dur); err != nil {
			rows.Close()
			return nil, err
		}
		d.attaches = append(d.attaches, a)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT day, action, n FROM actions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day, action string
		var n int
		if err := rows.Scan(&day, &action, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if action == ActionAttach {
			continue // attaches are counted from their own table
		}
		if d.actions[day] == nil {
			d.actions[day] = map[string]int{}
		}
		d.actions[day][action] += n
	}
	rows.Close()

	var v string
	switch err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'recording_since'`).Scan(&v); {
	case err == nil:
		d.recordingSince, _ = strconv.ParseInt(v, 10, 64)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var minStart sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(t.start_ms) FROM turns t ` + ownTurns).Scan(&minStart); err != nil {
		return nil, err
	}
	d.turnsSince = minStart.Int64
	return d, nil
}

// since is the earliest moment any data exists.
func (d *dataset) since() int64 {
	switch {
	case d.recordingSince == 0:
		return d.turnsSince
	case d.turnsSince == 0:
		return d.recordingSince
	default:
		return min(d.recordingSince, d.turnsSince)
	}
}

// intervals returns each session's agent-busy time clipped to [from,to),
// with a session's own overlapping turns merged — a session is one lane,
// however its turns are recorded.
func (d *dataset) intervals(from, to int64) map[string][]ival {
	per := map[string][]ival{}
	for _, t := range d.turns {
		s, e := max(t.start, from), min(t.end, to)
		if e <= s {
			continue
		}
		per[t.session] = append(per[t.session], ival{s, e})
	}
	for k, iv := range per {
		per[k] = mergeIvals(iv)
	}
	return per
}

func mergeIvals(iv []ival) []ival {
	if len(iv) < 2 {
		return iv
	}
	sort.Slice(iv, func(i, j int) bool { return iv[i].s < iv[j].s })
	out := iv[:1]
	for _, x := range iv[1:] {
		last := &out[len(out)-1]
		if x.s <= last.e {
			last.e = max(last.e, x.e)
			continue
		}
		out = append(out, x)
	}
	return out
}

func ivalsTotal(iv []ival) int64 {
	var n int64
	for _, x := range iv {
		n += x.e - x.s
	}
	return n
}

// overlap is the total length of a ∩ b, both sorted and non-overlapping.
func overlap(a, b []ival) int64 {
	var n int64
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		lo, hi := max(a[i].s, b[j].s), min(a[i].e, b[j].e)
		if hi > lo {
			n += hi - lo
		}
		if a[i].e < b[j].e {
			i++
		} else {
			j++
		}
	}
	return n
}

func contains(iv []ival, t int64) bool {
	k := sort.Search(len(iv), func(i int) bool { return iv[i].e > t })
	return k < len(iv) && iv[k].s <= t
}

type sweepOut struct {
	busy, integral int64    // ms with c>=1; ∫c dt
	byLevel        [4]int64 // ms at c==1, c==2, c>=3 (index 3)
	peak           int
	peakAt         int64
	mean           []float64
	peakB          []int
}

// sweep computes concurrency c(t) — the number of sessions with a turn in
// flight — across [from,to), bucketed into nb equal slots.
func sweep(per map[string][]ival, from, to int64, nb int) sweepOut {
	type ev struct {
		t int64
		d int
	}
	var evs []ev
	for _, iv := range per {
		for _, x := range iv {
			evs = append(evs, ev{x.s, 1}, ev{x.e, -1})
		}
	}
	// Ends before starts at the same instant: back-to-back is not overlap.
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].d < evs[j].d
	})
	out := sweepOut{}
	var sums []float64
	var width float64
	if nb > 0 && to > from {
		out.mean = make([]float64, nb)
		out.peakB = make([]int, nb)
		sums = make([]float64, nb)
		width = float64(to-from) / float64(nb)
	}
	bound := func(k int) int64 { return from + int64(float64(k)*width) }
	seg := func(a, b int64, c int) {
		dur := b - a
		out.busy += dur
		out.integral += int64(c) * dur
		out.byLevel[min(c, 3)] += dur
		if sums == nil {
			return
		}
		ka := clampInt(int(float64(a-from)/width), 0, nb-1)
		kb := clampInt(int(float64(b-1-from)/width), 0, nb-1)
		for k := ka; k <= kb; k++ {
			lo, hi := bound(k), bound(k+1)
			if k == nb-1 {
				hi = to
			}
			if ov := min(b, hi) - max(a, lo); ov > 0 {
				sums[k] += float64(c) * float64(ov)
				if c > out.peakB[k] {
					out.peakB[k] = c
				}
			}
		}
	}
	c := 0
	prev := from
	for _, e := range evs {
		if e.t > prev && c > 0 {
			seg(prev, e.t, c)
		}
		if e.t > prev {
			prev = e.t
		}
		c += e.d
		if c > out.peak {
			out.peak, out.peakAt = c, e.t
		}
	}
	for k := range sums {
		hi := bound(k + 1)
		if k == nb-1 {
			hi = to
		}
		if w := hi - bound(k); w > 0 {
			out.mean[k] = sums[k] / float64(w)
		}
	}
	return out
}

func clampInt(v, lo, hi int) int { return max(lo, min(v, hi)) }

type replySample struct {
	lat    int64
	needAt int64
	others int // other sessions busy when this one started needing you
}

// replies measures, per session, the time from entering waiting/finished to
// the next attach of that session or its next move back to running. Gaps
// with no closing event, or longer than awayCap, are not replies.
func (d *dataset) replies(from, to int64, per map[string][]ival) []replySample {
	type item struct {
		at int64
		to string // "" = attach
	}
	streams := map[string][]item{}
	for _, e := range d.status {
		streams[e.session] = append(streams[e.session], item{e.at, e.to})
	}
	for _, a := range d.attaches {
		streams[a.session] = append(streams[a.session], item{a.start, ""})
	}
	var out []replySample
	for sid, st := range streams {
		sort.SliceStable(st, func(i, j int) bool { return st[i].at < st[j].at })
		var need int64
		resolve := func(at int64) {
			lat := at - need
			if need >= from && need < to && lat >= 0 && lat <= awayCap.Milliseconds() {
				others := 0
				for other, iv := range per {
					if other != sid && contains(iv, need) {
						others++
					}
				}
				out = append(out, replySample{lat, need, others})
			}
			need = 0
		}
		for _, it := range st {
			switch it.to {
			case "":
				if need != 0 {
					resolve(it.at)
				}
			case "running":
				if need != 0 {
					resolve(it.at)
				}
			case "waiting", "finished":
				if need == 0 {
					need = it.at
				}
			case "error", "suspended", "starting":
				need = 0 // the session went away; nobody was waiting on you
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].needAt < out[j].needAt })
	return out
}

// percentile is the nearest-rank p-th percentile of sorted xs.
func percentile(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	k := int(p*float64(len(xs))+0.999999) - 1
	return xs[clampInt(k, 0, len(xs)-1)]
}

func latencies(samples []replySample, keep func(replySample) bool) []int64 {
	var xs []int64
	for _, s := range samples {
		if keep == nil || keep(s) {
			xs = append(xs, s.lat)
		}
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs
}

// dayActions is fleet actions + attaches per local day key.
func (d *dataset) dayActions() map[string]int {
	out := map[string]int{}
	for day, m := range d.actions {
		for _, n := range m {
			out[day] += n
		}
	}
	for _, a := range d.attaches {
		out[dayKey(time.UnixMilli(a.start))]++
	}
	return out
}

// actionCounts sums each action over local days in [from,to), attaches
// included.
func (d *dataset) actionCounts(from, to time.Time) map[string]int {
	out := map[string]int{}
	fromKey, toKey := dayKey(from), dayKey(to.Add(-time.Millisecond))
	for day, m := range d.actions {
		if day < fromKey || day > toKey {
			continue
		}
		for a, n := range m {
			out[a] += n
		}
	}
	for _, a := range d.attaches {
		if a.start >= from.UnixMilli() && a.start < to.UnixMilli() {
			out[ActionAttach]++
		}
	}
	return out
}

func sortedActivity(counts map[string]int) []ActionCount {
	var out []ActionCount
	for a, n := range counts {
		if n > 0 {
			out = append(out, ActionCount{Action: a, Label: ActionLabel(a), N: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Action < out[j].Action
	})
	return out
}

// splitByDay adds each interval's length to the local day it falls in.
func splitByDay(per map[string][]ival, add func(day time.Time, ms int64)) {
	for _, iv := range per {
		for _, x := range iv {
			cur := x.s
			for cur < x.e {
				t := time.UnixMilli(cur).In(time.Local)
				day := midnight(t)
				next := day.AddDate(0, 0, 1).UnixMilli()
				end := min(x.e, next)
				add(day, end-cur)
				cur = end
			}
		}
	}
}

func midnight(t time.Time) time.Time {
	t = t.In(time.Local)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// mondayOf is local midnight of the Monday starting t's week.
func mondayOf(t time.Time) time.Time {
	m := midnight(t)
	off := (int(m.Weekday()) + 6) % 7
	return m.AddDate(0, 0, -off)
}

// LastFullWeekStart is local Monday 00:00 of the last complete Mon–Sun week.
func LastFullWeekStart(now time.Time) time.Time {
	return mondayOf(now).AddDate(0, 0, -7)
}
