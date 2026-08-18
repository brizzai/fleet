package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
)

// apiEndpoint is Linear's only GraphQL endpoint. Overridable so tests can point
// at an httptest server.
const apiEndpoint = "https://api.linear.app/graphql"

var apiEndpointVar = apiEndpoint

// getenv is a seam for tests; production always reads the real environment.
var getenv = os.Getenv

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// httpClient is shared so connections are reused across a burst of image
// downloads. The per-call context does the real bounding; the client timeout is
// a backstop for a request that never reaches the deadline machinery.
var httpClient = &http.Client{Timeout: 90 * time.Second}

// failures are one-shot rather than polled, so this throttle isn't stopping a
// flood — it stops a user who creates ten worktrees against a broken credential
// from emitting ten identical events. reason is a low-cardinality label, never
// an issue identifier or a path.
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
	analytics.Track(analytics.EventLinearCommandFailure, map[string]any{
		"reason": reason,
	})
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlErrorEntry struct {
	Message    string `json:"message"`
	Extensions struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"extensions"`
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlErrorEntry `json:"errors"`
}

// classifyGraphQL maps a response onto a sentinel.
//
// Status alone cannot do this, which is the whole reason the function exists:
// Linear answers an unknown issue with **HTTP 200** and an errors[] entry whose
// own extensions carry statusCode 400. Captured from the live API rather than
// guessed:
//
//	unknown id  -> 200, message "Entity not found: Issue", code INPUT_ERROR
//	bad token   -> 401, code AUTHENTICATION_ERROR
func classifyGraphQL(status int, errs []gqlErrorEntry) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrNotAuthenticated
	}
	for _, e := range errs {
		code := strings.ToUpper(e.Extensions.Code)
		msg := strings.ToLower(e.Message)
		switch {
		case code == "AUTHENTICATION_ERROR", strings.Contains(msg, "not authenticated"):
			return ErrNotAuthenticated
		case strings.Contains(msg, "entity not found"):
			return ErrNotFound
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("linear: %s", truncate(errs[0].Message, 200))
	}
	if status != http.StatusOK {
		return fmt.Errorf("linear: http %d", status)
	}
	return nil
}

// execute runs one GraphQL operation and decodes data into out.
func execute(ctx context.Context, timeout time.Duration, query string, vars map[string]any, out any) error {
	cred, err := credential()
	if err != nil {
		return err
	}
	return executeWith(ctx, cred, timeout, query, vars, out)
}

// executeWith is execute against an explicit credential, so the Connect dialog
// can verify a pasted key before anything is stored.
func executeWith(ctx context.Context, cred Credential, timeout time.Duration, query string, vars map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiEndpointVar, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", cred.authHeader())

	resp, err := httpClient.Do(req)
	if err != nil {
		// A timeout must be distinguishable from a transport failure, and the
		// context is the only reliable witness: the error the client returns on
		// a cancelled request wraps its own type. Wrapped with %w so callers
		// can errors.Is it.
		if ctx.Err() == context.DeadlineExceeded {
			trackFailure("timeout")
			return fmt.Errorf("linear: request timed out: %w", ctx.Err())
		}
		trackFailure("transport")
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("linear: reading response: %w", err)
	}

	var env gqlEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		debuglog.Logger.Debug("linear: response was not JSON", "status", resp.StatusCode)
		return fmt.Errorf("linear: unreadable response (http %d)", resp.StatusCode)
	}
	if err := classifyGraphQL(resp.StatusCode, env.Errors); err != nil {
		if err != ErrNotFound {
			debuglog.Logger.Debug("linear: request failed", "status", resp.StatusCode, "error", err)
			trackFailure(reasonFor(err))
		}
		return err
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// reasonFor collapses an error to a low-cardinality analytics label. Never the
// error string: those can carry an issue identifier.
func reasonFor(err error) string {
	switch err {
	case ErrNotAuthenticated:
		return "not_authenticated"
	case ErrNotConnected:
		return "not_connected"
	case ErrNotFound:
		return "not_found"
	}
	return "request_failed"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// Documents
// ---------------------------------------------------------------------------

// issueLiteQuery is what the worktree dialog needs while you are still typing:
// enough to name the branch and show the row, nothing more.
const issueLiteQuery = `query Issue($id: String!) {
  issue(id: $id) { identifier title url state { name } }
}`

// issueFullQuery is everything Materialize writes to disk, in ONE round trip.
//
// The team's workflow states ride along deliberately: MoveToStarted needs them
// to resolve "started" by type, and fetching them here means the whole
// create-a-worktree-from-a-ticket flow costs one query plus the image GETs.
// Measured at 87 complexity points against a 10,000-per-query cap.
const issueFullQuery = `query Issue($id: String!) {
  issue(id: $id) {
    id identifier title url priority description
    state { name type }
    assignee { displayName }
    labels(first: 20) { nodes { name } }
    parent { identifier title }
    children(first: 20) { nodes { identifier title } }
    comments(first: 50) { nodes { body createdAt user { displayName } } }
    attachments(first: 20) { nodes { title url } }
    team { id key name states(first: 50) { nodes { id name type position } } }
  }
}`

// searchQuery uses searchIssues, confirmed against the live schema: both
// searchIssues(term:) and issueSearch(query:) exist and neither is deprecated,
// but searchIssues is the one that takes a plain full-text term.
const searchQuery = `query Search($term: String!, $first: Int!) {
  searchIssues(term: $term, first: $first) {
    nodes { identifier title url state { name } }
  }
}`

// assignedIssuesQuery is the "my tickets" list.
//
// Filtered server-side to open work: a finished ticket is not something you are
// about to start, and dropping them there rather than here keeps the payload
// small. Ordered by updatedAt so the tail of a long Todo list is at least in a
// useful order before the client regroups it.
const assignedIssuesQuery = `query Mine($first: Int!) {
  viewer {
    assignedIssues(
      first: $first
      filter: { state: { type: { nin: ["completed", "canceled"] } } }
      orderBy: updatedAt
    ) {
      nodes { identifier title url state { name type position } }
    }
  }
}`

const workspaceQuery = `query Workspace {
  organization { name urlKey }
  teams(first: 250) { nodes { key name } }
}`

const updateStateMutation = `mutation Start($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success issue { state { name } }
  }
}`

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

type issueLite struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      *struct {
		Name     string  `json:"name"`
		Type     string  `json:"type"`
		Position float64 `json:"position"`
	} `json:"state"`
}

func (i *issueLite) stateType() string {
	if i == nil || i.State == nil {
		return ""
	}
	return i.State.Type
}

// statePosition returns the state's order within its team. A missing state
// sorts last rather than first, so an unusable payload cannot lead the list.
func (i *issueLite) statePosition() float64 {
	if i == nil || i.State == nil {
		return 1 << 30
	}
	return i.State.Position
}

func (i *issueLite) ticket() Ticket {
	if i == nil {
		return Ticket{}
	}
	t := Ticket{Identifier: i.Identifier, Title: i.Title, URL: i.URL}
	if i.State != nil {
		t.StateName, t.StateType = i.State.Name, i.State.Type
	}
	return t
}

type workflowState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

type issueFull struct {
	issueLite
	Priority    int    `json:"priority"`
	Description string `json:"description"`
	Assignee    *struct {
		DisplayName string `json:"displayName"`
	} `json:"assignee"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Parent *struct {
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
	} `json:"parent"`
	Children struct {
		Nodes []struct {
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
		} `json:"nodes"`
	} `json:"children"`
	Comments struct {
		Nodes []struct {
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"createdAt"`
			User      *struct {
				DisplayName string `json:"displayName"`
			} `json:"user"`
		} `json:"nodes"`
	} `json:"comments"`
	Attachments struct {
		Nodes []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"nodes"`
	} `json:"attachments"`
	Team *issueTeam `json:"team"`
}

