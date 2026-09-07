package ui

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/session"
)

// reviewFilesTTL keeps the list fresh enough to notice a push without
// refetching every time the cursor passes over the row.
const reviewFilesTTL = 3 * time.Minute

// reviewKey identifies a pull request across every repo fleet knows about.
//
// A bare number is not an identity: PR #12 exists in most repositories, so maps
// keyed on the number alone hand one repo's diff — and one repo's queued
// comments — to another. That was survivable while the maps died with the
// process; it is a wrong-target post now that they are on disk.
type reviewKey struct {
	repo string // owner/name
	pr   int
}

// reviewFilesMsg carries a fetched file list back to Update.
type reviewFilesMsg struct {
	key   reviewKey
	files github.PRFiles
	err   error
}

// reviewPRNumber returns the PR a session is reviewing, or 0.
//
// Read from the worktree path rather than the title: the path is what fleet
// created, and a title is a display string the user can rename.
func reviewPRNumber(s *session.Session) int {
	if s == nil {
		return 0
	}
	root := session.GetRepoRoot(s.ProjectPath)
	if !git.IsReviewWorktree(root) {
		return 0
	}
	n, err := strconv.Atoi(path.Base(root))
	if err != nil {
		return 0
	}
	return n
}

// reviewKeyFor identifies the pull request a session is reviewing.
//
// Returns false for a session that is not a review, and for one whose origin
// fleet cannot map to an owner/name — which is the same condition that already
// stops the file list from loading, so nothing new becomes unreachable.
func (h *Home) reviewKeyFor(s *session.Session) (reviewKey, bool) {
	pr := reviewPRNumber(s)
	if pr == 0 {
		return reviewKey{}, false
	}
	repo := ownerRepoFromOrigin(h.originKeyFor(session.GetRepoRoot(s.ProjectPath)))
	if repo == "" {
		return reviewKey{}, false
	}
	return reviewKey{repo: repo, pr: pr}, true
}

// loadReviewFiles fetches a PR's changed files off the Update goroutine.
func loadReviewFiles(k reviewKey) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewListTimeout)
		defer cancel()
		files, err := github.FetchPRFiles(ctx, k.repo, k.pr)
		return reviewFilesMsg{key: k, files: files, err: err}
	}
}

// maybeLoadReviewFiles fetches the file list for the review under the cursor,
// unless it is already cached and fresh.
//
// Driven by the cursor rather than by a keypress: the list is what the pane
// shows, so it has to be on its way before you ask for it, or every review you
// land on starts with an empty pane.
func (h *Home) maybeLoadReviewFiles(s *session.Session) tea.Cmd {
	k, ok := h.reviewKeyFor(s)
	if !ok {
		return nil
	}
	if at, ok := h.reviewFilesAt[k]; ok && time.Since(at) < reviewFilesTTL {
		return nil
	}
	if h.reviewFilesAt == nil {
		h.reviewFilesAt = map[reviewKey]time.Time{}
	}
	// Stamped before the fetch, not after: the cursor sits on a row for many
	// ticks, and without this every one of them starts another request.
	h.reviewFilesAt[k] = time.Now()
	return loadReviewFiles(k)
}

