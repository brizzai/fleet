package git

import (
	"os"
	"path/filepath"
	"strings"
)

// ReadHead returns the checkout root containing path and its current branch,
// read straight from the HEAD file rather than by running git. That is what
// lets `fleet status` name ~70 sessions' branches for the cost of a few file
// reads, where GetBranchName would fork git once per session.
//
// A linked worktree's .git is a file pointing at its own gitdir, whose HEAD is
// per-worktree — so this follows the pointer rather than reading the main
// repository's HEAD. A detached HEAD reads as "HEAD", matching
// `git rev-parse --abbrev-ref HEAD`. Both values are "" when path is not inside
// a checkout.
func ReadHead(path string) (root, branch string) {
	dir := filepath.Clean(path)
	for {
		dotGit := filepath.Join(dir, ".git")
		if st, err := os.Stat(dotGit); err == nil {
			gitDir := dotGit
			if !st.IsDir() {
				gitDir = linkedGitDir(dotGit)
				if gitDir == "" {
					return dir, ""
				}
			}
			return dir, headBranch(filepath.Join(gitDir, "HEAD"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// linkedGitDir reads a "gitdir: <path>" pointer file, resolving a relative path
// against the file's own directory.
func linkedGitDir(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(file), target)
	}
	return target
}

func headBranch(headFile string) string {
	data, err := os.ReadFile(headFile)
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	}
	if head != "" {
		return "HEAD"
	}
	return ""
}
