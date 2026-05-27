package analytics

import (
	"testing"
)

func TestSanitizeDropsForbiddenKeys(t *testing.T) {
	t.Parallel()

	dropped := []string{
		"path", "project_path", "file_path",
		"repo", "repo_name",
		"branch", "branch_name",
		"title", "session_title",
		"url",
		"host", "hostname",
		"prompt", "message",
		// Case-insensitive.
		"PATH", "Repo", "Branch", "FilE_pATH",
	}
	for _, k := range dropped {
		if sanitizeKey(k) {
			t.Errorf("sanitizeKey(%q) = true, want false (key should be dropped)", k)
		}
	}

	kept := []string{
		"session_count", "repo_count", "worktree_repos_total",
		"provider", "status", "theme", "editor", "version",
		"running_count", "waiting_count",
		"seconds_since_install", "uptime_seconds",
		"app_version", "os_version", "arch", "device_id",
	}
	for _, k := range kept {
		if !sanitizeKey(k) {
			t.Errorf("sanitizeKey(%q) = false, want true (key should be kept)", k)
		}
	}
}

func TestPropertiesToAttributesTypeCoverage(t *testing.T) {
	t.Parallel()

	props := map[string]interface{}{
		"s":   "hello",
		"b":   true,
		"i":   int(7),
		"i32": int32(8),
		"i64": int64(9),
		"f32": float32(1.5),
		"f64": float64(2.5),
		"fb":  struct{ Name string }{Name: "x"}, // unknown → fallback string
		"n":   nil,                              // skipped
		// PII-blocked key — should be dropped entirely.
		"repo": "secret-repo",
	}

	attrs := propertiesToAttributes(props)

	// nil and repo dropped = 8 entries.
	if got, want := len(attrs), 8; got != want {
		t.Errorf("propertiesToAttributes returned %d attrs, want %d", got, want)
	}

	// Map key→true presence check — order isn't guaranteed.
	seen := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		seen[a.Key] = true
	}
	for _, k := range []string{"s", "b", "i", "i32", "i64", "f32", "f64", "fb"} {
		if !seen[k] {
			t.Errorf("expected attribute key %q in output", k)
		}
	}
	for _, k := range []string{"n", "repo"} {
		if seen[k] {
			t.Errorf("attribute key %q should have been dropped", k)
		}
	}
}

func TestPropertiesToAttributesEmpty(t *testing.T) {
	t.Parallel()

	if got := propertiesToAttributes(nil); got != nil {
		t.Errorf("propertiesToAttributes(nil) = %v, want nil", got)
	}
	if got := propertiesToAttributes(map[string]interface{}{}); got != nil {
		t.Errorf("propertiesToAttributes(empty) = %v, want nil", got)
	}
}

func TestSentryEnvironment(t *testing.T) {
	t.Parallel()

	cases := []struct {
		version string
		want    string
	}{
		{"", "development"},
		{"dev", "development"},
		{"v1.0.0", "production"},
		{"2.3.4", "production"},
	}
	for _, c := range cases {
		if got := sentryEnvironment(c.version); got != c.want {
			t.Errorf("sentryEnvironment(%q) = %q, want %q", c.version, got, c.want)
		}
	}
}
