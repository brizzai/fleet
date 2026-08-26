package jira

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// TestNormalizeSite pins what fleet accepts as a Jira site.
//
// Four shapes, because those are the four things people actually have to hand:
// the tenant name they say out loud, the host, the URL, and the URL of whatever
// page they were looking at when they hit copy. Refusing the last two would
// make the most common paste fail.
func TestNormalizeSite(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"acme", "acme.atlassian.net", true},
		{"acme.atlassian.net", "acme.atlassian.net", true},
		{"https://acme.atlassian.net", "acme.atlassian.net", true},
		{"https://acme.atlassian.net/", "acme.atlassian.net", true},
		{"  ACME.Atlassian.NET  ", "acme.atlassian.net", true},
		{"https://acme.atlassian.net/jira/software/projects/BRZ/boards/1", "acme.atlassian.net", true},

		// A customer-owned domain is a real Jira Cloud site. Restricting to
		// *.atlassian.net would refuse to connect to an ordinary one — the
		// Cloud-only decision is enforced by asking for /rest/api/3, not by
		// the hostname.
		{"https://jira.acme.com", "jira.acme.com", true},

		// The path, port and userinfo must not survive: baseURL is rebuilt from
		// the host alone, so anything left here would be a way to point a
		// credentialed request somewhere else.
		{"https://acme.atlassian.net:8443/x", "acme.atlassian.net", true},
		{"https://user:pw@acme.atlassian.net/x", "acme.atlassian.net", true},

		{"", "", false},
		{"   ", "", false},
		{"https://", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeSite(c.in)
		if (err == nil) != c.ok {
			t.Errorf("NormalizeSite(%q) err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeSite(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAuthHeaderIsBasicNotBearer pins the one form Jira Cloud accepts.
//
// An API token sent as a bearer is rejected exactly like a wrong password, with
// nothing anywhere saying the FORM was the problem — which is the same trap
// Linear's raw-vs-Bearer split sets, in the opposite direction.
func TestAuthHeaderIsBasicNotBearer(t *testing.T) {
	c := Credential{Site: "acme.atlassian.net", Email: "you@example.com", Token: "ATATTsecret"}
	got := c.authHeader()
	if !strings.HasPrefix(got, "Basic ") {
		t.Fatalf("authHeader = %q, want a Basic header", got)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic "))
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if string(raw) != "you@example.com:ATATTsecret" {
		t.Errorf("payload = %q, want email:token", raw)
	}
	if c.baseURL() != "https://acme.atlassian.net" {
		t.Errorf("baseURL = %q — it must be rebuilt from the host alone", c.baseURL())
	}
}

// TestCredentialNeedsAllThreeParts: a token with no site is not a credential.
//
// The env path in particular must be all-or-nothing. Falling back to the stored
// credential while JIRA_SITE is exported would authenticate against a different
// site than the variable names, which is the kind of wrong that looks like it
// worked.
func TestCredentialNeedsAllThreeParts(t *testing.T) {
	full := Credential{Site: "acme.atlassian.net", Email: "a@b.c", Token: "t"}
	if !full.ok() {
		t.Fatal("a complete credential must be ok")
	}
	for _, c := range []Credential{
		{Email: "a@b.c", Token: "t"},
		{Site: "acme.atlassian.net", Token: "t"},
		{Site: "acme.atlassian.net", Email: "a@b.c"},
		{},
	} {
		if c.ok() {
			t.Errorf("%+v should not be usable", c)
		}
	}
}

// TestEscapeJQL pins that a typed term cannot become syntax.
//
// TWO layers, because the value passes two parsers: Lucene reads the CONTENTS
// of the literal for a `~` comparison, and the JQL string literal wraps it.
// Escaping only the second was the bug — quoting does not make `(` or `?` data
// to Lucene, so `fix (login` in the New branch field, which fires a search at
// three runes, came back as a 400 the dialog reported as "Jira: unavailable".
func TestEscapeJQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `plain`},
		{`two words`, `two words`},

		// Lucene operators inside ordinary prose. Every one of these is a
		// character somebody types into a branch field without thinking.
		{`fix (login`, `fix \\(login`},
		{`what?`, `what\\?`},
		{`C++`, `C\\+\\+`},
		{`a-b`, `a\\-b`},
		{`50% off [beta]`, `50% off \\[beta\\]`},
		{`ns:key`, `ns\\:key`},

		// A quote must survive both layers: escaped for Lucene, then again for
		// the JQL literal it sits inside.
		{`say "hi"`, `say \\\"hi\\\"`},
		{`" OR project = X`, `\\\" OR project = X`},

		// A user's own backslash doubles at each layer.
		{`back\slash`, `back\\\\slash`},
	}
	for _, c := range cases {
		if got := escapeJQL(c.in); got != c.want {
			t.Errorf("escapeJQL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestErrorClassification pins that Jira is read by STATUS, not by body.
//
// This is the exact opposite of Linear, where an unknown issue arrives as HTTP
// 200 with an errors[] entry and reading the status alone files "no such issue"
// as a generic failure. Both packages now have a test saying which way round
// their tracker works, so the asymmetry is recorded rather than rediscovered.
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"ok", 200, `{"key":"BRZ-1"}`, nil},
		{"no content", 204, ``, nil},
		{"bad token", 401, `{"errorMessages":["Unauthorized"]}`, ticket.ErrNotAuthenticated},
		{"no permission", 403, `{}`, ticket.ErrNotAuthenticated},
		{"unknown issue", 404, `{"errorMessages":["Issue does not exist"]}`, ticket.ErrNotFound},
		{"server or dc site", 404, `<html>404</html>`, ticket.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.status, []byte(c.body)); got != c.want {
				t.Errorf("classify(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
			}
		})
	}

	// A 400 has no sentinel, but it must still name what Jira said — a bad JQL
	// is the common case and the message is the whole diagnosis.
	err := classify(400, []byte(`{"errorMessages":["Field 'nope' does not exist"]}`))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a 400 should carry Jira's own message, got %v", err)
	}
	// Including when the message is in the per-field map instead.
	err = classify(400, []byte(`{"errors":{"project":"is required"}}`))
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Errorf("a per-field error should surface too, got %v", err)
	}
}

