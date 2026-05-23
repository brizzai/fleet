package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// CopyConfiguredFiles copies each path declared in srcRepo's merged
// copy_files.paths list from srcRepo into dstRepo, preserving relative layout.
// Each entry is a repo-relative literal path or filepath.Glob pattern; matches
// may be files (copied) or directories (copied recursively, symlinks not
// followed). Missing source paths and patterns with no matches are silent.
// Per-entry errors are logged and the next entry is attempted; this function
// never fails the caller (worktree already exists by the time it runs).
func CopyConfiguredFiles(srcRepo, dstRepo string) {
	patterns := CopyFilesPatterns(srcRepo)
	if len(patterns) == 0 {
		return
	}

	srcRepoAbs, err := filepath.Abs(srcRepo)
	if err != nil {
		debuglog.Logger.Error("copy_files: failed to resolve src repo path", "src", srcRepo, "err", err)
		return
	}

	for _, pattern := range patterns {
		if !patternWithinRepo(pattern) {
			debuglog.Logger.Warn("copy_files: rejecting pattern that escapes repo root",
				"src", srcRepo, "pattern", pattern)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(srcRepoAbs, pattern))
		if err != nil {
			debuglog.Logger.Warn("copy_files: invalid glob pattern",
				"src", srcRepo, "pattern", pattern, "err", err)
			continue
		}
		if len(matches) == 0 {
			continue
		}
		for _, srcPath := range matches {
			rel, err := filepath.Rel(srcRepoAbs, srcPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				debuglog.Logger.Warn("copy_files: skipping match outside repo",
					"src", srcRepo, "match", srcPath)
				continue
			}
			dstPath := filepath.Join(dstRepo, rel)
			if err := copyPath(srcPath, dstPath); err != nil {
				debuglog.Logger.Error("copy_files: copy failed",
					"src", srcPath, "dst", dstPath, "err", err)
			}
		}
	}
}

// patternWithinRepo returns false for patterns that are absolute or whose
// cleaned form escapes the repo root with "..".
func patternWithinRepo(pattern string) bool {
	if filepath.IsAbs(pattern) {
		return false
	}
	cleaned := filepath.Clean(pattern)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// copyPath copies src to dst. If src is a directory, the tree is walked and
// each regular file is copied with parent dirs created as needed. Directory
// symlinks are not followed (filepath.WalkDir default).
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}
