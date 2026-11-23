package reviewjob

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// reviewMessagePrefix starts every message fleet types into a session on
// Send (selfreview.ComposeMessage). Those are fleet's words, not the user's
// intent, so they are filtered out of the prompts the next round reads.
const reviewMessagePrefix = "[fleet review"

// commitLogMax bounds `git log --oneline base..HEAD`; BuildPrompt caps the
// characters too.
const commitLogMax = 50

// ticketReadMax bounds how much of a ticket.md is read (BuildPrompt keeps 4k).
const ticketReadMax = 16 << 10

// rulesFileMax bounds one REVIEW.md read.
const rulesFileMax = 64 << 10

// gathered is Gather's output plus what the cache key needs.
type gathered struct {
	in        selfreview.Input
	snap      Snapshot
	rawRules  string // REVIEW.md as read, before ParseReviewRules
	prevRunID string
}

// Gather assembles everything the reviewer is handed for the checkout as it
// stands now, and the snapshot it describes. An empty diff returns
// ErrNothingToReview (with the snapshot filled in).
func Gather(ctx context.Context, d Deps, c Checkout) (selfreview.Input, Snapshot, error) {
	g, err := gather(ctx, d, c)
	if err != nil {
		return selfreview.Input{}, g.snap, err
	}
	return g.in, g.snap, nil
}

// storedBase is the base_ref of the most recently created session in the
// checkout that recorded one ("" = none did): the ref the worktree was cut
// from, which every session in it shares.
func storedBase(c Checkout) string {
	var best *session.Session
	for _, s := range c.Sessions {
		if s.BaseRef == "" {
			continue
		}
		if best == nil || s.CreatedAt.After(best.CreatedAt) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.BaseRef
}

// ResolveBase names the base a review of c would measure against: a session's
// stored base_ref, else the PR's base, else the default branch.
func ResolveBase(c Checkout, prBase string) string {
	return git.ResolveBase(c.Root, storedBase(c), prBase)
}

func gather(ctx context.Context, d Deps, c Checkout) (gathered, error) {
	var g gathered
	if c.Root == "" {
		return g, ErrNoCheckout
	}
	dir := c.Root
	if _, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel"); err != nil {
		return g, fmt.Errorf("%s is not a git checkout: %w", dir, err)
	}
	_, branch := git.ReadHead(dir)

	prOf := d.PR
	if prOf == nil {
		prOf = defaultPR(d.Store)
	}
	pr := prOf(c.Root, branch)
	prBase := ""
	if pr != nil {
		prBase = pr.BaseRefName
	}

	var err error
	g.snap.BaseRef = ResolveBase(c, prBase)
	if g.snap.BaseSHA, err = git.MergeBase(dir, g.snap.BaseRef); err != nil {
		return g, fmt.Errorf("finding where this branch left %s: %w", g.snap.BaseRef, err)
	}
	if g.snap.HeadSHA, err = gitOut(ctx, dir, "rev-parse", "--verify", "HEAD"); err != nil {
		return g, err
	}
	if err := ctx.Err(); err != nil {
		return g, err
	}
	if g.snap.TreeSHA, err = snapshotTree(dir); err != nil {
		return g, fmt.Errorf("snapshotting the worktree: %w", err)
	}
	if headTree, err := gitOut(ctx, dir, "rev-parse", "--verify", "HEAD^{tree}"); err == nil {
		g.snap.Dirty = headTree != g.snap.TreeSHA
	}

	diffs, err := git.DiffFiles(dir, g.snap.BaseSHA, g.snap.TreeSHA)
	if err != nil {
		return g, fmt.Errorf("diffing against %s: %w", g.snap.BaseRef, err)
	}
	if len(diffs) == 0 {
		return g, ErrNothingToReview
	}

	paths := make([]string, len(diffs))
	for i, f := range diffs {
		paths[i] = f.Path
	}
	// REVIEW.md is read at the BASE, not from the worktree: the agent under
	// review can edit the worktree's copy, and a rule like "never skip auth/"
	// must not be something the reviewed change can loosen. An edit to it is
	// a changed file like any other, in front of the reviewer and the human.
	raw, rulesText, alwaysHuman := readRules(ctx, dir, g.snap.BaseSHA, paths)
	g.rawRules = raw

	in := selfreview.Input{
		Title:       checkoutTitle(c, branch),
		ReviewRules: rulesText,
		CI:          ciState(pr),
		Ticket:      readTicket(dir),
	}
	for _, f := range diffs {
		fi := selfreview.FileInput{
			Path: f.Path, OldPath: f.OldPath, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
			RuleSkip: string(review.ClassifyPath(f.Path)),
			HighRisk: selfreview.HighRiskPath(f.Path, alwaysHuman),
		}
		if fi.RuleSkip != "" {
			// Counted before the patch goes: a test file that deletes
			// assertions is flagged on its skip, not silently "safe".
			fi.RemovedAssertions = selfreview.RemovedAssertions(fi.Patch)
			fi.Patch = "" // never sent; no point holding it
		}
		in.Files = append(in.Files, fi)
	}

	turnsOf := d.Turns
	if turnsOf == nil {
		turnsOf = defaultTurns
	}
	in.Prompts, in.Claims = checkoutIntent(c, turnsOf)

	if log, err := gitOut(ctx, dir, "log", "--oneline", "--no-decorate", "--no-color",
		"-n", strconv.Itoa(commitLogMax), g.snap.BaseSHA+"..HEAD", "--"); err == nil {
		in.CommitLog = log
	}

	in.Round = 1
	if prev, err := previousRun(d.Store, c.Root); err != nil {
		return g, err
	} else if prev != nil {
		in.Round = min(prev.Row.Round+1, selfreview.MaxRounds)
		in.Previous, in.PreviousAnswers = prev.Run, prev.Answers
		g.prevRunID = prev.Row.ID
	}
	g.in = in
	return g, nil
}

