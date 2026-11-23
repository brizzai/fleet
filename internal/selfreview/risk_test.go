package selfreview

import (
	"strings"
	"testing"
)

func TestHighRiskPath(t *testing.T) {
	always := []string{"libs/schemas/**", "deploy/", "*.tf", "**/secrets/*.yaml"}
	cases := map[string]bool{
		"db/migrations/0042_x.sql":            true,
		"alembic/versions/abc.py":             true,
		"internal/migration/migrate.go":       true,
		"pkg/auth/session.go":                 true,
		"internal/oauthClient/store.go":       true,
		"api/rbac_policy.go":                  true,
		"services/login_handler.py":           true,
		".github/workflows/ci.yml":            true,
		".gitlab-ci.yml":                      true,
		"go.mod":                              true,
		"web/package.json":                    true,
		"pyproject.toml":                      true,
		"requirements-dev.txt":                true,
		"requirements.txt":                    true,
		"libs/schemas/python/x.py":            true,
		"deploy/prod/values.yaml":             true,
		"infra/main.tf":                       true,
		"k8s/secrets/db.yaml":                 true,
		"internal/author/author.go":           false, // "author" is not auth
		"docs/authority.md":                   false,
		"go.sum":                              false, // a lockfile: rule-skipped, not high-risk
		"package-lock.json":                   false,
		"internal/ui/sidebar.go":              false,
		"README.md":                           false,
		"libs/other/x.py":                     false,
		"deployment.md":                       false,
		".github/ISSUE_TEMPLATE/bug.md":       false,
		"internal/session/session.go":         false,
		"pipelines/collectors/scanbox/col.go": false,
	}
	for p, want := range cases {
		if got := HighRiskPath(p, always); got != want {
			t.Errorf("HighRiskPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestParseReviewRules(t *testing.T) {
	md := strings.Join([]string{
		"# Review rules",
		"",
		"Never save a cursor before upload.",
		"",
		"always-human: billing/**, `infra/*.tf`",
		"",
		"- **Always-human:**",
		"- deploy/",
		"- 'migrations/**'",
		"",
		"Prefer small PRs.",
	}, "\n")
	text, globs := ParseReviewRules(md)
	if strings.Join(globs, " ") != "billing/** infra/*.tf deploy/ migrations/**" {
		t.Errorf("globs = %q", globs)
	}
	if strings.Contains(strings.ToLower(text), "always-human") || strings.Contains(text, "deploy/") {
		t.Errorf("always-human block left in the text:\n%s", text)
	}
	if !strings.Contains(text, "Never save a cursor") || !strings.Contains(text, "Prefer small PRs.") {
		t.Errorf("rules prose lost:\n%s", text)
	}
	if strings.Contains(text, "\n\n\n") {
		t.Error("removed block left a run of blank lines")
	}
}

// The value is globs, so a leading "*" belongs to the glob, not to bold.
func TestParseReviewRulesKeepsLeadingStar(t *testing.T) {
	for _, md := range []string{"always-human: *.tf", "**always-human:** *.tf", "`always-human:` *.tf"} {
		if _, globs := ParseReviewRules(md); len(globs) != 1 || globs[0] != "*.tf" {
			t.Errorf("ParseReviewRules(%q) globs = %q, want [*.tf]", md, globs)
		}
	}
	if _, globs := ParseReviewRules("Always-humanly speaking: no"); len(globs) != 0 {
		t.Errorf("prose matched the key: %q", globs)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.tf", "a/b/main.tf", true},
		{"infra/*.tf", "infra/main.tf", true},
		{"infra/*.tf", "infra/mod/main.tf", false},
		{"infra/**", "infra/mod/main.tf", true},
		{"**/x.go", "x.go", true},
		{"**/x.go", "a/b/x.go", true},
		{"deploy/", "deploy/a/b", true},
		{"deploy/", "deployment/a", false},
		{"/root.go", "root.go", true},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}
