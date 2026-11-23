// Package selfreview is the reviewer brain behind fleet's self-review: it turns
// one snapshot of a session's worktree into a short list of decisions only a
// human can make, plus an honest account of what was skipped and why.
//
// It is deliberately pure. Fleet assembles an Input (git, transcripts, CI),
// BuildPrompt renders it, a Runner asks a model, Parse reads the reply and
// Enforce applies fleet's own rules on top of it — because the model writes the
// first draft, never the last word: line counts, "not covered", high-risk paths
// and source checks are computed here and never trusted to the model.
//
// Nothing in this package imports the diff engine or the UI; the caller
// converts its own types into Input.
package selfreview

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the "v" field of the reviewer's JSON. v2 added the
// summary, each decision's recommend / how / elsewhere, the scope decision and
// the walkthrough's chapters; a stored v1 run still reads (the new fields are
// simply absent).
const SchemaVersion = 2

// MaxStops is how many decisions the reviewer is asked for at most. Enforce
// keeps every one it gets anyway: a decision is never hidden.
const MaxStops = 7

// MaxRounds caps the review → fix → review loop.
const MaxRounds = 3

// Input is everything fleet hands the reviewer for one run. The caller fills
// it; BuildPrompt applies the budgets.
type Input struct {
	Round       int      // 1-based
	Title       string   // session title
	Prompts     []string // the user's prompts, oldest first (intent)
	Claims      string   // the agent's last reply — a claim, not evidence
	Files       []FileInput
	CommitLog   string // git log --oneline base..HEAD
	CI          CIInfo
	ReviewRules string // REVIEW.md text (always-human block already parsed out)
	Ticket      string // .fleet/ticket/*/ticket.md, if any

	// Round ≥2 only.
	Previous        *Run
	PreviousAnswers []Answer
}

// FileInput is one changed file.
type FileInput struct {
	Path      string
	OldPath   string // renames/copies only
	Status    string // A, M, D, R, C … (git's letter, or a word)
	Additions int
	Deletions int
	Patch     string // unified diff (hunks); empty for binary or folded files

	// RuleSkip is the classifier's verdict ("lockfile", "generated", "test",
	// "vendored", …) or "" when a human may need to read the file. A rule
	// skip is never sent as a patch.
	RuleSkip string
	// HighRisk marks a path that can never be skipped by the model's say-so
	// (see HighRiskPath).
	HighRisk bool
	// RemovedAssertions is how many assertion lines the patch deletes
	// (RemovedAssertions), counted before a rule skip's patch is dropped.
	RemovedAssertions int
}

// Lines is the file's changed-line count.
func (f FileInput) Lines() int { return f.Additions + f.Deletions }

// CI states. Empty means fleet knows nothing (no PR, no gh).
const (
	CIUnknown = ""
	CIPending = "pending"
	CISuccess = "success"
	CIFailure = "failure"
)

// CIInfo is the PR's check rollup, if there is a PR.
type CIInfo struct {
	State  string   // CIUnknown | CIPending | CISuccess | CIFailure
	Failed []string // failing check names
}