// checkoutTitle names the checkout for the reviewer: its branch (else the
// directory), and the sessions that worked it.
func checkoutTitle(c Checkout, branch string) string {
	t := branch
	if t == "" {
		t = filepath.Base(c.Root)
	}
	var names []string
	for _, s := range c.Sessions {
		if n := strings.TrimSpace(s.Title); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		t += " (sessions: " + strings.Join(names, ", ") + ")"
	}
	return t
}

// timedPrompt is one user prompt with when it was given and by which session.
type timedPrompt struct {
	at    time.Time
	label string
	text  string
}

// checkoutIntent gathers the user's prompts from every session in the checkout
// — Claude transcripts, the first prompt for agents fleet can't read — ordered
// by time and, when more than one session contributed, labelled with the
// session's title. fleet's own review messages are dropped. The claims are the
// newest closing reply across the sessions, labelled the same way.
// selfreview.BuildPrompt applies the budgets (first + latest prompts, each
// capped; the claims capped).
func checkoutIntent(c Checkout, turnsOf func(*session.Session) []session.Turn) (prompts []string, claims string) {
	var all []timedPrompt
	var claimAt time.Time
	var claimText, claimLabel string
	contributors := map[string]bool{}
	for _, s := range c.Sessions {
		turns := turnsOf(s)
		ps, reply := intent(turns)
		if len(ps) == 0 && strings.TrimSpace(s.FirstPrompt) != "" &&
			!strings.HasPrefix(strings.TrimSpace(s.FirstPrompt), reviewMessagePrefix) {
			all = append(all, timedPrompt{at: s.CreatedAt, label: s.Title, text: s.FirstPrompt})
			contributors[s.ID] = true
		}
		for _, t := range turns {
			p := strings.TrimSpace(t.Prompt)
			if p == "" || strings.HasPrefix(p, reviewMessagePrefix) {
				continue
			}
			at := t.PromptAt
			if at.IsZero() {
				at = s.CreatedAt
			}
			all = append(all, timedPrompt{at: at, label: s.Title, text: p})
			contributors[s.ID] = true
		}
		if reply != "" {
			at := replyTime(turns)
			if at.IsZero() {
				at = lastWork(s)
			}
			if claimText == "" || at.After(claimAt) {
				claimAt, claimText, claimLabel = at, reply, s.Title
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	labelled := len(contributors) > 1
	for _, p := range all {
		if labelled && p.label != "" {
			prompts = append(prompts, "["+p.label+"] "+p.text)
			continue
		}
		prompts = append(prompts, p.text)
	}
	if labelled && claimText != "" && claimLabel != "" {
		claimText = "[" + claimLabel + "] " + claimText
	}
	return prompts, claimText
}

// replyTime is when the newest non-empty reply in turns was given.
func replyTime(turns []session.Turn) time.Time {
	for i := len(turns) - 1; i >= 0; i-- {
		if strings.TrimSpace(turns[i].Reply) != "" {
			if !turns[i].ReplyAt.IsZero() {
				return turns[i].ReplyAt
			}
			return turns[i].PromptAt
		}
	}
	return time.Time{}
}

// intent splits a transcript into the user's prompts (fleet's own review
// messages removed) and the agent's last reply, which is a claim.
func intent(turns []session.Turn) (prompts []string, claims string) {
	for _, t := range turns {
		p := strings.TrimSpace(t.Prompt)
		if p == "" || strings.HasPrefix(p, reviewMessagePrefix) {
			continue
		}
		prompts = append(prompts, p)
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if r := strings.TrimSpace(turns[i].Reply); r != "" {
			return prompts, r
		}
	}
	return prompts, ""
}

// previousRun is the newest run whose answers were sent (or that was
// approved): what round N+1 builds on. A ready-but-unsent run is not a
// previous round — re-reviewing before sending stays in the same round.
func previousRun(st Store, checkout string) (*Record, error) {
	runs, err := st.ListReviewRuns(checkout)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.Status != session.ReviewSent && r.Status != session.ReviewApproved {
			continue
		}
		var run selfreview.Run
		if json.Unmarshal([]byte(r.JSON), &run) != nil {
			continue
		}
		selfreview.Upgrade(&run)
		ans, err := st.LoadReviewAnswers(r.ID)
		if err != nil {
			return nil, err
		}
		rec := &Record{Row: r, Run: &run}
		for _, a := range ans {
			rec.Answers = append(rec.Answers, selfreview.Answer{StopID: a.StopID, Kind: a.Answer, Note: a.Note})
		}
		return rec, nil
	}
	return nil, nil
}

// readTicket returns the newest .fleet/ticket/*/ticket.md, if any.
func readTicket(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, ".fleet", "ticket", "*", "ticket.md"))
	var best string
	var bestMod int64
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if t := st.ModTime().UnixNano(); best == "" || t > bestMod {
			best, bestMod = m, t
		}
	}
	if best == "" {
		return ""
	}
	f, err := os.Open(best)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, ticketReadMax)
	n, _ := f.Read(buf)
	return string(buf[:n])
}

