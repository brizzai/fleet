package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

func TestBuildFlatItems_OriginGrouping(t *testing.T) {
	// Two repos that share a github origin (e.g. main + worktree),
	// plus a no-remote repo that lands under its own local: origin.
	s1 := session.NewSession("s1", "/tmp/repo-main")
	s2 := session.NewSession("s2", "/tmp/repo-worktree")
	s3 := session.NewSession("s3", "/tmp/scratchpad")
	originOf := func(repoRoot string) string {
		switch repoRoot {
		case "/tmp/repo-main", "/tmp/repo-worktree":
			return "github.com/acme/repo"
		case "/tmp/scratchpad":
			return "local:scratchpad"
		}
		return ""
	}
	expanded := map[string]bool{}
	items := BuildFlatItems(
		[]*session.Session{s1, s2, s3},
		nil,
		expanded,
		"",
		nil,
		nil,
		nil,
		time.Time{},
		originOf,
		nil,
	)

	// Expect: acme-origin header, two checkouts (with one session each),
	// then scratchpad origin + checkout + session.
	wantOriginLabels := []string{"repo", "scratchpad"}
	gotOrigins := []string{}
	for _, it := range items {
		if it.IsOriginHeader {
			gotOrigins = append(gotOrigins, it.OriginLabel)
		}
	}
	if len(gotOrigins) != len(wantOriginLabels) {
		t.Fatalf("origin count = %d (%v), want %d (%v)", len(gotOrigins), gotOrigins, len(wantOriginLabels), wantOriginLabels)
	}
	for i, want := range wantOriginLabels {
		if gotOrigins[i] != want {
			t.Errorf("origin[%d] = %q, want %q", i, gotOrigins[i], want)
		}
	}

	// First origin should report 2 sessions across its two checkouts.
	for _, it := range items {
		if it.IsOriginHeader && it.OriginLabel == "repo" && it.SessionCount != 2 {
			t.Errorf("acme origin SessionCount = %d, want 2", it.SessionCount)
		}
	}

	// Both checkouts under the github origin should be present.
	checkoutPaths := map[string]bool{}
	for _, it := range items {
		if it.IsCheckoutHeader && it.OriginKey == "github.com/acme/repo" {
			checkoutPaths[it.RepoPath] = true
		}
	}
	if !checkoutPaths["/tmp/repo-main"] || !checkoutPaths["/tmp/repo-worktree"] {
		t.Errorf("expected both repo checkouts under acme origin, got %v", checkoutPaths)
	}
}

func TestIsExpanded_DefaultsToExpanded(t *testing.T) {
	m := map[string]bool{}
	if !IsExpanded(m, "missing") {
		t.Errorf("IsExpanded on missing key = false, want true (default expanded)")
	}
	m["explicit-false"] = false
	if IsExpanded(m, "explicit-false") {
		t.Errorf("IsExpanded on explicit-false key = true, want false")
	}
	m["explicit-true"] = true
	if !IsExpanded(m, "explicit-true") {
		t.Errorf("IsExpanded on explicit-true key = false, want true")
	}
}

func TestLabelForOrigin(t *testing.T) {
	cases := map[string]string{
		"github.com/acme/repo": "repo",
		"local:scratchpad":     "scratchpad",
		"gitlab.com/org/proj":  "proj",
		"singleword":           "singleword",
	}
	for in, want := range cases {
		if got := labelForOrigin(in); got != want {
			t.Errorf("labelForOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderSessionItem_AgentGlyph(t *testing.T) {
	cases := []struct {
		name      string
		agentType agent.Type
		want      string
		notWant   string
	}{
		{"claude", agent.Claude, claudeGlyph, codexGlyph},
		{"codex", agent.Codex, codexGlyph, claudeGlyph},
		{"opencode", agent.OpenCode, opencodeGlyph, claudeGlyph},
		// Empty agent (legacy sessions) falls back to Claude.
		{"empty falls back to claude", "", claudeGlyph, codexGlyph},
	}
	for _, tc := range cases {
		for _, selected := range []bool{false, true} {
			s := session.NewSession("a title", "/tmp/repo")
			s.Agent = tc.agentType
			out := renderSessionItem(SidebarItem{Session: s}, 40, selected, -1)
			if !strings.Contains(out, tc.want) {
				t.Errorf("%s (selected=%v): output missing %q glyph: %q", tc.name, selected, tc.want, out)
			}
			if strings.Contains(out, tc.notWant) {
				t.Errorf("%s (selected=%v): output unexpectedly contains %q glyph: %q", tc.name, selected, tc.notWant, out)
			}
		}
	}
}

func TestPRBadge_Draft(t *testing.T) {
	draft := &github.PR{Number: 133, State: "OPEN", IsDraft: true}
	if got := prBadgeText(draft); got != "◌ #133" {
		t.Errorf("draft badge: got %q, want %q", got, "◌ #133")
	}
	if got := prBadgeStyle(draft).GetForeground(); got != PRDraftStyle.GetForeground() {
		t.Errorf("draft badge color = %v, want PRDraftStyle (dim)", got)
	}

	// CI failure still surfaces on a draft; review/approval glyphs do not.
	draftFail := &github.PR{Number: 135, State: "OPEN", IsDraft: true, CIStatus: "FAILURE"}
	if got := prBadgeText(draftFail); got != "◌ #135 ✕" {
		t.Errorf("failing-draft badge: got %q, want %q", got, "◌ #135 ✕")
	}

	// Approval on a draft is ignored — no ✓, stays gray.
	draftApproved := &github.PR{Number: 137, State: "OPEN", IsDraft: true, ReviewDecision: "APPROVED", CIStatus: "SUCCESS"}
	if got := prBadgeText(draftApproved); got != "◌ #137" {
		t.Errorf("approved-draft badge: got %q, want %q", got, "◌ #137")
	}
	if got := prBadgeStyle(draftApproved).GetForeground(); got != PRDraftStyle.GetForeground() {
		t.Errorf("approved-draft badge color = %v, want PRDraftStyle (dim), not green", got)
	}
}