// Run is schema v1 as the model writes it, plus the fields fleet computes in
// Enforce. Computed fields are serialized too, so a stored run renders without
// re-running Enforce.
type Run struct {
	V       int    `json:"v"`
	Verdict string `json:"verdict"`
	// Summary is what the change does, for someone who did not write it: at
	// most three short sentences, plain words a product manager understands.
	Summary string `json:"summary,omitempty"`
	Risk    string `json:"risk"`
	// IntentGap is what the user asked for that the change does not do. Kept
	// for compatibility; what the screens show is the scope decision Enforce
	// makes from it (a Stop of kind "scope", always first).
	IntentGap string   `json:"intent_gap"`
	Stops     []Stop   `json:"stops"`
	Skipped   []Skip   `json:"skipped"`
	Pending   []string `json:"pending"`
	// Chapters are the walkthrough: how the change works, in reading order
	// (heart → consequences → glue → docs). Every changed file sits in
	// exactly one chapter — Enforce holds that, adding an "Other changes"
	// chapter for files the reviewer left out.
	Chapters []Chapter `json:"chapters,omitempty"`
	// Prev is round ≥2's report on each previous "change" answer.
	Prev []Followup `json:"prev,omitempty"`

	// Computed by fleet (Enforce) — never trusted to the model.
	NotCovered []string `json:"not_covered,omitempty"`
	// NotCoveredWhy is fleet's own reason a Not covered file is there, keyed
	// by path — set when Enforce refused a skip (a high-risk path, or a guide
	// skip its kind and size rules reject). A file the model simply never
	// mentioned has no entry.
	NotCoveredWhy map[string]string `json:"not_covered_why,omitempty"`
	// MoreStops is stops past MaxStops that older fleets dropped; always 0
	// now (every stop is kept). Read only for runs stored before.
	MoreStops    int `json:"more_stops,omitempty"`
	TotalFiles   int `json:"total_files"`
	TotalLines   int `json:"total_lines"`
	StopLines    int `json:"stop_lines"`    // changed lines inside a stop's hunks
	SkippedFiles int `json:"skipped_files"` // files skipped whole (scope file)
	SkippedLines int `json:"skipped_lines"`
	// UncoveredLines is what is left: not in a stop, not skipped.
	UncoveredLines int `json:"uncovered_lines"`

	// Reviewer is what the reviewer call itself did — turns, cost, and which
	// files its tools opened — read from claude's event stream by the runner.
	// Stored with the run (a computed field, like the counts above), so "did
	// it open anything?" is answerable after the fact. Nil for a run stored
	// before fleet recorded it.
	Reviewer *ReviewerMeta `json:"reviewer,omitempty"`
}

// ReviewerMeta is the transparency record of one reviewer call: claude's own
// figures from the final result event, plus the tool calls it made.
type ReviewerMeta struct {
	Turns      int     `json:"turns"`
	CostUSD    float64 `json:"cost_usd"`
	DurationMS int     `json:"duration_ms"`
	// Tools counts tool calls by tool name ("Read": 9, "Grep": 2).
	Tools map[string]int `json:"tools,omitempty"`
	// Files are the distinct paths the tools touched — a Read's file, a
	// Grep's or Glob's path — relative to the worktree when inside it, in
	// first-touched order.
	Files []string `json:"files,omitempty"`
	// FilesRead is how many distinct files Read opened (Files minus the
	// directories a search was pointed at).
	FilesRead int `json:"files_read"`

	touched []touch // the raw tool paths, in call order (not stored)
}

// ReadLabel is the compact header form: "9 files read", or "read no files"
// — the case the record exists to expose.
func (m *ReviewerMeta) ReadLabel() string {
	if m == nil {
		return ""
	}
	switch m.FilesRead {
	case 0:
		return "read no files"
	case 1:
		return "1 file read"
	}
	return strconv.Itoa(m.FilesRead) + " files read"
}

// Searches is the number of Grep and Glob calls.
func (m *ReviewerMeta) Searches() int {
	if m == nil {
		return 0
	}
	return m.Tools["Grep"] + m.Tools["Glob"]
}

// Stop is one decision a human must make.
type Stop struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Why   string `json:"why"`
	// How is how the mechanism around this decision works — what a person
	// needs to understand before they can answer.
	How string `json:"how,omitempty"`
	// Elsewhere is how comparable code in this repo handles the same thing,
	// citing its path ("EdgeWatcher retries a failed query once —
	// collectors/edgewatcher/collector.go"). Optional.
	Elsewhere string `json:"elsewhere,omitempty"`
	// Recommend is the reviewer's own answer: keep the code, or change it
	// (and how). Nil for a run stored before fleet asked for one.
	Recommend *Recommend `json:"recommend,omitempty"`
	// Missing is a scope decision's list of what was asked and not done.
	Missing []string `json:"missing,omitempty"`
	Checked []Check  `json:"checked"`
	Decide  string   `json:"decide"`
	Hunks   []Hunk   `json:"hunks"`

	Lines int `json:"lines,omitempty"` // computed: changed lines in its hunks
}

// KindScope is the decision about whether this is the right change at all:
// what the user asked for and the change does not do. It belongs to no
// chapter and is always listed first.
const KindScope = "scope"

// IsScope reports a scope decision.
func (s Stop) IsScope() bool { return s.Kind == KindScope }

// Recommend answers.
const (
	RecommendKeep   = "keep"
	RecommendChange = "change"
)

