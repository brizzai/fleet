package ticket

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// identifierRe matches a tracker identifier at the start of a branch segment:
// BRZ-3182, brz-3182-some-slug, and (after the last "/") alice/brz-3182-x.
//
// One pattern serves both providers because a Linear team key and a Jira
// project key are the same shape — which is why this file was always the
// provider-neutral half of the old linear package.
//
// It is deliberately loose about the prefix, because the CALLER gates on the
// repo's real keys. That split matters: an ungated pattern like this reads
// fix-123-thing as FIX-123 and release-2024-cleanup as RELEASE-2024 —
// identifiers for teams that don't exist, costing a network round trip and a
// wrong answer.
var identifierRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]{0,9})-(\d{1,7})(?:[-_./]|$)`)

// matchesKey reports whether candidate is one of the repo's tracker keys.
//
// This is the gate the loose regex above depends on, and passing a SET rather
// than a single key is what lets one repo track more than one team or project —
// a real case, since a workspace routinely has several (this one has BRZ and
// PRD) and a repo may legitimately see branches from both. With two providers
// connected the set is the union across them.
func matchesKey(candidate string, keys []string) bool {
	for _, k := range keys {
		if strings.EqualFold(candidate, k) {
			return true
		}
	}
	return false
}

// IdentifierFromBranch extracts the identifier a branch names, gated on keys.
// It returns "" when the branch names no issue for those keys.
//
// Matching starts after the last "/", so a tracker's own suggested branch names
// (alice/brz-3182-slug) resolve as well as fleet's (brz-3182-slug).
func IdentifierFromBranch(branch string, keys []string) string {
	if branch == "" || len(keys) == 0 {
		return ""
	}
	seg := branch
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	m := identifierRe.FindStringSubmatch(seg)
	if m == nil || !matchesKey(m[1], keys) {
		return ""
	}
	return strings.ToUpper(m[1]) + "-" + m[2]
}

// LooksLikeIdentifier reports whether text is an identifier for one of keys and
// nothing else — the shape test the worktree dialog uses to decide whether what
// you typed denotes a ticket or is just a branch name.
//
// This is what keeps a picker from ever stealing the Enter key from someone
// naming a branch: prose fails this test, so the literal text stays the default.
func LooksLikeIdentifier(text string, keys []string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || len(keys) == 0 {
		return "", false
	}
	m := identifierRe.FindStringSubmatch(t)
	if m == nil || len(m[0]) != len(t) || !matchesKey(m[1], keys) {
		return "", false
	}
	return strings.ToUpper(m[1]) + "-" + m[2], true
}

// maxBranchSlug caps the title-derived tail. Long enough to stay readable in a
// sidebar row, short enough that the derived worktree directory name
// (<repo>-<branch>) doesn't run away.
const maxBranchSlug = 40

// BranchNameFor derives fleet's branch name for an issue: the lowercased
// identifier, then a slug of the title.
//
// Deliberately not the tracker's own suggested branch name: Linear's carries an
// owner prefix (alice/brz-3182-…) and Jira's carries the summary verbatim. Both
// trackers link a PR by finding the identifier ANYWHERE in the branch name, so
// every form links identically — and this one matches the convention already in
// use across the user's worktrees.
//
// The result is always a valid git ref: the identifier alone is valid, and the
// slug only ever appends [a-z0-9-] runs. An empty or punctuation-only title
// yields the bare identifier with no trailing dash.
func BranchNameFor(id, title string) string {
	base := strings.ToLower(strings.TrimSpace(id))
	slug := slugify(title, maxBranchSlug)
	if slug == "" {
		return base
	}
	return base + "-" + slug
}

// slugify lowercases, collapses every run of non-alphanumerics to a single
// dash, and truncates on a dash boundary so a cut never lands mid-word.
func slugify(s string, limit int) string {
	var b strings.Builder
	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII, unicode.IsDigit(r) && r < unicode.MaxASCII:
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) <= limit {
		return out
	}
	out = out[:limit]
	if i := strings.LastIndexByte(out, '-'); i > 0 {
		out = out[:i]
	}
	return strings.Trim(out, "-")
}

// sanitizeFilename makes a downloaded image's alt text safe to use as a file
// name, without its extension — the caller appends the one it recovered from
// the bytes, which is the only trustworthy source.
//
// Stripping a trailing image extension first matters because Linear's default
// alt text is literally "image.png", and a Jira attachment's alt IS its
// filename: slugifying either whole string would give "image-png", and the
// result would read "1-image-png.png".
//
// The index prefix keeps files distinct when several images share alt text,
// which is the common case.
func sanitizeFilename(alt string, index int) string {
	base := strings.TrimSpace(alt)
	if ext := strings.ToLower(filepath.Ext(base)); ext != "" {
		if _, known := knownImageExt[ext]; known {
			base = base[:len(base)-len(ext)]
		}
	}
	slug := slugify(base, 48)
	if slug == "" {
		slug = "image"
	}
	return strconv.Itoa(index) + "-" + slug
}
