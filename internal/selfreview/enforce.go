package selfreview

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Enforce applies fleet's rules to a parsed run, in place, and returns a note
// for every correction it made. The model writes the first draft; these rules
// decide what reaches a human:
//
//   - every free-text field is cleaned to one line and held to a hard cap (cut
//     with "…"; the verdict at a clause boundary) — the prose budgets proper
//     are Lint's, enforced by a repair call, never by cutting; decide always
//     ends in "?";
//   - every stop is kept: a decision is never hidden, however many there are
//     (more than MaxStops is only a warning); stop ids unique;
//   - a checked item with no valid source is dropped; a "ci" source fleet
//     cannot confirm from in.CI is demoted to "claim"; a checked item that is
//     really unfinished verification ("CI still running", or a near-copy of a
//     pending line) moves to pending — it is not evidence; a checked item
//     claiming CI passed while fleet's CI is pending or failing is dropped:
//     it contradicts what the human sees;
//   - hunks must name a changed file; a range outside the diff falls back to
//     the file as a whole;
//   - classifier skips are merged in as "by rule"; a high-risk file is never
//     skipped "by guide"; a guide skip's kind must fit the path, a source file
//     of 30+ changed lines may only be skipped as "mechanical", and
//     "mechanical" stops under 80 lines — a refused skip becomes Not covered
//     with fleet's reason (NotCoveredWhy); a rule-skipped test file whose
//     patch removes assertions carries fleet's Note saying so;
//   - a changed file in no stop and no skip is Not covered — computed here,
//     never taken from the model;
//   - every line count is computed here;
//   - round ≥2: a stop re-asking an answered one is dropped, and every
//     previous "change" gets a followup (unreported when the model gave none);
//   - an intent gap is a decision, never a banner: the scope stop goes first,
//     and fleet makes one from intent_gap when the model wrote none
//     (ScopeStop); its why lists everything missing and is never cut;
//   - a recommend is "keep" or "change" (anything else is dropped); prose
//     the person reads in full — summary, how, elsewhere, recommend, intent
//     gap, chapter text — is never cut: Lint holds those budgets;
//   - every changed file sits in exactly one chapter: a file named twice
//     stays in the first, files named nowhere go in an "Other changes"
//     chapter fleet adds, and each chapter lists every decision whose code
//     it holds;
//   - a guide skip of a file with at most SmallSkipMaxLines changed lines is
//     never refused over its kind: nobody needs to read a one-line change
//     because the reviewer called it config.
func Enforce(run *Run, in Input) []string {
	e := enforcer{run: run, in: in}
	run.Reviewer, run.NotCoveredWhy = nil, nil // computed by fleet, never the model's
	e.index()
	e.scalars()
	e.stops()
	e.scope()
	e.skips()
	e.chapters()
	e.followups()
	e.count()
	return e.warn
}

// Upgrade brings a stored run to what the screens expect, without the inputs
// Enforce needs: a run from before the scope decision existed gets one made
// from its intent gap. Idempotent; a nil run is left alone.
func Upgrade(run *Run) {
	if run == nil {
		return
	}
	for _, s := range run.Stops {
		if s.IsScope() {
			return
		}
	}
	if gap := cleanLine(run.IntentGap); gap != "" {
		run.Stops = append([]Stop{ScopeStop(gap)}, run.Stops...)
	}
}

// ScopeStopID is the id of the scope decision fleet makes from an intent gap.
const ScopeStopID = "scope"

// ScopeStop is the scope decision fleet makes from an intent gap the reviewer
// did not turn into one itself. Yes keeps the change as it is and tracks the
// rest as a follow-up. There is no recommendation: fleet doesn't know.
func ScopeStop(gap string) Stop {
	return Stop{
		ID: ScopeStopID, Kind: KindScope,
		Title:  "Not everything asked for is done",
		Why:    gap,
		Decide: "Ship without the rest and track it as a follow-up?",
	}
}

// SmallSkipMaxLines is the size under which a guide skip stands whatever
// kind the reviewer gave it.
const SmallSkipMaxLines = 5

