package stats

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// rangeBounds returns [from,to) for a range ending now. Day ranges start at
// a local midnight so "7d" is today plus the six days before it.
func rangeBounds(r Range, now time.Time, sinceMs int64) (time.Time, time.Time) {
	today := midnight(now)
	switch r {
	case Range7d:
		return today.AddDate(0, 0, -6), now
	case Range30d:
		return today.AddDate(0, 0, -29), now
	default:
		if sinceMs == 0 {
			return today, now
		}
		return midnight(time.UnixMilli(sinceMs)), now
	}
}

// heatStart is the Monday 52 weeks before this week's: 53 week-columns.
func heatStart(now time.Time) time.Time { return mondayOf(now).AddDate(0, 0, -52*7) }

// Report computes everything the Stats screen shows for r. Read-only and
// safe to call from any goroutine except Bubble Tea's Update.
func (s *Store) Report(r Range, now time.Time) (Report, error) {
	rep := Report{Range: r, To: now, GeneratedAt: now, Coverage: Coverage{ClaudeOnly: true}}
	if s == nil {
		rep.From = midnight(now)
		return rep, nil
	}
	// Load enough history for the heatmap and the previous window; for the
	// all-time range that is everything.
	hs := heatStart(now)
	probeFrom, _ := rangeBounds(r, now, 0)
	loadFrom := hs
	if prev := probeFrom.Add(-now.Sub(probeFrom)); prev.Before(loadFrom) {
		loadFrom = prev
	}
	if r == RangeAll {
		loadFrom = time.Time{}
	}
	d, err := s.load(loadFromMs(loadFrom))
	if err != nil {
		return rep, err
	}
	since := d.since()
	from, to := rangeBounds(r, now, since)
	rep.From, rep.To = from, to
	if since != 0 {
		rep.Since = time.UnixMilli(since)
	}
	fromMs, toMs := from.UnixMilli(), to.UnixMilli()

	nb := bucketsOther
	if r == Range7d {
		nb = buckets7d
	}
	per := d.intervals(fromMs, toMs)
	sw := sweep(per, fromMs, toMs, nb)
	rep.Concurrency, rep.ConcurrencyPeak = sw.mean, sw.peakB
	if sw.busy > 0 {
		rep.Hero.Parallel = float64(sw.integral) / float64(sw.busy)
		rep.ShareSolo = float64(sw.byLevel[1]) / float64(sw.busy)
		rep.ShareTwo = float64(sw.byLevel[2]) / float64(sw.busy)
		rep.ShareThreePlus = float64(sw.byLevel[3]) / float64(sw.busy)
	}
	rep.Hero.Peak = sw.peak
	if sw.peak > 0 {
		rep.Hero.PeakAt = time.UnixMilli(sw.peakAt)
	}
	rep.Hero.AgentTime = time.Duration(sw.integral) * time.Millisecond

	if r != RangeAll {
		prevFrom := from.Add(-to.Sub(from))
		pp := sweep(d.intervals(prevFrom.UnixMilli(), fromMs), prevFrom.UnixMilli(), fromMs, 0)
		if pp.busy > 0 {
			rep.Hero.ParallelPrev = float64(pp.integral) / float64(pp.busy)
			rep.Hero.AgentTimePrev = time.Duration(pp.integral) * time.Millisecond
		}
	}

	// Tokens and dollars: turns that started inside the window.
	var turnsIn []turnData
	for _, t := range d.turns {
		if t.start >= fromMs && t.start < toMs {
			turnsIn = append(turnsIn, t)
		}
	}
	for _, t := range turnsIn {
		rep.Hero.Tokens += t.tokens()
		if c, ok := turnCost(t.model, t.in, t.out, t.cr, t.cw5, t.cw1); ok {
			rep.Hero.CostUSD += c
		}
	}

	replies := d.replies(fromMs, toMs, per)
	lat := latencies(replies, nil)
	rep.Hero.ReplyN = len(lat)
	if len(lat) >= MinSamples {
		rep.Hero.ReplyMedian = time.Duration(percentile(lat, 0.5)) * time.Millisecond
		rep.Hero.ReplyP90 = time.Duration(percentile(lat, 0.9)) * time.Millisecond
	}

	// Heatmap + streaks: always the last 53 weeks.
	dayActs := d.dayActions()
	dayAgent := map[string]int64{}
	splitByDay(d.intervals(hs.UnixMilli(), now.UnixMilli()), func(day time.Time, ms int64) {
		dayAgent[dayKey(day)] += ms
	})
	today := midnight(now)
	for day := hs; !day.After(today); day = day.AddDate(0, 0, 1) {
		k := dayKey(day)
		rep.Days = append(rep.Days, Day{Date: day, Actions: dayActs[k], AgentTime: time.Duration(dayAgent[k]) * time.Millisecond})
	}
	rep.Streak, rep.BestStreak = streaks(rep.Days)

	counts := d.actionCounts(from, to)
	rep.Activity = sortedActivity(counts)

	active := activeSessions(d, per, fromMs, toMs)
	rep.Repos, rep.Agents = breakdowns(d, per, active)
	rep.Records = records(d, turnsIn, sw, dayActs, from, to)
	rep.FunFacts = funFacts(d, &rep, per, replies, counts, turnsIn, fromMs)

	rep.Coverage.Sessions = len(active)
	if d.recordingSince != 0 {
		rep.Coverage.RecordingSince = time.UnixMilli(d.recordingSince)
	}
	if d.turnsSince != 0 {
		rep.Coverage.TurnsSince = time.UnixMilli(d.turnsSince)
	}
	return rep, nil
}

func loadFromMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// streaks: the run of active days ending today (or yesterday, when today
// has nothing yet — a streak isn't broken before the day is over) and the
// longest run in days.
func streaks(days []Day) (cur, best int) {
	run := 0
	for _, d := range days {
		if d.Actions > 0 || d.AgentTime > 0 {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	i := len(days) - 1
	if i >= 0 && days[i].Actions == 0 && days[i].AgentTime == 0 {
		i--
	}
	for ; i >= 0 && (days[i].Actions > 0 || days[i].AgentTime > 0); i-- {
		cur++
	}
	return cur, best
}

// activeSessions: every session with a turn, an attach or a status change
// in the window.
func activeSessions(d *dataset, per map[string][]ival, from, to int64) map[string]bool {
	out := map[string]bool{}
	for sid := range per {
		out[sid] = true
	}
	for _, a := range d.attaches {
		if a.start >= from && a.start < to {
			out[a.session] = true
		}
	}
	for _, e := range d.status {
		if e.at >= from && e.at < to {
			out[e.session] = true
		}
	}
	return out
}

func breakdowns(d *dataset, per map[string][]ival, active map[string]bool) ([]RepoTime, []AgentTime) {
	repos := map[string]*RepoTime{}
	agents := map[string]*AgentTime{}
	for sid := range active {
		si, ok := d.sessions[sid]
		if !ok {
			si = sessionInfo{agent: "claude"}
		}
		name := si.repo
		if name == "" {
			name = "other"
		}
		ms := time.Duration(ivalsTotal(per[sid])) * time.Millisecond
		if repos[name] == nil {
			repos[name] = &RepoTime{Name: name}
		}
		repos[name].AgentTime += ms
		repos[name].Sessions++
		if agents[si.agent] == nil {
			agents[si.agent] = &AgentTime{Agent: si.agent}
		}
		agents[si.agent].AgentTime += ms
		agents[si.agent].Sessions++
	}
	var rs []RepoTime
	for _, r := range repos {
		rs = append(rs, *r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].AgentTime != rs[j].AgentTime {
			return rs[i].AgentTime > rs[j].AgentTime
		}
		if rs[i].Sessions != rs[j].Sessions {
			return rs[i].Sessions > rs[j].Sessions
		}
		return rs[i].Name < rs[j].Name
	})
	var as []AgentTime
	for _, a := range agents {
		as = append(as, *a)
	}
	sort.Slice(as, func(i, j int) bool {
		if as[i].AgentTime != as[j].AgentTime {
			return as[i].AgentTime > as[j].AgentTime
		}
		return as[i].Agent < as[j].Agent
	})
	return rs, as
}

func dateLabel(t time.Time) string     { return t.In(time.Local).Format("Jan 2") }
func dateTimeLabel(t time.Time) string { return t.In(time.Local).Format("Jan 2 15:04") }

// withRepo appends the repo after the date, so a narrow column truncates the
// repo name rather than the date.
func withRepo(detail, repo string) string {
	if repo == "" {
		return detail
	}
	return detail + " · " + repo
}

func records(d *dataset, turnsIn []turnData, sw sweepOut, dayActs map[string]int, from, to time.Time) []Record {
	var out []Record

	var longest turnData
	for _, t := range turnsIn {
		if t.end-t.start > longest.end-longest.start {
			longest = t
		}
	}
	if longest.end > longest.start {
		at := time.UnixMilli(longest.start)
		out = append(out, Record{Label: "longest turn", Value: FormatDuration(time.Duration(longest.end-longest.start) * time.Millisecond),
			Detail: withRepo(dateLabel(at), d.sessions[longest.session].repo), At: at})
	}

	if sw.peak >= 2 {
		at := time.UnixMilli(sw.peakAt)
		out = append(out, Record{Label: "most at once", Value: fmt.Sprintf("%d sessions", sw.peak), Detail: dateTimeLabel(at), At: at})
	}

	bestDay, bestN := time.Time{}, 0
	spaceDay, spaceN := time.Time{}, 0
	for day := midnight(from); day.Before(to); day = day.AddDate(0, 0, 1) {
		k := dayKey(day)
		if n := dayActs[k]; n > bestN {
			bestDay, bestN = day, n
		}
		if n := d.actions[k][ActionSpaceJump]; n > spaceN {
			spaceDay, spaceN = day, n
		}
	}
	if bestN > 0 {
		out = append(out, Record{Label: "busiest day", Value: fmt.Sprintf("%s %s", formatInt(int64(bestN)), plural(bestN, "action", "actions")),
			Detail: dateLabel(bestDay), At: bestDay})
	}

	var la attachEv
	for _, a := range d.attaches {
		if a.start >= from.UnixMilli() && a.start < to.UnixMilli() && a.dur > la.dur {
			la = a
		}
	}
	if la.dur >= 1000 {
		at := time.UnixMilli(la.start)
		out = append(out, Record{Label: "longest attach", Value: FormatDuration(time.Duration(la.dur) * time.Millisecond),
			Detail: withRepo(dateLabel(at), d.sessions[la.session].repo), At: at})
	}

	if spaceN > 0 {
		out = append(out, Record{Label: "most Space jumps", Value: fmt.Sprintf("%d in a day", spaceN), Detail: dateLabel(spaceDay), At: spaceDay})
	}

	perDay := map[string]int{}
	for _, si := range d.sessions {
		if si.created >= from.UnixMilli() && si.created < to.UnixMilli() {
			perDay[dayKey(time.UnixMilli(si.created))]++
		}
	}
	newDay, newN := "", 0
	for k, n := range perDay {
		if n > newN || (n == newN && k < newDay) {
			newDay, newN = k, n
		}
	}
	if newN >= 2 {
		at, _ := time.ParseInLocation("2006-01-02", newDay, time.Local)
		out = append(out, Record{Label: "most sessions started", Value: fmt.Sprintf("%d in a day", newN), Detail: dateLabel(at), At: at})
	}
	return out
}

const maxFunFacts = 6

// minCompareSamples is the fewest measurements a comparative fun fact needs in
// EACH bucket it compares. MinSamples (5) is enough to show a median; it is not
// enough to claim one bucket differs from another.
const minCompareSamples = 10

// minFactDays is the fewest distinct local days a pattern must span before a
// fun fact states it: an afternoon's worth of replies, all from the first day
// of recording, is an anecdote rather than a habit.
const minFactDays = 3

// minRecordedDays is the fewest local calendar days fleet must have been
// RECORDING (recording_since through today, inclusive) before a fun fact that
// compares or ranks is stated at all, on top of each fact's own per-bucket
// gates. Those gates count samples, and a first weekend of recording can clear
// them with a burst of replies or one long session: "your replies get quicker
// when 2+ other agents are busy" on day three is a coincidence, not a habit.
const minRecordedDays = 7

// recordedDays is how many local calendar days recording has spanned by now,
// counting the first and the current day; 0 before anything was recorded.
func recordedDays(recordingSince int64, now time.Time) int {
	if recordingSince == 0 {
		return 0
	}
	first, today := midnight(time.UnixMilli(recordingSince)), midnight(now)
	n := 0
	for d := first; !d.After(today); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
}

// distinctDays counts the local calendar days the timestamps (ms) fall on.
func distinctDays(ms []int64) int {
	days := map[string]bool{}
	for _, t := range ms {
		days[dayKey(time.UnixMilli(t))] = true
	}
	return len(days)
}

// enoughToCompare: a bucket supports a comparison once it holds
// minCompareSamples replies spread over minFactDays days.
func enoughToCompare(xs []replySample) bool {
	if len(xs) < minCompareSamples {
		return false
	}
	at := make([]int64, len(xs))
	for i, x := range xs {
		at[i] = x.needAt
	}
	return distinctDays(at) >= minFactDays
}

// replySpeedFact compares reply latency when you were the only agent's
// audience against when 2+ other agents were busy. Empty unless both buckets
// hold enough replies (enoughToCompare) and the gap is at least 30%.
func replySpeedFact(replies []replySample) string {
	var calm, busy []replySample
	for _, r := range replies {
		switch {
		case r.others == 0:
			calm = append(calm, r)
		case r.others >= 2:
			busy = append(busy, r)
		}
	}
	if !enoughToCompare(calm) || !enoughToCompare(busy) {
		return ""
	}
	a, b := percentile(latencies(calm, nil), 0.5), percentile(latencies(busy, nil), 0.5)
	if a <= 0 {
		return ""
	}
	switch ratio := float64(b) / float64(a); {
	case ratio >= 1.3:
		return fmt.Sprintf("With 2+ other agents busy, your replies take %.1f× longer.", ratio)
	case ratio <= 1/1.3:
		return "Your replies get quicker when 2+ other agents are busy."
	}
	return ""
}

// busiestHourFact names the hour of day holding the most agent time. It ranks
// hours against each other, so it needs 2h of work spread over minFactDays
// days — one long session would otherwise crown whichever hour it ran through
// — and the winning hour must itself hold time from minFactDays days and beat
// the runner-up by busiestHourMargin: a near-tie names a coin flip.
func busiestHourFact(per map[string][]ival) string {
	var hours [24]int64
	var hourStarts [24][]int64
	var total int64
	var starts []int64
	for _, iv := range per {
		for _, x := range iv {
			for cur := x.s; cur < x.e; {
				t := time.UnixMilli(cur).In(time.Local)
				next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.Local).UnixMilli()
				end := min(x.e, next)
				hours[t.Hour()] += end - cur
				hourStarts[t.Hour()] = append(hourStarts[t.Hour()], cur)
				total += end - cur
				starts = append(starts, cur)
				cur = end
			}
		}
	}
	if total < (2*time.Hour).Milliseconds() || distinctDays(starts) < minFactDays {
		return ""
	}
	best, second := 0, -1
	for h := 1; h < 24; h++ {
		switch {
		case hours[h] > hours[best]:
			best, second = h, best
		case second < 0 || hours[h] > hours[second]:
			second = h
		}
	}
	if distinctDays(hourStarts[best]) < minFactDays ||
		float64(hours[best]) < busiestHourMargin*float64(hours[second]) {
		return ""
	}
	return fmt.Sprintf("Your agents are busiest between %02d:00 and %02d:00.", best, (best+1)%24)
}