// Recommend is the reviewer's answer to its own question. Answer "keep" means
// yes — the code stays; "change" carries the concrete change in Text.
type Recommend struct {
	Answer string `json:"answer"`
	Text   string `json:"text,omitempty"`
}

// Chapter roles, in reading order.
const (
	RoleHeart       = "heart"       // the core mechanism
	RoleConsequence = "consequence" // what follows from it
	RoleGlue        = "glue"        // wiring, schemas, generated code
	RoleDocs        = "docs"
)

// Chapter is one step of the walkthrough: how one part of the change works.
type Chapter struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Role  string `json:"role"`
	// Text explains HOW this part works — the mechanism, not the risk.
	Text      string   `json:"text"`
	Files     []string `json:"files"`
	Decisions []string `json:"decisions,omitempty"` // stop ids whose code is here
	// ByFleet marks the chapter Enforce added for files no chapter named.
	ByFleet bool `json:"by_fleet,omitempty"`
}

// Check is one thing the reviewer verified, with where the evidence came from.
type Check struct {
	Text string `json:"text"`
	Src  string `json:"src"`
	// Demoted is set when fleet could not verify a "ci" source and rewrote it
	// to "claim" (rendered dim).
	Demoted bool `json:"demoted,omitempty"`
}

// UnmarshalJSON accepts a bare string as a sourceless check (which Enforce
// then drops) rather than failing the whole run over one item.
func (c *Check) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = Check{Text: s}
		return nil
	}
	type plain Check
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Check(p)
	return nil
}

// Hunk anchors a stop to lines of a changed file. Start = End = 0 means "the
// file as a whole" (fleet's fallback when the range is not in the diff).
type Hunk struct {
	Path  string `json:"path"`
	Side  string `json:"side"` // "new" | "old"
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// UnmarshalJSON tolerates quoted line numbers.
func (h *Hunk) UnmarshalJSON(b []byte) error {
	var raw struct {
		Path  string  `json:"path"`
		Side  string  `json:"side"`
		Start flexInt `json:"start"`
		End   flexInt `json:"end"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*h = Hunk{Path: raw.Path, Side: raw.Side, Start: int(raw.Start), End: int(raw.End)}
	return nil
}

// Skip is a changed file (or the rest of one) that needs no decision.
type Skip struct {
	Path string `json:"path"`
	// Kind is what the model says the file is: mechanical | docs | fixture |
	// config | generated. Enforce checks it against the path and the size of
	// the change; a rule skip carries none.
	Kind   string `json:"kind,omitempty"`
	Reason string `json:"reason"`
	Scope  string `json:"scope"` // "file" | "rest"
	By     string `json:"by"`    // "rule" | "guide" — set by fleet
	// Note is fleet's own flag on a skip, never the model's: today only
	// AssertionsRemovedNote on a test file that deletes assertions.
	Note string `json:"note,omitempty"`
}

// Followup is round ≥2's report on a previous "change" answer.
type Followup struct {
	ID     string `json:"id"`     // the previous run's stop id
	Status string `json:"status"` // addressed | not_addressed | unreported
	Note   string `json:"note"`
}

// Answer kinds.
const (
	AnswerFine   = "fine"
	AnswerChange = "change"
	AnswerAsk    = "ask"
	AnswerLater  = "later"
)

// Answer is the human's response to one stop. Change and ask carry a note.
//
// A stop the human promoted from the skip list has no model stop behind it;
// its StopID is FileStopID(path).
type Answer struct {
	StopID string `json:"stop_id"`
	Kind   string `json:"kind"`
	Note   string `json:"note,omitempty"`
}

// FileStopID is the StopID of a skipped file the human promoted to a stop.
func FileStopID(path string) string { return "file:" + path }

// RunMeta is what a Runner observed about the call itself.
type RunMeta struct {
	Model     string
	Elapsed   time.Duration
	Attempts  int // 2 when the repair retry ran
	CostUSD   float64
	NumTurns  int
	SessionID string
	Warnings  []string // Enforce's notes, plus the repair retry if any
}

// flexInt decodes a JSON number or a numeric string — models occasionally
// quote line numbers, and one quoted number must not cost the whole review.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0 // unreadable → file-level anchor, decided in Enforce
		return nil
	}
	*n = flexInt(f)
	return nil
}
