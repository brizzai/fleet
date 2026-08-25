package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// fakeJira stands up a TLS server speaking Jira Cloud's REST v3 and installs a
// credential pointing at it.
//
// TLS rather than plain http because the site is only ever addressed as
// https://<host>, and a test that reached it any other way would not be
// exercising the URL the product builds. The credential's Site carries the
// port, which a real site never does — harmless here, since only the image
// gate compares hostnames and no image is fetched in these tests.
func fakeJira(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *recorder {
	t.Helper()
	rec := &recorder{seen: map[string]string{}}

	// Longest prefix wins, and the order is deterministic. Map iteration order
	// is not, and /rest/api/3/issue/OPS-42 is a prefix of
	// .../OPS-42/comment — so a naive loop routes the comment fetch to the
	// issue handler on roughly half of runs.
	prefixes := make([]string, 0, len(routes))
	for p := range routes {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(a, b int) bool { return len(prefixes[a]) > len(prefixes[b]) })

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		for _, prefix := range prefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				w.Header().Set("Content-Type", "application/json")
				routes[prefix](w, r)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["not found"]}`))
	}))
	t.Cleanup(srv.Close)

	origClient := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = origClient })

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	origCred := credState.cred
	origLoaded := credState.loaded
	credState.mu.Lock()
	credState.cred = Credential{Site: u.Host, Email: "you@example.com", Token: "ATATTsecret"}
	credState.loaded = true
	credState.mu.Unlock()
	credState.warmed.Store(true)
	credState.present.Store(true)
	t.Cleanup(func() {
		credState.mu.Lock()
		credState.cred, credState.loaded = origCred, origLoaded
		credState.mu.Unlock()
	})

	// getenv is consulted before the store; an inherited JIRA_SITE on the
	// developer's machine would otherwise point these tests at a real site.
	origEnv := getenv
	getenv = func(string) string { return "" }
	t.Cleanup(func() { getenv = origEnv })

	return rec
}

type recorder struct {
	seen  map[string]string // path -> raw query
	auth  string
	posts map[string]string // path -> body
}

func (r *recorder) record(req *http.Request) {
	if r.posts == nil {
		r.posts = map[string]string{}
	}
	r.seen[req.URL.Path] = req.URL.RawQuery
	r.auth = req.Header.Get("Authorization")
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		r.posts[req.URL.Path] = string(body)
	}
}

const issueJSON = `{
  "key": "OPS-42",
  "fields": {
    "summary": "Checkout 500s on retry",
    "status": {"name": "In Progress", "statusCategory": {"key": "indeterminate"}},
    "priority": {"name": "High"},
    "assignee": {"displayName": "Dana Ruiz"},
    "labels": ["billing", "regression"],
    "parent": {"key": "OPS-1", "fields": {"summary": "Checkout hardening"}},
    "subtasks": [{"key": "OPS-43", "fields": {"summary": "Add the retry test"}}],
    "issuelinks": [
      {"type": {"outward": "blocks", "inward": "is blocked by"},
       "outwardIssue": {"key": "OPS-50", "fields": {"summary": "Ship 4.2"}}}
    ],
    "attachment": [
      {"id": "9001", "filename": "trace.png", "mimeType": "image/png",
       "content": "https://%s/rest/api/3/attachment/content/9001"}
    ],
    "description": {"type":"doc","content":[
      {"type":"paragraph","content":[{"type":"text","text":"Retrying a failed charge 500s."}]},
      {"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"trace.png"}}]}
    ]},
    "comment": {"total": 1, "comments": [
      {"author": {"displayName": "Sam"}, "created": "2026-08-25T09:41:22.113+0000",
       "body": {"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Repros on staging."}]}]}}
    ]}
  }
}`

// TestFetchSendsBasicAuthAndAsksForTheLiteFields pins the request, not just the
// reply: the header form and the field list are both things that fail silently
// if they are wrong — a bearer reads as a bad password, and asking for the full
// field set would pull an ADF document per suggestion on every keystroke.
func TestFetchSendsBasicAuthAndAsksForTheLiteFields(t *testing.T) {
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"key":"OPS-42","fields":{"summary":"Checkout 500s",
				"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
				"priority":{"name":"High"}}}`))
		},
	})

	got, err := Fetch(context.Background(), "ops-42")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Identifier != "OPS-42" || got.Title != "Checkout 500s" {
		t.Errorf("ticket = %+v", got)
	}
	if got.State != ticket.StateStarted || got.Priority != ticket.PriorityHigh {
		t.Errorf("state/priority = %v/%v", got.State, got.Priority)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("you@example.com:ATATTsecret"))
	if rec.auth != want {
		t.Errorf("Authorization = %q, want Basic email:token", rec.auth)
	}
	if q := rec.seen["/rest/api/3/issue/OPS-42"]; q != "fields="+liteFields {
		t.Errorf("query = %q, want the lite field set", q)
	}
}

