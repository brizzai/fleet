package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/ticket"
)

// httpClient is shared so connections are reused across a burst of requests.
// The per-call context does the real bounding; the client timeout is a backstop
// for a request that never reaches the deadline machinery.
var httpClient = &http.Client{Timeout: 90 * time.Second}

// liteFields is what the dialog needs while you are still typing: enough to
// name a branch and draw a row, nothing more. Asking for the description here
// would pull an ADF document per suggestion on every keystroke's search.
const liteFields = "summary,status,priority"

// fullFields is everything Materialize writes to disk, in ONE round trip.
const fullFields = "summary,status,priority,assignee,labels,parent,subtasks,issuelinks,attachment,description,comment"

// failures are one-shot rather than polled, so this throttle isn't stopping a
// flood — it stops a user who creates ten worktrees against a broken credential
// from emitting ten identical events. reason is a low-cardinality label, never
// an issue key or a path.
const failTrackInterval = 10 * time.Minute

var (
	failMu   sync.Mutex
	failLast time.Time
)

func trackFailure(reason string) {
	failMu.Lock()
	if !failLast.IsZero() && time.Since(failLast) < failTrackInterval {
		failMu.Unlock()
		return
	}
	failLast = time.Now()
	failMu.Unlock()
	analytics.Track(analytics.EventTicketCommandFailure, map[string]any{
		"provider": kind,
		"reason":   reason,
	})
}

// apiError is Atlassian's error envelope. Present on most 4xx replies, absent
// on some, which is why classify never depends on it alone.
type apiError struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

// classify maps a response onto a sentinel.
//
// Jira answers with real HTTP status codes, which is the exact opposite of
// Linear: there, an unknown issue comes back as HTTP 200 with an errors[] entry
// carrying its own statusCode, and reading the status alone files "no such
// issue" as a generic failure. Here reading the body first would do the same in
// reverse. The asymmetry is pinned by a test in each package rather than left
// as something a reader has to notice.
//
// 404 is worth a word: Jira returns it both for an issue that does not exist and
// for one the credential may not see, and deliberately does not distinguish them
// — telling an unauthorized caller which keys are real is an information leak.
// So "not found" here honestly means "not found, for you".
func classify(status int, body []byte) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ticket.ErrNotAuthenticated
	case status == http.StatusNotFound:
		return ticket.ErrNotFound
	case status >= 200 && status < 300:
		return nil
	}
	var e apiError
	if json.Unmarshal(body, &e) == nil {
		if len(e.ErrorMessages) > 0 {
			return fmt.Errorf("jira: %s", truncate(e.ErrorMessages[0], 200))
		}
		for field, msg := range e.Errors {
			return fmt.Errorf("jira: %s: %s", field, truncate(msg, 200))
		}
	}
	return fmt.Errorf("jira: http %d", status)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// do runs one REST call against the connected site and decodes into out.
//
// path is site-relative and always begins with "/rest/api/3/". It is joined to
// a base rebuilt from the stored host, never to anything a user typed, so a
// pasted URL cannot redirect a request somewhere else.
func do(ctx context.Context, timeout time.Duration, method, path string, in, out any) error {
	cred, err := credential()
	if err != nil {
		return err
	}
	return doWith(ctx, cred, timeout, method, path, in, out)
}