type enforcer struct {
	run  *Run
	in   Input
	warn []string

	byPath  map[string]int // path (and old path) → index into in.Files
	patches []parsedPatch
}

func (e *enforcer) warnf(format string, a ...any) {
	e.warn = append(e.warn, fmt.Sprintf(format, a...))
}

func (e *enforcer) index() {
	e.byPath = make(map[string]int, len(e.in.Files)*2)
	e.patches = make([]parsedPatch, len(e.in.Files))
	for i, f := range e.in.Files {
		e.byPath[f.Path] = i
		e.patches[i] = parsePatch(f.Patch)
	}
	for i, f := range e.in.Files {
		if _, taken := e.byPath[f.OldPath]; f.OldPath != "" && !taken {
			e.byPath[f.OldPath] = i
		}
	}
}

// resolve maps a path as the model wrote it to a changed file.
func (e *enforcer) resolve(p string) (int, bool) {
	p = cleanLine(p)
	p = strings.Trim(p, "`'\"")
	for _, pre := range []string{"./", "a/", "b/", "/"} {
		if i, ok := e.byPath[strings.TrimPrefix(p, pre)]; ok {
			return i, true
		}
	}
	i, ok := e.byPath[p]
	return i, ok
}

func (e *enforcer) scalars() {
	r := e.run
	r.V = SchemaVersion
	var cut bool
	r.Verdict, cut = capVerdict(cleanLine(r.Verdict), VerdictHardCap)
	if cut {
		e.warnf("verdict over %d words, cut", VerdictHardCap)
	}
	if r.Verdict == "" {
		e.warnf("no verdict")
	}

	r.Risk = strings.ToLower(cleanLine(r.Risk))
	switch r.Risk {
	case "medium", "moderate":
		r.Risk = "med"
	}
	if !oneOf(r.Risk, riskLevels) {
		e.warnf("risk %q is not low|med|high, set to med", r.Risk)
		r.Risk = "med"
	}

	// Never cut: the scope decision lists every missing item from it.
	r.IntentGap = cleanLine(r.IntentGap)
	r.Summary = cleanLine(r.Summary)
	if strings.EqualFold(strings.Trim(r.IntentGap, ". "), "none") {
		r.IntentGap = ""
	}

	var pending []string
	for _, p := range r.Pending {
		p, _ = capWords(cleanLine(p), PendingMaxWords)
		if p != "" {
			pending = append(pending, p)
		}
	}
	if len(pending) > MaxPending {
		e.warnf("%d pending items, kept %d", len(pending), MaxPending)
		pending = pending[:MaxPending]
	}
	r.Pending = pending
}