// TestFetchReportsAMissingIssueAsNotFound: Jira answers with a real 404, the
// exact opposite of Linear's HTTP 200 plus an errors[] entry.
func TestFetchReportsAMissingIssueAsNotFound(t *testing.T) {
	fakeJira(t, nil)
	if _, err := Fetch(context.Background(), "OPS-999"); err != ticket.ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestAssignedIssuesUsesCurrentUser pins the JQL.
//
// currentUser() rather than the email address: the two differ on a site where
// the account's email is hidden by privacy settings — the default for many
// organizations — and an email-keyed query there quietly returns nothing at
// all, which reads as "you have no work".
func TestAssignedIssuesUsesCurrentUser(t *testing.T) {
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/search/jql": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"issues":[
				{"key":"OPS-2","fields":{"summary":"todo one","status":{"name":"To Do","statusCategory":{"key":"new"}},"priority":{"name":"Low"}}},
				{"key":"OPS-1","fields":{"summary":"in flight","status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},"priority":{"name":"Medium"}}}
			]}`))
		},
	})

	got, err := AssignedIssues(context.Background(), 50)
	if err != nil {
		t.Fatalf("AssignedIssues: %v", err)
	}
	if len(got) != 2 || got[0].Identifier != "OPS-1" {
		t.Errorf("order = %v, want in-progress work first", got)
	}

	var req searchRequest
	if err := json.Unmarshal([]byte(rec.posts["/rest/api/3/search/jql"]), &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if !strings.Contains(req.JQL, "assignee = currentUser()") {
		t.Errorf("JQL = %q, want currentUser()", req.JQL)
	}
	if !strings.Contains(req.JQL, "statusCategory != Done") {
		t.Errorf("JQL = %q, want done issues filtered server-side", req.JQL)
	}
	if req.MaxResults != 50 {
		t.Errorf("maxResults = %d", req.MaxResults)
	}
}

// TestSearchQuotesTheTerm: whatever the user typed must be data, never syntax.
//
// Both layers are asserted through the real request body, because the failure
// this guards is not a wrong string — it is a 400 the dialog then reports as
// "Jira: unavailable", blaming the tracker for the user's own half-typed branch
// name.
func TestSearchQuotesTheTerm(t *testing.T) {
	for _, term := range []string{`retry "charge"`, `fix (login`, `what?`, `C++`} {
		rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
			"/rest/api/3/search/jql": func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"issues":[]}`))
			},
		})
		if _, err := Search(context.Background(), term, 5); err != nil {
			t.Fatal(err)
		}
		var req searchRequest
		if err := json.Unmarshal([]byte(rec.posts["/rest/api/3/search/jql"]), &req); err != nil {
			t.Fatalf("request body: %v", err)
		}
		want := `text ~ "` + escapeJQL(term) + `"`
		if !strings.Contains(req.JQL, want) {
			t.Errorf("JQL = %q, want it to contain %q", req.JQL, want)
		}
		// No reserved character may reach Lucene unescaped.
		for _, r := range luceneReserved {
			bare := string(r)
			if strings.Contains(term, bare) && !strings.Contains(req.JQL, `\\`+bare) {
				t.Errorf("%q reached the query unescaped in %q", bare, req.JQL)
			}
		}
	}
}

