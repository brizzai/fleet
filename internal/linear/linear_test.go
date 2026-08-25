package linear

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// TestStartedStateResolvesByTypeAndPosition pins how the one mutation picks a
// state.
//
// Matching on TYPE rather than name is what makes this work on a team whose
// started state is called "In Dev" or "Doing". Position ordering matters just as
// much: a real team has several started states, and the lowest position is the
// one a human means by "I am starting this" — picking any other would move a
// fresh ticket straight to In Review.
func TestStartedStateResolvesByTypeAndPosition(t *testing.T) {
	issue := &issueFull{Team: &issueTeam{}}
	issue.Team.States.Nodes = []workflowState{
		{ID: "d", Name: "Done", Type: "completed", Position: 3},
		{ID: "r", Name: "In Review", Type: "started", Position: 1002},
		{ID: "p", Name: "In Dev", Type: "started", Position: 2},
		{ID: "b", Name: "Backlog", Type: "backlog", Position: 0},
	}
	got, ok := issue.startedState()
	if !ok || got.ID != "p" {
		t.Fatalf("startedState = (%+v, %v), want the lowest-position started state (In Dev)", got, ok)
	}

	// A team with no started state at all is a team fleet has nothing to say
	// about, not an error.
	issue.Team.States.Nodes = []workflowState{{ID: "b", Name: "Backlog", Type: "backlog"}}
	if _, ok := issue.startedState(); ok {
		t.Error("a team with no started state must report none")
	}
}

// TestGraphQLErrorClassification pins the shapes the live API actually returns.
//
// Captured from api.linear.app rather than guessed, because the obvious guess is
// wrong in the case that matters most: an unknown issue comes back as HTTP 200
// with an errors[] entry whose own extensions carry statusCode 400. Classifying
// on the HTTP status alone would report "no such issue" as a generic failure and
// break the negative pin that stops fleet re-asking on every session start.
func TestGraphQLErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		errs   []gqlErrorEntry
		want   error
	}{
		{"ok", 200, nil, nil},
		{"unknown issue is a 200", 200,
			[]gqlErrorEntry{{Message: "Entity not found: Issue"}}, ticket.ErrNotFound},
		{"rejected credential", 401,
			[]gqlErrorEntry{{Message: "Authentication required, not authenticated"}}, ticket.ErrNotAuthenticated},
		{"auth error code without a 401", 200,
			[]gqlErrorEntry{{Message: "nope", Extensions: struct {
				Type string `json:"type"`
				Code string `json:"code"`
			}{Code: "AUTHENTICATION_ERROR"}}}, ticket.ErrNotAuthenticated},
		{"forbidden", 403, nil, ticket.ErrNotAuthenticated},
	}
	for _, c := range cases {
		got := classifyGraphQL(c.status, c.errs)
		if got != c.want {
			t.Errorf("%s: classifyGraphQL = %v, want %v", c.name, got, c.want)
		}
	}

	// An unrecognised error must still be an error, not a silent success.
	if err := classifyGraphQL(200, []gqlErrorEntry{{Message: "Query too complex"}}); err == nil {
		t.Error("an unclassified errors[] entry must not read as success")
	}
}

// TestFindImagesTakesOnlyRemoteLinks pins what fleet is willing to go and fetch.
//
// Linear's markdown carries absolute uploads.linear.app URLs; anything else in a
// description — a relative path, a data URI, a link to someone's laptop — is not
// something fleet has any business reading off the filesystem and copying into a
// worktree.
func TestFindImagesTakesOnlyRemoteLinks(t *testing.T) {
	md := "text\n" +
		"![shot](/etc/passwd)\n" +
		"![rel](../../secrets.png)\n" +
		"![inline](data:image/png;base64,AAAA)\n" +
		"![evil](https://uploads.linear.app.evil.example/a)\n" +
		"![other](https://uploads.linear.app/a/b/c)\n"
	_, targets := findImages(md)
	if len(targets) != 1 {
		t.Fatalf("found %d images, want only the remote one: %+v", len(targets), targets)
	}
	if targets[0] != "https://uploads.linear.app/a/b/c" {
		t.Errorf("kept the wrong link: %q", targets[0])
	}
}

