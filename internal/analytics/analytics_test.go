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

func TestSanitizePropertiesDropsBlocklisted(t *testing.T) {
	t.Parallel()

	in := map[string]interface{}{
		"session_count": 5,
		"provider":      "git_worktree",
		"path":          "/should/be/dropped",
		"repo_name":     "secret-repo",
		"branch":        "main",
		"theme":         "tokyo-night",
	}
	out := sanitizeProperties(in)

	want := map[string]any{
		"session_count": 5,
		"provider":      "git_worktree",
		"theme":         "tokyo-night",
	}
	if got := len(out); got != len(want) {
		t.Fatalf("sanitizeProperties returned %d keys, want %d (out=%v)", got, len(want), out)
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("sanitizeProperties[%q] = %v, want %v", k, out[k], v)
		}
	}
	for _, k := range []string{"path", "repo_name", "branch"} {
		if _, ok := out[k]; ok {
			t.Errorf("expected key %q to be dropped, but it's present", k)
		}
	}
}

func TestSanitizePropertiesEmpty(t *testing.T) {
	t.Parallel()

	if got := sanitizeProperties(nil); got != nil {
		t.Errorf("sanitizeProperties(nil) = %v, want nil", got)
	}
	if got := sanitizeProperties(map[string]interface{}{}); got != nil {
		t.Errorf("sanitizeProperties(empty) = %v, want nil", got)
	}
}

func TestReadGitIdentityNeverPanics(t *testing.T) {
	t.Parallel()

	// We can't assert specific values (depends on dev machine's git config),
	// but the function MUST return without panicking and yield strings.
	// On a CI runner without git configured, both should be "".
	name, email := readGitIdentity()
	_ = name
	_ = email
}

func TestMergeValueAttachesValueAndStrips(t *testing.T) {
	t.Parallel()

	in := map[string]interface{}{
		"status": "running",
		"path":   "/should/be/dropped",
	}
	out := mergeValue(in, 42.5)

	if out["value"] != 42.5 {
		t.Errorf(`mergeValue did not attach "value": got %v`, out["value"])
	}
	if out["status"] != "running" {
		t.Errorf(`mergeValue dropped a non-blocked key: got %v`, out["status"])
	}
	if _, ok := out["path"]; ok {
		t.Errorf(`mergeValue kept a blocklisted key`)
	}
	// Caller's map must not be mutated.
	if _, ok := in["value"]; ok {
		t.Errorf("mergeValue mutated caller's map")
	}
}

func TestDeclineShouldSendRespectsEnvOptOut(t *testing.T) {
	// Not parallel: mutates process env via t.Setenv.

	// Clear both opt-out vars so the default case is unambiguous regardless of
	// the runner's environment.
	t.Setenv("FLEET_TELEMETRY_DISABLED", "")
	t.Setenv("DO_NOT_TRACK", "")
	if !declineShouldSend() {
		t.Error("declineShouldSend() = false with no opt-out env, want true")
	}

	t.Setenv("FLEET_TELEMETRY_DISABLED", "1")
	if declineShouldSend() {
		t.Error("declineShouldSend() = true with FLEET_TELEMETRY_DISABLED=1, want false")
	}

	t.Setenv("FLEET_TELEMETRY_DISABLED", "")
	t.Setenv("DO_NOT_TRACK", "1")
	if declineShouldSend() {
		t.Error("declineShouldSend() = true with DO_NOT_TRACK=1, want false")
	}
}
