package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// FileDiff is one changed file between two trees, as the self-review reader
// consumes it. Status is a single letter — A added, M modified (a type change
// folds in here), D deleted, R renamed. OldPath is set for renames only.
// Additions/Deletions come from numstat and are zero for a binary file, which
// is flagged instead. Patch is that file's hunks from its first `@@` line to
// the end of its section — no `diff --git`/index/---/+++ preamble — and is
// empty for a binary file or a pure rename.
type FileDiff struct {
	Path      string
	OldPath   string
	Status    string
	Additions int
	Deletions int
	Binary    bool
	Patch     string
}

// diffTimeout bounds the snapshot and diff subprocesses. Longer than
// gitTimeout because these walk a whole tree rather than answer one question:
// `git add -A` over a large worktree full of untracked files stats and hashes
// every one of them. Still bounded, since a held lock or a wedged filesystem
// must cost a failed review, not a goroutine parked forever. Vars rather than
// consts so a test can shorten them, the same reason FetchBaseRef's are.
var (
	diffTimeout   = 60 * time.Second
	diffWaitDelay = time.Second
)

// runGitDiff runs `git -C dir <args>` under diffTimeout and returns stdout.
// Modelled on FetchBaseRef: WaitDelay so a pipe-holding grandchild (a clean or
// smudge filter, say) can't stretch the timeout, GIT_TERMINAL_PROMPT=0 so
// nothing sits on a prompt, and GIT_OPTIONAL_LOCKS=0 so a read never
// opportunistically rewrites the user's real index behind their back. extraEnv
// is appended last, and exec keeps the last value of a duplicated key — which is
// what lets SnapshotTree's GIT_INDEX_FILE win over one inherited from the
// environment.
func runGitDiff(dir string, extraEnv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = diffWaitDelay
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(env, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("git %s: timed out after %s", args[0], diffTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

// checkRev refuses a revision git's option parser would read as a flag. The
// `--` terminators below separate revisions from *paths*; a revision itself
// sits before them, so `--output=/tmp/x` passed as one would reach the option
// parser. Same threat FetchBaseRef's terminator answers: a base ref is typed by
// a person or stored from one.
func checkRev(rev string) error {
	if rev == "" {
		return errors.New("empty revision")
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("revision %q looks like an option", rev)
	}
	return nil
}

// resolveCommit turns a ref into a full commit SHA, or fails.
func resolveCommit(dir, rev string) (string, error) {
	if err := checkRev(rev); err != nil {
		return "", err
	}
	out, err := runGitDiff(dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown revision %q", rev)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the SHA of the best common ancestor of base and dir's HEAD
// — the point a branch's own changes are measured from, so commits that landed
// on base after the branch was cut never show up as the branch's work.
func MergeBase(dir, base string) (string, error) {
	baseSHA, err := resolveCommit(dir, base)
	if err != nil {
		return "", err
	}
	headSHA, err := resolveCommit(dir, "HEAD")
	if err != nil {
		return "", err
	}
	out, err := runGitDiff(dir, nil, "merge-base", baseSHA, headSHA)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("no common ancestor between %s and HEAD", base)
	}
	return sha, nil
}

// SnapshotTree writes the worktree exactly as it stands — committed, staged,
// unstaged and untracked files alike, minus whatever .gitignore excludes — as a
// git tree object and returns its SHA. That SHA is the review's anchor: the
// same bytes on disk always produce the same tree, so "has anything changed
// since the last review" is a string comparison.
//
// It never touches the real index. Everything runs against a throwaway
// GIT_INDEX_FILE in a temp dir, seeded from a *copy* of the worktree's own
// index: the copy carries stat data, so `add -A` re-hashes only files that
// actually changed instead of every file in the tree, and it carries
// skip-worktree bits, so a sparse checkout's absent files don't read as
// deletions. When there is no index to copy (or the copy is unusable) it falls
// back to `read-tree HEAD`, and to an empty index on an unborn branch.
//
// Side effect, and an unavoidable one: the blobs for changed and untracked
// files are written into the object store, as `git add` always does. They're
// unreachable until committed and gc prunes them on its normal schedule.
func SnapshotTree(dir string) (string, error) {
	tmp, err := os.MkdirTemp("", "fleet-snapshot-*")
	if err != nil {
		return "", fmt.Errorf("snapshot: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	idx := filepath.Join(tmp, "index")
	env := []string{"GIT_INDEX_FILE=" + idx}

	seeded := false
	if real, err := indexPath(dir); err == nil {
		if err := copyFile(real, idx); err == nil {
			seeded = true
		} else if !errors.Is(err, os.ErrNotExist) {
			debuglog.Logger.Debug("snapshot: index copy failed; seeding from HEAD", "dir", dir, "err", err)
		}
	}

	addAndWrite := func() (string, error) {
		if _, err := runGitDiff(dir, env, "add", "-A", "--", ":/"); err != nil {
			return "", err
		}
		out, err := runGitDiff(dir, env, "write-tree")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}

	if seeded {
		sha, err := addAndWrite()
		if err == nil {
			return sha, nil
		}
		// A copy torn mid-write by a concurrent git, or an index format this
		// git can't read: start over from HEAD rather than fail the review.
		debuglog.Logger.Debug("snapshot from copied index failed; reseeding from HEAD", "dir", dir, "err", err)
		_ = os.Remove(idx)
	}
	if _, err := resolveCommit(dir, "HEAD"); err == nil {
		if _, err := runGitDiff(dir, env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	// No HEAD (unborn branch): leave idx absent, which git reads as an empty
	// index, so the tree holds exactly the files on disk.
	return addAndWrite()
}

// indexPath returns the absolute path of dir's own index file. A linked
// worktree has its own index under .git/worktrees/<name>/, which
// `--git-path index` resolves and `--git-dir` + "/index" would get right only
// by accident.
func indexPath(dir string) (string, error) {
	out, err := runGitDiff(dir, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// diffFlags are the options every DiffFiles subprocess shares, so the three
// outputs list the same file pairs in the same order. Each one neutralizes a
// user config that would otherwise change what comes back: --no-ext-diff
// (diff.external), --no-color (color.diff=always), --no-relative
// (diff.relative, which would hide changes outside dir), and -M pinned rather
// than inherited from diff.renames. core.quotePath=false keeps non-ASCII paths
// readable in the patch headers; the path lists use -z and need no unquoting.
var diffFlags = []string{"-M", "--no-color", "--no-ext-diff", "--no-relative"}

// DiffFiles lists every file that differs between from and to (any tree-ish:
// a commit, a tree SHA from SnapshotTree), with per-file line counts and the
// file's hunks. Three git calls — name-status for the letter, numstat for the
// counts, the patch for the hunks — all with identical diff options, so they
// enumerate the same pairs in the same order.
func DiffFiles(dir, from, to string) ([]FileDiff, error) {
	if err := checkRev(from); err != nil {
		return nil, err
	}
	if err := checkRev(to); err != nil {
		return nil, err
	}
	diff := func(extra ...string) ([]byte, error) {
		args := []string{"-c", "core.quotePath=false", "diff"}
		args = append(args, diffFlags...)
		args = append(args, extra...)
		args = append(args, from, to, "--")
		return runGitDiff(dir, nil, args...)
	}

	nameStatus, err := diff("--name-status", "-z")
	if err != nil {
		return nil, err
	}
	files, err := parseNameStatusZ(nameStatus)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	numstat, err := diff("--numstat", "-z")
	if err != nil {
		return nil, err
	}
	counts, err := parseNumstatZ(numstat)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Additions, files[i].Deletions, files[i].Binary = c.add, c.del, c.binary
		}
	}

	patch, err := diff("-U3")
	if err != nil {
		return nil, err
	}
	sections := splitPatch(patch)
	if len(sections) == len(files) {
		for i := range files {
			files[i].Patch = hunksOf(sections[i])
		}
	} else {
		// Every file pair emits exactly one `diff --git` section, so this is
		// not expected. If it ever happens, a wrong hunk under the right file
		// header is worse than none: leave the patches empty and say so.
		debuglog.Logger.Warn("DiffFiles: patch sections don't match the file list; patches dropped",
			"dir", dir, "files", len(files), "sections", len(sections))
	}
	return files, nil
}

// parseNameStatusZ parses `git diff --name-status -z`: a status field, then one
// path, or two (old, new) for a rename or copy, each NUL-terminated.
func parseNameStatusZ(out []byte) ([]FileDiff, error) {
	fields := strings.Split(string(out), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	var files []FileDiff
	for i := 0; i < len(fields); {
		st := fields[i]
		if st == "" {
			return nil, errors.New("name-status: empty status field")
		}
		i++
		letter := st[:1]
		switch letter {
		case "R", "C":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("name-status: truncated %s entry", st)
			}
			f := FileDiff{OldPath: fields[i], Path: fields[i+1], Status: "R"}
			if letter == "C" {
				// Only reachable with copy detection, which DiffFiles doesn't
				// ask for. The new file is what was added.
				f = FileDiff{Path: fields[i+1], Status: "A"}
			}
			files = append(files, f)
			i += 2
		default:
			if i >= len(fields) {
				return nil, fmt.Errorf("name-status: truncated %s entry", st)
			}
			switch letter {
			case "A", "D", "M":
			default: // T (type change), U, X: content of an existing path moved
				letter = "M"
			}
			files = append(files, FileDiff{Path: fields[i], Status: letter})
			i++
		}
	}
	return files, nil
}

type lineCounts struct {
	add, del int
	binary   bool
}

// parseNumstatZ parses `git diff --numstat -z`, keyed by the file's (new)
// path: `<add>\t<del>\t<path>\0`, or for a rename `<add>\t<del>\t\0<old>\0<new>\0`.
// A binary file reports `-\t-`.
func parseNumstatZ(out []byte) (map[string]lineCounts, error) {
	fields := strings.Split(string(out), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	counts := make(map[string]lineCounts, len(fields))
	for i := 0; i < len(fields); {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("numstat: malformed field %q", fields[i])
		}
		i++
		path := parts[2]
		if path == "" { // rename: old and new follow as their own fields
			if i+1 >= len(fields) {
				return nil, errors.New("numstat: truncated rename entry")
			}
			path = fields[i+1]
			i += 2
		}
		var c lineCounts
		if parts[0] == "-" && parts[1] == "-" {
			c.binary = true
		} else {
			a, err1 := strconv.Atoi(parts[0])
			d, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("numstat: bad counts in %q", fields[i-1])
			}
			c.add, c.del = a, d
		}
		counts[path] = c
	}
	return counts, nil
}

// splitPatch cuts a unified diff into one section per file, each starting at
// its `diff --git` line. A hunk line always carries a one-character prefix
// (' ', '+', '-', '\'), so a line that *begins* with "diff --git " can only be
// a header, whatever the files contain.
func splitPatch(out []byte) []string {
	var sections []string
	var cur strings.Builder
	started := false
	for _, line := range strings.SplitAfter(string(out), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			if started {
				sections = append(sections, cur.String())
				cur.Reset()
			}
			started = true
		}
		if started {
			cur.WriteString(line)
		}
	}
	if started {
		sections = append(sections, cur.String())
	}
	return sections
}

// hunksOf returns a file section from its first `@@` line on, or "" when it has
// none (binary, pure rename, mode-only change).
func hunksOf(section string) string {
	if strings.HasPrefix(section, "@@") {
		return section
	}
	if i := strings.Index(section, "\n@@"); i >= 0 {
		return section[i+1:]
	}
	return ""
}

// ResolveBase picks the ref a session's changes are measured against, most
// specific first:
//
//  1. stored — the base the worktree was cut from (sessions.base_ref), when it
//     still resolves. It's what the user actually chose, so it wins over
//     anything inferred; but a branch deleted since is no answer at all.
//  2. the PR's base, as origin/<prBase>, when that remote-tracking ref exists:
//     a PR retargeted after creation is reviewed against where it will land.
//  3. GetDefaultBranch.
//
// It never fails: the last step always names something, and a base that turns
// out not to resolve fails later at MergeBase with a message that names it.
func ResolveBase(dir, stored, prBase string) string {
	if stored != "" && checkRev(stored) == nil &&
		gitRun("-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", stored+"^{commit}") == nil {
		return stored
	}
	if prBase != "" {
		ref := "origin/" + prBase
		if checkRev(prBase) == nil &&
			gitRun("-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/remotes/"+ref) == nil {
			return ref
		}
	}
	return GetDefaultBranch(dir)
}