// doWith is do against an explicit credential, so the Connect dialog can verify
// a pasted token before anything is stored.
func doWith(ctx context.Context, cred Credential, timeout time.Duration, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var payload io.Reader
	if in != nil {
		body, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, cred.baseURL()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", cred.authHeader())

	resp, err := httpClient.Do(req)
	if err != nil {
		// A timeout must be distinguishable from a transport failure, and the
		// context is the only reliable witness: the error the client returns on
		// a cancelled request wraps its own type. Wrapped with %w so callers
		// can errors.Is it.
		if ctx.Err() == context.DeadlineExceeded {
			trackFailure("timeout")
			return fmt.Errorf("jira: request timed out: %w", ctx.Err())
		}
		trackFailure("transport")
		return fmt.Errorf("jira: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, ticket.MaxResponseBytes))
	if err != nil {
		return fmt.Errorf("jira: reading response: %w", err)
	}
	if err := classify(resp.StatusCode, raw); err != nil {
		if err != ticket.ErrNotFound {
			debuglog.Logger.Debug("jira: request failed", "status", resp.StatusCode, "error", err)
			trackFailure(reasonFor(err))
		}
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// reasonFor collapses an error to a low-cardinality analytics label. Never the
// error string: those can carry an issue key.
func reasonFor(err error) string {
	switch err {
	case ticket.ErrNotAuthenticated:
		return "not_authenticated"
	case ticket.ErrNotConnected:
		return "not_connected"
	case ticket.ErrNotFound:
		return "not_found"
	}
	return "request_failed"
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

type issue struct {
	Key    string      `json:"key"`
	Fields issueFields `json:"fields"`
}

type issueFields struct {
	Summary  string      `json:"summary"`
	Status   *status     `json:"status"`
	Priority *namedField `json:"priority"`
	Assignee *struct {
		DisplayName string `json:"displayName"`
	} `json:"assignee"`
	Labels      []string        `json:"labels"`
	Parent      *issue          `json:"parent"`
	Subtasks    []issue         `json:"subtasks"`
	IssueLinks  []issueLink     `json:"issuelinks"`
	Attachment  []attachment    `json:"attachment"`
	Description json.RawMessage `json:"description"`
	Comment     *commentPage    `json:"comment"`
}

type status struct {
	Name     string `json:"name"`
	Category struct {
		Key string `json:"key"` // "new" | "indeterminate" | "done"
	} `json:"statusCategory"`
}

type namedField struct {
	Name string `json:"name"`
}

type issueLink struct {
	Type         linkType `json:"type"`
	InwardIssue  *issue   `json:"inwardIssue"`
	OutwardIssue *issue   `json:"outwardIssue"`
}

// linkType carries Jira's own words for a relationship in each direction
// ("blocks" / "is blocked by"). Named rather than inline so renderLinks can be
// tested without reconstructing an anonymous struct type by hand.
type linkType struct {
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

type attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
}

func (a attachment) isImage() bool { return strings.HasPrefix(a.MimeType, "image/") }

type comment struct {
	Author *struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string          `json:"created"`
	Body    json.RawMessage `json:"body"`
}

type commentPage struct {
	Comments []comment `json:"comments"`
	Total    int       `json:"total"`
}

// stateType maps Jira's status category onto fleet's.
//
// The category is the only orderable thing Jira exposes about a status: names
// are per-project and endlessly customized, and there is no position. Jira has
// no distinct backlog or triage category, so both of fleet's collapse into
// Unstarted here — which is honest rather than lossy: a Jira "To Do" really is
// one bucket.
func stateType(categoryKey string) ticket.StateType {
	switch categoryKey {
	case "indeterminate":
		return ticket.StateStarted
	case "new":
		return ticket.StateUnstarted
	case "done":
		return ticket.StateOther
	}
	return ticket.StateOther
}

// priority maps a Jira priority NAME onto fleet's scale.
//
// By name rather than by id because priority schemes are per-project and fully
// customizable: the default ids 1..5 mean Highest..Lowest only on a site nobody
// has configured. The aliases cover the schemes teams actually build — the
// Bugzilla-flavoured Blocker/Critical/Major/Minor/Trivial set is the common one.
//
// Anything unrecognized is None rather than Medium: guessing a middle rank for a
// priority fleet does not understand would put it above a real Low, and the list
// is sorted on this.
func priority(p *namedField) ticket.Priority {
	if p == nil {
		return ticket.PriorityNone
	}
	switch strings.ToLower(strings.TrimSpace(p.Name)) {
	case "highest", "blocker", "critical":
		return ticket.PriorityUrgent
	case "high", "major":
		return ticket.PriorityHigh
	case "medium", "normal":
		return ticket.PriorityMedium
	case "low", "minor", "lowest", "trivial":
		return ticket.PriorityLow
	}
	return ticket.PriorityNone
}

func (i *issue) ticket(site string) ticket.Ticket {
	if i == nil {
		return ticket.Ticket{}
	}
	t := ticket.Ticket{
		Provider:   kind,
		Identifier: strings.ToUpper(i.Key),
		Title:      i.Fields.Summary,
		Priority:   priority(i.Fields.Priority),
		// Jira has no ordering within a status category, so every ticket
		// supplies the same position and the tiebreak falls through to
		// priority. Deliberately 0 rather than the "sorts last" sentinel
		// Linear uses for a missing state: this is an absence of the concept,
		// not a missing value.
		State: ticket.StateOther,
	}
	if site != "" && t.Identifier != "" {
		t.URL = "https://" + site + "/browse/" + t.Identifier
	}
	if s := i.Fields.Status; s != nil {
		t.StateName, t.State = s.Name, stateType(s.Category.Key)
	}
	return t
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

// Fetch returns an issue's metadata. Used by the worktree dialog and by
// `fleet wt --ticket` to confirm a key exists before a branch is named after it.
func Fetch(ctx context.Context, id string) (ticket.Ticket, error) {
	cred, err := credential()
	if err != nil {
		return ticket.Ticket{}, err
	}
	var out issue
	path := "/rest/api/3/issue/" + url.PathEscape(strings.ToUpper(id)) + "?fields=" + liteFields
	if err := doWith(ctx, cred, ticket.MetaTimeout, http.MethodGet, path, nil, &out); err != nil {
		return ticket.Ticket{}, err
	}
	if out.Key == "" {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return out.ticket(cred.Site), nil
}

// searchRequest is the body of POST /rest/api/3/search/jql.
//
// That endpoint replaced /rest/api/3/search, which Atlassian removed. The
// replacement paginates by an opaque nextPageToken rather than startAt and no
// longer reports a total — neither of which fleet needs, because every call
// here wants one page of at most a few dozen rows.
type searchRequest struct {
	JQL        string   `json:"jql"`
	Fields     []string `json:"fields"`
	MaxResults int      `json:"maxResults"`
}

type searchResponse struct {
	Issues []issue `json:"issues"`
}

func search(ctx context.Context, cred Credential, jql string, limit int) ([]ticket.Ticket, error) {
	req := searchRequest{JQL: jql, Fields: strings.Split(liteFields, ","), MaxResults: limit}
	var out searchResponse
	if err := doWith(ctx, cred, ticket.MetaTimeout, http.MethodPost, "/rest/api/3/search/jql", req, &out); err != nil {
		return nil, err
	}
	tickets := make([]ticket.Ticket, 0, len(out.Issues))
	for i := range out.Issues {
		if out.Issues[i].Key == "" {
			continue
		}
		tickets = append(tickets, out.Issues[i].ticket(cred.Site))
	}
	return tickets, nil
}

// Search returns issues matching a full-text term, for the dialog's suggestions.
//
// Deliberately unscoped by project: the repo gate already decides WHETHER we
// search here, and someone typing prose wants matches, not a filter they didn't
// ask for. Done issues are dropped server-side — a closed ticket is not
// something you are about to start a worktree for.
func Search(ctx context.Context, term string, limit int) ([]ticket.Ticket, error) {
	if limit <= 0 {
		limit = 5
	}
	cred, err := credential()
	if err != nil {
		return nil, err
	}
	jql := fmt.Sprintf(`text ~ "%s" AND statusCategory != Done ORDER BY updated DESC`, escapeJQL(term))
	return search(ctx, cred, jql, limit)
}

// AssignedIssues returns your open assigned issues, most actionable first.
//
// currentUser() rather than the email address: the two differ on a site where
// the account's email is hidden by privacy settings, which is the default for
// many organizations, and an email-keyed query there quietly returns nothing.
func AssignedIssues(ctx context.Context, limit int) ([]ticket.Ticket, error) {
	if limit <= 0 {
		limit = 100
	}
	cred, err := credential()
	if err != nil {
		return nil, err
	}
	const jql = `assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC`
	tickets, err := search(ctx, cred, jql, limit)
	if err != nil {
		return nil, err
	}
	// ticket.SortAssigned owns the comparison so a merged Linear+Jira list is
	// ordered by the same rule as either one alone. The server's updated-desc
	// order survives underneath as the stable tiebreak.
	ticket.SortAssigned(tickets)
	return tickets, nil
}

// ---------------------------------------------------------------------------
// The one mutation
// ---------------------------------------------------------------------------

type transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   status `json:"to"`
}

// startedTransition picks the transition that means "I'm starting this".
//
// Jira has no equivalent of Linear's state position, so "the lowest-positioned
// started state" has no counterpart: the only structure is the target's status
// category. Every in-progress-ish status shares the `indeterminate` category,
// so a workflow with both "In Progress" and "In Review" offers two candidates
// and picking the wrong one moves a fresh ticket straight to review.
//
// The tiebreak is the exact name "In Progress", which is Jira's own default and
// what the overwhelming majority of workflows still call it. Failing that, the
// first indeterminate transition in the order the API returned — which is the
// workflow's own order, so the earliest step wins.
func startedTransition(ts []transition) (transition, bool) {
	var first transition
	var found bool
	for _, t := range ts {
		if t.To.Category.Key != "indeterminate" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(t.To.Name), "In Progress") {
			return t, true
		}
		if !found {
			first, found = t, true
		}
	}
	return first, found
}

// moveToStarted moves an issue into its workflow's first started status and
// returns the resulting status name.
//
// Returns ("", nil) when the workflow offers no started transition at all, which
// is not an error — it is a workflow fleet has nothing to say about.
//
// Unlike Linear this costs a second round trip: available transitions depend on
// the issue's current status and the caller's permissions, so they cannot ride
// along with the issue fetch the way a Linear team's workflow states do.
func moveToStarted(ctx context.Context, key string) (string, error) {
	var list struct {
		Transitions []transition `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := do(ctx, ticket.StateTimeout, http.MethodGet, path, nil, &list); err != nil {
		trackFailure("state_write_failed")
		return "", err
	}
	t, ok := startedTransition(list.Transitions)
	if !ok {
		return "", nil
	}
	body := map[string]any{"transition": map[string]string{"id": t.ID}}
	if err := do(ctx, ticket.StateTimeout, http.MethodPost, path, body, nil); err != nil {
		trackFailure("state_write_failed")
		return "", err
	}
	if t.To.Name != "" {
		return t.To.Name, nil
	}
	return t.Name, nil
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

var acctCache struct {
	mu     sync.RWMutex
	acct   ticket.Account
	loaded bool
}

func resetAccountCache() {
	acctCache.mu.Lock()
	acctCache.acct, acctCache.loaded = ticket.Account{}, false
	acctCache.mu.Unlock()
}

// accountInfo returns the cached site reading, and whether it has been taken.
// Free and non-blocking — safe from the Update goroutine.
func accountInfo() (ticket.Account, bool) {
	acctCache.mu.RLock()
	defer acctCache.mu.RUnlock()
	return acctCache.acct, acctCache.loaded
}

func fetchAccount(ctx context.Context) (ticket.Account, error) {
	cred, err := credential()
	if err != nil {
		return ticket.Account{}, err
	}
	return fetchAccountWith(ctx, cred, true)
}

// VerifyCredential proves a credential works before fleet stores it, and returns
// what it is attached to.
//
// Verifying first is what lets the Connect dialog say "connected" as a fact
// rather than a hope, and it means a typo is caught while the user is still
// looking at the field they typed it into. It is also the only place fleet can
// honestly tell a Server or Data Center user that this won't work: /rest/api/3
// exists on Cloud and nowhere else, so a 404 here is the diagnosis.
func VerifyCredential(ctx context.Context, cred Credential) (ticket.Account, error) {
	return fetchAccountWith(ctx, cred, false)
}

func fetchAccountWith(ctx context.Context, cred Credential, useStored bool) (ticket.Account, error) {
	var me struct {
		DisplayName string `json:"displayName"`
		AccountID   string `json:"accountId"`
	}
	if err := doWith(ctx, cred, ticket.MetaTimeout, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return ticket.Account{}, err
	}

	acct := ticket.Account{Name: cred.Site}

	// The project list is a nicety, not a proof: /myself already verified the
	// credential, and a token scoped away from project browsing must not read
	// as a bad token. A failure here costs the dialog's "put this in
	// .fleet.local.json" hint and nothing else.
	var projects struct {
		Values []struct {
			Key string `json:"key"`
		} `json:"values"`
	}
	if err := doWith(ctx, cred, ticket.MetaTimeout, http.MethodGet,
		"/rest/api/3/project/search?maxResults=50&orderBy=key", nil, &projects); err != nil {
		debuglog.Logger.Debug("jira: could not list projects", "error", err)
	}
	for _, p := range projects.Values {
		if p.Key != "" {
			acct.Keys = append(acct.Keys, strings.ToUpper(p.Key))
		}
	}
	sort.Strings(acct.Keys)

	// Only cache a reading taken with the STORED credential. VerifyCredential
	// calls this with a candidate the user has just pasted and that nothing has
	// persisted yet; if the store then refuses, Account() would go on reporting
	// a site no stored credential backs.
	if useStored {
		acctCache.mu.Lock()
		acctCache.acct, acctCache.loaded = acct, true
		acctCache.mu.Unlock()
	}
	return acct, nil
}

// SetAccountForTest installs a site reading without a network call, so UI tests
// can exercise the wrong-site path. An empty Account clears it.
func SetAccountForTest(a ticket.Account) {
	acctCache.mu.Lock()
	defer acctCache.mu.Unlock()
	acctCache.acct = a
	acctCache.loaded = a.Name != "" || len(a.Keys) > 0
}
