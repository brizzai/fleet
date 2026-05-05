package workspace

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
)

// RepoWorkspaceConfig is the structure of .fleet.json files.
type RepoWorkspaceConfig struct {
	Workspace ShellConfig    `json:"workspace"`
	PRChecks  PRChecksConfig `json:"pr_checks"`
}

// ShellConfig holds shell command configuration for workspace operations.
type ShellConfig struct {
	List    string `json:"list,omitempty"`
	Create  string `json:"create,omitempty"`
	Destroy string `json:"destroy,omitempty"`
}

// PRChecksConfig holds repo-level controls over how PR check rollup is computed.
type PRChecksConfig struct {
	// Ignore is a list of path.Match globs against check names. Matching checks
	// are dropped from the PR-badge rollup so a single noisy check (e.g. a
	// gitstream "minimum reviewers" gate) doesn't turn the whole badge red.
	Ignore []string `json:"ignore,omitempty"`
}

// loadMergedRepoConfig resolves .fleet.json / .fleet.local.json (with legacy
// .bc.json / .bc.local.json fallback) and returns the merged config. Workspace
// fields are merged field-by-field (local overrides base); PRChecks.Ignore is
// merged additively (concat + dedupe) so a checked-in shared list and a
// personal local list can coexist.
func loadMergedRepoConfig(repoPath string) RepoWorkspaceConfig {
	base := preferredConfig(repoPath, ".fleet.json", ".bc.json")
	local := preferredConfig(repoPath, ".fleet.local.json", ".bc.local.json")

	merged := base
	if local.Workspace.List != "" {
		merged.Workspace.List = local.Workspace.List
	}
	if local.Workspace.Create != "" {
		merged.Workspace.Create = local.Workspace.Create
	}
	if local.Workspace.Destroy != "" {
		merged.Workspace.Destroy = local.Workspace.Destroy
	}

	merged.PRChecks.Ignore = dedupeStrings(append(base.PRChecks.Ignore, local.PRChecks.Ignore...))
	return merged
}

// dedupeStrings returns the input with duplicates removed, preserving order.
func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// IgnorePatterns returns the merged pr_checks.ignore list for a repo, or nil
// if no config sets it. Patterns are path.Match globs against check names.
func IgnorePatterns(repoPath string) []string {
	return loadMergedRepoConfig(repoPath).PRChecks.Ignore
}

// ResolveProvider loads workspace config from repoPath. Preference is by file
// presence, not contents: if .fleet.json exists it wins (even when empty —
// that's how a user disables a stale legacy .bc.json without deleting it);
// otherwise .bc.json is used. Same rule for .fleet.local.json over
// .bc.local.json. Local overrides base field-by-field. Returns ShellProvider
// if any command ends up set, otherwise GitWorktreeProvider.
func ResolveProvider(repoPath string) Provider {
	merged := loadMergedRepoConfig(repoPath).Workspace

	// If any shell command is set, use ShellProvider.
	if merged.List != "" || merged.Create != "" || merged.Destroy != "" {
		return &ShellProvider{
			ListCmd:    merged.List,
			CreateCmd:  merged.Create,
			DestroyCmd: merged.Destroy,
		}
	}

	// Default: built-in git worktree provider.
	return &GitWorktreeProvider{}
}

// preferredConfig returns the config from preferredName if that file exists at
// repoPath, otherwise the config from legacyName. File presence is the signal —
// an empty preferred file still suppresses the legacy one (intended way to
// "disable" a stale legacy config without deleting it).
func preferredConfig(repoPath, preferredName, legacyName string) RepoWorkspaceConfig {
	if cfg, ok := loadRepoConfig(filepath.Join(repoPath, preferredName)); ok {
		return cfg
	}
	cfg, _ := loadRepoConfig(filepath.Join(repoPath, legacyName))
	return cfg
}

// loadRepoConfig reads and parses a repo workspace config file. Returns the
// parsed config and true when the file exists and parses; returns the zero
// config and false when the file is missing. A file that exists but fails to
// parse is logged and treated as "exists with empty config" (true) — the user's
// intent to override is honored even if their JSON is wrong, and the warning
// surfaces the parse failure in debug.log.
func loadRepoConfig(configPath string) (RepoWorkspaceConfig, bool) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return RepoWorkspaceConfig{}, false
		}
		// File exists but is unreadable (e.g. permission denied). Treat as
		// "exists with empty config" so the documented presence-wins behavior
		// holds — falling through to .bc.json would silently break it.
		debuglog.Logger.Warn("workspace: failed to read repo config; treating as empty override",
			"path", configPath, "err", err)
		return RepoWorkspaceConfig{}, true
	}
	var cfg RepoWorkspaceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		debuglog.Logger.Warn("workspace: failed to parse repo config; treating as empty override",
			"path", configPath, "err", err)
		return RepoWorkspaceConfig{}, true
	}
	cfg.PRChecks.Ignore = validateGlobs(cfg.PRChecks.Ignore, configPath)
	return cfg, true
}

// validateGlobs returns the subset of patterns that path.Match accepts as
// well-formed. Bad patterns are warn-logged once (here, at load time) with the
// originating config path; this keeps the runtime matcher in internal/github
// free of repeated log spam every refresh cycle.
func validateGlobs(patterns []string, configPath string) []string {
	if len(patterns) == 0 {
		return patterns
	}
	out := patterns[:0:len(patterns)]
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			debuglog.Logger.Warn("workspace: dropping invalid pr_checks.ignore glob",
				"path", configPath, "pattern", p, "err", err)
			continue
		}
		out = append(out, p)
	}
	return out
}
