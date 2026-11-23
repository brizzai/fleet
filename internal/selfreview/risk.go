package selfreview

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// HighRiskPath reports whether a changed path is one the model may never skip
// on its own judgement: migrations, auth, CI workflows, dependency manifests,
// and whatever REVIEW.md lists under `always-human:`.
//
// A high-risk file can still be skipped by rule (a classifier verdict such as
// "generated"), and it can still hold a stop; what it cannot be is skipped "by
// guide". When the model tries, Enforce moves it to Not covered — the place a
// human is told to look — rather than letting it vanish into the skip list.
//
// The test is deliberately broad: a false positive costs a human one glance at
// a file, a false negative hides a credential change under "looks fine".
func HighRiskPath(p string, alwaysHuman []string) bool {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "./")
	if p == "" || p == "." {
		return false
	}
	for _, g := range alwaysHuman {
		if globMatch(g, p) {
			return true
		}
	}
	base := path.Base(p)
	lowerBase := strings.ToLower(base)
	lowerPath := strings.ToLower(p)

	if manifestFiles[lowerBase] || manifestPattern.MatchString(lowerBase) {
		return true
	}
	if ciFiles[lowerBase] || strings.HasPrefix(lowerPath, ".github/workflows/") ||
		strings.HasPrefix(lowerPath, ".github/actions/") ||
		strings.HasPrefix(lowerPath, ".circleci/") || strings.HasPrefix(lowerPath, ".buildkite/") {
		return true
	}
	dirs := strings.Split(lowerPath, "/")
	for _, d := range dirs[:len(dirs)-1] {
		if migrationDirs[d] {
			return true
		}
	}
	if strings.Contains(lowerBase, "migration") {
		return true
	}
	for _, w := range pathWords(p) {
		if authWords[w] {
			return true
		}
	}
	return false
}

var manifestFiles = map[string]bool{
	"go.mod": true, "package.json": true, "pyproject.toml": true, "pipfile": true,
	"setup.py": true, "setup.cfg": true, "cargo.toml": true, "gemfile": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"settings.gradle": true, "settings.gradle.kts": true, "composer.json": true,
	"mix.exs": true, "pubspec.yaml": true, "package.swift": true, "podfile": true,
	"environment.yml": true, "deno.json": true, "bun.toml": true,
}

// manifestPattern covers the families with free-form names.
var manifestPattern = regexp.MustCompile(`^(requirements[\w.-]*\.(txt|in)|constraints[\w.-]*\.txt|.*\.gemspec|.*\.csproj|.*\.fsproj|directory\.packages\.props)$`)

var ciFiles = map[string]bool{
	".gitlab-ci.yml": true, "jenkinsfile": true, "azure-pipelines.yml": true,
	".travis.yml": true, "bitbucket-pipelines.yml": true, "cloudbuild.yaml": true,
	"cloudbuild.yml": true, "codeowners": true,
}

var migrationDirs = map[string]bool{
	"migrations": true, "migration": true, "migrate": true, "alembic": true,
	"flyway": true, "liquibase": true, "schema_migrations": true,
}

// authWords are matched against whole words of the path, never substrings:
// "author.go" and "authority" are not auth, "oauth_client.go" is.
var authWords = map[string]bool{
	"auth": true, "oauth": true, "oauth2": true, "authn": true, "authz": true,
	"authentication": true, "authorization": true, "authorize": true,
	"authorizer": true, "login": true, "logout": true, "sso": true, "saml": true,
	"oidc": true, "jwt": true, "rbac": true, "acl": true, "acls": true, "iam": true,
	"permission": true, "permissions": true, "credential": true, "credentials": true,
	"secret": true, "secrets": true, "password": true, "passwords": true,
	"keychain": true, "csrf": true, "cors": true,
}

// pathWords splits a path into lower-case words on separators and camelCase
// boundaries: "internal/oauthClient/tokenStore.go" → internal, oauth, client,
// token, store, go.
func pathWords(p string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(p)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) ||
			(i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

// globMatch matches a gitignore-flavoured glob against a slash path:
//   - `**` spans directories, `*` and `?` stay inside one segment;
//   - a pattern without a slash matches the base name at any depth;
//   - a pattern ending in `/` matches everything under that directory.
func globMatch(pattern, p string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if !strings.Contains(strings.TrimSuffix(pattern, "/**"), "/") && !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, path.Base(p))
		return ok
	}
	pattern = strings.TrimPrefix(pattern, "/")
	re, err := globRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(p)
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?") // zero or more directories
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ParseReviewRules splits REVIEW.md into the prose the reviewer reads and the
// `always-human:` globs fleet enforces itself. Both forms are accepted:
//
//	always-human: migrations/**, infra/*.tf
//
//	always-human:
//	- `billing/**`
//	- deploy/
//
// The always-human block is removed from the returned text: it is fleet's
// configuration, and the model gains nothing from seeing it that Enforce does
// not already guarantee.
func ParseReviewRules(md string) (text string, alwaysHuman []string) {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var keep []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), "`'\"")
		if s != "" && !seen[s] {
			seen[s] = true
			alwaysHuman = append(alwaysHuman, s)
		}
	}
	inBlock := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if inBlock {
			if item, ok := listItem(t); ok {
				add(item)
				continue
			}
			inBlock = false
			if t == "" {
				keep = append(keep, line)
				continue
			}
		}
		if rest, ok := alwaysHumanKey(t); ok {
			if strings.TrimSpace(rest) == "" {
				inBlock = true
				continue
			}
			for _, g := range strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == ' ' }) {
				add(g)
			}
			continue
		}
		keep = append(keep, line)
	}
	text = strings.TrimSpace(strings.Join(keep, "\n"))
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text, alwaysHuman
}

// alwaysHumanKey recognises the key line, tolerating list markers, bold and a
// heading prefix ("- **always-human:**", "## always-human:"). Bold is only
// unwrapped around the key: the value is globs, where "**" means something.
func alwaysHumanKey(t string) (string, bool) {
	t = strings.TrimLeft(t, "-*#>` \t")
	lower := strings.ToLower(t)
	for _, k := range []string{"always-human", "always_human", "always human"} {
		if !strings.HasPrefix(lower, k) {
			continue
		}
		rest := strings.TrimLeft(t[len(k):], "*`")
		if !strings.HasPrefix(rest, ":") {
			return "", false
		}
		rest = rest[1:]
		if r := strings.TrimLeft(rest, "*`"); len(r) < len(rest) && (r == "" || r[0] == ' ' || r[0] == '\t') {
			rest = r // the closing "**" / "`" of a bold or code key
		}
		return rest, true
	}
	return "", false
}

func listItem(t string) (string, bool) {
	for _, m := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(t, m) {
			return strings.TrimSpace(t[len(m):]), true
		}
	}
	return "", false
}
