package daemonclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	fleetv1 "github.com/brizzai/fleet/gen/proto/fleet/v1"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/service"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Client is a service.Service implementation that proxies to a fleet daemon
// over gRPC. It maintains a local cache of session/repo/slot state populated
// by the daemon's streaming RPCs (ListSessions, ListRepos, ListSlotBindings),
// and forwards every mutation as a unary RPC. Reads come from the cache so
// the TUI's hot path (sidebar render, preview-fetch tick) doesn't pay an
// RPC round-trip per call.
//
// Concurrency: cache state lives behind `mu` (write-locked from stream
// goroutines, read-locked from the UI goroutine). Observer fan-out lives
// behind `observerMu`; observer callbacks fire outside `mu` to keep the
// surface non-reentrant.
type Client struct {
	conn   *grpc.ClientConn
	api    fleetv1.FleetClient
	dialer Dialer

	mu          sync.RWMutex
	sessions    map[string]*session.Session
	sessionList []*session.Session
	repos       map[string]*git.RepoInfo
	pinned      map[string]bool
	slotBind    map[int]string

	// ghAvailable is probed once at Start (the daemon and TUI share $PATH so
	// the local probe matches what the daemon would report).
	ghAvailable bool

	observerMu sync.RWMutex
	observers  []service.Observer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Dialer abstracts the underlying gRPC dial so tests can substitute bufconn.
// In production this is dialUnix (defined below).
type Dialer func(ctx context.Context) (*grpc.ClientConn, error)

// dialUnix returns a Dialer that opens a grpc.ClientConn against a Unix
// domain socket at sockPath. Used by production callers; tests inject their
// own Dialer over bufconn.
func dialUnix(sockPath string) Dialer {
	return func(ctx context.Context) (*grpc.ClientConn, error) {
		// passthrough scheme + Unix dialer keeps gRPC's name resolver out
		// of the socket-path business.
		return grpc.NewClient(
			"unix:"+sockPath,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
}

// New builds a Client that dials the daemon's Unix socket at sockPath. The
// returned Client is not yet connected — call Start to open the connection
// and begin streaming.
func New(sockPath string) *Client {
	return newWithDialer(dialUnix(sockPath))
}

// newWithDialer is the test-friendly constructor. Production code uses New.
func newWithDialer(d Dialer) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		dialer:   d,
		sessions: map[string]*session.Session{},
		repos:    map[string]*git.RepoInfo{},
		pinned:   map[string]bool{},
		slotBind: map[int]string{},
		ctx:      ctx,
		cancel:   cancel,
	}
}

// ── Lifecycle ──────────────────────────────────────────────────────────────

// LoadFromStorage is a no-op for the daemon client. The daemon hydrated its
// own state from SQLite on its Start; the client gets that state through
// the initial snapshot of each streaming RPC.
func (c *Client) LoadFromStorage() error { return nil }

// Start dials the daemon, performs an initial blocking snapshot pull from
// each list RPC so the cache is fully populated before TUI rendering begins,
// then spawns background goroutines that consume the streams and apply
// deltas. Returns a non-empty `warning` string if the daemon connects but
// the local environment is missing optional pieces (e.g. gh CLI not on
// PATH); returns a non-nil error if the daemon is unreachable.
func (c *Client) Start() (string, error) {
	conn, err := c.dialer(c.ctx)
	if err != nil {
		return "", fmt.Errorf("dial daemon: %w", err)
	}
	c.conn = conn
	c.api = fleetv1.NewFleetClient(conn)

	// Probe gh locally — daemon and TUI share $PATH so the answer matches.
	c.mu.Lock()
	c.ghAvailable = github.IsGHAvailable()
	c.mu.Unlock()

	// Pull slot bindings synchronously: it's a unary call and the TUI uses
	// it during initial render. Failure here is fatal — if the unary call
	// fails the daemon almost certainly can't serve the streams either.
	resp, err := c.api.ListSlotBindings(c.ctx, &emptypb.Empty{})
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("initial ListSlotBindings: %w", err)
	}
	c.mu.Lock()
	for _, b := range resp.GetBindings() {
		c.slotBind[int(b.GetSlot())] = b.GetSessionId()
	}
	c.mu.Unlock()

	// Streams kick off in background. Each goroutine handles its own
	// reconnect loop; if the daemon dies the streams emit EventError to
	// observers and back off.
	c.wg.Add(2)
	go c.runSessionStream()
	go c.runRepoStream()

	var warning string
	if !c.ghAvailable {
		warning = "gh CLI not found — PR info will be unavailable"
	}
	return warning, nil
}

