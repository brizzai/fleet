package daemonsrv

import (
	"context"
	"sync"
	"time"

	fleetv1 "github.com/brizzai/fleet/gen/proto/fleet/v1"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/service"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tombstoneTTL is how long a soft-deleted session row sits in the daemon's
// tombstone buffer before its tmux pane is killed and the row is forgotten.
// 30s comfortably covers the TUI's 5s undo banner plus latency for the user
// to reach for `z`.
const tombstoneTTL = 30 * time.Second

// Server implements fleetv1.FleetServer by routing each RPC into the in-process
// *service.SessionService. Embedding UnimplementedFleetServer makes every RPC
// we don't override return codes.Unimplemented automatically.
type Server struct {
	fleetv1.UnimplementedFleetServer
	svc service.Service

	tombstoneMu sync.Mutex
	tombstones  map[string]*tombstone
}

// tombstone holds a soft-deleted session pending undo. The timer kills the
// stranded tmux pane if undo doesn't fire within tombstoneTTL.
type tombstone struct {
	row   *session.SessionRow
	timer *time.Timer
}

func NewServer(svc service.Service) *Server {
	return &Server{
		svc:        svc,
		tombstones: map[string]*tombstone{},
	}
}

// ── Sessions ───────────────────────────────────────────────────────────────

func (s *Server) GetSession(_ context.Context, req *fleetv1.GetSessionRequest) (*fleetv1.Session, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	sess := s.svc.GetSession(req.GetId())
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetId())
	}
	return convertSession(sess), nil
}

func (s *Server) CreateSession(_ context.Context, req *fleetv1.CreateSessionRequest) (*fleetv1.Session, error) {
	if req.GetProjectPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "project_path is required")
	}

	if req.GetForkFromId() != "" {
		// Service.ForkSession takes the *parent's claude session id*, not the
		// fleet session id. We treat fork_from_id as the parent's claude
		// session id verbatim per proto field documentation.
		s2, err := s.svc.ForkSession(req.GetTitle(), req.GetProjectPath(), req.GetWorkspaceName(), req.GetForkFromId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "fork session: %v", err)
		}
		return convertSession(s2), nil
	}

	s2, err := s.svc.CreateSession(req.GetTitle(), req.GetProjectPath(), req.GetWorkspaceName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	return convertSession(s2), nil
}

func (s *Server) DeleteSession(_ context.Context, req *fleetv1.DeleteSessionRequest) (*emptypb.Empty, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	// PR 4 ignored DeleteOption (workspace/repo cleanup) and defer_tmux_kill;
	// undo-delete now lives behind SoftDeleteSession/RestoreSession instead of
	// being expressed via the boolean field. Hard delete kills the tmux pane
	// immediately.
	s.svc.DeleteSession(req.GetId())
	return &emptypb.Empty{}, nil
}

func (s *Server) SoftDeleteSession(_ context.Context, req *fleetv1.SoftDeleteSessionRequest) (*emptypb.Empty, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := s.svc.SoftDelete(req.GetId())
	if err != nil {
		// Service returns "not found" for unknown ids; map to NotFound so the
		// client can distinguish from transient daemon failures.
		return nil, status.Errorf(codes.NotFound, "soft delete: %v", err)
	}

	s.tombstoneMu.Lock()
	if existing, ok := s.tombstones[req.GetId()]; ok {
		// Race-safety: a prior tombstone is being evicted by AfterFunc right
		// now. Stop the old timer so it doesn't kill the pane we're about to
		// re-stash. Stop returns false if the timer already fired; in that
		// case the tmux pane is already dead and SoftDelete above couldn't
		// have actually found the session — so this branch is mostly defensive.
		existing.timer.Stop()
	}
	id := req.GetId()
	t := &tombstone{row: row}
	t.timer = time.AfterFunc(tombstoneTTL, func() { s.evictTombstone(id) })
	s.tombstones[id] = t
	s.tombstoneMu.Unlock()

	return &emptypb.Empty{}, nil
}