// TestPriorityMapsByNameNotID pins why the mapping is what it is.
//
// Priority schemes are per-project and fully customizable, so the default ids
// 1..5 mean Highest..Lowest only on a site nobody has configured. The aliases
// cover the schemes teams actually build.
func TestPriorityMapsByNameNotID(t *testing.T) {
	cases := []struct {
		name string
		want ticket.Priority
	}{
		{"Highest", ticket.PriorityUrgent},
		{"Blocker", ticket.PriorityUrgent},
		{"Critical", ticket.PriorityUrgent},
		{"High", ticket.PriorityHigh},
		{"Major", ticket.PriorityHigh},
		{"Medium", ticket.PriorityMedium},
		{"normal", ticket.PriorityMedium},
		{"Low", ticket.PriorityLow},
		{"Lowest", ticket.PriorityLow},
		{"Trivial", ticket.PriorityLow},
		{"  hIgH  ", ticket.PriorityHigh},
	}
	for _, c := range cases {
		if got := priority(&namedField{Name: c.name}); got != c.want {
			t.Errorf("priority(%q) = %v, want %v", c.name, got, c.want)
		}
	}

	// Absent and unrecognized both map to None, NOT to Medium. Guessing a
	// middle rank for a priority fleet does not understand would put it above a
	// real Low — and the tickets tab is sorted on this.
	if got := priority(nil); got != ticket.PriorityNone {
		t.Errorf("a missing priority = %v, want None", got)
	}
	if got := priority(&namedField{Name: "Yesterday"}); got != ticket.PriorityNone {
		t.Errorf("an unknown priority = %v, want None — a guess would outrank a real Low", got)
	}
	if ticket.PriorityNone.Rank() <= ticket.PriorityLow.Rank() {
		t.Error("None must sort below Low, or unprioritised tickets float to the top")
	}
}