// busiestHourMargin is how far ahead of the runner-up the busiest hour must be.
const busiestHourMargin = 1.2

func pct(x float64) int { return int(math.Round(x * 100)) }

// funFacts are rule-based and each gated on enough data; a thin window gets
// a short list rather than padding. The facts that compare or rank — the
// not-attached share, reply speed under load, the busiest hour — also wait for
// minRecordedDays of recording (seasoned); the descriptive ones do not.
func funFacts(d *dataset, rep *Report, per map[string][]ival, replies []replySample, counts map[string]int, turnsIn []turnData, fromMs int64) []string {
	seasoned := recordedDays(d.recordingSince, rep.To) >= minRecordedDays
	var out []string
	add := func(s string) {
		if len(out) < maxFunFacts {
			out = append(out, s)
		}
	}

	busy := rep.Hero.AgentTime
	if busy >= time.Hour {
		if share := rep.ShareTwo + rep.ShareThreePlus; share >= 0.05 {
			add(fmt.Sprintf("%d%% of the time an agent was working, another one was too.", pct(share)))
		}
	}

	// Work you weren't attached for — only measurable since attaches were
	// first recorded. It splits agent time into attached and not, so it wants
	// comparison-grade data: enough attaches, over enough days.
	if seasoned {
		attached := map[string][]ival{}
		var attachAt []int64
		for _, a := range d.attaches {
			attached[a.session] = append(attached[a.session], ival{a.start, a.start + a.dur})
			if a.start >= fromMs {
				attachAt = append(attachAt, a.start)
			}
		}
		var total, with int64
		for sid, iv := range per {
			var clipped []ival
			for _, x := range iv {
				if s := max(x.s, d.recordingSince); x.e > s {
					clipped = append(clipped, ival{s, x.e})
				}
			}
			total += ivalsTotal(clipped)
			with += overlap(clipped, mergeIvals(attached[sid]))
		}
		if len(attachAt) >= minCompareSamples && distinctDays(attachAt) >= minFactDays && total >= time.Hour.Milliseconds() {
			add(fmt.Sprintf("%d%% of agent work happened while you weren't attached to it.", pct(1-float64(with)/float64(total))))
		}
	}

	if seasoned {
		if f := replySpeedFact(replies); f != "" {
			add(f)
		}
	}

	if n := counts[ActionSpaceJump]; n >= MinSamples {
		add(fmt.Sprintf("You pressed Space %s times to jump to the next agent that needed you.", formatInt(int64(n))))
	}
	if n := counts[ActionQuickApprove]; n >= MinSamples {
		add(fmt.Sprintf("You approved %s prompts with Y without attaching.", formatInt(int64(n))))
	}

	var inputSide, cached int64
	for _, t := range turnsIn {
		inputSide += t.in + t.cr + t.cw5 + t.cw1
		cached += t.cr
	}
	if inputSide >= 1_000_000 && float64(cached)/float64(inputSide) >= 0.5 {
		add(fmt.Sprintf("%d%% of what your agents read came straight from the prompt cache.", pct(float64(cached)/float64(inputSide))))
	}

	if seasoned {
		if f := busiestHourFact(per); f != "" {
			add(f)
		}
	}
	return out
}