func (s *Server) RestoreSession(_ context.Context, req *fleetv1.RestoreSessionRequest) (*fleetv1.Session, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	s.tombstoneMu.Lock()
	t, ok := s.tombstones[req.GetId()]
	if ok {
		t.timer.Stop()
		delete(s.tombstones, req.GetId())
	}
	s.tombstoneMu.Unlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "no recently deleted session %q", req.GetId())
	}

	if err := s.svc.RestoreDeleted(t.row); err != nil {
		return nil, status.Errorf(codes.Internal, "restore: %v", err)
	}
	sess := s.svc.GetSession(req.GetId())
	if sess == nil {
		return nil, status.Errorf(codes.Internal, "restored session %q vanished", req.GetId())
	}
	return convertSession(sess), nil
}

// evictTombstone runs from the AfterFunc timer when the undo window expires.
// It kills the still-alive tmux pane and forgets the row. Safe to call even
// if the entry has already been popped by RestoreSession (the map check
// short-circuits).
func (s *Server) evictTombstone(id string) {
	s.tombstoneMu.Lock()
	t, ok := s.tombstones[id]
	if ok {
		delete(s.tombstones, id)
	}
	s.tombstoneMu.Unlock()
	if !ok {
		return
	}
	ts := tmux.ReconnectSession(t.row.TmuxSession, t.row.Title, t.row.ProjectPath)
	if ts.Exists() {
		if err := ts.Kill(); err != nil {
			debuglog.Logger.Warn("tombstone evict: tmux kill failed", "id", id, "err", err)
		}
	}
}

func (s *Server) RestartSession(_ context.Context, req *fleetv1.RestartSessionRequest) (*fleetv1.Session, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.svc.RestartSession(req.GetId()); err != nil {
		return nil, status.Errorf(codes.Internal, "restart: %v", err)
	}
	sess := s.svc.GetSession(req.GetId())
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found post-restart", req.GetId())
	}
	return convertSession(sess), nil
}

func (s *Server) RenameSession(_ context.Context, req *fleetv1.RenameSessionRequest) (*fleetv1.Session, error) {
	if req.GetId() == "" || req.GetTitle() == "" {
		return nil, status.Error(codes.InvalidArgument, "id and title are required")
	}
	s.svc.RenameSession(req.GetId(), req.GetTitle())
	sess := s.svc.GetSession(req.GetId())
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetId())
	}
	return convertSession(sess), nil
}

func (s *Server) AcknowledgeSession(_ context.Context, req *fleetv1.AcknowledgeSessionRequest) (*emptypb.Empty, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	s.svc.AcknowledgeSession(req.GetId())
	return &emptypb.Empty{}, nil
}

// SendKeys forwards the requested keystrokes to the session's tmux pane.
// Used by the TUI's quick-approve `Y` and any future steering commands. The
// daemon does not interpret the keys — it relies on tmux send-keys' own key
// vocabulary (literal characters, named keys like "Enter", "C-q"…).
func (s *Server) SendKeys(_ context.Context, req *fleetv1.SendKeysRequest) (*emptypb.Empty, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess := s.svc.GetSession(req.GetSessionId())
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionId())
	}
	ts := sess.GetTmuxSession()
	if ts == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q has no tmux pane", req.GetSessionId())
	}

	keys := req.GetKeys()
	if req.GetSubmit() {
		keys = append(keys, "Enter")
	}
	if len(keys) == 0 {
		return &emptypb.Empty{}, nil
	}
	if err := ts.SendKeys(keys...); err != nil {
		return nil, status.Errorf(codes.Internal, "send-keys: %v", err)
	}
	// Nudge the status worker so the row reflects the post-send state
	// without waiting up to a full tick. Hooks usually arrive within a few
	// hundred ms of the keys landing in the pane.
	s.svc.TriggerRefresh()
	return &emptypb.Empty{}, nil
}

