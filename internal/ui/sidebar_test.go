package ui

import (
	"testing"

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
		originOf,
		nil,
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

func TestBuildFlatItems_IdleFold(t *testing.T) {
	idle1 := session.NewSession("idle1", "/tmp/r")
	idle1.SetStatus(session.StatusIdle)
	idle2 := session.NewSession("idle2", "/tmp/r")
	idle2.SetStatus(session.StatusIdle)
	running := session.NewSession("run", "/tmp/r")
	running.SetStatus(session.StatusRunning)

	originOf := func(string) string { return "github.com/acme/r" }
	idleFolded := map[string]bool{"/tmp/r": true}

	items := BuildFlatItems(
		[]*session.Session{idle1, idle2, running},
		nil,
		map[string]bool{},
		"",
		nil,
		originOf,
		nil,
		idleFolded,
	)

	var foldItems int
	var sessionItems int
	for _, it := range items {
		if it.IsIdleFold {
			foldItems++
			if it.IdleCount != 2 {
				t.Errorf("IdleCount = %d, want 2", it.IdleCount)
			}
		}
		if it.Session != nil {
			sessionItems++
		}
	}
	if foldItems != 1 {
		t.Errorf("idle fold rows = %d, want 1", foldItems)
	}
	if sessionItems != 1 {
		t.Errorf("session rows = %d, want 1 (running only)", sessionItems)
	}

	// With folding off, all three sessions should render.
	items = BuildFlatItems(
		[]*session.Session{idle1, idle2, running},
		nil,
		map[string]bool{},
		"",
		nil,
		originOf,
		nil,
		nil,
	)
	sessionItems = 0
	for _, it := range items {
		if it.Session != nil {
			sessionItems++
		}
		if it.IsIdleFold {
			t.Errorf("unexpected idle-fold row when folding off")
		}
	}
	if sessionItems != 3 {
		t.Errorf("session rows = %d, want 3 (no fold)", sessionItems)
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
