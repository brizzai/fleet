package reviewjob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/deliver"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// CheckSendable reports why rec cannot be sent yet, or nil. The UI dims its
// send key with this reason; Send runs the same check.
func CheckSendable(rec *Record) error {
	if rec == nil || rec.Row == nil {
		return ErrNoRun
	}
	switch rec.Status {
	case session.ReviewSent, session.ReviewApproved:
		return ErrAlreadySent
	case session.ReviewReady:
	default:
		return ErrNotReady
	}
	if rec.Run == nil {
		return ErrNotReady
	}
	answers := sendableAnswers(rec)
	for _, a := range answers {
		switch a.Kind {
		case selfreview.AnswerChange, selfreview.AnswerAsk:
			if a.Note == "" {
				return &NoteError{StopID: a.StopID, Kind: a.Kind}
			}
		}
	}
	// Only a change or a question gives the agent something to do; waking it
	// to say "nothing to change" is what approving is for.
	c := selfreview.CountsOf(rec.Run, answers)
	switch {
	case c.Acting() > 0:
		return nil
	case c.OK+c.Later > 0:
		return ErrNothingToChange
	}
	return ErrNoAnswers
}

// sendableAnswers are the answers that belong to rec's run: one of its stops,
// or a file the human promoted from the skip list.
func sendableAnswers(rec *Record) []selfreview.Answer {
	ids := map[string]bool{}
	for _, s := range rec.Run.Stops {
		ids[s.ID] = true
	}
	var out []selfreview.Answer
	for _, a := range rec.Answers {
		if ids[a.StopID] || strings.HasPrefix(a.StopID, "file:") {
			out = append(out, a)
		}
	}
	return out
}

// Send delivers rec's answers to s — the send target, one of the checkout's
// sessions: it writes the full round to
// <worktree>/.fleet/review/round-N.md, types one compact message into the
// session through internal/deliver (refusing while the agent is running), and
// marks the run sent with s recorded as its target.
//
// Errors: ErrNoRun / ErrNotReady / ErrAlreadySent / ErrNoAnswers / *NoteError
// before anything is written; deliver's *Refusal (errors.Is against its
// sentinels; Short() for the TUI) when the pane refused. A refused send leaves
// the run ready, so it can be sent again.
func Send(ctx context.Context, d Deps, s *session.Session, rec *Record) (deliver.Result, error) {
	wt := ""
	if s != nil {
		wt = s.ProjectPath
	}
	msg, err := Compose(rec, wt)
	if err != nil {
		return deliver.Result{}, err
	}
	res, err := d.deliver(ctx, s, msg)
	if res.Woke {
		// The wake happened even if the paste after it did not; the row must
		// name the new tmux session either way.
		deliver.PersistRelaunch(d.Store, s.ID, res.PrevTmuxSession, res.PrevClaudeSessionID, s)
	}
	if err != nil {
		return res, err
	}
	at := d.now()
	if err := MarkSentTo(d.Store, rec.Row.ID, s.ID, at); err != nil {
		return res, err
	}
	rec.Row.SessionID = s.ID
	rec.Row.Status, rec.Status = session.ReviewSent, session.ReviewSent
	if rec.Sent == nil {
		rec.Sent = map[string]time.Time{}
	}
	for _, a := range rec.Answers {
		rec.Sent[a.StopID] = at
	}
	return res, nil
}

// Compose checks rec is sendable, writes the full round to
// <worktree>/.fleet/review/round-N.md (git-excluded first) and returns the
// compact message for the agent — Send's half that types nothing, also what a
// new session started with the answers takes as its first prompt. fallbackDir
// is used when the run recorded no worktree.
func Compose(rec *Record, fallbackDir string) (string, error) {
	if err := CheckSendable(rec); err != nil {
		return "", err
	}
	answers := sendableAnswers(rec)
	round := rec.Row.Round
	tree := rec.Row.TreeSHA

	// The exclude goes in BEFORE the first byte: a window where the file
	// exists and the exclude does not is a window where `git add -A` sweeps
	// it into a commit (the ticket rule).
	wt := rec.Row.Worktree
	if wt == "" {
		wt = fallbackDir
	}
	if wt == "" {
		return "", ErrNoCheckout
	}
	if err := git.AddFleetExclude(wt); err != nil {
		return "", fmt.Errorf("excluding .fleet/ from git: %w", err)
	}
	file := filepath.Join(wt, filepath.FromSlash(selfreview.RoundFile(round)))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	md := selfreview.ComposeRoundMarkdown(rec.Run, answers, round, tree)
	if err := writeFileAtomic(file, []byte(md)); err != nil {
		return "", err
	}
	return selfreview.ComposeMessage(rec.Run, answers, round, tree), nil
}

// MarkSentTo records that the run's answers reached sessionID: the session
// becomes the run's target and the run (and every answer) is marked sent.
func MarkSentTo(st Store, runID, sessionID string, at time.Time) error {
	if err := st.SetReviewTarget(runID, sessionID); err != nil {
		return fmt.Errorf("delivered, but recording it failed: %w", err)
	}
	if err := st.MarkReviewSent(runID, at); err != nil {
		return fmt.Errorf("delivered, but recording it failed: %w", err)
	}
	return nil
}

// writeFileAtomic writes via a temp file + rename, so the agent never reads a
// half-written round file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".round-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
