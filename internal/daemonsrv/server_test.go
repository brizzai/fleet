package daemonsrv

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"sync"
	"testing"
	"time"

	fleetv1 "github.com/brizzai/fleet/gen/proto/fleet/v1"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/service"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeService satisfies service.Service for daemonsrv unit tests. It uses
// the public surface only — no SessionService internals. Tests drive state
// transitions via Add/Update/Remove, then call notify() to fan out to
// subscribers exactly the way SessionService does.
type fakeService struct {
	mu         sync.Mutex
	sessions   []*session.Session
	gitInfo    map[string]*git.RepoInfo
	pinned     map[string]bool
	slots      map[int]string
	observers  []service.Observer
	workspaces map[string][]workspace.WorkspaceInfo
	wsCreates  []wsCreateCall
	wsDestroys []wsDestroyCall
}

type wsCreateCall struct{ repoRoot, name, baseBranch, newBranch string }
type wsDestroyCall struct{ repoRoot, name string }

func newFakeService() *fakeService {
	return &fakeService{
		gitInfo: map[string]*git.RepoInfo{},
		pinned:  map[string]bool{},
		slots:   map[int]string{},
	}
}

func (f *fakeService) addSession(s *session.Session) {
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionsChanged})
}

func (f *fakeService) renameSession(id, title string) {
	f.mu.Lock()
	for _, s := range f.sessions {
		if s.ID == id {
			s.Title = title
		}
	}
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
}

func (f *fakeService) deleteSessionDirect(id string) {
	f.mu.Lock()
	out := f.sessions[:0]
	for _, s := range f.sessions {
		if s.ID != id {
			out = append(out, s)
		}
	}
	f.sessions = out
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionsChanged})
}

func (f *fakeService) notify(e service.Event) {
	f.mu.Lock()
	obs := append([]service.Observer(nil), f.observers...)
	f.mu.Unlock()
	for _, o := range obs {
		o.OnEvent(e)
	}
}

// service.Service implementation — public surface.

func (f *fakeService) LoadFromStorage() error   { return nil }
func (f *fakeService) Start() (string, error)   { return "", nil }
func (f *fakeService) Stop()                    {}
func (f *fakeService) TriggerRefresh()          {}
func (f *fakeService) EnqueuePriority(_ string) {}
func (f *fakeService) IsGHAvailable() bool      { return false }
func (f *fakeService) CapturePreview(_ string) (string, error) {
	return "", nil
}
func (f *fakeService) OnFirstPrompt(_, _ string)     {}
func (f *fakeService) OnPromptCount(_ string, _ int) {}

func (f *fakeService) Subscribe(o service.Observer) {
	f.mu.Lock()
	f.observers = append(f.observers, o)
	f.mu.Unlock()
}

func (f *fakeService) Unsubscribe(o service.Observer) {
	f.mu.Lock()
	out := f.observers[:0]
	for _, ob := range f.observers {
		if ob != o {
			out = append(out, ob)
		}
	}
	f.observers = out
	f.mu.Unlock()
}

func (f *fakeService) Sessions() []*session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*session.Session, len(f.sessions))
	copy(out, f.sessions)
	return out
}

func (f *fakeService) GetSession(id string) *session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (f *fakeService) GitInfo(repo string) *git.RepoInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gitInfo[repo]
}

func (f *fakeService) GitInfoAll() map[string]*git.RepoInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*git.RepoInfo, len(f.gitInfo))
	maps.Copy(out, f.gitInfo)
	return out
}

func (f *fakeService) PinnedRepos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.pinned))
	for p := range f.pinned {
		out = append(out, p)
	}
	return out
}

func (f *fakeService) SlotBindings() map[int]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]string, len(f.slots))
	maps.Copy(out, f.slots)
	return out
}

func (f *fakeService) CreateSession(title, projectPath, _ string) (*session.Session, error) {
	s := &session.Session{ID: "new-" + title, Title: title, ProjectPath: projectPath, Status: session.StatusStarting, CreatedAt: time.Now()}
	f.addSession(s)
	return s, nil
}

func (f *fakeService) ForkSession(title, projectPath, _, parentID string) (*session.Session, error) {
	s := &session.Session{ID: "fork-" + title, Title: title, ProjectPath: projectPath, ClaudeSessionID: parentID, ForkFromID: parentID, CreatedAt: time.Now()}
	f.addSession(s)
	return s, nil
}

func (f *fakeService) DeleteSession(id string) {
	f.deleteSessionDirect(id)
}

func (f *fakeService) RestartSession(id string) error {
	if f.GetSession(id) == nil {
		return errors.New("not found")
	}
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
	return nil
}