// TestDocumentBuildsTheWholeTicket is the full read path: one round trip, ADF
// converted, metadata lifted, and the attachment queued for download.
func TestDocumentBuildsTheWholeTicket(t *testing.T) {
	var site string
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Replace(issueJSON, "%s", site, 1)))
		},
	})
	credState.mu.Lock()
	site = credState.cred.Site
	credState.mu.Unlock()

	doc, err := New().Document(context.Background(), "ops-42")
	if err != nil {
		t.Fatalf("Document: %v", err)
	}

	if doc.Identifier != "OPS-42" || doc.Title != "Checkout 500s on retry" {
		t.Errorf("ticket = %+v", doc.Ticket)
	}
	if doc.Assignee != "Dana Ruiz" {
		t.Errorf("assignee = %q", doc.Assignee)
	}
	if strings.Join(doc.Labels, ",") != "billing,regression" {
		t.Errorf("labels = %v", doc.Labels)
	}
	if doc.Parent != "OPS-1 — Checkout hardening" {
		t.Errorf("parent = %q", doc.Parent)
	}
	for _, want := range []string{
		"Retrying a failed charge 500s.",
		"## Comments (1)",
		"### Sam — 2026-08-25 09:41",
		"Repros on staging.",
		"- OPS-43 — Add the retry test",
		"- blocks OPS-50 — Ship 4.2",
		"![trace.png](" + ticket.PlaceholderFor(0) + ")",
	} {
		if !strings.Contains(doc.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, doc.Body)
		}
	}
	if len(doc.Images) != 1 || !strings.HasSuffix(doc.Images[0].URL, "/attachment/content/9001") {
		t.Errorf("images = %+v", doc.Images)
	}
	if doc.Start == nil || doc.Auth == nil || doc.Host == nil {
		t.Fatal("Document must carry the auth, gate and mutation closures")
	}
	if q := rec.seen["/rest/api/3/issue/OPS-42"]; q != "fields="+fullFields {
		t.Errorf("query = %q, want the full field set in ONE round trip", q)
	}

	// The comment page was complete, so no second call was needed.
	if _, extra := rec.seen["/rest/api/3/issue/OPS-42/comment"]; extra {
		t.Error("a second comment fetch ran though the first page was complete")
	}
}

// TestDocumentFetchesMoreCommentsOnlyWhenTruncated: the newest comment is
// usually what changes what a ticket means, so a truncated page is worth a
// round trip — and a complete one is not.
func TestDocumentFetchesMoreCommentsOnlyWhenTruncated(t *testing.T) {
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42/comment": func(w http.ResponseWriter, r *http.Request) {
			// Newest first, which is what orderBy=-created returns. The
			// fixture has to match the request or the reversal back to
			// chronological cannot be tested at all.
			_, _ = w.Write([]byte(`{"total":2,"comments":[
				{"author":{"displayName":"Dana"},"created":"2026-08-25T10:00:00.000+0000",
				 "body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}},
				{"author":{"displayName":"Sam"},"created":"2026-08-25T09:41:22.113+0000",
				 "body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]}}
			]}`))
		},
		"/rest/api/3/issue/OPS-42": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"key":"OPS-42","fields":{"summary":"x",
				"status":{"name":"To Do","statusCategory":{"key":"new"}},
				"comment":{"total":2,"comments":[
					{"author":{"displayName":"Sam"},"created":"2026-08-25T09:41:22.113+0000",
					 "body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]}}
				]}}}`))
		},
	})

	doc, err := New().Document(context.Background(), "OPS-42")
	if err != nil {
		t.Fatal(err)
	}
	q, ok := rec.seen["/rest/api/3/issue/OPS-42/comment"]
	if !ok {
		t.Fatal("a truncated comment page should have been re-fetched")
	}
	// -created, not created: Jira's orderBy is ascending by default, so the
	// plain form fetches the OLDEST page — the opposite of the reason this
	// round trip is worth taking.
	if !strings.Contains(q, "orderBy=-created") {
		t.Errorf("query = %q, want the NEWEST comments", q)
	}
	if !strings.Contains(doc.Body, "second") || !strings.Contains(doc.Body, "## Comments (2)") {
		t.Errorf("the later comment did not reach the body:\n%s", doc.Body)
	}
	// Rendered oldest-first even though they arrived newest-first: ticket.md
	// reads as a conversation, and Linear's comments are chronological.
	if strings.Index(doc.Body, "first") > strings.Index(doc.Body, "second") {
		t.Errorf("comments render newest-first; they must read in order:\n%s", doc.Body)
	}
}

// TestCommentRefetchNeverShrinksTheSet: an issue can embed more comments than
// commentFetchLimit, and swapping 100 embedded for 50 fetched would lose half
// of them while looking like a fix.
func TestCommentRefetchNeverShrinksTheSet(t *testing.T) {
	var embedded strings.Builder
	for i := 0; i < 60; i++ {
		if i > 0 {
			embedded.WriteString(",")
		}
		fmt.Fprintf(&embedded, `{"author":{"displayName":"Sam"},"created":"2026-08-25T09:%02d:00.000+0000",
			"body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"embedded-%d"}]}]}}`, i, i)
	}

	fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42/comment": func(w http.ResponseWriter, r *http.Request) {
			// A smaller page than what the issue already carried.
			_, _ = w.Write([]byte(`{"total":120,"comments":[
				{"author":{"displayName":"Dana"},"created":"2026-08-25T11:00:00.000+0000",
				 "body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"page-only"}]}]}}
			]}`))
		},
		"/rest/api/3/issue/OPS-42": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"key":"OPS-42","fields":{"summary":"x",
				"status":{"name":"To Do","statusCategory":{"key":"new"}},
				"comment":{"total":120,"comments":[%s]}}}`, embedded.String())
		},
	})

	doc, err := New().Document(context.Background(), "OPS-42")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Body, "## Comments (60)") {
		t.Errorf("the smaller page replaced the larger embedded set:\n%s", doc.Body[:min(400, len(doc.Body))])
	}
	if strings.Contains(doc.Body, "page-only") {
		t.Error("a one-comment page displaced 60 embedded comments")
	}
}

// TestStartMovesTheIssueOnce drives the one mutation fleet makes, through the
// closure Materialize calls.
func TestStartMovesTheIssueOnce(t *testing.T) {
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42/transitions": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write([]byte(`{"transitions":[
				{"id":"11","name":"To Do","to":{"name":"To Do","statusCategory":{"key":"new"}}},
				{"id":"21","name":"Review","to":{"name":"In Review","statusCategory":{"key":"indeterminate"}}},
				{"id":"31","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}}
			]}`))
		},
	})

	got, err := moveToStarted(context.Background(), "OPS-42")
	if err != nil {
		t.Fatalf("moveToStarted: %v", err)
	}
	if got != "In Progress" {
		t.Errorf("moved to %q, want In Progress — not the review state that comes first", got)
	}
	body := rec.posts["/rest/api/3/issue/OPS-42/transitions"]
	if !strings.Contains(body, `"id":"31"`) {
		t.Errorf("posted %q, want the In Progress transition id", body)
	}
}

// TestStartIsSilentWhenThereIsNowhereToGo: a workflow with no started
// transition is not an error — it is a workflow fleet has nothing to say about,
// and inventing a move would be worse than doing nothing.
func TestStartIsSilentWhenThereIsNowhereToGo(t *testing.T) {
	rec := fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
		"/rest/api/3/issue/OPS-42/transitions": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"transitions":[
				{"id":"41","name":"Done","to":{"name":"Done","statusCategory":{"key":"done"}}}]}`))
		},
	})
	got, err := moveToStarted(context.Background(), "OPS-42")
	if err != nil || got != "" {
		t.Errorf("moveToStarted = (%q, %v), want no move and no error", got, err)
	}
	if body := rec.posts["/rest/api/3/issue/OPS-42/transitions"]; body != "" {
		t.Errorf("a POST was made though there was nowhere to move: %q", body)
	}
}