// Stop tears down the streams and closes the gRPC connection.
func (c *Client) Stop() {
	c.cancel()
	c.wg.Wait()
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// Subscribe / Unsubscribe / notify mirror SessionService's observer plumbing.
// notify snapshots the observer slice under RLock so callbacks don't run
// while another goroutine is mutating it.
func (c *Client) Subscribe(o service.Observer) {
	c.observerMu.Lock()
	c.observers = append(c.observers, o)
	c.observerMu.Unlock()
}

func (c *Client) Unsubscribe(o service.Observer) {
	c.observerMu.Lock()
	for i, ob := range c.observers {
		if ob == o {
			c.observers = append(c.observers[:i], c.observers[i+1:]...)
			break
		}
	}
	c.observerMu.Unlock()
}

func (c *Client) notify(evt service.Event) {
	c.observerMu.RLock()
	obs := make([]service.Observer, len(c.observers))
	copy(obs, c.observers)
	c.observerMu.RUnlock()
	for _, o := range obs {
		o.OnEvent(evt)
	}
}

// TriggerRefresh / EnqueuePriority are no-ops on the client — the daemon
// owns the worker cycle. They stay on the interface for in-process
// symmetry; debug-log the call so we can spot accidental hot-path use.
func (c *Client) TriggerRefresh()             { debuglog.Logger.Debug("daemonclient: TriggerRefresh ignored (server-driven)") }
func (c *Client) EnqueuePriority(id string)   { debuglog.Logger.Debug("daemonclient: EnqueuePriority ignored", "id", id) }
func (c *Client) OnFirstPrompt(_, _ string)   {}
func (c *Client) OnPromptCount(_ string, _ int) {}

// ── Queries (cache reads) ──────────────────────────────────────────────────

func (c *Client) Sessions() []*session.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*session.Session, len(c.sessionList))
	copy(out, c.sessionList)
	return out
}

func (c *Client) GetSession(id string) *session.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[id]
}

func (c *Client) GitInfo(repo string) *git.RepoInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.repos[repo]
}

func (c *Client) GitInfoAll() map[string]*git.RepoInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*git.RepoInfo, len(c.repos))
	for k, v := range c.repos {
		out[k] = v
	}
	return out
}

func (c *Client) IsGHAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ghAvailable
}

func (c *Client) PinnedRepos() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.pinned))
	for p := range c.pinned {
		out = append(out, p)
	}
	return out
}

func (c *Client) SlotBindings() map[int]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[int]string, len(c.slotBind))
	for k, v := range c.slotBind {
		out[k] = v
	}
	return out
}

// CapturePreview shells to local tmux exactly the way the in-process
// service does. The TUI's preview tick (~500ms) calls this on the hot
// path; routing through gRPC would add latency without benefit because
// the tmux server is shared with the daemon. The CapturePane RPC remains
// available for clients that *don't* have local tmux access.
func (c *Client) CapturePreview(id string) (string, error) {
	sess := c.GetSession(id)
	if sess == nil {
		return "", fmt.Errorf("session %s not found", id)
	}
	ts := sess.GetTmuxSession()
	if ts == nil {
		return "", fmt.Errorf("session %s has no tmux pane", id)
	}
	return ts.CapturePane()
}

// ── Mutations ──────────────────────────────────────────────────────────────

// rpcTimeout caps the wait on any unary RPC. Daemons run locally over a
// Unix socket so even slow operations (CreateSession spawns tmux + claude)
// finish well inside this budget. Without a cap a wedged daemon would
// freeze the UI goroutine.
const rpcTimeout = 10 * time.Second

func (c *Client) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.ctx, rpcTimeout)
}

func (c *Client) CreateSession(title, projectPath, workspaceName string) (*session.Session, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	resp, err := c.api.CreateSession(ctx, &fleetv1.CreateSessionRequest{
		Title:         title,
		ProjectPath:   projectPath,
		WorkspaceName: workspaceName,
	})
	if err != nil {
		return nil, err
	}
	sess := protoToSession(resp)
	c.applySessionImmediate(sess)
	return sess, nil
}

func (c *Client) ForkSession(title, projectPath, workspaceName, parentClaudeSessionID string) (*session.Session, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	resp, err := c.api.CreateSession(ctx, &fleetv1.CreateSessionRequest{
		Title:         title,
		ProjectPath:   projectPath,
		WorkspaceName: workspaceName,
		ForkFromId:    parentClaudeSessionID,
	})
	if err != nil {
		return nil, err
	}
	sess := protoToSession(resp)
	c.applySessionImmediate(sess)
	return sess, nil
}