// CapturePane returns the current pane content. The TUI's preview pane
// polls this every ~500ms while a session is selected, so it is a hot RPC
// — no logging on the success path.
func (s *Server) CapturePane(_ context.Context, req *fleetv1.CapturePaneRequest) (*fleetv1.CapturePaneResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess := s.svc.GetSession(req.GetSessionId())
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionId())
	}
	ts := sess.GetTmuxSession()
	if ts == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q has no tmux pane", req.GetSessionId())
	}
	content, err := ts.CapturePane()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "capture-pane: %v", err)
	}
	if req.GetStripAnsi() {
		content = session.StripANSI(content)
	}
	// req.Lines is intentionally ignored for now: tmux capture-pane returns
	// the full visible pane (which is the only mode CapturePane() supports);
	// trimming to N lines client-side is fine for current callers. If a
	// future caller needs strict line counts we can add a CapturePaneN
	// variant on tmux.Session.
	return &fleetv1.CapturePaneResponse{
		Content:    content,
		CapturedAt: timestamppb.Now(),
	}, nil
}

// ── Repos ──────────────────────────────────────────────────────────────────

func (s *Server) PinRepo(_ context.Context, req *fleetv1.PinRepoRequest) (*emptypb.Empty, error) {
	if req.GetRoot() == "" {
		return nil, status.Error(codes.InvalidArgument, "root is required")
	}
	if err := s.svc.PinRepo(req.GetRoot()); err != nil {
		return nil, status.Errorf(codes.Internal, "pin: %v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) UnpinRepo(_ context.Context, req *fleetv1.UnpinRepoRequest) (*emptypb.Empty, error) {
	if req.GetRoot() == "" {
		return nil, status.Error(codes.InvalidArgument, "root is required")
	}
	if err := s.svc.UnpinRepo(req.GetRoot()); err != nil {
		return nil, status.Errorf(codes.Internal, "unpin: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ── Workspaces ─────────────────────────────────────────────────────────────

func (s *Server) ListWorkspaces(_ context.Context, req *fleetv1.ListWorkspacesRequest) (*fleetv1.ListWorkspacesResponse, error) {
	debuglog.Logger.Info("rpc: ListWorkspaces", "repoRoot", req.GetRepoRoot())
	if req.GetRepoRoot() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_root is required")
	}
	infos, providerName, err := s.svc.ListWorkspaces(req.GetRepoRoot())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list workspaces: %v", err)
	}
	out := &fleetv1.ListWorkspacesResponse{
		Workspaces:   make([]*fleetv1.Workspace, 0, len(infos)),
		ProviderName: providerName,
	}
	for _, w := range infos {
		out.Workspaces = append(out.Workspaces, convertWorkspace(w))
	}
	debuglog.Logger.Info("rpc: ListWorkspaces ok", "repoRoot", req.GetRepoRoot(), "count", len(out.Workspaces))
	return out, nil
}

// CreateWorkspace creates the workspace and a session pointing at it in one
// shot — see SessionService.CreateWorkspace for why it's combined. The
// proto's optional `pending_id` is left empty: the daemon does the work
// synchronously, and the new session reaches the client via the ListSessions
// stream rather than a follow-up notification keyed on pending_id.
func (s *Server) CreateWorkspace(_ context.Context, req *fleetv1.CreateWorkspaceRequest) (*fleetv1.CreateWorkspaceResponse, error) {
	debuglog.Logger.Info("rpc: CreateWorkspace", "repoRoot", req.GetRepoRoot(), "name", req.GetName(), "baseBranch", req.GetBaseBranch(), "newBranch", req.GetNewBranch())
	if req.GetRepoRoot() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_root is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	info, sess, err := s.svc.CreateWorkspace(req.GetRepoRoot(), req.GetName(), req.GetBaseBranch(), req.GetNewBranch())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create workspace: %v", err)
	}
	if info == nil {
		return nil, status.Error(codes.Internal, "create workspace: provider returned nil info")
	}
	if sess != nil {
		debuglog.Logger.Info("rpc: CreateWorkspace ok", "name", info.Name, "path", info.Path, "sessionID", sess.ID)
	} else {
		debuglog.Logger.Info("rpc: CreateWorkspace ok (no session)", "name", info.Name, "path", info.Path)
	}
	return &fleetv1.CreateWorkspaceResponse{Workspace: convertWorkspace(*info)}, nil
}

func (s *Server) DestroyWorkspace(_ context.Context, req *fleetv1.DestroyWorkspaceRequest) (*emptypb.Empty, error) {
	debuglog.Logger.Info("rpc: DestroyWorkspace", "repoRoot", req.GetRepoRoot(), "name", req.GetName())
	if req.GetRepoRoot() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_root is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := s.svc.DestroyWorkspace(req.GetRepoRoot(), req.GetName()); err != nil {
		return nil, status.Errorf(codes.Internal, "destroy workspace: %v", err)
	}
	debuglog.Logger.Info("rpc: DestroyWorkspace ok", "repoRoot", req.GetRepoRoot(), "name", req.GetName())
	return &emptypb.Empty{}, nil
}

// ── Config ─────────────────────────────────────────────────────────────────

func (s *Server) GetConfig(_ context.Context, _ *emptypb.Empty) (*fleetv1.Config, error) {
	debuglog.Logger.Info("rpc: GetConfig")
	cfg, err := s.svc.GetConfig()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get config: %v", err)
	}
	return convertConfig(cfg), nil
}

func (s *Server) UpdateConfig(_ context.Context, req *fleetv1.UpdateConfigRequest) (*fleetv1.Config, error) {
	debuglog.Logger.Info("rpc: UpdateConfig", "theme", req.GetConfig().GetTheme(), "editor", req.GetConfig().GetEditor())
	cfg, err := s.svc.UpdateConfig(req.GetConfig())
	if err != nil {
		// "unknown theme" comes back as a plain error from the service. Map it
		// to InvalidArgument so the client can render a precise toast instead
		// of "internal".
		if isValidationErr(err) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "update config: %v", err)
	}
	debuglog.Logger.Info("rpc: UpdateConfig ok", "theme", cfg.Theme)
	return convertConfig(cfg), nil
}

