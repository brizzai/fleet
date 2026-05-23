package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// GetBranchName returns the current branch name for the given repo path.
// Returns empty string if not a git repo or on error.
func GetBranchName(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// HasUncommittedChanges returns true if the working tree has uncommitted changes.
func HasUncommittedChanges(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// BranchInfo holds metadata about a git branch.
type BranchInfo struct {
	Name        string
	IsRemote    bool // only exists as remote (no local)
	IsCurrent   bool
	CommitDate  time.Time
	AuthorEmail string // email of the last commit's author
}

// ListBranches returns all branches sorted by most recently committed first.
// Includes both local and remote branches, with deduplication.
func ListBranches(repoPath string) ([]BranchInfo, error) {
	cmd := exec.Command("git", "-C", repoPath, "for-each-ref",
		"--sort=-committerdate",
		"--format=%(refname:short)\t%(committerdate:unix)\t%(authoremail)",
		"refs/heads/", "refs/remotes/origin/")
	output, err := cmd.Output()
	if err != nil {
		debuglog.Logger.Debug("ListBranches failed", "path", repoPath, "error", err)
		return nil, fmt.Errorf("git for-each-ref: %w", err)
	}

	currentBranch := GetBranchName(repoPath)

	localSet := make(map[string]bool)
	var branches []BranchInfo

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		name := parts[0]
		var commitDate time.Time
		var authorEmail string
		if len(parts) >= 2 {
			if unix, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				commitDate = time.Unix(unix, 0)
			}
		}
		if len(parts) >= 3 {
			authorEmail = strings.Trim(parts[2], "<>")
		}

		if strings.HasPrefix(name, "origin/") {
			remoteName := strings.TrimPrefix(name, "origin/")
			if remoteName == "HEAD" {
				continue
			}
			if localSet[remoteName] {
				continue // already have local version
			}
			branches = append(branches, BranchInfo{
				Name:        remoteName,
				IsRemote:    true,
				IsCurrent:   remoteName == currentBranch,
				CommitDate:  commitDate,
				AuthorEmail: authorEmail,
			})
		} else {
			localSet[name] = true
			branches = append(branches, BranchInfo{
				Name:        name,
				IsRemote:    false,
				IsCurrent:   name == currentBranch,
				CommitDate:  commitDate,
				AuthorEmail: authorEmail,
			})
		}
	}

	// Move current branch to index 0.
	for i, b := range branches {
		if b.IsCurrent && i > 0 {
			branches = append([]BranchInfo{b}, append(branches[:i], branches[i+1:]...)...)
			break
		}
	}

	return branches, nil
}

// GetUserEmail returns the git user.email for the given repo.
func GetUserEmail(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "config", "user.email")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// CheckoutBranch checks out the given branch in the repo.
// For remote-only branches, git auto-creates a local tracking branch.
func CheckoutBranch(repoPath, branch string) error {
	cmd := exec.Command("git", "-C", repoPath, "checkout", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		debuglog.Logger.Debug("CheckoutBranch failed", "path", repoPath, "branch", branch, "error", strings.TrimSpace(string(output)))
		return fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return nil
}

// GetDefaultBranch returns the default base ref for the given repo, used to
// pre-fill the worktree dialog. When an origin remote is configured, it returns
// the remote-tracking ref (e.g. "origin/master") so new worktrees start from the
// remote tip rather than the local branch. Falls back to local "main"/"master".
func GetDefaultBranch(repoPath string) string {
	// Try origin HEAD reference first.
	cmd := exec.Command("git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if output, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(output))
		return "origin/" + strings.TrimPrefix(ref, "refs/remotes/origin/")
	} else {
		debuglog.Logger.Debug("GetDefaultBranch symbolic-ref failed, trying fallback", "path", repoPath, "error", err)
	}
	// Fallback: check if "main" or "master" exists.
	for _, name := range []string{"main", "master"} {
		cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+name)
		if cmd.Run() == nil {
			return name
		}
	}
	debuglog.Logger.Debug("GetDefaultBranch no default branch found, using 'main'", "path", repoPath)
	return "main"
}

// IsWorktree returns true if the given path is a git worktree (not the main repo).
func IsWorktree(repoPath string) bool {
	gitDir := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir")
	gitDirOut, err := gitDir.Output()
	if err != nil {
		return false
	}

	commonDir := exec.Command("git", "-C", repoPath, "rev-parse", "--git-common-dir")
	commonDirOut, err := commonDir.Output()
	if err != nil {
		return false
	}

	gitDirPath := strings.TrimSpace(string(gitDirOut))
	commonDirPath := strings.TrimSpace(string(commonDirOut))

	// Resolve to absolute paths for comparison.
	if !filepath.IsAbs(gitDirPath) {
		gitDirPath = filepath.Join(repoPath, gitDirPath)
	}
	if !filepath.IsAbs(commonDirPath) {
		commonDirPath = filepath.Join(repoPath, commonDirPath)
	}

	gitDirPath = filepath.Clean(gitDirPath)
	commonDirPath = filepath.Clean(commonDirPath)

	return gitDirPath != commonDirPath
}