// Recap builds the weekly "Your week" reel for the Mon–Sun week starting at
// weekStart (local Monday 00:00). Cards is empty when the week has no data.
func (s *Store) Recap(weekStart time.Time) (Recap, error) {
	weekStart = mondayOf(weekStart)
	rc := Recap{WeekStart: weekStart}
	if s == nil {
		return rc, nil
	}
	weekEnd := weekStart.AddDate(0, 0, 7)
	prevStart := weekStart.AddDate(0, 0, -7)
	d, err := s.load(prevStart.UnixMilli())
	if err != nil {
		return rc, err
	}
	from, to := weekStart.UnixMilli(), weekEnd.UnixMilli()
	per := d.intervals(from, to)
	sw := sweep(per, from, to, 28) // 6-hour buckets

	// 1. Peak parallel moment.
	if sw.peak >= 2 {
		at := time.UnixMilli(sw.peakAt)
		var names []string
		seen := map[string]bool{}
		for sid, iv := range per {
			if contains(iv, sw.peakAt) {
				n := d.sessions[sid].repo
				if n != "" && !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
		sort.Strings(names)
		// The curve plots each bucket's peak, like the Stats chart: a 6-hour
		// bucket's mean sits under 1 and never reaches the peak the card states.
		spark := make([]float64, len(sw.peakB))
		for i, p := range sw.peakB {
			spark[i] = float64(p)
		}
		card := RecapCard{Big: fmt.Sprintf("%d", sw.peak), Caption: "agents at the same moment",
			Lines: []string{at.In(time.Local).Format("Monday 15:04")}, Spark: spark}
		if len(names) > 0 {
			if len(names) > 4 {
				names = append(names[:4], fmt.Sprintf("+%d", len(names)-4))
			}
			card.RepoLines = append(card.RepoLines, len(card.Lines))
			card.Lines = append(card.Lines, "in "+strings.Join(names, " · "))
		}
		rc.Cards = append(rc.Cards, card)
	}

	// 2. Agent hours + top repo.
	if sw.integral > 0 {
		var lines []string
		var repoLines []int
		repos, _ := breakdowns(d, per, activeSessions(d, per, from, to))
		if len(repos) > 0 && repos[0].Name != "other" && repos[0].AgentTime > 0 {
			repoLines = append(repoLines, len(lines))
			lines = append(lines, fmt.Sprintf("most of it in %s (%s)", repos[0].Name, FormatDuration(repos[0].AgentTime)))
		}
		prev := sweep(d.intervals(prevStart.UnixMilli(), from), prevStart.UnixMilli(), from, 0)
		if prev.integral > 0 {
			change := float64(sw.integral-prev.integral) / float64(prev.integral)
			switch {
			case change >= 0.05:
				lines = append(lines, fmt.Sprintf("up %d%% on the week before", pct(change)))
			case change <= -0.05:
				lines = append(lines, fmt.Sprintf("down %d%% on the week before", pct(-change)))
			default:
				lines = append(lines, "about the same as the week before")
			}
		}
		spark := make([]float64, 7)
		splitByDay(per, func(day time.Time, ms int64) {
			// Rounded: a week the clocks change in has a 23h or 25h day.
			if i := int(math.Round(day.Sub(weekStart).Hours() / 24)); i >= 0 && i < 7 {
				spark[i] += float64(ms) / float64(time.Hour.Milliseconds())
			}
		})
		rc.Cards = append(rc.Cards, RecapCard{Big: FormatDuration(time.Duration(sw.integral) * time.Millisecond), Caption: "of agent work", Lines: lines, Spark: spark, RepoLines: repoLines})
	}

	// 3. Reply median.
	lat := latencies(d.replies(from, to, per), nil)
	if len(lat) >= MinSamples {
		rc.Cards = append(rc.Cards, RecapCard{
			Big:     FormatDuration(time.Duration(percentile(lat, 0.5)) * time.Millisecond),
			Caption: "your median reply",
			Lines: []string{
				"p90 " + FormatDuration(time.Duration(percentile(lat, 0.9))*time.Millisecond),
				fmt.Sprintf("across %s replies", formatInt(int64(len(lat)))),
			},
		})
	}

	// 4. Dollars.
	var cost float64
	var tokens int64
	nTurns := 0
	for _, t := range d.turns {
		if t.start >= from && t.start < to {
			nTurns++
			tokens += t.tokens()
			if c, ok := turnCost(t.model, t.in, t.out, t.cr, t.cw5, t.cw1); ok {
				cost += c
			}
		}
	}
	if cost >= 1 {
		rc.Cards = append(rc.Cards, RecapCard{Big: formatUSD(cost), Caption: "of tokens at API list prices",
			Lines: []string{fmt.Sprintf("%s tokens across %s %s", formatTokens(tokens), formatInt(int64(nTurns)), plural(nTurns, "turn", "turns"))}})
	}

	// 5. Activity highlight.
	if act := sortedActivity(d.actionCounts(weekStart, weekEnd)); len(act) > 0 {
		var rest []string
		for _, a := range act[1:min(len(act), 3)] {
			rest = append(rest, fmt.Sprintf("%s %s", formatInt(int64(a.N)), lowerFirst(a.Label)))
		}
		card := RecapCard{Big: formatInt(int64(act[0].N)), Caption: lowerFirst(act[0].Label)}
		if len(rest) > 0 {
			card.Lines = []string{strings.Join(rest, " · ")}
		}
		rc.Cards = append(rc.Cards, card)
	}
	return rc, nil
}

// lowerFirst lowercases a label's first letter unless it starts an acronym or
// a key name ("Forks" → "forks"; "PRs opened" and "Space jumps" stay).
func lowerFirst(s string) string {
	if len(s) >= 2 && s[1] >= 'A' && s[1] <= 'Z' || strings.HasPrefix(s, "Space ") {
		return s
	}
	return strings.ToLower(s[:min(1, len(s))]) + s[min(1, len(s)):]
}