// TestStateTypeMapsFromCategory: the status category is the only orderable
// thing Jira exposes about a status, since names are per-project and there is
// no position.
func TestStateTypeMapsFromCategory(t *testing.T) {
	cases := []struct {
		key  string
		want ticket.StateType
	}{
		{"indeterminate", ticket.StateStarted},
		{"new", ticket.StateUnstarted},
		{"done", ticket.StateOther},
		{"", ticket.StateOther},
		{"something-new-from-atlassian", ticket.StateOther},
	}
	for _, c := range cases {
		if got := stateType(c.key); got != c.want {
			t.Errorf("stateType(%q) = %v, want %v", c.key, got, c.want)
		}
	}
	if ticket.StateStarted.Rank() >= ticket.StateUnstarted.Rank() {
		t.Error("in-progress work must sort above to-do work")
	}
}

// TestStartedTransitionPrefersInProgress pins the choice Jira gives fleet no
// structure for.
//
// Linear resolves this by position: a team's started states are ordered and the
// lowest is what a human means by "I'm starting this". Jira has no position at
// all, and every in-progress-ish status shares the `indeterminate` category —
// so a workflow with both "In Progress" and "In Review" offers two candidates,
// and picking the wrong one moves a fresh ticket straight to review.
func TestStartedTransitionPrefersInProgress(t *testing.T) {
	mk := func(id, name, category string) transition {
		var t transition
		t.ID, t.Name = id, name+" transition"
		t.To.Name, t.To.Category.Key = name, category
		return t
	}

	// In Review comes first in the workflow's own order, so "first
	// indeterminate" alone would pick it.
	got, ok := startedTransition([]transition{
		mk("11", "To Do", "new"),
		mk("21", "In Review", "indeterminate"),
		mk("31", "In Progress", "indeterminate"),
		mk("41", "Done", "done"),
	})
	if !ok || got.ID != "31" {
		t.Errorf("got %+v, want the In Progress transition (31)", got)
	}

	// A workflow that calls it something else falls back to the first
	// indeterminate transition, which is the earliest step in the workflow.
	got, ok = startedTransition([]transition{
		mk("11", "To Do", "new"),
		mk("21", "Doing", "indeterminate"),
		mk("31", "In Dev", "indeterminate"),
	})
	if !ok || got.ID != "21" {
		t.Errorf("got %+v, want the first indeterminate transition (21)", got)
	}

	// A workflow with nowhere to start is not an error — it is a workflow fleet
	// has nothing to say about, and inventing a move would be worse.
	if _, ok := startedTransition([]transition{mk("11", "To Do", "new"), mk("41", "Done", "done")}); ok {
		t.Error("a workflow with no started transition must report none, not guess one")
	}
	if _, ok := startedTransition(nil); ok {
		t.Error("no transitions at all must report none")
	}
}

// TestTicketURLIsBuiltFromTheSite: Jira's issue payload carries a `self` link
// to the REST resource, not to the page a human opens. The browse URL has to be
// composed, and it is what `p` and the palette row link to.
func TestTicketURLIsBuiltFromTheSite(t *testing.T) {
	var iss issue
	if err := json.Unmarshal([]byte(`{
		"key": "brz-1",
		"fields": {"summary":"Fix it","status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
		           "priority":{"name":"High"}}
	}`), &iss); err != nil {
		t.Fatal(err)
	}
	got := iss.ticket("acme.atlassian.net")
	if got.Identifier != "BRZ-1" {
		t.Errorf("identifier = %q, want it upper-cased", got.Identifier)
	}
	if got.URL != "https://acme.atlassian.net/browse/BRZ-1" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Provider != kind {
		t.Errorf("provider = %q, want %q — a merged list routes on this", got.Provider, kind)
	}
	if got.State != ticket.StateStarted || got.StateName != "In Progress" {
		t.Errorf("state = %v/%q", got.State, got.StateName)
	}
	if got.Priority != ticket.PriorityHigh {
		t.Errorf("priority = %v", got.Priority)
	}

	// With no site there is no URL to build. An empty one is better than a
	// relative link the browser would resolve against nothing.
	if u := iss.ticket("").URL; u != "" {
		t.Errorf("URL without a site = %q, want empty", u)
	}
}
