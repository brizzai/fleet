package selfreview

import (
	"fmt"
	"regexp"
	"strings"
)

// Lint checks the reviewer's prose against the rules a person reading it
// depends on, and returns one plain problem per violation ("stop s2 decide:
// 17 words (max 15)"). It never edits the run: the runner sends the problems
// back for one repair call, and whichever version has fewer problems is kept.
// Wording never fails a review.
//
// The rules (the prompt states them; this is how fleet checks them):
//
//   - verdict: starts with "Ready", "Ready once …" or "Not ready: …"; at most
//     VerdictMaxWords; no code identifiers;
//   - title: the consequence, at most TitleMaxWords, no code identifiers;
//   - why: at most WhyMaxWords in at most WhyMaxSentences sentences, no code
//     identifiers (they belong in checked items);
//   - decide: ONE yes/no question ("yes" keeps the code as is), at most
//     DecideMaxWords, ending in "?", never two asks or an "or" alternative;
//   - summary: present, at most SummaryMaxWords in SummaryMaxSentences
//     sentences, no code identifiers;
//   - every decision has a recommend (keep, or change with the change named
//     in at most RecommendMaxWords) and a how (at most HowMaxWords); an
//     elsewhere, when given, is at most ElsewhereMaxWords and cites a path;
//   - an intent_gap has its scope decision, first; a scope decision's why
//     may list every missing item (ScopeWhyMaxWords) and needs no how;
//   - chapters: present, at most MaxChapters, each with a known role, a
//     title of at most ChapterTitleMaxWords, a text of at most
//     ChapterTextMaxWords and at least one file.
//
// It reads cleaned but uncut text, so call it before Enforce.
func Lint(run *Run) []string {
	if run == nil {
		return nil
	}
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }

	v := cleanLine(run.Verdict)
	switch {
	case v == "":
		add("verdict: missing")
	default:
		if !verdictPrefix.MatchString(v) {
			add(`verdict: must start with "Ready", "Ready once …" or "Not ready: …"`)
		}
		if n := wordCount(v); n > VerdictMaxWords {
			add("verdict: %d words (max %d)", n, VerdictMaxWords)
		}
		if id := firstIdentifier(v); id != "" {
			add("verdict: code identifier %q; say it in plain words", id)
		}
	}

	sum := cleanLine(run.Summary)
	switch {
	case sum == "":
		add("summary: missing; say what the change does in plain words")
	default:
		if n := wordCount(sum); n > SummaryMaxWords {
			add("summary: %d words (max %d)", n, SummaryMaxWords)
		}
		if n := sentenceCount(sum); n > SummaryMaxSentences {
			add("summary: %d sentences (max %d)", n, SummaryMaxSentences)
		}
		if id := firstIdentifier(sum); id != "" {
			add("summary: code identifier %q; say it in plain words", id)
		}
	}

	scopeAt := -1
	for i, s := range run.Stops {
		if strings.EqualFold(cleanLine(s.Kind), KindScope) && scopeAt < 0 {
			scopeAt = i
		}
	}
	if cleanLine(run.IntentGap) != "" && scopeAt < 0 {
		add(`intent_gap: add a stop of kind "scope" first for it, with decide "Ship without … and track it as a follow-up?"`)
	}
	if scopeAt > 0 {
		add(`stop %s: the scope stop must come first`, cleanLine(run.Stops[scopeAt].ID))
	}

	for i, s := range run.Stops {
		name := cleanLine(s.ID)
		if name == "" {
			name = fmt.Sprintf("%d", i+1)
		}
		label := "stop " + name

		t := cleanLine(s.Title)
		if n := wordCount(t); n > TitleMaxWords {
			add("%s title: %d words (max %d)", label, n, TitleMaxWords)
		}
		if id := firstIdentifier(t); id != "" {
			add("%s title: code identifier %q; name the consequence in plain words", label, id)
		}

		scope := strings.EqualFold(cleanLine(s.Kind), KindScope)
		w := cleanLine(s.Why)
		switch {
		case scope:
			if n := wordCount(w); n > ScopeWhyMaxWords {
				add("%s why: %d words (max %d)", label, n, ScopeWhyMaxWords)
			}
			for _, m := range s.Missing {
				if n := wordCount(cleanLine(m)); n > MissingItemMaxWords {
					add("%s missing item: %d words (max %d)", label, n, MissingItemMaxWords)
				}
			}
		default:
			if n := wordCount(w); n > WhyMaxWords {
				add("%s why: %d words (max %d)", label, n, WhyMaxWords)
			}
			if n := sentenceCount(w); n > WhyMaxSentences {
				add("%s why: %d sentences (max %d: what happens, then who is hurt)", label, n, WhyMaxSentences)
			}
			h := cleanLine(s.How)
			if h == "" {
				add("%s how: missing; explain how the mechanism around this decision works", label)
			} else if n := wordCount(h); n > HowMaxWords {
				add("%s how: %d words (max %d)", label, n, HowMaxWords)
			}
		}
		if e := cleanLine(s.Elsewhere); e != "" {
			if n := wordCount(e); n > ElsewhereMaxWords {
				add("%s elsewhere: %d words (max %d)", label, n, ElsewhereMaxWords)
			}
			if !citesPath(e) {
				add("%s elsewhere: cite the path of the comparable code you read", label)
			}
		}
		switch r := s.Recommend; {
		case r == nil || cleanLine(r.Answer) == "":
			add(`%s recommend: missing; answer your own question: "keep", or "change" with the change`, label)
		case !oneOf(normRecommend(r.Answer), recommends):
			add(`%s recommend: answer %q must be "keep" or "change"`, label, cleanLine(r.Answer))
		default:
			t := cleanLine(r.Text)
			if normRecommend(r.Answer) == RecommendChange && t == "" {
				add("%s recommend: name the change in text", label)
			}
			if n := wordCount(t); n > RecommendMaxWords {
				add("%s recommend: %d words (max %d)", label, n, RecommendMaxWords)
			}
		}
		if id := firstIdentifier(w); id != "" {
			add("%s why: code identifier %q; identifiers belong in checked items", label, id)
		}

		d := cleanLine(s.Decide)
		switch {
		case d == "":
			add("%s decide: missing", label)
		default:
			if n := wordCount(d); n > DecideMaxWords {
				add("%s decide: %d words (max %d)", label, n, DecideMaxWords)
			}
			if q := strings.Count(d, "?"); q > 1 {
				add("%s decide: %d questions; ask exactly one yes/no question", label, q)
			} else if !strings.HasSuffix(d, "?") {
				add(`%s decide: must be a question ending in "?"`, label)
			}
			if m := twoAsks.FindString(d); m != "" {
				add("%s decide: %q asks two things or offers alternatives; ask one yes/no question where yes keeps the code as is", label, strings.TrimSpace(m))
			}
			if id := firstIdentifier(d); id != "" {
				add("%s decide: code identifier %q; say it in plain words", label, id)
			}
		}
	}
	if len(run.Chapters) == 0 {
		add("chapters: missing; explain how the change works in %d to %d chapters", MinChapters, MaxChapters)
	} else if len(run.Chapters) > MaxChapters {
		add("chapters: %d (max %d)", len(run.Chapters), MaxChapters)
	}
	for i, c := range run.Chapters {
		name := cleanLine(c.ID)
		if name == "" {
			name = fmt.Sprintf("%d", i+1)
		}
		label := "chapter " + name
		if !oneOf(strings.ToLower(cleanLine(c.Role)), roles) {
			add("%s role: %q must be heart, consequence, glue or docs", label, cleanLine(c.Role))
		}
		if n := wordCount(cleanLine(c.Title)); n == 0 || n > ChapterTitleMaxWords {
			add("%s title: %d words (1 to %d)", label, n, ChapterTitleMaxWords)
		}
		if n := wordCount(cleanLine(c.Text)); n == 0 {
			add("%s text: missing; explain how this part works", label)
		} else if n > ChapterTextMaxWords {
			add("%s text: %d words (max %d)", label, n, ChapterTextMaxWords)
		}
		if len(c.Files) == 0 {
			add("%s files: none; list the changed files it covers", label)
		}
	}
	return out
}