// rulesFile is the REVIEW.md basename.
const rulesFile = "REVIEW.md"

// readRules reads REVIEW.md at the repo root plus the nearest one above each
// changed path, all at commit rev. It returns the raw text (for the cache
// key), the prose for the reviewer, and the always-human globs — a nested
// file's globs re-rooted under its directory.
func readRules(ctx context.Context, dir, rev string, changed []string) (raw, text string, alwaysHuman []string) {
	// Every directory that could hold a relevant REVIEW.md: the root and each
	// ancestor of each changed path.
	cands := map[string]bool{"": true}
	for _, p := range changed {
		for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			cands[d] = true
		}
	}
	found := existingRules(ctx, dir, rev, cands)
	if len(found) == 0 {
		return "", "", nil
	}

	use := map[string]bool{}
	if _, ok := found[""]; ok {
		use[""] = true
	}
	for _, p := range changed {
		for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if _, ok := found[d]; ok {
				use[d] = true
				break
			}
		}
	}
	dirs := make([]string, 0, len(use))
	for d := range use {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs) // "" (the root) sorts first

	var rawB, textB strings.Builder
	for _, d := range dirs {
		body := found[d]
		rawB.WriteString(rulesPath(d) + "\n" + body + "\n")
		t, globs := selfreview.ParseReviewRules(body)
		for _, gl := range globs {
			alwaysHuman = append(alwaysHuman, rerootGlob(d, gl))
		}
		if t == "" {
			continue
		}
		if len(dirs) > 1 {
			fmt.Fprintf(&textB, "From %s:\n", rulesPath(d))
		}
		textB.WriteString(t + "\n\n")
	}
	return rawB.String(), strings.TrimSpace(textB.String()), alwaysHuman
}

func rulesPath(d string) string {
	if d == "" {
		return rulesFile
	}
	return d + "/" + rulesFile
}

// rerootGlob anchors a nested REVIEW.md's glob under its directory. A bare
// name ("*.sql") matched at any depth below the root keeps matching at any
// depth below the nested file's directory.
func rerootGlob(d, g string) string {
	if d == "" {
		return g
	}
	g = strings.TrimPrefix(strings.TrimSpace(g), "./")
	// Mirrors selfreview's globMatch: only a pattern with no slash at all is
	// a basename match; "db/" is a directory anchored where the file sits.
	if !strings.Contains(g, "/") && !strings.Contains(g, "**") {
		return d + "/**/" + g
	}
	return d + "/" + strings.TrimPrefix(g, "/")
}

// existingRules reads every candidate REVIEW.md that exists at rev, in one
// `git cat-file --batch` call. Returns dir → contents.
func existingRules(ctx context.Context, dir, rev string, cands map[string]bool) map[string]string {
	keys := make([]string, 0, len(cands))
	for d := range cands {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	var in strings.Builder
	for _, d := range keys {
		in.WriteString(rev + ":" + rulesPath(d) + "\n")
	}
	out, err := gitRaw(ctx, dir, in.String(), "cat-file", "--batch")
	if err != nil {
		return nil
	}
	found := map[string]string{}
	r := bufio.NewReader(bytes.NewReader(out))
	for _, d := range keys {
		header, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[1] != "blob" {
			continue // "<name> missing", or a tree named REVIEW.md
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 {
			break
		}
		body := make([]byte, size+1) // + the trailing newline cat-file adds
		if _, err := io.ReadFull(r, body); err != nil {
			break
		}
		if size > rulesFileMax {
			size = rulesFileMax
		}
		found[d] = string(body[:size])
	}
	return found
}
