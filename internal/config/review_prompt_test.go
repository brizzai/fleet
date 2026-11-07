package config

import "testing"

func TestGetReviewPromptPerOrigin(t *testing.T) {
	c := &Config{ReviewPrompts: map[string]string{
		"github.com/brizzai/brizzai": "/review-team-pr-v2 {pr}",
	}}
	if got := c.GetReviewPrompt("github.com/brizzai/brizzai", 5163); got != "/review-team-pr-v2 5163" {
		t.Errorf("configured origin = %q", got)
	}
	if got := c.GetReviewPrompt("github.com/brizzai/fleet", 12); got != "" {
		t.Errorf("unconfigured origin should be empty, got %q", got)
	}
	if got := (&Config{}).GetReviewPrompt("anything", 1); got != "" {
		t.Errorf("no config should be empty, got %q", got)
	}
}