// ownerRepoFromOrigin turns "github.com/brizzai/brizzai" into "brizzai/brizzai".
func ownerRepoFromOrigin(origin string) string {
	parts := strings.Split(origin, "/")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// renderReviewFiles draws the changed-file list for a review session.
//
// The list is the answer to "what is actually in here" — the question that has
// to be settled before any diff is worth reading. On a 34-file PR that is 23
// test files, showing all 34 at equal weight is the failure; showing 11 with
// the rest counted and one key away is the fix.
func renderReviewFiles(pr int, files github.PRFiles, showFolded bool, width, height int) string {
	if len(files.Files) == 0 {
		return DimStyle.Render("  Loading files for #" + strconv.Itoa(pr) + "…")
	}

	shown, folded := github.Split(files.Files)

	var b strings.Builder

	hiddenCount := 0
	for _, fs := range folded {
		hiddenCount += len(fs)
	}
	head := fmt.Sprintf("%d files", files.Total)
	if hiddenCount > 0 {
		head += fmt.Sprintf(" · %d shown", len(shown))
	}
	b.WriteString("  " + DimStyle.Render(head) + "\n")
	b.WriteString("  " + DimStyle.Render("⏎ read · v agent pane") + "\n\n")

	// Biggest first. On a PR you have not read, size is the only ordering
	// signal available without a model, and it puts the substantial change
	// above the one-line import fix.
	sort.SliceStable(shown, func(i, j int) bool {
		return shown[i].Additions+shown[i].Deletions > shown[j].Additions+shown[j].Deletions
	})

	rows := height - 4
	for i, f := range shown {
		if rows > 0 && i >= rows {
			b.WriteString("  " + DimStyle.Render(fmt.Sprintf("… %d more", len(shown)-i)) + "\n")
			break
		}
		b.WriteString(renderFileRow(f, width) + "\n")
	}

	if hiddenCount > 0 {
		b.WriteString("\n  " + DimStyle.Render(foldSummary(folded, showFolded)) + "\n")
		if showFolded {
			for _, r := range foldOrder(folded) {
				for _, f := range folded[r] {
					b.WriteString(renderFileRow(f, width) + "\n")
				}
			}
		}
	}

	return b.String()
}

// renderFileRow is one file: viewed mark, size, path.
func renderFileRow(f github.PRFile, width int) string {
	adds := PROpenStyle.Render(fmt.Sprintf("+%-5d", f.Additions))
	dels := PRFailStyle.Render(fmt.Sprintf("−%-5d", f.Deletions))

	p := f.Path
	// Truncate from the LEFT: the basename and its immediate parent are what
	// identify a file, and a monorepo path's leading segments are the part
	// every row shares.
	budget := width - 20
	if budget > 10 && len(p) > budget {
		p = "…" + p[len(p)-budget+1:]
	}
	return "    " + adds + dels + " " + lipgloss.NewStyle().Render(p)
}

// foldOrder keeps the fold sections in a stable, meaningful order — the least
// review-worthy first, tests last, since tests are the ones you might open.
func foldOrder(folded map[github.DemoteReason][]github.PRFile) []github.DemoteReason {
	all := []github.DemoteReason{
		github.DemoteLockfile, github.DemoteVendored,
		github.DemoteGenerated, github.DemoteSnapshot, github.DemoteTest,
	}
	var out []github.DemoteReason
	for _, r := range all {
		if len(folded[r]) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// foldSummary names what was hidden and why, never just how much.
//
// A count alone asks you to trust the rule; naming the rule lets you check it,
// which is the only thing that makes hiding safe to do on someone else's behalf.
func foldSummary(folded map[github.DemoteReason][]github.PRFile, shown bool) string {
	var parts []string
	for _, r := range foldOrder(folded) {
		parts = append(parts, fmt.Sprintf("%d %s", len(folded[r]), pluralize(string(r), len(folded[r]))))
	}
	key := " — s to show"
	if shown {
		key = " — s to hide"
	}
	return "⊞ " + strings.Join(parts, ", ") + key
}

// pluralize keeps the fold summary readable: "1 lockfile", "2 lockfiles",
// and "1 test" rather than the "1 tests" the raw reason string produces.
func pluralize(reason string, n int) string {
	if n == 1 {
		return strings.TrimSuffix(reason, "s")
	}
	if strings.HasSuffix(reason, "s") {
		return reason
	}
	return reason + "s"
}

// handleReviewFiles stores a fetched list.
func (h *Home) handleReviewFiles(msg reviewFilesMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Debug("review files: fetch failed", "pr", msg.key.pr, "err", msg.err)
		// Cleared so the next pass retries rather than backing off forever on
		// a failure the user can fix (a network blip, a token that expired).
		delete(h.reviewFilesAt, msg.key)
		return h, nil
	}
	if h.reviewFiles == nil {
		h.reviewFiles = map[reviewKey]github.PRFiles{}
	}
	h.reviewFiles[msg.key] = msg.files
	return h, nil
}

// openReader opens the full-screen diff reader on a PR.
//
// The files are already cached by the time you press Enter — the cursor
// sitting on the row is what fetched them — so this is instant. When they are
// not (the fetch is still in flight, or failed) it says so rather than opening
// an empty reader that looks like a PR with no changes.
func (h *Home) openReader(k reviewKey) tea.Cmd {
	files, ok := h.reviewFiles[k]
	if !ok || len(files.Files) == 0 {
		h.setInfo("Still loading #" + strconv.Itoa(k.pr) + "…")
		return nil
	}

	// The reader gets the same list the summary shows: salience is a property
	// of the review, not of one surface, and a file hidden in one place that
	// appears in the other is two different answers to one question.
	// The reader carries BOTH lists and swaps them on `s`, so salience is
	// decided once here and the toggle costs no refetch.
	shown, _ := github.Split(files.Files)
	if h.reviewShowFolded {
		shown = files.Files
	}

	// By path, not by size: the tree groups by directory, so a size-ordered
	// diff would scroll in an order the tree does not show — two answers to
	// "where am I". GitHub sorts both this way for the same reason. Salience
	// still does the "what matters" work by folding, not by reordering.
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].Path < shown[j].Path })

	if h.reviewRead == nil {
		h.reviewRead = map[reviewKey]map[string]bool{}
	}
	if h.reviewRead[k] == nil {
		h.reviewRead[k] = map[string]bool{}
	}

	// The queue is where the title and author come from, but the key already
	// knows the repo — so a PR fleet has since dropped from the queue still
	// opens, and still submits, with only its heading looking bare.
	title, author := "", ""
	if r, ok := h.findReviewByKey(k); ok {
		title, author = r.Title, r.Author
	}

	h.reader.SetSize(h.width, h.height)
	h.reader.Show(k.pr, title, author, k.repo, h.reviewWorktree(k), shown, files.Files,
		h.reviewRead[k], h.seedComments(k))
	h.actionLog.Add("read review", strconv.Itoa(k.pr), true)

	// The tour rides out on the same keypress, off the Update goroutine. It
	// takes tens of seconds, so the reader is fully usable the whole time and
	// the route arrives into a screen you are already reading.
	return h.maybeStartTour(k, files)
}