func (f *fakeService) RenameSession(id, newTitle string) {
	f.renameSession(id, newTitle)
}

func (f *fakeService) AcknowledgeSession(id string) {
	f.mu.Lock()
	for _, s := range f.sessions {
		if s.ID == id {
			s.Acknowledged = true
		}
	}
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
}

func (f *fakeService) BindSlot(slot int, sessionID string) error {
	if f.GetSession(sessionID) == nil {
		return errors.New("session not found")
	}
	f.mu.Lock()
	f.slots[slot] = sessionID
	f.mu.Unlock()
	return nil
}

func (f *fakeService) UnbindSlot(slot int) error {
	f.mu.Lock()
	delete(f.slots, slot)
	f.mu.Unlock()
	return nil
}

func (f *fakeService) PinRepo(path string) error {
	f.mu.Lock()
	f.pinned[path] = true
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventGitInfoChanged})
	return nil
}

func (f *fakeService) UnpinRepo(path string) error {
	f.mu.Lock()
	delete(f.pinned, path)
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventGitInfoChanged})
	return nil
}

func (f *fakeService) ListWorkspaces(repoRoot string) ([]workspace.WorkspaceInfo, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.workspaces == nil {
		return []workspace.WorkspaceInfo{}, "git-worktree", nil
	}
	out := append([]workspace.WorkspaceInfo(nil), f.workspaces[repoRoot]...)
	if out == nil {
		out = []workspace.WorkspaceInfo{}
	}
	return out, "git-worktree", nil
}

func (f *fakeService) CreateWorkspace(repoRoot, name, baseBranch, newBranch string) (*workspace.WorkspaceInfo, *session.Session, error) {
	f.mu.Lock()
	f.wsCreates = append(f.wsCreates, wsCreateCall{repoRoot, name, baseBranch, newBranch})
	info := workspace.WorkspaceInfo{Name: name, Path: repoRoot + "-" + name, Branch: newBranch}
	if f.workspaces == nil {
		f.workspaces = map[string][]workspace.WorkspaceInfo{}
	}
	f.workspaces[repoRoot] = append(f.workspaces[repoRoot], info)
	f.mu.Unlock()
	sess, err := f.CreateSession(name, info.Path, name)
	if err != nil {
		return &info, nil, err
	}
	return &info, sess, nil
}

func (f *fakeService) DestroyWorkspace(repoRoot, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsDestroys = append(f.wsDestroys, wsDestroyCall{repoRoot, name})
	if f.workspaces != nil {
		out := f.workspaces[repoRoot][:0]
		for _, w := range f.workspaces[repoRoot] {
			if w.Name != name {
				out = append(out, w)
			}
		}
		f.workspaces[repoRoot] = out
	}
	return nil
}