func (c *Client) DeleteSession(id string) {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.DeleteSession(ctx, &fleetv1.DeleteSessionRequest{Id: id}); err != nil {
		debuglog.Logger.Warn("daemonclient: DeleteSession", "id", id, "err", err)
		c.notify(service.Event{Type: service.EventError, Message: err.Error()})
		return
	}
	c.removeSessionImmediate(id)
}

func (c *Client) RestartSession(id string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	resp, err := c.api.RestartSession(ctx, &fleetv1.RestartSessionRequest{Id: id})
	if err != nil {
		return err
	}
	c.applySessionImmediate(protoToSession(resp))
	return nil
}

func (c *Client) RenameSession(id, newTitle string) {
	ctx, cancel := c.callCtx()
	defer cancel()
	resp, err := c.api.RenameSession(ctx, &fleetv1.RenameSessionRequest{Id: id, Title: newTitle})
	if err != nil {
		debuglog.Logger.Warn("daemonclient: RenameSession", "id", id, "err", err)
		c.notify(service.Event{Type: service.EventError, Message: err.Error()})
		return
	}
	c.applySessionImmediate(protoToSession(resp))
}

func (c *Client) AcknowledgeSession(id string) {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.AcknowledgeSession(ctx, &fleetv1.AcknowledgeSessionRequest{Id: id}); err != nil {
		debuglog.Logger.Warn("daemonclient: AcknowledgeSession", "id", id, "err", err)
	}
	// Optimistic local update so the UI clears the "needs attention" badge
	// without waiting for the next stream tick.
	c.mu.Lock()
	if sess := c.sessions[id]; sess != nil {
		sess.Acknowledged = true
	}
	c.mu.Unlock()
}

func (c *Client) BindSlot(slot int, sessionID string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.BindSlot(ctx, &fleetv1.BindSlotRequest{Slot: int32(slot), SessionId: sessionID}); err != nil {
		return err
	}
	c.mu.Lock()
	c.slotBind[slot] = sessionID
	c.mu.Unlock()
	c.notify(service.Event{Type: service.EventSessionsChanged})
	return nil
}

func (c *Client) UnbindSlot(slot int) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.UnbindSlot(ctx, &fleetv1.UnbindSlotRequest{Slot: int32(slot)}); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.slotBind, slot)
	c.mu.Unlock()
	c.notify(service.Event{Type: service.EventSessionsChanged})
	return nil
}

func (c *Client) PinRepo(path string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.PinRepo(ctx, &fleetv1.PinRepoRequest{Root: path}); err != nil {
		return err
	}
	c.mu.Lock()
	c.pinned[path] = true
	c.mu.Unlock()
	c.notify(service.Event{Type: service.EventSessionsChanged})
	return nil
}

func (c *Client) UnpinRepo(path string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.UnpinRepo(ctx, &fleetv1.UnpinRepoRequest{Root: path}); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.pinned, path)
	c.mu.Unlock()
	c.notify(service.Event{Type: service.EventSessionsChanged})
	return nil
}

// ── Workspaces ─────────────────────────────────────────────────────────────

// ListWorkspaces forwards to the daemon. The provider name is passed through
// untouched. Returns an empty slice (not nil) on success with no workspaces.
func (c *Client) ListWorkspaces(repoRoot string) ([]workspace.WorkspaceInfo, string, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	debuglog.Logger.Info("daemonclient: ListWorkspaces", "repoRoot", repoRoot)
	resp, err := c.api.ListWorkspaces(ctx, &fleetv1.ListWorkspacesRequest{RepoRoot: repoRoot})
	if err != nil {
		debuglog.Logger.Error("daemonclient: ListWorkspaces failed", "repoRoot", repoRoot, "err", err)
		return nil, "", err
	}
	out := make([]workspace.WorkspaceInfo, 0, len(resp.GetWorkspaces()))
	for _, w := range resp.GetWorkspaces() {
		out = append(out, workspace.WorkspaceInfo{
			Name:   w.GetName(),
			Path:   w.GetPath(),
			Branch: w.GetBranch(),
			Status: w.GetStatus(),
		})
	}
	debuglog.Logger.Info("daemonclient: ListWorkspaces ok", "repoRoot", repoRoot, "count", len(out), "provider", resp.GetProviderName())
	return out, resp.GetProviderName(), nil
}