// citesPath reports whether text names a file or directory path.
func citesPath(s string) bool { return pathIdent.MatchString(s) || sourceFile.MatchString(s) }

// normRecommend reads the reviewer's recommend answer: keep (yes, the code
// stays) or change.
func normRecommend(a string) string {
	switch strings.ToLower(strings.Trim(cleanLine(a), ".! ")) {
	case "keep", "yes", "ok", "keep as is", "fine":
		return RecommendKeep
	case "change", "no", "fix":
		return RecommendChange
	}
	return strings.ToLower(cleanLine(a))
}

// verdictPrefix is the verdict's required opening.
var verdictPrefix = regexp.MustCompile(`^(Ready\b|Not ready\s*[:—–-])`)

// twoAsks catches a decide that is not one yes/no question: a second clause
// of its own (", and is", "and will …"), an alternative (" or will", "or
// should …") or a counter-proposal ("instead of").
var twoAsks = regexp.MustCompile(`(?i)(, and is\b| or will\b|\binstead of\b|\b(and|or)\s+(is|are|will|would|do|does|did|can|could|should|has|have)\b)`)

// Code identifiers, which a reader has to decode. Proper nouns that happen to
// carry an inner capital are allowed.
var (
	snakeIdent  = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9]*_[A-Za-z0-9_]+\b`)
	camelIdent  = regexp.MustCompile(`\b[a-z]+[A-Z][A-Za-z0-9]*\b`)
	innerCap    = regexp.MustCompile(`\b[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*\b`)
	callIdent   = regexp.MustCompile(`\b\w+\(\)`)
	backticked  = regexp.MustCompile("`[^`]*`")
	pathIdent   = regexp.MustCompile(`[A-Za-z0-9_.-]*[._][A-Za-z0-9_.-]*/[A-Za-z0-9_./-]+|[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]*[._][A-Za-z0-9_./-]*|[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+`)
	sourceFile  = regexp.MustCompile(`\b[\w-]+\.(go|py|ts|tsx|js|jsx|mjs|rb|rs|java|kt|swift|sql|ya?ml|json|toml|sh|c|h|cc|cpp|cs|php|proto)\b`)
	identifiers = []*regexp.Regexp{backticked, callIdent, sourceFile, pathIdent, snakeIdent, camelIdent, innerCap}
)

// properNouns carry an inner capital without being code.
var properNouns = map[string]bool{
	"GitHub": true, "GitLab": true, "PostHog": true, "JavaScript": true, "TypeScript": true,
	"PowerShell": true, "MacBook": true, "YouTube": true, "LinkedIn": true, "OpenAI": true,
	"DevOps": true, "PagerDuty": true, "BigQuery": true, "PostgreSQL": true, "MySQL": true,
	"MongoDB": true, "DynamoDB": true, "WhatsApp": true, "iPhone": true, "iOS": true, "macOS": true,
	"eBay": true, "OAuth": true,
}

// firstIdentifier returns the first code identifier in s, or "".
func firstIdentifier(s string) string {
	best, at := "", -1
	for _, re := range identifiers {
		for _, loc := range re.FindAllStringIndex(s, -1) {
			m := s[loc[0]:loc[1]]
			if properNouns[m] {
				continue
			}
			if at < 0 || loc[0] < at {
				best, at = m, loc[0]
			}
			break
		}
	}
	return best
}

func wordCount(s string) int { return len(strings.Fields(s)) }

// sentenceEnd is a sentence's closing punctuation followed by a space or the
// end (so "1.5" and "e.g" inside a word don't count).
var sentenceEnd = regexp.MustCompile(`[.!?]+(\s|$)`)

// sentenceCount counts sentences; text with no closing punctuation is one.
func sentenceCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := len(sentenceEnd.FindAllStringIndex(s, -1))
	if !strings.ContainsAny(s[len(s)-1:], ".!?") {
		n++
	}
	return n
}