func (e *enforcer) stops() {
	answered := e.answeredBefore()
	seenID := map[string]bool{}
	var kept []Stop
	for n, s := range e.run.Stops {
		label := fmt.Sprintf("stop %d", n+1)
		s.Title = cleanLine(s.Title)
		s.Decide = cleanLine(s.Decide)
		s.Why = cleanLine(s.Why)
		s.Kind = strings.ToLower(cleanLine(s.Kind))
		s.How = cleanLine(s.How)
		s.Elsewhere = cleanLine(s.Elsewhere)
		s.Recommend = e.recommend(label, s.Recommend)
		var missing []string
		for _, m := range s.Missing {
			if m = cleanLine(m); m != "" {
				missing = append(missing, m)
			}
		}
		s.Missing = missing

		if s.Title == "" && s.Decide == "" {
			e.warnf("%s has no title and no question, dropped", label)
			continue
		}
		if answered[normKey(s.Title)] || answered[normKey(s.Decide)] {
			e.warnf("%s re-asks an answered stop (%q), dropped", label, s.Title)
			continue
		}
		if !oneOf(s.Kind, stopKinds) {
			e.warnf("%s kind %q unknown, set to logic", label, s.Kind)
			s.Kind = "logic"
		}

		var cut bool
		if s.Title == "" {
			s.Title, _ = capWords(strings.TrimSuffix(s.Decide, "?"), TitleHardCap)
		}
		if s.Title, cut = capWords(s.Title, TitleHardCap); cut {
			e.warnf("%s title over %d words, cut", label, TitleHardCap)
		}
		if s.Kind == KindScope {
			// The scope decision's why is the list of what is missing: the
			// person decides on all of it, so none of it is cut.
			if len(s.Hunks) > 0 {
				e.warnf("%s is the scope decision; its hunks dropped", label)
				s.Hunks = nil
			}
		} else if s.Why, cut = capWords(s.Why, WhyHardCap); cut {
			e.warnf("%s why over %d words, cut", label, WhyHardCap)
		}
		s.Decide = e.decide(label, s)
		s.Checked = e.checked(label, s.Checked)
		s.Hunks = e.hunks(label, s.Hunks)

		s.ID = cleanLine(s.ID)
		if s.ID == "" || seenID[s.ID] || strings.HasPrefix(s.ID, "file:") {
			old := s.ID
			for i := len(kept) + 1; ; i++ {
				if id := fmt.Sprintf("s%d", i); !seenID[id] && !e.idUsedLater(id, n) {
					s.ID = id
					break
				}
			}
			if old != "" {
				e.warnf("%s id %q duplicated, renamed %s", label, old, s.ID)
			}
		}
		seenID[s.ID] = true
		kept = append(kept, s)
	}
	// Never hide a decision: a reviewer that asks too many questions is a
	// problem to see, not to paper over by dropping the last ones.
	if len(kept) > MaxStops {
		e.warnf("%d stops, over the %d the reviewer was asked for; all kept", len(kept), MaxStops)
	}
	e.run.MoreStops = 0
	e.run.Stops = kept
}

// recommend normalises the reviewer's own answer: "keep" or "change" (with
// the change, never cut), or nil when it gave none fleet can read.
func (e *enforcer) recommend(label string, r *Recommend) *Recommend {
	if r == nil {
		return nil
	}
	a := normRecommend(r.Answer)
	if !oneOf(a, recommends) {
		if a != "" {
			e.warnf("%s recommend %q is not keep|change, dropped", label, a)
		}
		return nil
	}
	return &Recommend{Answer: a, Text: cleanLine(r.Text)}
}

// scope puts the scope decision first — it is about whether this is the right
// change at all — keeps only one, and makes one from intent_gap when the
// reviewer gave a gap but no decision for it.
func (e *enforcer) scope() {
	var scope *Stop
	var rest []Stop
	for _, s := range e.run.Stops {
		if s.IsScope() {
			if scope != nil {
				e.warnf("second scope stop %q dropped", s.Title)
				continue
			}
			sc := s
			scope = &sc
			continue
		}
		rest = append(rest, s)
	}
	if scope == nil && e.run.IntentGap != "" {
		sc := ScopeStop(e.run.IntentGap)
		for _, s := range rest {
			if s.ID == sc.ID {
				sc.ID = "scope-gap"
			}
		}
		if e.answeredBefore()[normKey(sc.Title)] {
			e.warnf("intent gap re-asks an answered scope decision, not raised again")
			return
		}
		e.warnf("intent_gap without a scope stop: made one")
		scope = &sc
	}
	if scope == nil {
		return
	}
	e.run.Stops = append([]Stop{*scope}, rest...)
}

