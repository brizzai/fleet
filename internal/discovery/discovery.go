// Package discovery surfaces the projects a user has recently worked in with
// Claude Code, by reading Claude's transcript journal at ~/.claude/projects.
//
// Fleet uses this to turn an empty first-run screen into a launchpad: instead
// of asking a new user to type a filesystem path from memory, it shows the
// repos they already use — ready to resume the exact conversation they left.
package discovery

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/naming"
)

// Recent is one project the user has run Claude Code in, resolved to a real
// on-disk path with the newest conversation ready to resume.
type Recent struct {
	Path            string    // real cwd, read from the transcript (dash-safe)
	Branch          string    // git branch recorded in the transcript
	OriginKey       string    // git origin identity (git.GetOriginKey), for grouping
	ClaudeSessionID string    // newest transcript's id — the resume target
	Title           string    // derived from the first real user prompt
	LastUsed        time.Time // newest transcript's mtime
	IsWorktree      bool      // cwd/.git is a file (linked worktree) vs a dir
}

const (
	maxLineScan = 400             // lines to scan per transcript before giving up
	maxLineSize = 8 * 1024 * 1024 // generous cap for a single JSONL line
)

// RecentRepos scans ~/.claude/projects and returns up to limit git projects,
// most-recently-used first. Non-git dirs and paths that no longer exist are
// dropped. Best-effort: unreadable entries are skipped silently. A limit <= 0
// returns every match.
func RecentRepos(limit int) []Recent {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects"))
	if err != nil {
		return nil
	}

	var out []Recent
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, ok := readProject(filepath.Join(home, ".claude", "projects", e.Name()))
		if !ok {
			continue
		}
		out = append(out, rec)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	// Dedup AFTER the recency sort so the kept record per resolved cwd is the
	// most-recently-used one — two project dirs can map to the same path (e.g.
	// a /var ↔ /private/var symlink), and the resume target must be the latest
	// transcript, not whichever dir sorted first alphabetically.
	seen := make(map[string]bool, len(out))
	deduped := out[:0]
	for _, rec := range out {
		if seen[rec.Path] {
			continue
		}
		seen[rec.Path] = true
		deduped = append(deduped, rec)
	}
	out = deduped
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	// Resolve the git origin only for the items we'll actually show — each is a
	// `git config` call, so we don't want it for the whole (possibly large)
	// candidate set. This is what lets the launchpad group worktrees of one
	// repo under a single origin header, exactly like fleet's sidebar.
	for i := range out {
		out[i].OriginKey = git.GetOriginKey(out[i].Path)
	}
	return out
}

// readProject reads one ~/.claude/projects/<dir>, resolving the newest
// transcript's cwd, branch, session id and a derived title. Returns ok=false
// when the dir holds no usable transcript or the cwd is gone / not a git repo.
func readProject(dir string) (Recent, bool) {
	path, sessionID, mod := newestTranscript(dir)
	if path == "" {
		return Recent{}, false
	}
	cwd, branch, prompt := scanTranscript(path)
	if cwd == "" {
		return Recent{}, false
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return Recent{}, false
	}
	// Fleet manages git repos and worktrees — skip plain directories (e.g. the
	// home dir, where Claude is sometimes run ad hoc).
	gitInfo, err := os.Stat(filepath.Join(cwd, ".git"))
	if err != nil {
		return Recent{}, false
	}

	title := naming.GenerateTitle(prompt)
	if title == "" {
		title = filepath.Base(cwd)
	}
	return Recent{
		Path:            cwd,
		Branch:          branch,
		ClaudeSessionID: sessionID,
		Title:           title,
		LastUsed:        mod,
		IsWorktree:      !gitInfo.IsDir(), // linked worktrees carry a .git *file*
	}, true
}

// newestTranscript returns the path, session id (filename sans .jsonl) and
// mtime of the most-recently-modified non-empty .jsonl in dir.
func newestTranscript(dir string) (path, sessionID string, mod time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", time.Time{}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		if info.ModTime().After(mod) {
			mod = info.ModTime()
			path = filepath.Join(dir, e.Name())
			sessionID = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return path, sessionID, mod
}

// transcriptLine is the subset of a Claude Code JSONL entry we read.
type transcriptLine struct {
	Type      string `json:"type"`
	IsMeta    bool   `json:"isMeta"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// scanTranscript walks a transcript for the recorded cwd/branch and the first
// real user prompt (skipping meta lines and command/caveat blocks). Stops as
// soon as both are known, or after maxLineScan lines — the early entries are
// all we need, so even multi-megabyte transcripts stay cheap.
func scanTranscript(path string) (cwd, branch, prompt string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for n := 0; sc.Scan() && n < maxLineScan; n++ {
		var ln transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		if cwd == "" && ln.Cwd != "" {
			cwd, branch = ln.Cwd, ln.GitBranch
		}
		if prompt == "" && ln.Type == "user" && !ln.IsMeta {
			prompt = firstPromptText(ln.Message.Content)
		}
		if cwd != "" && prompt != "" {
			break
		}
	}
	return cwd, branch, prompt
}

// firstPromptText pulls human prompt text from a message content field, which
// is either a JSON string or an array of typed parts. Returns "" for tool
// results and command/caveat blocks (which begin with "<").
func firstPromptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanPrompt(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				if t := cleanPrompt(p.Text); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// cleanPrompt trims a prompt and rejects the messages Claude records as
// "user" turns that aren't real typed prompts: command/caveat blocks wrapped
// in tags (<local-command-caveat>) and bracketed system notices
// ([Request interrupted by user]).
func cleanPrompt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "[") {
		return ""
	}
	return s
}