// CreateWorkspace forwards to the daemon. The daemon-side handler creates
// both the workspace and a session pointing at it (see SessionService for the
// rationale). The session arrives via the ListSessions stream — we don't
// optimistically insert it here because we don't have its ID yet (the proto
// returns only the Workspace).
func (c *Client) CreateWorkspace(repoRoot, name, baseBranch, newBranch string) (*workspace.WorkspaceInfo, *session.Session, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	debuglog.Logger.Info("daemonclient: CreateWorkspace", "repoRoot", repoRoot, "name", name, "baseBranch", baseBranch, "newBranch", newBranch)
	resp, err := c.api.CreateWorkspace(ctx, &fleetv1.CreateWorkspaceRequest{
		RepoRoot:   repoRoot,
		Name:       name,
		BaseBranch: baseBranch,
		NewBranch:  newBranch,
	})
	if err != nil {
		debuglog.Logger.Error("daemonclient: CreateWorkspace failed", "repoRoot", repoRoot, "name", name, "err", err)
		return nil, nil, err
	}
	w := resp.GetWorkspace()
	if w == nil {
		debuglog.Logger.Warn("daemonclient: CreateWorkspace returned nil workspace", "repoRoot", repoRoot, "name", name)
		return nil, nil, nil
	}
	debuglog.Logger.Info("daemonclient: CreateWorkspace ok", "name", w.GetName(), "path", w.GetPath(), "branch", w.GetBranch())
	return &workspace.WorkspaceInfo{
		Name:   w.GetName(),
		Path:   w.GetPath(),
		Branch: w.GetBranch(),
		Status: w.GetStatus(),
	}, nil, nil
}

// ── Config ─────────────────────────────────────────────────────────────────

func (c *Client) GetConfig() (*config.Config, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	debuglog.Logger.Info("daemonclient: GetConfig")
	resp, err := c.api.GetConfig(ctx, &emptypb.Empty{})
	if err != nil {
		debuglog.Logger.Error("daemonclient: GetConfig failed", "err", err)
		return nil, err
	}
	return protoConfigToConfig(resp), nil
}

func (c *Client) UpdateConfig(updates *fleetv1.Config) (*config.Config, error) {
	ctx, cancel := c.callCtx()
	defer cancel()
	debuglog.Logger.Info("daemonclient: UpdateConfig", "theme", updates.GetTheme(), "editor", updates.GetEditor())
	resp, err := c.api.UpdateConfig(ctx, &fleetv1.UpdateConfigRequest{Config: updates})
	if err != nil {
		debuglog.Logger.Error("daemonclient: UpdateConfig failed", "err", err)
		return nil, err
	}
	debuglog.Logger.Info("daemonclient: UpdateConfig ok", "theme", resp.GetTheme())
	return protoConfigToConfig(resp), nil
}

// protoConfigToConfig is the inverse of daemonsrv.convertConfig — used by
// in-process callers that hold a daemonclient.Client. We carry the bool
// fields as plain *bool so the receiver can pass the result back to TUI
// helpers like IsAutoNameEnabled / IsCopyClaudeSettingsEnabled.
func protoConfigToConfig(p *fleetv1.Config) *config.Config {
	if p == nil {
		return &config.Config{}
	}
	an := p.GetAutoNameSessions()
	cc := p.GetCopyClaudeSettings()
	return &config.Config{
		TickIntervalSec:    int(p.GetTickIntervalSec()),
		DefaultProjectPath: p.GetDefaultProjectPath(),
		Editor:             p.GetEditor(),
		Theme:              p.GetTheme(),
		AutoNameSessions:   &an,
		CopyClaudeSettings: &cc,
	}
}

func (c *Client) DestroyWorkspace(repoRoot, name string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	debuglog.Logger.Info("daemonclient: DestroyWorkspace", "repoRoot", repoRoot, "name", name)
	if _, err := c.api.DestroyWorkspace(ctx, &fleetv1.DestroyWorkspaceRequest{RepoRoot: repoRoot, Name: name}); err != nil {
		debuglog.Logger.Error("daemonclient: DestroyWorkspace failed", "repoRoot", repoRoot, "name", name, "err", err)
		return err
	}
	debuglog.Logger.Info("daemonclient: DestroyWorkspace ok", "repoRoot", repoRoot, "name", name)
	return nil
}

// ── Undo-delete ────────────────────────────────────────────────────────────

// SnapshotForUndo returns a row built from the cached session. The TUI uses
// only the row's `ID` and `Title` for its undo-stack display, so a thin
// projection is sufficient — the daemon holds the canonical row in its
// tombstone buffer.
func (c *Client) SnapshotForUndo(id string) (*session.SessionRow, error) {
	c.mu.RLock()
	sess := c.sessions[id]
	c.mu.RUnlock()
	if sess == nil {
		return nil, errors.New("session not found")
	}
	return &session.SessionRow{
		ID:          sess.ID,
		Title:       sess.Title,
		ProjectPath: sess.ProjectPath,
		Status:      string(sess.Status),
		TmuxSession: sess.TmuxSessionName,
	}, nil
}