// chapters holds the walkthrough to the changed files: every file in exactly
// one chapter (the first that names it), an "Other changes" chapter for the
// rest, chapters in reading order, and each listing the decisions whose code
// it holds. A run with no chapters keeps none — there is nothing to walk.
func (e *enforcer) chapters() {
	if len(e.run.Chapters) == 0 {
		e.run.Chapters = nil
		e.warnf("no chapters: the walkthrough is empty")
		return
	}
	claimed := map[int]bool{}
	seenID := map[string]bool{}
	var out []Chapter
	for n, c := range e.run.Chapters {
		label := fmt.Sprintf("chapter %d", n+1)
		c.Title = cleanLine(c.Title)
		c.Text = cleanLine(c.Text)
		c.Role = strings.ToLower(cleanLine(c.Role))
		c.ByFleet = false
		if !oneOf(c.Role, roles) {
			e.warnf("%s role %q unknown, set to glue", label, c.Role)
			c.Role = RoleGlue
		}
		var files []string
		for _, p := range c.Files {
			i, ok := e.resolve(p)
			switch {
			case !ok:
				e.warnf("%s names %q, not a changed file, dropped", label, cleanLine(p))
			case claimed[i]:
				e.warnf("%s: %s is already in an earlier chapter", label, e.in.Files[i].Path)
			default:
				claimed[i] = true
				files = append(files, e.in.Files[i].Path)
			}
		}
		if len(files) == 0 {
			e.warnf("%s has no changed file of its own, dropped", label)
			continue
		}
		c.Files = files
		c.ID = cleanLine(c.ID)
		if c.ID == "" || seenID[c.ID] {
			c.ID = fmt.Sprintf("c%d", len(out)+1)
		}
		seenID[c.ID] = true
		out = append(out, c)
	}
	sort.SliceStable(out, func(a, b int) bool { return roleRank(out[a].Role) < roleRank(out[b].Role) })
	var other []string
	for i, f := range e.in.Files {
		if !claimed[i] {
			other = append(other, f.Path)
		}
	}
	if len(other) > 0 {
		// Last, whatever its role: it is fleet's, not part of the story.
		e.warnf("%d %s in no chapter: added to Other changes", len(other), plural(len(other), "file", "files"))
		id := "other"
		for seenID[id] {
			id += "x"
		}
		out = append(out, Chapter{ID: id, Title: "Other changes", Role: RoleGlue, Files: other, ByFleet: true})
	}
	for i := range out {
		out[i].Decisions = e.chapterDecisions(out[i])
	}
	e.run.Chapters = out
}

func roleRank(role string) int {
	for i, r := range roles {
		if r == role {
			return i
		}
	}
	return len(roles)
}

// chapterDecisions is every decision the chapter names or whose code it
// holds, in decision order. The scope decision is in none.
func (e *enforcer) chapterDecisions(c Chapter) []string {
	named := map[string]bool{}
	for _, id := range c.Decisions {
		named[cleanLine(id)] = true
	}
	files := map[string]bool{}
	for _, p := range c.Files {
		files[p] = true
	}
	var out []string
	for _, s := range e.run.Stops {
		if s.IsScope() {
			continue
		}
		in := named[s.ID]
		for _, h := range s.Hunks {
			if files[h.Path] {
				in = true
			}
		}
		if in {
			out = append(out, s.ID)
		}
	}
	return out
}

// idUsedLater keeps a renamed duplicate from taking an id a later stop
// already uses legitimately.
func (e *enforcer) idUsedLater(id string, after int) bool {
	for _, s := range e.run.Stops[after+1:] {
		if cleanLine(s.ID) == id {
			return true
		}
	}
	return false
}

func (e *enforcer) decide(label string, s Stop) string {
	d := s.Decide
	if d == "" {
		// "yes" always means keep the code as it is.
		if s.Kind == "bug" {
			d = "Keep it as is?"
		} else {
			d = "OK as is?"
		}
		e.warnf("%s has no question, set %q", label, d)
		return d
	}
	d, cut := capWords(d, DecideHardCap)
	if cut {
		e.warnf("%s decide over %d words, cut", label, DecideHardCap)
	}
	if !strings.HasSuffix(d, "?") {
		d = strings.TrimRightFunc(d, func(r rune) bool { return r == '.' || r == '!' || r == ':' || r == ';' || unicode.IsSpace(r) }) + "?"
		if !cut {
			e.warnf("%s decide did not end with ?, fixed", label)
		}
	}
	return d
}