// isValidationErr returns true for known user-input validation failures from
// the service layer (currently only the unknown-theme check). Lives here so
// the gRPC layer owns the error code mapping.
func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= len("unknown theme") && msg[:len("unknown theme")] == "unknown theme"
}

// ── Slot bindings ──────────────────────────────────────────────────────────

func (s *Server) ListSlotBindings(_ context.Context, _ *emptypb.Empty) (*fleetv1.ListSlotBindingsResponse, error) {
	return &fleetv1.ListSlotBindingsResponse{Bindings: convertSlotBindings(s.svc.SlotBindings())}, nil
}

func (s *Server) BindSlot(_ context.Context, req *fleetv1.BindSlotRequest) (*emptypb.Empty, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetSlot() < 0 || req.GetSlot() > 9 {
		return nil, status.Errorf(codes.InvalidArgument, "slot must be 0-9, got %d", req.GetSlot())
	}
	if err := s.svc.BindSlot(int(req.GetSlot()), req.GetSessionId()); err != nil {
		return nil, status.Errorf(codes.Internal, "bind: %v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) UnbindSlot(_ context.Context, req *fleetv1.UnbindSlotRequest) (*emptypb.Empty, error) {
	if req.GetSlot() < 0 || req.GetSlot() > 9 {
		return nil, status.Errorf(codes.InvalidArgument, "slot must be 0-9, got %d", req.GetSlot())
	}
	if err := s.svc.UnbindSlot(int(req.GetSlot())); err != nil {
		return nil, status.Errorf(codes.Internal, "unbind: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// Compile-time check that *Server satisfies FleetServer.
var _ fleetv1.FleetServer = (*Server)(nil)