// reviewWorktree is the local checkout of a PR, if one exists.
//
// Only a directory that is actually there: the path is what makes context
// expansion a file read, and handing the reader a path to nothing would draw
// fold markers that refuse to open. Reviews opened without ever pressing Enter
// have no worktree yet, and that is the common case on the first look.
func (h *Home) reviewWorktree(k reviewKey) string {
	repo := h.repoForOrigin(k.repo)
	if repo == "" {
		return ""
	}
	path := git.ReviewWorktreePath(repo, k.pr)
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return ""
	}
	return path
}

// seedComments hands the reader back what was written on an earlier visit, so
// closing it is not the same as discarding a review in progress.
func (h *Home) seedComments(k reviewKey) []review.Comment {
	out := make([]review.Comment, 0, len(h.reviewComments[k]))
	for _, c := range h.reviewComments[k] {
		out = append(out, review.Comment{
			File: c.file, Line: c.line, Kind: c.kind, Body: c.body,
			// Carried, not recomputed: a comment written three pushes ago is
			// still true about the commit it was written against, and stamping
			// it with today's head would be the exact lie this field prevents.
			HeadSHA: c.headSHA,
		})
	}
	return out
}

// findReviewByKey looks a pull request up in the queue for its title, author
// and head SHA. Matched on repo AND number: two repos routinely have a #12.
func (h *Home) findReviewByKey(k reviewKey) (github.ReviewRequest, bool) {
	for _, r := range h.reviewQueue {
		if r.Number == k.pr && r.Repo == k.repo {
			return r, true
		}
	}
	return github.ReviewRequest{}, false
}

// submitReview posts the reader's queued comments as one GitHub review.
//
// The reader asked; the app answers, because this is the layer that knows the
// owner/repo and the commit the diff was read at. It also keeps the gh
// subprocess off the reader, which runs on the Update goroutine.
func (h *Home) submitReview(msg reviewSubmitRequestMsg) (tea.Model, tea.Cmd) {
	k := msg.key
	if k.repo == "" || k.pr == 0 {
		h.reader.SubmitDone(fmt.Errorf("no GitHub repo known for #%d", k.pr), 0)
		return h, nil
	}
	cs := h.reader.pendingComments()
	sub := buildSubmission(reviewAnchor(cs, h.reviewFiles[k].HeadSHA), msg.event, msg.body, cs)
	event, count := msg.event, len(sub.Comments)
	return h, func() tea.Msg {
		err := github.SubmitReview(context.Background(), k.repo, k.pr, sub)
		return reviewSubmitResultMsg{key: k, event: event, count: count, err: err}
	}
}