func (e *enforcer) checked(label string, in []Check) []Check {
	var out []Check
	for _, c := range in {
		c.Text = cleanLine(c.Text)
		c.Src = strings.ToLower(cleanLine(c.Src))
		if c.Text == "" {
			continue
		}
		if !oneOf(c.Src, checkSrcs) {
			e.warnf("%s checked item without a valid source (%q), dropped", label, c.Src)
			continue
		}
		if e.isPending(c.Text) {
			e.addPending(c.Text)
			e.warnf("%s checked item is pending verification, not evidence: moved to pending", label)
			continue
		}
		if ciContradicts(c.Text, e.in.CI) {
			e.warnf("%s checked item says CI passed, but CI is %s: dropped", label, ciWord(e.in.CI.State))
			continue
		}
		if c.Src == "ci" && !ciConfirms(c.Text, e.in.CI) {
			c.Src, c.Demoted = "claim", true
			e.warnf("%s ci claim not confirmed by CI (%s), shown as claim", label, ciWord(e.in.CI.State))
		}
		var cut bool
		if c.Text, cut = capWords(c.Text, CheckedMaxWords); cut {
			e.warnf("%s checked item over %d words, cut", label, CheckedMaxWords)
		}
		out = append(out, c)
	}
	if len(out) > MaxChecked {
		e.warnf("%s has %d checked items, kept %d", label, len(out), MaxChecked)
		out = out[:MaxChecked]
	}
	return out
}

// pendingPhrase matches wording that says verification has not finished.
var pendingPhrase = regexp.MustCompile(`(?i)\b(still running|is running|not run|not yet|pending|in progress|waiting for)\b`)

// isPending reports whether a checked item is unfinished verification: it
// says so in words, or it near-duplicates something already in pending.
func (e *enforcer) isPending(text string) bool {
	if pendingPhrase.MatchString(text) {
		return true
	}
	for _, p := range e.run.Pending {
		if nearDuplicate(text, p) {
			return true
		}
	}
	return false
}

// addPending records an item in pending unless it is already there in other
// words, holding the list to its budgets.
func (e *enforcer) addPending(text string) {
	for _, p := range e.run.Pending {
		if nearDuplicate(text, p) {
			return
		}
	}
	if len(e.run.Pending) >= MaxPending {
		e.warnf("pending is full (%d), dropped %q", MaxPending, text)
		return
	}
	p, _ := capWords(text, PendingMaxWords)
	e.run.Pending = append(e.run.Pending, p)
}

// fillerWords carry no meaning for nearDuplicate.
var fillerWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true, "be": true,
	"of": true, "on": true, "in": true, "to": true, "for": true, "and": true, "or": true,
	"it": true, "its": true, "this": true, "that": true, "with": true, "against": true,
}

func contentWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !fillerWords[w] {
			out[w] = true
		}
	}
	return out
}

// nearDuplicate reports whether two short lines say the same thing: the same
// letters, or at least two meaningful words shared and at least three
// quarters of the shorter line's words found in the longer.
func nearDuplicate(a, b string) bool {
	if ka, kb := normKey(a), normKey(b); ka != "" && ka == kb {
		return true
	}
	wa, wb := contentWords(a), contentWords(b)
	if len(wa) > len(wb) {
		wa, wb = wb, wa
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return shared >= 2 && shared*4 >= len(wa)*3
}

// ciConfirms reports whether fleet's own CI rollup backs a "ci" check. Only a
// finished rollup can: success backs anything that doesn't claim a failure,
// failure backs a line that names a failing check or says something failed.
func ciConfirms(text string, ci CIInfo) bool {
	t := strings.ToLower(text)
	failing := strings.Contains(t, "fail") || strings.Contains(t, "broken")
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if w == "red" || w == "error" || w == "errors" {
			failing = true
		}
	}
	switch ci.State {
	case CISuccess:
		return !failing
	case CIFailure:
		for _, f := range ci.Failed {
			if f = strings.ToLower(strings.TrimSpace(f)); f != "" && strings.Contains(t, f) {
				return true
			}
		}
		return failing
	}
	return false
}