// TestAllowedImageHostRejectsLookalikes pins the host predicate this package
// hands to ticket.Document.
//
// The comparison must be on url.Hostname() and nothing else: a suffix test
// matches uploads.linear.app.evil.example, a prefix test matches
// evil-uploads.linear.app.evil.example, and either one hands a live credential
// to whoever registered the domain. ticket's own security test covers the
// scheme and the re-check at the download; this covers the half that lives here.
func TestAllowedImageHostRejectsLookalikes(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"https://uploads.linear.app/a", true},
		{"https://uploads.linear.app:443/a", true},
		{"https://uploads.linear.app.evil.example/a", false},
		{"https://evil-uploads.linear.app/a", false},
		{"https://uploads.linear.app@evil.example/a", false},
		{"https://api.linear.app/a", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.host)
		if err != nil {
			t.Fatalf("%s: %v", c.host, err)
		}
		if got := allowedImageHost(u); got != c.want {
			t.Errorf("allowedImageHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestTeamKeysReadOnlyTeamID(t *testing.T) {
	dir := t.TempDir()
	toml := "# linear cli\nworkspace = \"brizz\"\nteam_id = \"BRZ\"\napi_key = \"lin_api_SECRET\"\n"
	if err := os.WriteFile(filepath.Join(dir, linearConfigFile), []byte(toml), 0644); err != nil {
		t.Fatal(err)
	}
	got := TeamKeys(dir)
	if len(got) != 1 || got[0] != "BRZ" {
		t.Fatalf("TeamKeys = %v, want [BRZ]", got)
	}

	// fleet resolves its own credential. An api_key sitting in another tool's
	// config is none of its business, and must never be adopted.
	if data, err := os.ReadFile(filepath.Join(dir, linearConfigFile)); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(data), "lin_api_SECRET") {
		t.Fatal("precondition: the fixture should contain an api_key to ignore")
	}

	// A repo naming no team is the resting state — nil, not an error, and it is
	// what keeps an unrelated repo silent for a connected user.
	if got := TeamKeys(t.TempDir()); got != nil {
		t.Errorf("a repo with no Linear config must report no teams, got %v", got)
	}

	// .fleet.json wins over .linear.toml: it is fleet's own config, and it is
	// the only form available to someone who never installed the other tool.
	if err := os.WriteFile(filepath.Join(dir, ".fleet.json"),
		[]byte(`{"linear":{"teams":["prd","inf"]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got = TeamKeys(dir)
	if len(got) != 2 || got[0] != "PRD" || got[1] != "INF" {
		t.Fatalf("TeamKeys = %v, want [PRD INF] upper-cased from .fleet.json", got)
	}
}

// TestCredentialResolutionOrder pins that the environment outranks the store.
//
// That order is what lets a stale or wrong stored credential be overridden
// without any UI, and it is the only path that works in CI, where there is no
// keychain to read.
func TestCredentialResolutionOrder(t *testing.T) {
	isolateStore(t)
	orig := getenv
	t.Cleanup(func() { getenv = orig; resetCredentialForTest() })

	getenv = func(k string) string {
		if k == APIKeyEnvVar {
			return "lin_api_fromEnv"
		}
		return ""
	}
	resetCredentialForTest()
	if !Available() {
		t.Fatal("an environment key must make Linear available without touching the keychain")
	}
	c, err := credential()
	if err != nil || c.Token != "lin_api_fromEnv" || c.Kind != credAPIKey {
		t.Fatalf("credential = (%+v, %v), want the environment key", c, err)
	}
	if got := ConnectedVia(); !strings.Contains(got, APIKeyEnvVar) {
		t.Errorf("ConnectedVia = %q, should name the environment so the dialog can say it is not disconnectable from here", got)
	}

	getenv = func(string) string { return "" }
	resetCredentialForTest()
	if _, err := credential(); err != ticket.ErrNotConnected {
		t.Errorf("with no environment key and nothing stored, credential must be ticket.ErrNotConnected, got %v", err)
	}
}

// TestAuthHeaderFormDiffersByKind pins the one place the two credential kinds
// diverge. A personal API key is sent raw; an OAuth token takes Bearer. Sending
// either in the other form reads as a rejected credential.
func TestAuthHeaderFormDiffersByKind(t *testing.T) {
	if got := (Credential{Kind: credAPIKey, Token: "k"}).authHeader(); got != "k" {
		t.Errorf("api key header = %q, want the raw token", got)
	}
	if got := (Credential{Kind: credOAuth, Token: "k"}).authHeader(); got != "Bearer k" {
		t.Errorf("oauth header = %q, want Bearer", got)
	}
}

// isolateStore points the credential store at a service name nothing has ever
// written, so a test that resolves a credential can never read the developer's
// real one.
//
// Without it the "nothing stored" branch below passes in CI and fails on any
// machine that is actually connected to Linear — which is every machine where
// someone would run the suite while changing this code.
func isolateStore(t *testing.T) {
	t.Helper()
	orig := store
	store = ticket.Store{
		Service:  "fleet-linear-test-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Label:    "fleet: test",
		FileName: "linear-test-does-not-exist.json",
	}
	t.Cleanup(func() { store = orig })
}

// resetCredentialForTest drops the cached credential so a test can re-resolve.
func resetCredentialForTest() {
	credState.mu.Lock()
	credState.cred = Credential{}
	credState.loaded = false
	credState.warmed.Store(false)
	credState.present.Store(false)
	credState.mu.Unlock()
}

// TestAssignedIssuesOrderPutsWorkInHand pins the ordering of the tickets tab.
//
// Ranking on state TYPE is what makes it work on a team that renamed its
// states. Position is what separates two states of the SAME type, and it is the
// difference between "In Progress" and "In Review" leading the list — a work
// queue wants the thing you are in the middle of, not the thing you already
// handed off.
//
// It exercises the real comparator, ticket.SortAssigned, through this package's
// real projection. The previous version re-implemented the sort inline, which
// meant it could keep passing while AssignedIssues sorted differently — and
// that gap got wider, not narrower, once a merged Linear+Jira list had to come
// out in the same order as either one alone.
func TestAssignedIssuesOrderPutsWorkInHand(t *testing.T) {
	nodes := []issueLite{
		mkWithPriority("BRZ-4", "Backlog", "backlog", 0, 0),
		mkWithPriority("BRZ-3", "Todo", "unstarted", 1, 0),
		mkWithPriority("BRZ-2", "In Review", "started", 1002, 0),
		mkWithPriority("BRZ-1", "In Dev", "started", 2, 0),
	}
	got := sortedIdentifiers(nodes)
	want := []string{"BRZ-1", "BRZ-2", "BRZ-3", "BRZ-4"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (started by position, then todo, then backlog)", got, want)
		}
	}

	// A payload with no state must sort last rather than lead. issueLite.ticket
	// gives it StateOther and the sorts-last position sentinel for exactly this.
	nodes = append(nodes, issueLite{Identifier: "BRZ-9"})
	got = sortedIdentifiers(nodes)
	if last := got[len(got)-1]; last != "BRZ-9" {
		t.Errorf("a stateless issue should sort last, got order ending in %s", last)
	}
}

// sortedIdentifiers projects nodes the way AssignedIssues does and applies the
// shared comparator, so these tests fail if either half changes.
func sortedIdentifiers(nodes []issueLite) []string {
	tickets := make([]ticket.Ticket, 0, len(nodes))
	for i := range nodes {
		tickets = append(tickets, nodes[i].ticket())
	}
	ticket.SortAssigned(tickets)
	out := make([]string, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, t.Identifier)
	}
	return out
}

// mkWithPriority builds a decoded node for the ordering tests.
func mkWithPriority(id, stateName, stateType string, pos float64, pri int) issueLite {
	n := issueLite{Identifier: id, Title: id, Priority: pri}
	n.State = &struct {
		Name     string  `json:"name"`
		Type     string  `json:"type"`
		Position float64 `json:"position"`
	}{Name: stateName, Type: stateType, Position: pos}
	return n
}

// TestPriorityOrderingWithinAState pins the second sort key.
//
// The raw Linear number cannot be sorted on: 0 means "not set", so ascending
// order would float every unprioritised ticket above the urgent ones. And
// priority alone is not enough — most of a real backlog shares one priority
// (21 of 50 in the workspace this was built against were High), so recency has
// to survive as the tiebreak or that block reorders itself between opens.
//
// ticket.Priority deliberately keeps Linear's numbering, so these raw numbers
// are the same values the API returns and the palette's gauge switches on.
func TestPriorityOrderingWithinAState(t *testing.T) {
	rank := func(p int) int { return ticket.Priority(p).Rank() }
	if rank(0) <= rank(4) {
		t.Errorf("unset priority ranked %d, must sort BELOW low (%d)", rank(0), rank(4))
	}
	for _, c := range []struct{ hi, lo int }{{1, 2}, {2, 3}, {3, 4}, {4, 0}} {
		if rank(c.hi) >= rank(c.lo) {
			t.Errorf("priority %d should outrank %d", c.hi, c.lo)
		}
	}

	// Same state, mixed priorities, entering in a deliberately unhelpful order.
	got := sortedIdentifiers([]issueLite{
		mkWithPriority("BRZ-none", "Todo", "unstarted", 1, 0),
		mkWithPriority("BRZ-high-a", "Todo", "unstarted", 1, 2),
		mkWithPriority("BRZ-urgent", "Todo", "unstarted", 1, 1),
		mkWithPriority("BRZ-high-b", "Todo", "unstarted", 1, 2),
		mkWithPriority("BRZ-low", "Todo", "unstarted", 1, 4),
	})
	want := []string{"BRZ-urgent", "BRZ-high-a", "BRZ-high-b", "BRZ-low", "BRZ-none"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// high-a before high-b is the recency tiebreak surviving: they entered in
	// that order and a stable sort must not disturb it.
}

// TestFullDecodeKeepsPriority pins the field-shadowing trap that silently
// emptied ticket.md's priority line.
//
// issueFull embeds issueLite, and both used to declare a `priority` JSON field.
// encoding/json resolves that by depth: the outer one wins and the embedded one
// stays zero — while issueLite.ticket(), which builds the projection every
// caller reads, returns the embedded one. So a full fetch decoded the priority
// correctly into a field nothing looked at, and every materialized ticket lost
// its front-matter priority with no error anywhere.
//
// Asserted through the same projection Document returns, not through the struct
// field, because reading the field directly is what made the bug invisible.
func TestFullDecodeKeepsPriority(t *testing.T) {
	const payload = `{
		"identifier": "BRZ-1", "title": "Filter bar cramped", "priority": 2,
		"state": {"name": "In Progress", "type": "started", "position": 2},
		"description": "x"
	}`
	var full issueFull
	if err := json.Unmarshal([]byte(payload), &full); err != nil {
		t.Fatal(err)
	}
	got := full.ticket()
	if got.Priority != ticket.PriorityHigh {
		t.Errorf("priority = %v, want High — a duplicate field on issueFull shadows the embedded one", got.Priority)
	}
	if got.Identifier != "BRZ-1" || got.StateName != "In Progress" {
		t.Errorf("the rest of the projection broke: %+v", got)
	}

	// And the lite path, which decodes issueLite directly rather than embedded,
	// must keep agreeing with it — the two are compared in one merged list.
	var lite issueLite
	if err := json.Unmarshal([]byte(payload), &lite); err != nil {
		t.Fatal(err)
	}
	if lite.ticket().Priority != got.Priority {
		t.Errorf("lite=%v full=%v — the two decode paths disagree",
			lite.ticket().Priority, got.Priority)
	}
}