func (f *fakeService) SnapshotForUndo(id string) (*session.SessionRow, error) {
	if s := f.GetSession(id); s != nil {
		return &session.SessionRow{ID: s.ID, Title: s.Title, ProjectPath: s.ProjectPath, Status: string(s.Status)}, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeService) SoftDelete(id string) (*session.SessionRow, error) {
	row, err := f.SnapshotForUndo(id)
	if err != nil {
		return nil, err
	}
	f.deleteSessionDirect(id)
	return row, nil
}

func (f *fakeService) RestoreDeleted(row *session.SessionRow) error {
	if row == nil {
		return errors.New("nil row")
	}
	f.addSession(&session.Session{ID: row.ID, Title: row.Title, ProjectPath: row.ProjectPath, Status: session.Status(row.Status), CreatedAt: time.Now()})
	return nil
}

func (f *fakeService) SoftRestore(_ *session.Session, _ *session.SessionRow) error {
	return nil
}

// Compile-time check.
var _ service.Service = (*fakeService)(nil)

// ── Test harness ───────────────────────────────────────────────────────────

func startTestServer(t *testing.T, svc service.Service) (fleetv1.FleetClient, func()) {
	t.Helper()
	lis := bufconn.Listen(64 * 1024)
	srv := grpc.NewServer()
	fleetv1.RegisterFleetServer(srv, NewServer(svc))
	go func() { _ = srv.Serve(lis) }()

	dialer := func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	return fleetv1.NewFleetClient(conn), func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestServer_GetSession(t *testing.T) {
	fake := newFakeService()
	fake.addSession(&session.Session{ID: "abc", Title: "demo", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})
	client, stop := startTestServer(t, fake)
	defer stop()

	got, err := client.GetSession(context.Background(), &fleetv1.GetSessionRequest{Id: "abc"})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Id != "abc" || got.Title != "demo" {
		t.Errorf("session: want id=abc title=demo, got id=%q title=%q", got.Id, got.Title)
	}
	if got.Status != fleetv1.Status_STATUS_RUNNING {
		t.Errorf("status: want RUNNING, got %v", got.Status)
	}

	_, err = client.GetSession(context.Background(), &fleetv1.GetSessionRequest{Id: "nope"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("missing session: want NotFound, got %v", err)
	}
}

func TestServer_ListSessions_SnapshotThenAddRenameRemove(t *testing.T) {
	fake := newFakeService()
	fake.addSession(&session.Session{ID: "a", Title: "alpha", ProjectPath: "/tmp", Status: session.StatusIdle, CreatedAt: time.Now()})
	client, stop := startTestServer(t, fake)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.ListSessions(ctx, &fleetv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	// First message: snapshot for "a".
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv snapshot: %v", err)
	}
	if msg.Kind != fleetv1.SessionUpdateKind_SESSION_UPDATE_KIND_SNAPSHOT {
		t.Fatalf("first message kind: want SNAPSHOT, got %v", msg.Kind)
	}
	if msg.Session.Id != "a" {
		t.Errorf("snapshot id: want a, got %q", msg.Session.Id)
	}

	// Add a new session — server should emit ADDED.
	fake.addSession(&session.Session{ID: "b", Title: "bravo", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})
	msg = mustRecv(t, stream, 2*time.Second)
	if msg.Kind != fleetv1.SessionUpdateKind_SESSION_UPDATE_KIND_ADDED || msg.Session.Id != "b" {
		t.Errorf("after add: want ADDED b, got kind=%v id=%q", msg.Kind, msg.Session.GetId())
	}

	// Rename "a" — server should emit CHANGED.
	fake.renameSession("a", "alpha-renamed")
	msg = mustRecv(t, stream, 2*time.Second)
	if msg.Kind != fleetv1.SessionUpdateKind_SESSION_UPDATE_KIND_CHANGED || msg.Session.Title != "alpha-renamed" {
		t.Errorf("after rename: want CHANGED title='alpha-renamed', got kind=%v title=%q", msg.Kind, msg.Session.GetTitle())
	}

	// Delete "b" — server should emit REMOVED.
	fake.deleteSessionDirect("b")
	msg = mustRecv(t, stream, 2*time.Second)
	if msg.Kind != fleetv1.SessionUpdateKind_SESSION_UPDATE_KIND_REMOVED || msg.RemovedId != "b" {
		t.Errorf("after delete: want REMOVED b, got kind=%v id=%q", msg.Kind, msg.RemovedId)
	}
}

func TestServer_ListSessions_RepoFilter(t *testing.T) {
	fake := newFakeService()
	// GetRepoRoot will fall through to project_path itself for non-git paths,
	// so use distinct non-git paths to make the filter exact.
	fake.addSession(&session.Session{ID: "in", ProjectPath: "/tmp/repo-a", CreatedAt: time.Now()})
	fake.addSession(&session.Session{ID: "out", ProjectPath: "/tmp/repo-b", CreatedAt: time.Now()})

	client, stop := startTestServer(t, fake)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.ListSessions(ctx, &fleetv1.ListSessionsRequest{RepoRootFilter: "/tmp/repo-a"})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	msg := mustRecv(t, stream, 2*time.Second)
	if msg.Session.Id != "in" {
		t.Errorf("filtered snapshot: want id=in, got %q", msg.Session.Id)
	}

	// No second snapshot message expected — second session is filtered out.
	// We can't easily assert "no further messages" without blocking the test;
	// we instead verify by adding an unrelated session and confirming we
	// don't receive an ADDED for it.
	fake.addSession(&session.Session{ID: "also-out", ProjectPath: "/tmp/repo-b", CreatedAt: time.Now()})
	fake.addSession(&session.Session{ID: "also-in", ProjectPath: "/tmp/repo-a", CreatedAt: time.Now()})

	msg = mustRecv(t, stream, 2*time.Second)
	if msg.Session.Id != "also-in" {
		t.Errorf("after filtered add: want id=also-in, got %q", msg.Session.Id)
	}
}

func TestServer_PinUnpin_RoundTrip(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	if _, err := client.PinRepo(context.Background(), &fleetv1.PinRepoRequest{Root: "/repo"}); err != nil {
		t.Fatalf("PinRepo: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.ListRepos(ctx, &fleetv1.ListReposRequest{})
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	msg := mustRecvRepo(t, stream, 2*time.Second)
	if msg.Kind != fleetv1.RepoUpdateKind_REPO_UPDATE_KIND_SNAPSHOT || !msg.Repo.Pinned || msg.Repo.Root != "/repo" {
		t.Errorf("pinned snapshot: want SNAPSHOT pinned=true root=/repo, got %+v", msg)
	}

	if _, err := client.UnpinRepo(context.Background(), &fleetv1.UnpinRepoRequest{Root: "/repo"}); err != nil {
		t.Fatalf("UnpinRepo: %v", err)
	}
	msg = mustRecvRepo(t, stream, 2*time.Second)
	if msg.Kind != fleetv1.RepoUpdateKind_REPO_UPDATE_KIND_REMOVED || msg.RemovedRoot != "/repo" {
		t.Errorf("after unpin: want REMOVED root=/repo, got %+v", msg)
	}
}

func TestServer_SlotBindings_RoundTrip(t *testing.T) {
	fake := newFakeService()
	fake.addSession(&session.Session{ID: "x", ProjectPath: "/tmp", CreatedAt: time.Now()})
	client, stop := startTestServer(t, fake)
	defer stop()

	if _, err := client.BindSlot(context.Background(), &fleetv1.BindSlotRequest{Slot: 3, SessionId: "x"}); err != nil {
		t.Fatalf("BindSlot: %v", err)
	}
	resp, err := client.ListSlotBindings(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListSlotBindings: %v", err)
	}
	if len(resp.Bindings) != 1 || resp.Bindings[0].Slot != 3 || resp.Bindings[0].SessionId != "x" {
		t.Errorf("bindings: want [{3,x}], got %+v", resp.Bindings)
	}

	_, err = client.BindSlot(context.Background(), &fleetv1.BindSlotRequest{Slot: 99, SessionId: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("out-of-range slot: want InvalidArgument, got %v", err)
	}
}

func TestServer_SendKeys_NotFound(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	_, err := client.SendKeys(context.Background(), &fleetv1.SendKeysRequest{SessionId: "ghost", Keys: []string{"y"}})
	if status.Code(err) != codes.NotFound {
		t.Errorf("SendKeys ghost: want NotFound, got %v", err)
	}
}

func TestServer_SendKeys_NoTmuxPane(t *testing.T) {
	fake := newFakeService()
	// fakeService creates Sessions without a tmux handle, so SendKeys should
	// reject with FailedPrecondition rather than panic.
	fake.addSession(&session.Session{ID: "no-tmux", ProjectPath: "/tmp", CreatedAt: time.Now()})
	client, stop := startTestServer(t, fake)
	defer stop()

	_, err := client.SendKeys(context.Background(), &fleetv1.SendKeysRequest{SessionId: "no-tmux", Keys: []string{"y"}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("SendKeys without tmux: want FailedPrecondition, got %v", err)
	}
}

func TestServer_CapturePane_NotFound(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	_, err := client.CapturePane(context.Background(), &fleetv1.CapturePaneRequest{SessionId: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("CapturePane ghost: want NotFound, got %v", err)
	}
}

func TestServer_SoftDelete_RestoreRoundTrip(t *testing.T) {
	fake := newFakeService()
	fake.addSession(&session.Session{ID: "doomed", Title: "doomed", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})
	client, stop := startTestServer(t, fake)
	defer stop()

	if _, err := client.SoftDeleteSession(context.Background(), &fleetv1.SoftDeleteSessionRequest{Id: "doomed"}); err != nil {
		t.Fatalf("SoftDeleteSession: %v", err)
	}
	if got := fake.GetSession("doomed"); got != nil {
		t.Errorf("after soft delete: session should be removed from svc, got %+v", got)
	}

	resp, err := client.RestoreSession(context.Background(), &fleetv1.RestoreSessionRequest{Id: "doomed"})
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if resp.Id != "doomed" || resp.Title != "doomed" {
		t.Errorf("restored session: want id=title=doomed, got id=%q title=%q", resp.Id, resp.Title)
	}
	if got := fake.GetSession("doomed"); got == nil {
		t.Errorf("after restore: session should be present in svc, got nil")
	}
}

func TestServer_RestoreSession_NotFound(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	_, err := client.RestoreSession(context.Background(), &fleetv1.RestoreSessionRequest{Id: "never-deleted"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("RestoreSession with no tombstone: want NotFound, got %v", err)
	}
}

func TestServer_SoftDelete_NotFound(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	_, err := client.SoftDeleteSession(context.Background(), &fleetv1.SoftDeleteSessionRequest{Id: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("SoftDeleteSession ghost: want NotFound, got %v", err)
	}
}

func TestServer_Unimplemented_StillStubbed(t *testing.T) {
	fake := newFakeService()
	client, stop := startTestServer(t, fake)
	defer stop()

	// Config / HookEvent surfaces remain stubbed after PR 5; guard against
	// accidental implementation that would break existing clients before
	// they're ready. Workspaces are wired (see TestServer_Workspaces below).
	_, err := client.GetConfig(context.Background(), nil)
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("GetConfig: want Unimplemented, got %v", err)
	}
}

func TestServer_Workspaces(t *testing.T) {
	fake := newFakeService()
	fake.workspaces = map[string][]workspace.WorkspaceInfo{
		"/r": {{Name: "wt-a", Path: "/r-wt-a", Branch: "feat/a"}},
	}
	client, stop := startTestServer(t, fake)
	defer stop()

	listResp, err := client.ListWorkspaces(context.Background(), &fleetv1.ListWorkspacesRequest{RepoRoot: "/r"})
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if got := len(listResp.GetWorkspaces()); got != 1 {
		t.Fatalf("ListWorkspaces: want 1 workspace, got %d", got)
	}
	if got := listResp.GetProviderName(); got != "git-worktree" {
		t.Errorf("ListWorkspaces: want provider git-worktree, got %q", got)
	}
	if got := listResp.GetWorkspaces()[0].GetName(); got != "wt-a" {
		t.Errorf("ListWorkspaces: name=%q, want wt-a", got)
	}

	createResp, err := client.CreateWorkspace(context.Background(), &fleetv1.CreateWorkspaceRequest{
		RepoRoot: "/r", Name: "wt-b", BaseBranch: "main", NewBranch: "feat/b",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if got := createResp.GetWorkspace().GetName(); got != "wt-b" {
		t.Errorf("CreateWorkspace: name=%q, want wt-b", got)
	}
	if len(fake.wsCreates) != 1 || fake.wsCreates[0].name != "wt-b" {
		t.Errorf("CreateWorkspace: did not call service: %+v", fake.wsCreates)
	}

	if _, err := client.DestroyWorkspace(context.Background(), &fleetv1.DestroyWorkspaceRequest{RepoRoot: "/r", Name: "wt-a"}); err != nil {
		t.Fatalf("DestroyWorkspace: %v", err)
	}
	if len(fake.wsDestroys) != 1 || fake.wsDestroys[0].name != "wt-a" {
		t.Errorf("DestroyWorkspace: did not call service: %+v", fake.wsDestroys)
	}

	// InvalidArgument paths.
	_, err = client.ListWorkspaces(context.Background(), &fleetv1.ListWorkspacesRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("ListWorkspaces empty repo_root: want InvalidArgument, got %v", err)
	}
	_, err = client.CreateWorkspace(context.Background(), &fleetv1.CreateWorkspaceRequest{Name: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("CreateWorkspace empty repo_root: want InvalidArgument, got %v", err)
	}
	_, err = client.DestroyWorkspace(context.Background(), &fleetv1.DestroyWorkspaceRequest{RepoRoot: "/r"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("DestroyWorkspace empty name: want InvalidArgument, got %v", err)
	}
}

// mustRecv pulls one message off the stream, failing the test on timeout.
// io.EOF is treated as a server-side close, not a timeout.
func mustRecv(t *testing.T, stream grpc.ServerStreamingClient[fleetv1.SessionUpdate], timeout time.Duration) *fleetv1.SessionUpdate {
	t.Helper()
	type result struct {
		msg *fleetv1.SessionUpdate
		err error
	}
	ch := make(chan result, 1)
	go func() {
		m, e := stream.Recv()
		ch <- result{m, e}
	}()
	select {
	case r := <-ch:
		if errors.Is(r.err, io.EOF) {
			t.Fatalf("stream closed unexpectedly")
		}
		if r.err != nil {
			t.Fatalf("Recv: %v", r.err)
		}
		return r.msg
	case <-time.After(timeout):
		t.Fatalf("Recv timed out after %v", timeout)
		return nil
	}
}

func mustRecvRepo(t *testing.T, stream grpc.ServerStreamingClient[fleetv1.RepoUpdate], timeout time.Duration) *fleetv1.RepoUpdate {
	t.Helper()
	type result struct {
		msg *fleetv1.RepoUpdate
		err error
	}
	ch := make(chan result, 1)
	go func() {
		m, e := stream.Recv()
		ch <- result{m, e}
	}()
	select {
	case r := <-ch:
		if errors.Is(r.err, io.EOF) {
			t.Fatalf("stream closed unexpectedly")
		}
		if r.err != nil {
			t.Fatalf("Recv: %v", r.err)
		}
		return r.msg
	case <-time.After(timeout):
		t.Fatalf("Recv timed out after %v", timeout)
		return nil
	}
}