// ciSays matches a line that talks about CI (or the PR's checks).
var ciSays = regexp.MustCompile(`(?i)\b(ci|checks?|pipeline|github actions|workflow)\b`)

// ciPassedSays matches a line claiming it passed.
var ciPassedSays = regexp.MustCompile(`(?i)\b(green|pass(es|ed|ing)?|succeed(s|ed)?|successful|all good)\b`)

// ciContradicts reports a checked item claiming CI passed while fleet's own
// rollup says it is still running or failing. Whatever its source, it
// contradicts the CI status the human sees next to it, so it is not shown.
func ciContradicts(text string, ci CIInfo) bool {
	if ci.State != CIPending && ci.State != CIFailure {
		return false
	}
	return ciSays.MatchString(text) && ciPassedSays.MatchString(text) && !strings.Contains(strings.ToLower(text), "fail")
}

func ciWord(state string) string {
	if state == CIUnknown {
		return "unknown"
	}
	return state
}

func (e *enforcer) hunks(label string, in []Hunk) []Hunk {
	var out []Hunk
	seen := map[Hunk]bool{}
	for _, h := range in {
		i, ok := e.resolve(h.Path)
		if !ok {
			e.warnf("%s hunk names %q, not a changed file, dropped", label, cleanLine(h.Path))
			continue
		}
		f := e.in.Files[i]
		h.Path = f.Path
		h.Side = strings.ToLower(cleanLine(h.Side))
		if h.Side != "old" {
			h.Side = "new"
		}
		if h.Start > h.End {
			h.Start, h.End = h.End, h.Start
		}
		if h.Start <= 0 && h.End > 0 {
			h.Start = 1
		}
		if h.Start <= 0 {
			h.Start, h.End = 0, 0
		}
		if !h.fileLevel() && f.Patch != "" && !e.patches[i].intersects(h) {
			e.warnf("%s hunk %s:%d-%d is outside the diff, anchored to the file", label, f.Path, h.Start, h.End)
			h.Start, h.End = 0, 0
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// answeredBefore is the set of previous stops the human settled (fine,
// change, ask), keyed by normalised title and question. "later" is deferred,
// not settled, so it may come back.
func (e *enforcer) answeredBefore() map[string]bool {
	out := map[string]bool{}
	if e.in.Previous == nil {
		return out
	}
	settled := map[string]bool{}
	for _, a := range e.in.PreviousAnswers {
		if a.Kind == AnswerFine || a.Kind == AnswerChange || a.Kind == AnswerAsk {
			settled[a.StopID] = true
		}
	}
	for _, s := range e.in.Previous.Stops {
		if settled[s.ID] {
			if k := normKey(s.Title); k != "" {
				out[k] = true
			}
			if k := normKey(s.Decide); k != "" {
				out[k] = true
			}
		}
	}
	return out
}

func normKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (e *enforcer) skips() {
	inStop := e.filesInStops()
	byFile := map[int]Skip{}
	refused := map[int]string{}
	for _, s := range e.run.Skipped {
		i, ok := e.resolve(s.Path)
		if !ok {
			e.warnf("skip names %q, not a changed file, dropped", cleanLine(s.Path))
			continue
		}
		if _, dup := byFile[i]; dup {
			continue
		}
		if _, dup := refused[i]; dup {
			continue
		}
		f := e.in.Files[i]
		s.Path = f.Path
		s.By, s.Note = "guide", "" // Note is fleet's, never the model's
		if f.RuleSkip != "" {
			s.By, s.Reason, s.Kind = "rule", cleanLine(f.RuleSkip), ""
			s.Note = ruleSkipNote(f)
		} else if f.HighRisk {
			if inStop[i] {
				e.warnf("%s is high-risk; the rest of it cannot be skipped by guide", f.Path)
			} else {
				e.warnf("%s is high-risk and cannot be skipped by guide; not covered", f.Path)
				refused[i] = highRiskRefusal(s)
			}
			continue
		} else {
			var cut bool
			if s.Reason, cut = capWords(cleanLine(s.Reason), SkipReasonMaxWord); cut {
				e.warnf("skip reason for %s over %d words, cut", f.Path, SkipReasonMaxWord)
			}
			s.Kind = e.skipKind(s, f.Path)
			if why := skipRefusal(s, f.Path, e.skipLines(i, inStop[i])); why != "" {
				if inStop[i] {
					e.warnf("%s: rest of file %s; not skipped", f.Path, why)
				} else {
					e.warnf("%s: %s; not covered", f.Path, why)
					refused[i] = why
				}
				continue
			}
		}
		byFile[i] = s
	}
	for i, f := range e.in.Files {
		if _, ok := byFile[i]; !ok && f.RuleSkip != "" {
			byFile[i] = Skip{Path: f.Path, Reason: cleanLine(f.RuleSkip), By: "rule", Note: ruleSkipNote(f)}
		}
	}

	var skipped []Skip
	var notCovered []string
	for i, f := range e.in.Files {
		s, ok := byFile[i]
		switch {
		case ok:
			s.Scope = "file"
			if inStop[i] {
				s.Scope = "rest"
			}
			skipped = append(skipped, s)
		case !inStop[i]:
			notCovered = append(notCovered, f.Path)
			if why := refused[i]; why != "" {
				if e.run.NotCoveredWhy == nil {
					e.run.NotCoveredWhy = map[string]string{}
				}
				e.run.NotCoveredWhy[f.Path] = why
			}
		}
	}
	e.run.Skipped = skipped
	e.run.NotCovered = notCovered
}

// skipKind normalises the model's kind. An unknown or missing one is inferred
// from the path for a docs/fixture/config/generated file, and left empty for
// a source file — where the size rule then decides whether it may stand.
func (e *enforcer) skipKind(s Skip, p string) string {
	k := strings.ToLower(cleanLine(s.Kind))
	switch k {
	case "doc", "documentation":
		k = SkipDocs
	case "fixtures", "testdata", "test data":
		k = SkipFixture
	}
	if oneOf(k, skipKinds) {
		return k
	}
	if k != "" {
		e.warnf("skip kind %q for %s unknown, ignored", k, p)
	}
	return fileClass(p)
}

// skipLines is what a skip of file i claims: all of its changed lines, or for
// scope "rest" the ones outside every stop.
func (e *enforcer) skipLines(i int, rest bool) int {
	total := e.fileLines(i)
	if !rest {
		return total
	}
	var hs []Hunk
	for _, s := range e.run.Stops {
		for _, h := range s.Hunks {
			if h.Path == e.in.Files[i].Path {
				hs = append(hs, h)
			}
		}
	}
	return max(total-e.coveredLines(i, hs), 0)
}

func (e *enforcer) filesInStops() map[int]bool {
	out := map[int]bool{}
	for _, s := range e.run.Stops {
		for _, h := range s.Hunks {
			if i, ok := e.byPath[h.Path]; ok {
				out[i] = true
			}
		}
	}
	return out
}

func (e *enforcer) followups() {
	if e.in.Previous == nil {
		e.run.Prev = nil
		return
	}
	want := map[string]bool{}
	var order []string
	for _, a := range e.in.PreviousAnswers {
		if a.Kind == AnswerChange && !want[a.StopID] {
			want[a.StopID] = true
			order = append(order, a.StopID)
		}
	}
	got := map[string]Followup{}
	for _, f := range e.run.Prev {
		f.ID = cleanLine(f.ID)
		f.Status = strings.ToLower(cleanLine(f.Status))
		if !want[f.ID] {
			e.warnf("prev entry %q is not a previous change, dropped", f.ID)
			continue
		}
		if !oneOf(f.Status, followupSts) {
			e.warnf("prev entry %q status %q unknown, unreported", f.ID, f.Status)
			f.Status = "unreported"
		}
		f.Note, _ = capWords(cleanLine(f.Note), FollowupMaxWords)
		if _, dup := got[f.ID]; !dup {
			got[f.ID] = f
		}
	}
	var out []Followup
	for _, id := range order {
		f, ok := got[id]
		if !ok {
			e.warnf("previous change %s not reported on", id)
			f = Followup{ID: id, Status: "unreported"}
		}
		out = append(out, f)
	}
	e.run.Prev = out
}

// count computes every line figure the view shows. A file-level anchor, or a
// stop on a file with no text diff, claims the whole file: that is what the
// human is shown.
func (e *enforcer) count() {
	r := e.run
	r.TotalFiles = len(e.in.Files)
	r.TotalLines, r.StopLines, r.SkippedLines, r.SkippedFiles = 0, 0, 0, 0

	covered := make([]int, len(e.in.Files)) // union over all stops
	for i, f := range e.in.Files {
		var hs []Hunk
		for _, s := range r.Stops {
			for _, h := range s.Hunks {
				if h.Path == f.Path {
					hs = append(hs, h)
				}
			}
		}
		covered[i] = e.coveredLines(i, hs)
	}
	for si := range r.Stops {
		n := 0
		for i, f := range e.in.Files {
			var hs []Hunk
			for _, h := range r.Stops[si].Hunks {
				if h.Path == f.Path {
					hs = append(hs, h)
				}
			}
			n += e.coveredLines(i, hs)
		}
		r.Stops[si].Lines = n
	}

	skipScope := map[string]string{}
	for _, s := range r.Skipped {
		skipScope[s.Path] = s.Scope
	}
	for i, f := range e.in.Files {
		total := e.fileLines(i)
		r.TotalLines += total
		r.StopLines += covered[i]
		switch skipScope[f.Path] {
		case "file":
			r.SkippedFiles++
			r.SkippedLines += total
		case "rest":
			r.SkippedLines += total - covered[i]
		}
	}
	r.UncoveredLines = r.TotalLines - r.StopLines - r.SkippedLines
}

func (e *enforcer) fileLines(i int) int {
	if n := e.in.Files[i].Lines(); n > 0 {
		return n
	}
	return len(e.patches[i].lines)
}

func (e *enforcer) coveredLines(i int, hs []Hunk) int {
	if len(hs) == 0 {
		return 0
	}
	p := e.patches[i]
	total := e.fileLines(i)
	if len(p.lines) == 0 {
		return total
	}
	for _, h := range hs {
		if h.fileLevel() {
			return total
		}
	}
	n := 0
	for _, l := range p.lines {
		for _, h := range hs {
			if h.covers(l) {
				n++
				break
			}
		}
	}
	return min(n, total)
}

// AssertionsRemovedNote is fleet's flag on a rule-skipped test file whose
// patch deletes assertions: a test change that weakens what is checked is not
// "safe to skip" just because it lives in a test file.
const AssertionsRemovedNote = "tests changed — assertions removed"

// ruleSkipNote is fleet's own note on a rule skip, or "".
func ruleSkipNote(f FileInput) string {
	if f.RemovedAssertions > 0 && strings.Contains(strings.ToLower(f.RuleSkip), "test") {
		return AssertionsRemovedNote
	}
	return ""
}

// assertionLine matches a source line that asserts something, in the test
// styles fleet sees most: Go (t.Error/t.Fatal, testify, gomega), Python
// (assert, self.assert*, mock assert_called*), JS/TS (expect, should), C++
// (EXPECT_/ASSERT_).
var assertionLine = regexp.MustCompile(`\b(assert\w*|expect\w*|require\.\w+|t\.(Error|Errorf|Fatal|Fatalf|Fail)\b|self\.assert\w*|should|EXPECT_\w+|ASSERT_\w+|Expect\(|Ω\()`)

// RemovedAssertions counts the assertion lines a unified diff deletes.
func RemovedAssertions(patch string) int {
	n := 0
	for _, l := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(l, "-") || strings.HasPrefix(l, "---") {
			continue
		}
		if assertionLine.MatchString(l[1:]) {
			n++
		}
	}
	return n
}