// issueTeam is named rather than inline so the started-state resolution can be
// tested without reconstructing an anonymous struct type by hand.
type issueTeam struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	States struct {
		Nodes []workflowState `json:"nodes"`
	} `json:"states"`
}

// startedState returns the team's first started state, resolved by TYPE against
// a position-sorted list.
//
// Matching on type rather than name is what makes this work on a team whose
// started state is called "In Dev" or "Doing". Verified against a live team
// whose started states are In Progress (position 2) and In Review (1002) — the
// lower position is the one a human means by "I'm starting this".
func (i *issueFull) startedState() (workflowState, bool) {
	if i == nil || i.Team == nil {
		return workflowState{}, false
	}
	var started []workflowState
	for _, s := range i.Team.States.Nodes {
		if s.Type == "started" {
			started = append(started, s)
		}
	}
	if len(started) == 0 {
		return workflowState{}, false
	}
	sort.Slice(started, func(a, b int) bool { return started[a].Position < started[b].Position })
	return started[0], true
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

// Fetch returns an issue's metadata. Used by the worktree dialog and by
// `fleet wt --ticket` to confirm an identifier exists before a branch is named
// after it.
func Fetch(ctx context.Context, id string) (Ticket, error) {
	var out struct {
		Issue *issueLite `json:"issue"`
	}
	if err := execute(ctx, metaTimeout, issueLiteQuery, map[string]any{"id": strings.ToUpper(id)}, &out); err != nil {
		return Ticket{}, err
	}
	// A null node with no errors[] is Linear's other way of saying "no such
	// issue" — treat it the same rather than returning an empty ticket that
	// callers would have to re-check.
	if out.Issue == nil || out.Issue.Identifier == "" {
		return Ticket{}, ErrNotFound
	}
	return out.Issue.ticket(), nil
}

// Search returns issues matching a full-text term, for the dialog's suggestions.
//
// Deliberately unscoped by team: the repo gate already decides WHETHER we
// search here, and someone typing prose wants matches, not a filter they
// didn't ask for.
func Search(ctx context.Context, term string, limit int) ([]Ticket, error) {
	if limit <= 0 {
		limit = 5
	}
	var out struct {
		SearchIssues struct {
			Nodes []issueLite `json:"nodes"`
		} `json:"searchIssues"`
	}
	if err := execute(ctx, metaTimeout, searchQuery, map[string]any{"term": term, "first": limit}, &out); err != nil {
		return nil, err
	}
	var tickets []Ticket
	for i := range out.SearchIssues.Nodes {
		n := out.SearchIssues.Nodes[i]
		if n.Identifier == "" {
			continue
		}
		tickets = append(tickets, n.ticket())
	}
	return tickets, nil
}

// MoveToStarted moves an issue into its team's first started state and returns
// the resulting state name.
//
// Takes the already-fetched issue so the whole thing is one mutation: the
// states came along with the full fetch. Returns ("", nil) when the team has no
// started state at all, which is not an error — it is a team fleet has nothing
// to say about.
func MoveToStarted(ctx context.Context, issue *issueFull) (string, error) {
	state, ok := issue.startedState()
	if !ok {
		return "", nil
	}
	var out struct {
		IssueUpdate struct {
			Success bool `json:"success"`
			Issue   *struct {
				State *struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"issueUpdate"`
	}
	vars := map[string]any{"id": issue.Identifier, "stateId": state.ID}
	if err := execute(ctx, stateTimeout, updateStateMutation, vars, &out); err != nil {
		trackFailure("state_write_failed")
		return "", err
	}
	if !out.IssueUpdate.Success {
		trackFailure("state_write_failed")
		return "", fmt.Errorf("linear: issue update was refused")
	}
	if u := out.IssueUpdate.Issue; u != nil && u.State != nil {
		return u.State.Name, nil
	}
	return state.Name, nil
}

// stateTypeRank orders Linear's state categories by how close the work is to
// your hands. Ranking on the TYPE rather than the name is what makes this work
// on a team that renamed its states.
func stateTypeRank(t string) int {
	switch t {
	case "started":
		return 0
	case "unstarted": // Linear's "Todo"
		return 1
	case "triage":
		return 2
	case "backlog":
		return 3
	}
	return 4
}

// AssignedIssues returns your open assigned issues, most actionable first.
//
// Sorted client-side rather than by the API because the useful order is by
// state category, and Linear can only order by one field. Within a category the
// server's updatedAt order is preserved, so the top of each group is what you
// touched last.
func AssignedIssues(ctx context.Context, limit int) ([]Ticket, error) {
	if limit <= 0 {
		limit = 100
	}
	var out struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []issueLite `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}
	if err := execute(ctx, metaTimeout, assignedIssuesQuery, map[string]any{"first": limit}, &out); err != nil {
		return nil, err
	}

	// Sort the raw nodes, not the projection: position is what separates two
	// states of the same type, and it is the difference between "In Progress"
	// and "In Review" leading the list. A work queue wants the one you are
	// actually in the middle of.
	nodes := out.Viewer.AssignedIssues.Nodes
	sort.SliceStable(nodes, func(a, b int) bool {
		ra, rb := stateTypeRank(nodes[a].stateType()), stateTypeRank(nodes[b].stateType())
		if ra != rb {
			return ra < rb
		}
		return nodes[a].statePosition() < nodes[b].statePosition()
	})

	tickets := make([]Ticket, 0, len(nodes))
	for i := range nodes {
		if nodes[i].Identifier == "" {
			continue
		}
		tickets = append(tickets, nodes[i].ticket())
	}
	return tickets, nil
}

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

// Workspace is the connected organization, for display and for telling the user
// which team keys they can put in .fleet.json.
type Workspace struct {
	Name     string
	URLKey   string
	TeamKeys []string
}

var wsCache struct {
	mu     sync.RWMutex
	ws     Workspace
	loaded bool
}

func resetWorkspaceCache() {
	wsCache.mu.Lock()
	wsCache.ws, wsCache.loaded = Workspace{}, false
	wsCache.mu.Unlock()
}

// WorkspaceInfo returns the cached workspace, and whether it has been read yet.
// Free and non-blocking — safe from the Update goroutine.
func WorkspaceInfo() (Workspace, bool) {
	wsCache.mu.RLock()
	defer wsCache.mu.RUnlock()
	return wsCache.ws, wsCache.loaded
}

// FetchWorkspace reads the organization and its team keys, caching the result.
func FetchWorkspace(ctx context.Context) (Workspace, error) {
	return fetchWorkspaceWith(ctx, Credential{}, true)
}

// VerifyCredential proves a credential works before fleet stores it, and
// returns what it is attached to.
//
// Verifying first is what lets the Connect dialog say "connected" as a fact
// rather than a hope, and it means a typo is caught while the user is still
// looking at the field they typed it into.
func VerifyCredential(ctx context.Context, cred Credential) (Workspace, error) {
	return fetchWorkspaceWith(ctx, cred, false)
}

func fetchWorkspaceWith(ctx context.Context, cred Credential, useStored bool) (Workspace, error) {
	var out struct {
		Organization *struct {
			Name   string `json:"name"`
			URLKey string `json:"urlKey"`
		} `json:"organization"`
		Teams struct {
			Nodes []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	var err error
	if useStored {
		err = execute(ctx, metaTimeout, workspaceQuery, nil, &out)
	} else {
		err = executeWith(ctx, cred, metaTimeout, workspaceQuery, nil, &out)
	}
	if err != nil {
		return Workspace{}, err
	}

	ws := Workspace{}
	if out.Organization != nil {
		ws.Name, ws.URLKey = out.Organization.Name, out.Organization.URLKey
	}
	for _, t := range out.Teams.Nodes {
		if t.Key != "" {
			ws.TeamKeys = append(ws.TeamKeys, strings.ToUpper(t.Key))
		}
	}
	sort.Strings(ws.TeamKeys)

	wsCache.mu.Lock()
	wsCache.ws, wsCache.loaded = ws, true
	wsCache.mu.Unlock()
	return ws, nil
}

// SetWorkspaceForTest installs a workspace reading without a network call, so
// UI tests can exercise the wrong-workspace path. An empty Workspace clears it.
func SetWorkspaceForTest(ws Workspace) {
	wsCache.mu.Lock()
	defer wsCache.mu.Unlock()
	wsCache.ws = ws
	wsCache.loaded = ws.Name != "" || len(ws.TeamKeys) > 0
}