// reviewAnchor is the commit a submission should be anchored to.
//
// The comments decide, not the current head: their line numbers were read off
// one particular diff, and that is the only commit they are true against. A
// draft written before a push and submitted after it therefore lands as an
// outdated comment on the code it was actually about, rather than silently on
// whatever occupies that line now.
//
// falls back to the loaded diff's own SHA when the comments cannot agree —
// which happens only if the file list refreshed mid-review and more comments
// were written after it. GitHub takes exactly one commit per review, so a mixed
// batch has no right answer; the newest diff is the one on screen, and it is at
// least the commit the user was last looking at. An approve with no comments at
// all uses it too, for want of anything better to mean.
func reviewAnchor(cs []review.Comment, loadedSHA string) string {
	sha := ""
	for _, c := range cs {
		if c.HeadSHA == "" {
			continue
		}
		if sha != "" && sha != c.HeadSHA {
			return loadedSHA
		}
		sha = c.HeadSHA
	}
	if sha == "" {
		return loadedSHA
	}
	return sha
}

// buildSubmission turns the reader's queue into the payload GitHub takes.
//
// Pure, and separate from the call that posts it, because this is where the
// review's whole meaning is decided — the kind prefix, which side of the diff a
// line refers to, and which commit it is anchored to. A mistake in any of those
// posts to a teammate's pull request and cannot be tested through a subprocess.
func buildSubmission(anchorSHA string, event github.ReviewEvent, body string, cs []review.Comment) github.ReviewSubmission {
	sub := github.ReviewSubmission{
		// The commit the reader actually rendered. If the author has pushed
		// since, GitHub marks the review outdated — which is true — instead of
		// silently re-pointing every line number at code nobody read.
		CommitID: anchorSHA,
		Body:     body,
		Event:    event,
	}
	for _, c := range cs {
		sub.Comments = append(sub.Comments, github.ReviewComment{
			Path: c.File,
			Line: c.Line,
			// RIGHT always: the reader only lets a comment anchor to a line of
			// the new side, so there is no LEFT case to represent.
			Side: "RIGHT",
			// Payload and not Body: the kind is a prefix people already type by
			// hand ("nit: "), so it has to ship in the text or a nit arrives on
			// GitHub indistinguishable from a blocking objection.
			Body: c.Payload(),
		})
	}
	return sub
}

// handleReviewSubmitted records the outcome.
//
// The pending comments are dropped only on success: a failed submit leaves them
// exactly where they were, because the fix is usually to retry, and a review
// that was refused must not also have been thrown away.
func (h *Home) handleReviewSubmitted(msg reviewSubmitResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Debug("review submit: failed", "pr", msg.key.pr, "err", msg.err)
		h.reader.SubmitDone(msg.err, 0)
		if !h.reader.Visible() {
			// The sheet carries the reason while the reader is open. Closed, it
			// has nowhere to land but the main screen's error line.
			h.setError(msg.err)
		}
		return h, nil
	}

	delete(h.reviewComments, msg.key)
	// Cleared on disk too, in the same breath. A comment that reached GitHub and
	// stayed in the table would come back as a draft on the next launch and be
	// offered for posting a second time.
	h.saveReviewComments(msg.key)
	h.reader.SubmitDone(nil, msg.count)
	h.actionLog.Add("submit review", strconv.Itoa(msg.key.pr), true)
	if !h.reader.Visible() {
		h.setInfo(fmt.Sprintf("Submitted review on #%d", msg.key.pr))
	}
	return h, nil
}

