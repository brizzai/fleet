package git

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// GetOriginKey returns a stable origin identity for a repo path.
//
// For a github remote it returns "github.com/org/repo"; other hosts return
// "<host>/<path>". Repos with no remote — or any parse failure — fall back to
// "local:<basename>" so they remain distinct groups keyed off the folder name.
func GetOriginKey(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return "local:" + filepath.Base(repoPath)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "local:" + filepath.Base(repoPath)
	}

	url = strings.TrimSuffix(url, ".git")

	// git@host:org/repo
	if rest, ok := strings.CutPrefix(url, "git@"); ok {
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return "local:" + filepath.Base(repoPath)
		}
		return host + "/" + path
	}

	// scheme://[user@]host/path
	if _, rest, ok := strings.Cut(url, "://"); ok {
		if _, after, ok := strings.Cut(rest, "@"); ok {
			rest = after
		}
		host, path, ok := strings.Cut(rest, "/")
		if !ok {
			return "local:" + filepath.Base(repoPath)
		}
		return host + "/" + path
	}

	return "local:" + filepath.Base(repoPath)
}