// SoftDelete asks the daemon to soft-delete the session (deferring tmux
// kill until the undo window expires) and returns a stub row so the TUI's
// undo stack can carry the ID. Behaviour mirrors the in-process service:
// tmux pane stays alive, session is removed from the cache and the daemon's
// state.
func (c *Client) SoftDelete(id string) (*session.SessionRow, error) {
	row, err := c.SnapshotForUndo(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.callCtx()
	defer cancel()
	if _, err := c.api.SoftDeleteSession(ctx, &fleetv1.SoftDeleteSessionRequest{Id: id}); err != nil {
		return nil, err
	}
	c.removeSessionImmediate(id)
	return row, nil
}

// RestoreDeleted and SoftRestore both pop the daemon's tombstone via the
// RestoreSession RPC. The Service interface exposes two methods because the
// in-process implementation distinguishes "I have the live pointer"
// (SoftRestore) from "I only have the row" (RestoreDeleted) — the live
// pointer matters in-process to keep the same `*session.Session` instance
// after undo. Daemon mode has no such distinction: the daemon owns the
// canonical session and the client just re-mirrors whatever it returns.
//
// In practice the TUI's undo path calls SoftRestore (it holds the live
// pointer in its undo stack), so getting that one wrong silently breaks
// `z` — the daemon never knows you wanted the tombstone back, and 30s
// later the eviction timer kills the tmux pane.
func (c *Client) RestoreDeleted(row *session.SessionRow) error {
	if row == nil {
		return errors.New("nil row")
	}
	return c.restore(row.ID)
}

func (c *Client) SoftRestore(sess *session.Session, row *session.SessionRow) error {
	switch {
	case sess != nil:
		return c.restore(sess.ID)
	case row != nil:
		return c.restore(row.ID)
	default:
		return errors.New("nil session and nil row")
	}
}

// restore is the shared body for both RestoreDeleted and SoftRestore.
// Calls the daemon's RestoreSession RPC, applies the rehydrated session
// to the local cache immediately so the UI repaints without waiting for
// the next stream tick (which will deliver a benign duplicate ADDED).
func (c *Client) restore(id string) error {
	ctx, cancel := c.callCtx()
	defer cancel()
	resp, err := c.api.RestoreSession(ctx, &fleetv1.RestoreSessionRequest{Id: id})
	if err != nil {
		return err
	}
	c.applySessionImmediate(protoToSession(resp))
	return nil
}

// ── Cache helpers (called from RPC paths and stream paths) ─────────────────

// applySessionImmediate inserts/updates the session in the cache and fires
// EventSessionsChanged. Used by mutation RPCs to hide stream latency from
// the UI — the user who pressed `r` shouldn't wait for the round trip
// through ListSessions to see their renamed title.
func (c *Client) applySessionImmediate(sess *session.Session) {
	if sess == nil {
		return
	}
	c.mu.Lock()
	_, existed := c.sessions[sess.ID]
	c.sessions[sess.ID] = sess
	c.rebuildSessionList()
	c.mu.Unlock()
	if existed {
		c.notify(service.Event{Type: service.EventSessionStatusChanged})
	} else {
		c.notify(service.Event{Type: service.EventSessionsChanged})
	}
}

func (c *Client) removeSessionImmediate(id string) {
	c.mu.Lock()
	if _, existed := c.sessions[id]; !existed {
		c.mu.Unlock()
		return
	}
	delete(c.sessions, id)
	c.rebuildSessionList()
	for slot, sid := range c.slotBind {
		if sid == id {
			delete(c.slotBind, slot)
		}
	}
	c.mu.Unlock()
	c.notify(service.Event{Type: service.EventSessionsChanged})
}

// rebuildSessionList recomputes the ordered slice that Sessions() returns.
// Order tracks the daemon's insertion order via CreatedAt — the in-process
// service keeps a slice in insertion order too. Callers MUST hold mu.
func (c *Client) rebuildSessionList() {
	out := make([]*session.Session, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, s)
	}
	// Stable order by CreatedAt then ID for deterministic display. The
	// in-process Service preserves insertion order; using CreatedAt is a
	// near-equivalent that's resilient to map iteration nondeterminism.
	sortSessionsByCreatedAt(out)
	c.sessionList = out
}

// Compile-time check.
var _ service.Service = (*Client)(nil)
