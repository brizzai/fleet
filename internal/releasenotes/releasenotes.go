// Package releasenotes fetches fleet's GitHub Releases so the TUI can show an
// in-app changelog. Content is cached to ~/.config/fleet so reopening is instant
// and works offline. Each release body is the curated CHANGELOG section (the
// release workflow feeds GoReleaser --release-notes built from CHANGELOG.md), so
// what we render reads exactly like the changelog.
package releasenotes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	repo         = "brizzai/fleet"
	apiURL       = "https://api.github.com/repos/" + repo + "/releases?per_page=100"
	cacheFile    = "releases.json"
	cacheMaxAge  = time.Hour // reuse the cache within this window; older triggers a refresh
	fetchTimeout = 3 * time.Second
)

// Section is one "### Added"/"### Fixed" group parsed from a release body.
type Section struct {
	Title   string   `json:"title"`
	Bullets []string `json:"bullets"`
}

// Release is a normalized GitHub release ready for rendering.
type Release struct {
	Version    string    `json:"version"` // normalized, e.g. "2.15.0"
	Name       string    `json:"name"`    // release name / tag as shown by GitHub
	Date       string    `json:"date"`    // "2006-01-02", from published_at
	Sections   []Section `json:"sections"`
	Prerelease bool      `json:"prerelease"`
	URL        string    `json:"url"` // html_url
}

// configDir returns ~/.config/fleet/, matching internal/update and the rest of fleet.
func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet")
}

// Load returns the release list, preferring a fresh on-disk cache and otherwise
// fetching from GitHub. On a network error it falls back to a stale cache; it
// only returns an error when there's no cache to fall back to.
func Load() ([]Release, error) {
	cached, cachedAt, haveCache := readCache()
	if haveCache && time.Since(cachedAt) < cacheMaxAge {
		return cached, nil
	}

	fresh, err := fetch()
	if err != nil {
		if haveCache {
			return cached, nil // offline / rate-limited: stale is better than nothing
		}
		return nil, err
	}
	writeCache(fresh)
	return fresh, nil
}

// ghRelease is the subset of the GitHub Releases API response we use.
type ghRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
	HTMLURL     string `json:"html_url"`
}

// fetch pulls all releases from GitHub and normalizes them, newest-first.
func fetch() ([]Release, error) {
	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := ghToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var raw []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return normalizeReleases(raw), nil
}

// normalizeReleases converts raw API rows into Releases, dropping drafts and
// sorting newest-first by version.
func normalizeReleases(raw []ghRelease) []Release {
	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		if r.Draft {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.TagName
		}
		releases = append(releases, Release{
			Version:    NormalizeVersion(r.TagName),
			Name:       name,
			Date:       formatDate(r.PublishedAt),
			Sections:   parseBody(r.Body),
			Prerelease: r.Prerelease,
			URL:        r.HTMLURL,
		})
	}
	sort.SliceStable(releases, func(i, j int) bool {
		return CompareVersions(releases[i].Version, releases[j].Version) > 0
	})
	return releases
}

// formatDate turns an RFC3339 published_at into "2006-01-02"; on any parse
// failure it returns the leading date-looking prefix (or the raw string).
func formatDate(published string) string {
	if published == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, published); err == nil {
		return t.Format("2006-01-02")
	}
	if len(published) >= 10 {
		return published[:10]
	}
	return published
}

// parseBody extracts the changelog sections from a release body. The body is
// "## fleet vX" + an "Install / Update" block + a "---" divider + the changelog
// sections, so we parse only what's after the first "---". If there's no divider
// (older/odd releases) we parse the whole body defensively.
func parseBody(body string) []Section {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")

	start := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			start = i + 1
			break
		}
	}

	var sections []Section
	var cur *Section
	ensure := func() {
		if cur == nil {
			sections = append(sections, Section{})
			cur = &sections[len(sections)-1]
		}
	}
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "### "):
			sections = append(sections, Section{Title: strings.TrimSpace(trimmed[4:])})
			cur = &sections[len(sections)-1]
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			ensure()
			cur.Bullets = append(cur.Bullets, strings.TrimSpace(trimmed[2:]))
		default:
			// Continuation of the previous bullet (wrapped line) — join with a space.
			if cur != nil && len(cur.Bullets) > 0 {
				last := len(cur.Bullets) - 1
				cur.Bullets[last] += " " + trimmed
			}
		}
	}
	return sections
}

// cacheEnvelope is what we persist: the parsed releases plus a fetch timestamp.
type cacheEnvelope struct {
	FetchedAt int64     `json:"fetched_at"`
	Releases  []Release `json:"releases"`
}

// readCache loads the on-disk cache, reporting whether it was present.
func readCache() (releases []Release, fetchedAt time.Time, ok bool) {
	data, err := os.ReadFile(filepath.Join(configDir(), cacheFile))
	if err != nil {
		return nil, time.Time{}, false
	}
	var env cacheEnvelope
	if err := json.Unmarshal(data, &env); err != nil || len(env.Releases) == 0 {
		return nil, time.Time{}, false
	}
	return env.Releases, time.Unix(env.FetchedAt, 0), true
}

// writeCache persists the releases with the current timestamp (best-effort).
func writeCache(releases []Release) {
	env := cacheEnvelope{FetchedAt: time.Now().Unix(), Releases: releases}
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	dir := configDir()
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, cacheFile), data, 0600)
}

// ghToken returns a GitHub token from env or the gh CLI. The repo is public so a
// token is optional; it only raises the API rate limit.
func ghToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

// Ago returns a short, human "time ago" for a "2006-01-02" date string, e.g.
// "3 days ago". Returns "" if the date can't be parsed.
func Ago(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return ago(t, time.Now())
}

// WithinLastDays reports whether a "2006-01-02" date is within the last n days
// (inclusive of today, forgiving of small clock skew). Unparseable or future
// dates beyond a day return false. Used to cap the What's New reel.
func WithinLastDays(date string, n int) bool {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	days := int(time.Since(t).Hours() / 24)
	return days >= -1 && days < n
}

// ago is the testable core of Ago: how long before now the date was.
func ago(then, now time.Time) string {
	days := int(now.Sub(then).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 30:
		return fmt.Sprintf("%d days ago", days)
	case days < 365:
		m := days / 30
		if m <= 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", m)
	default:
		y := days / 365
		if y <= 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", y)
	}
}

// NormalizeVersion strips a leading "v" and any git-describe suffix so build-time
// version strings ("v2.15.0", "v2.15.0-3-gabc-dirty", "2.15.0") compare against
// CHANGELOG-style "2.15.0". "dev" is returned unchanged.
func NormalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	return s
}

// CompareVersions compares two normalized dotted versions numerically.
// Returns -1 if a < b, 0 if equal, +1 if a > b. Non-numeric or unequal-length
// versions fall back to a segment-wise numeric compare (missing segments = 0).
func CompareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := max(len(as), len(bs))
	for i := range n {
		av, bv := segment(as, i), segment(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// segment returns the integer value of the i-th dotted segment, or 0 if missing
// or non-numeric.
func segment(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
	if err != nil {
		return 0
	}
	return n
}
