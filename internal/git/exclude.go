package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brizzai/fleet/internal/debuglog"
)

// FleetExcludeEntry is the single ignore pattern fleet manages. Anchored at the
// working-tree root so a vendored .fleet/ deeper in the tree is untouched.
//
// It covers .fleet/ticket/<ID>/, where materialized Linear tickets and their
// screenshots land. Those are a working aid, not source: a customer screenshot
// committed into git history is a data-retention problem nobody signed up for.
const FleetExcludeEntry = "/.fleet/"

const excludeMarker = "# fleet: materialized tickets and scratch state (managed by fleet)"

// excludeMu serializes AddFleetExclude. Several worktrees of one repo can be
// created concurrently and they all resolve to the SAME exclude file (see
// ExcludeFilePath), so without this the read-check-append races and duplicates.
var excludeMu sync.Mutex

// ExcludeFilePath returns the exclude file git actually reads for the working
// tree at path.
//
// This MUST go through `rev-parse --git-path`, never `--git-dir` joined with
// "info/exclude". In a linked worktree --git-dir returns
// .git/worktrees/<name>, but "info" is on git's shared-path list, so git reads
// the COMMON .git/info/exclude. Writing to .git/worktrees/<name>/info/exclude
// creates a file git never opens — the pattern would look installed in our logs
// and exclude nothing, which is the worst of both outcomes. Verified: from
// inside a linked worktree, `git check-ignore -v` attributes rules to the main
// checkout's .git/info/exclude.
//
// --git-path also saves us a version check: `--path-format=absolute` needs git
// 2.31+, whereas resolving a relative answer against path works everywhere.
func ExcludeFilePath(path string) (string, error) {
	out, err := gitOutput("-C", path, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", fmt.Errorf("resolve exclude path: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("resolve exclude path: git returned no path")
	}
	if !filepath.IsAbs(p) {
		// `git -C path` ran with path as its working directory, so a relative
		// answer is relative to path.
		p = filepath.Join(path, p)
	}
	return p, nil
}

// AddFleetExclude ensures FleetExcludeEntry is present in the exclude file for
// the working tree at path. Idempotent: repeat calls — including from sibling
// worktrees, which share one exclude file — add nothing.
//
// Note this writes into the MAIN checkout's .git/info/exclude even when called
// from a worktree, and so covers .fleet/ everywhere in the repo. That is the
// intended scope: a session started on the main clone can materialize a ticket
// there too.
func AddFleetExclude(path string) error {
	excludeMu.Lock()
	defer excludeMu.Unlock()

	file, err := ExcludeFilePath(path)
	if err != nil {
		return err
	}

	existing, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", file, err)
	}
	for line := range strings.SplitSeq(string(existing), "\n") {
		if strings.TrimSpace(line) == FleetExcludeEntry {
			return nil
		}
	}

	// A bare-ish or freshly-cloned repo may not have info/ yet.
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(file), err)
	}

	var b strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(excludeMarker + "\n")
	b.WriteString(FleetExcludeEntry + "\n")

	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}

	debuglog.Logger.Debug("added fleet exclude", "path", path, "file", file)
	return nil
}
