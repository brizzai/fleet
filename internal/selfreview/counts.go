package selfreview

import (
	"fmt"
	"strings"
)

// Counts is the one definition of every number a review shows. The CLI, the
// sidebar's review row, the Review view's header and its footer all read
// these fields and these phrases; none of them counts on its own. They used to:
// the row counted the reviewer's decisions plus the files it never looked at
// (9), the CLI counted unanswered decisions (6), and the view's "lines need
// you" added the lines nobody covered to the lines in decisions (463 against
// the CLI's 106) — three numbers for one review.
//
// The definitions:
//
//   - a decision is one of the reviewer's stops — nothing else. "N to decide"
//     is the decisions with no answer yet;
//   - "Not reviewed" is a changed file the reviewer covered nowhere. It is a
//     section of its own, never counted as a decision: fleet surfaces it so no
//     file is silently skipped, but nobody asked a question about it;
//   - "changed lines need you" is the changed lines inside a decision's hunks
//     (Run.StopLines), out of every changed line (Run.TotalLines);
//   - "safe to skip" is every entry of the skip list — a file, or the rest of
//     one whose other lines are in a decision — by fleet's rules or by the
//     reviewer (len(Run.Skipped)), so the count matches the list it opens.
type Counts struct {
	Decisions  int // the reviewer's decisions (stops)
	Answered   int // decisions with an answer
	Unanswered int // Decisions − Answered: "N to decide"

	// Answers by kind, over the decisions AND the files the human raised
	// themselves — what a send carries.
	Changes, Asks, OK, Later int
	// AddedByYou is the skipped or unreviewed files the human raised as a
	// change of their own ("x review it myself").
	AddedByYou int
	// FollowUps is a scope decision answered "ok as is": ship without the
	// rest — which still asks the agent for something, to note the
	// follow-up (counted in OK too).
	FollowUps int
	// Scope is the 1-based number of the scope decision, 0 when there is
	// none: the header's pointer to it.
	Scope int

	NotReviewed int // changed files the reviewer covered nowhere
	SafeToSkip  int // entries in the skip list: files, or the rest of one with a decision
	NeedLines   int // changed lines inside a decision's hunks
	TotalLines  int // every changed line
}

// CountsOf computes a run's counts against the human's answers. Answers to
// stops the run does not have are ignored, except a promoted file's
// (FileStopID), which is the human's own.
func CountsOf(run *Run, answers []Answer) Counts {
	var c Counts
	if run == nil {
		return c
	}
	byID := make(map[string]Answer, len(answers))
	for _, a := range answers {
		if a.Kind != "" {
			byID[a.StopID] = a
		}
	}
	tally := func(a Answer) {
		switch a.Kind {
		case AnswerChange:
			c.Changes++
		case AnswerAsk:
			c.Asks++
		case AnswerFine:
			c.OK++
		case AnswerLater:
			c.Later++
		}
	}
	c.Decisions = len(run.Stops)
	for i, s := range run.Stops {
		if s.IsScope() && c.Scope == 0 {
			c.Scope = i + 1
		}
		if a, ok := byID[s.ID]; ok {
			c.Answered++
			tally(a)
			if s.IsScope() && a.Kind == AnswerFine {
				c.FollowUps++
			}
		}
	}
	c.Unanswered = c.Decisions - c.Answered
	for id, a := range byID {
		if strings.HasPrefix(id, "file:") {
			c.AddedByYou++
			tally(a)
		}
	}
	c.NotReviewed = len(run.NotCovered)
	// Every entry the skip list shows, so the count and the list it opens
	// agree; a file whose other lines are in a decision is one of them.
	c.SafeToSkip = len(run.Skipped)
	c.NeedLines = run.StopLines
	c.TotalLines = run.TotalLines
	return c
}

// Acting is how many answers give the agent something to do: a change, a
// question, or a scope accepted as a follow-up to note. Only these are worth
// waking a session for.
func (c Counts) Acting() int { return c.Changes + c.Asks + c.FollowUps }

// NothingNeedsYou reports a run with no decision and no file left unreviewed.
func (c Counts) NothingNeedsYou() bool { return c.Decisions == 0 && c.NotReviewed == 0 }

// ToDecide is "4 to decide", "nothing to decide" when every decision is
// answered or there were none.
func (c Counts) ToDecide() string {
	if c.Unanswered == 0 {
		return "nothing to decide"
	}
	return fmt.Sprintf("%d to decide", c.Unanswered)
}

// Progress is "0 of 4 answered"; "" with no decisions.
func (c Counts) Progress() string {
	if c.Decisions == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d answered", c.Answered, c.Decisions)
}

// LinesNeedYou is "106 of 1,489 changed lines need you"
// ("1 of 12 changed lines needs you").
func (c Counts) LinesNeedYou() string {
	verb := "need"
	if c.NeedLines == 1 {
		verb = "needs"
	}
	return fmt.Sprintf("%s of %s changed lines %s you", thousands(c.NeedLines), thousands(c.TotalLines), verb)
}

// SafeToSkipLabel is "14 files safe to skip".
func (c Counts) SafeToSkipLabel() string {
	return fmt.Sprintf("%d %s safe to skip", c.SafeToSkip, plural(c.SafeToSkip, "file", "files"))
}

// NotReviewedLabel is "2 files not reviewed".
func (c Counts) NotReviewedLabel() string {
	return fmt.Sprintf("%d %s not reviewed", c.NotReviewed, plural(c.NotReviewed, "file", "files"))
}

// AnswerSummary is what a send carries, in the human's words:
// "1 change, 1 question, 1 ok, 6 left for later". Empty kinds are left out.
func (c Counts) AnswerSummary() string {
	var parts []string
	if c.Changes > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", c.Changes, plural(c.Changes, "change", "changes")))
	}
	if c.Asks > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", c.Asks, plural(c.Asks, "question", "questions")))
	}
	if c.OK > 0 {
		parts = append(parts, fmt.Sprintf("%d ok", c.OK))
	}
	if c.Later > 0 {
		parts = append(parts, fmt.Sprintf("%d left for later", c.Later))
	}
	return strings.Join(parts, ", ")
}

// kindPhrases are the stop kinds as a reader says them: a dim phrase on the
// card and the CLI's stop lines, never an uppercase tag.
var kindPhrases = map[string]string{
	"product":  "product call",
	"customer": "customer impact",
	"bug":      "likely bug",
	"deploy":   "deploy order",
	"data":     "data-loss risk",
	"edge":     "edge case",
	"scope":    "scope",
}

// KindPhrase is a stop kind in plain words: "product call", "deploy order",
// … and the kind itself, lowercased, for the rest ("logic", "security").
func KindPhrase(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if p, ok := kindPhrases[k]; ok {
		return p
	}
	return k
}

// AnswerWord is an answer kind as the human chose it: "ok as is", "change",
// "ask", "later" ("" for none).
func AnswerWord(kind string) string {
	switch kind {
	case AnswerFine:
		return "ok as is"
	case AnswerChange:
		return "change"
	case AnswerAsk:
		return "ask"
	case AnswerLater:
		return "later"
	}
	return ""
}