// saveReviewComments writes a pull request's queued comments to SQLite.
//
// Called at every moment the app's own copy changes — a comment saved, the
// reader handing its edits back on close, a submit clearing the queue — because
// those are the only three, and a review in progress that survives everything
// except a crash at the wrong moment is not one you can rely on.
func (h *Home) saveReviewComments(k reviewKey) {
	if h.storage == nil || k.repo == "" {
		return
	}
	rows := make([]session.ReviewComment, 0, len(h.reviewComments[k]))
	for _, c := range h.reviewComments[k] {
		rows = append(rows, session.ReviewComment{
			Repo: k.repo, PR: k.pr, Path: c.file, Line: c.line,
			// The NAME, so reordering CommentKinds cannot silently reinterpret
			// a saved question as a nit.
			Kind: c.kind.String(), Body: c.body,
			// Each comment's OWN anchor, so a draft that has sat through a push
			// still submits against the commit it was written against.
			HeadSHA: c.headSHA,
		})
	}
	if err := h.storage.SaveReviewComments(k.repo, k.pr, rows); err != nil {
		debuglog.Logger.Error("review: failed to save comments", "repo", k.repo, "pr", k.pr, "err", err)
	}
}

// saveReviewRead writes a pull request's read marks to SQLite.
//
// Only on reader close, not per keypress: the reader owns the map while it is
// open and mutates it directly, so close is the moment the app learns anything
// changed. The cost is a crash mid-review losing that sitting's marks — cheaper
// than a write on every press of the key you press most.
func (h *Home) saveReviewRead(k reviewKey) {
	if h.storage == nil || k.repo == "" {
		return
	}
	headSHA := h.reviewFiles[k].HeadSHA
	var paths []string
	for path, read := range h.reviewRead[k] {
		if read {
			paths = append(paths, path)
		}
	}
	// Sorted so the table does not churn on Go's random map order — it makes a
	// diff of the database legible when something goes wrong.
	sort.Strings(paths)
	if err := h.storage.SaveReviewRead(k.repo, k.pr, headSHA, paths); err != nil {
		debuglog.Logger.Error("review: failed to save read marks", "repo", k.repo, "pr", k.pr, "err", err)
	}
}

// loadReviewState restores read marks and queued comments at startup.
//
// A row naming a repo fleet no longer knows is kept rather than pruned: the
// checkout may simply not be open right now, and throwing away someone's
// half-written review because a directory moved is not a trade worth making.
func (h *Home) loadReviewState() {
	if h.storage == nil {
		return
	}
	if marks, err := h.storage.LoadReviewRead(); err == nil {
		for _, m := range marks {
			k := reviewKey{repo: m.Repo, pr: m.PR}
			if h.reviewRead[k] == nil {
				h.reviewRead[k] = map[string]bool{}
			}
			h.reviewRead[k][m.Path] = true
		}
	}
	if cs, err := h.storage.LoadReviewComments(); err == nil {
		for _, c := range cs {
			k := reviewKey{repo: c.Repo, pr: c.PR}
			h.reviewComments[k] = append(h.reviewComments[k], reviewCommentMsg{
				key: k, file: c.Path, line: c.Line,
				kind: review.ParseCommentKind(c.Kind), body: c.Body, headSHA: c.HeadSHA,
			})
		}
	}
}

// syncReviewComments replaces the app's record of a PR's pending comments with
// the reader's, which is authoritative while the reader is open.
//
// A replace and not an append: the reader is where a comment is edited and
// deleted, so appending would resurrect every one the user threw away and hand
// it to whatever eventually submits the batch.
func (h *Home) syncReviewComments(k reviewKey, cs []review.Comment) {
	if k.pr == 0 {
		return
	}
	if h.reviewComments == nil {
		h.reviewComments = map[reviewKey][]reviewCommentMsg{}
	}
	if len(cs) == 0 {
		delete(h.reviewComments, k)
		h.saveReviewComments(k)
		return
	}
	out := make([]reviewCommentMsg, 0, len(cs))
	for _, c := range cs {
		// A submitted comment is not pending. Carrying it here would re-seed it
		// as unsent on the next open and offer to post it a second time.
		if c.Sent {
			continue
		}
		// A comment with no anchor is one written in this sitting, against the
		// diff currently loaded. One that already has an anchor keeps it.
		sha := c.HeadSHA
		if sha == "" {
			sha = h.reviewFiles[k].HeadSHA
		}
		out = append(out, reviewCommentMsg{
			key: k, file: c.File, line: c.Line, kind: c.Kind, body: c.Body, headSHA: sha,
		})
	}
	if len(out) == 0 {
		delete(h.reviewComments, k)
	} else {
		h.reviewComments[k] = out
	}
	h.saveReviewComments(k)
}