// TestVerifyCredentialChecksTheAccountThenTheProjects.
//
// The project list is a nicety, not a proof: /myself already verified the
// credential, and a token scoped away from project browsing must not read as a
// bad token — that would refuse a perfectly good credential at the one moment
// the user is deciding whether they typed it correctly.
func TestVerifyCredentialChecksTheAccountThenTheProjects(t *testing.T) {
	t.Run("both answer", func(t *testing.T) {
		fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
			"/rest/api/3/myself": func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"displayName":"Dana","accountId":"5f0"}`))
			},
			"/rest/api/3/project/search": func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"values":[{"key":"ops"},{"key":"BRZ"}]}`))
			},
		})
		acct, err := VerifyCredential(context.Background(), currentCred())
		if err != nil {
			t.Fatalf("VerifyCredential: %v", err)
		}
		if len(acct.Keys) != 2 || acct.Keys[0] != "BRZ" || acct.Keys[1] != "OPS" {
			t.Errorf("keys = %v, want them upper-cased and sorted", acct.Keys)
		}
	})

	t.Run("projects refused, credential still good", func(t *testing.T) {
		fakeJira(t, map[string]func(http.ResponseWriter, *http.Request){
			"/rest/api/3/myself": func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"displayName":"Dana"}`))
			},
			"/rest/api/3/project/search": func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
		})
		acct, err := VerifyCredential(context.Background(), currentCred())
		if err != nil {
			t.Fatalf("a scoped-down token must still verify: %v", err)
		}
		if len(acct.Keys) != 0 {
			t.Errorf("keys = %v, want none", acct.Keys)
		}
	})

	t.Run("server or data center site", func(t *testing.T) {
		// /rest/api/3 exists on Cloud and nowhere else, so this 404 IS the
		// diagnosis the connect dialog turns into "Jira Cloud only".
		fakeJira(t, nil)
		if _, err := VerifyCredential(context.Background(), currentCred()); err != ticket.ErrNotFound {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func currentCred() Credential {
	credState.mu.Lock()
	defer credState.mu.Unlock()
	return credState.cred
}
