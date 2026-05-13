package daemonclient

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fleetv1 "github.com/brizzai/fleet/gen/proto/fleet/v1"
	"github.com/brizzai/fleet/internal/daemonsrv"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/service"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeSvc mirrors the daemonsrv tests' fakeService — the daemonclient tests
// run a real daemonsrv.Server over bufconn and drive it through this fake.
// Using the real server means convert.go and stream.go are exercised
// end-to-end without the test having to know about proto details.
type fakeSvc struct {
	mu        sync.Mutex
	sessions  []*session.Session
	gitInfo   map[string]*git.RepoInfo
	pinned    map[string]bool
	slots     map[int]string
	observers []service.Observer
	tombstone *session.SessionRow // simulated tombstone for SoftDelete/Restore
}

func newFakeSvc() *fakeSvc {
	return &fakeSvc{
		gitInfo: map[string]*git.RepoInfo{},
		pinned:  map[string]bool{},
		slots:   map[int]string{},
	}
}

func (f *fakeSvc) addSession(s *session.Session) {
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionsChanged})
}

func (f *fakeSvc) renameSession(id, title string) {
	f.mu.Lock()
	for _, s := range f.sessions {
		if s.ID == id {
			s.Title = title
		}
	}
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
}

func (f *fakeSvc) notify(e service.Event) {
	f.mu.Lock()
	obs := append([]service.Observer(nil), f.observers...)
	f.mu.Unlock()
	for _, o := range obs {
		o.OnEvent(e)
	}
}

func (f *fakeSvc) LoadFromStorage() error      { return nil }
func (f *fakeSvc) Start() (string, error)      { return "", nil }
func (f *fakeSvc) Stop()                       {}
func (f *fakeSvc) TriggerRefresh()             {}
func (f *fakeSvc) EnqueuePriority(_ string)    {}
func (f *fakeSvc) IsGHAvailable() bool         { return false }
func (f *fakeSvc) OnFirstPrompt(_, _ string)   {}
func (f *fakeSvc) OnPromptCount(_ string, _ int) {}
func (f *fakeSvc) CapturePreview(_ string) (string, error) {
	return "", nil
}

func (f *fakeSvc) Subscribe(o service.Observer) {
	f.mu.Lock()
	f.observers = append(f.observers, o)
	f.mu.Unlock()
}

func (f *fakeSvc) Unsubscribe(o service.Observer) {
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

func (f *fakeSvc) Sessions() []*session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*session.Session, len(f.sessions))
	for i, s := range f.sessions {
		// Return decoupled snapshots so the server's stream goroutine
		// can fingerprint these without racing the test goroutine's
		// field writes (e.g. fakeSvc.renameSession). Only the public
		// fields convertSession projects need to be carried.
		out[i] = snapshotSession(s)
	}
	return out
}

func (f *fakeSvc) GetSession(id string) *session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id {
			return snapshotSession(s)
		}
	}
	return nil
}

// snapshotSession returns a value-copied *session.Session containing only
// the exported fields the daemon's wire format reads. Importantly, it
// allocates a fresh sync.RWMutex on the new pointer instead of copying the
// original (copying a held lock is undefined behavior).
func snapshotSession(s *session.Session) *session.Session {
	if s == nil {
		return nil
	}
	return &session.Session{
		ID:                s.ID,
		Title:             s.Title,
		ProjectPath:       s.ProjectPath,
		Status:            s.Status,
		TmuxSessionName:   s.TmuxSessionName,
		CreatedAt:         s.CreatedAt,
		LastAccessedAt:    s.LastAccessedAt,
		Acknowledged:      s.Acknowledged,
		ClaudeSessionID:   s.ClaudeSessionID,
		ClaudeSessionName: s.ClaudeSessionName,
		WorkspaceName:     s.WorkspaceName,
		ManuallyRenamed:   s.ManuallyRenamed,
		FirstPrompt:       s.FirstPrompt,
		TitleGenerated:    s.TitleGenerated,
		PromptCount:       s.PromptCount,
		ForkFromID:        s.ForkFromID,
	}
}

func (f *fakeSvc) GitInfo(repo string) *git.RepoInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gitInfo[repo]
}

func (f *fakeSvc) GitInfoAll() map[string]*git.RepoInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*git.RepoInfo, len(f.gitInfo))
	for k, v := range f.gitInfo {
		out[k] = v
	}
	return out
}

func (f *fakeSvc) PinnedRepos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.pinned))
	for p := range f.pinned {
		out = append(out, p)
	}
	return out
}

func (f *fakeSvc) SlotBindings() map[int]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]string, len(f.slots))
	for k, v := range f.slots {
		out[k] = v
	}
	return out
}

func (f *fakeSvc) CreateSession(title, projectPath, _ string) (*session.Session, error) {
	s := &session.Session{ID: "new-" + title, Title: title, ProjectPath: projectPath, Status: session.StatusStarting, CreatedAt: time.Now()}
	f.addSession(s)
	return s, nil
}

func (f *fakeSvc) ForkSession(title, projectPath, _, parentID string) (*session.Session, error) {
	s := &session.Session{ID: "fork-" + title, Title: title, ProjectPath: projectPath, ForkFromID: parentID, CreatedAt: time.Now()}
	f.addSession(s)
	return s, nil
}

func (f *fakeSvc) DeleteSession(id string) {
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

func (f *fakeSvc) RestartSession(id string) error {
	if f.GetSession(id) == nil {
		return errors.New("not found")
	}
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
	return nil
}

func (f *fakeSvc) RenameSession(id, newTitle string) {
	f.renameSession(id, newTitle)
}

func (f *fakeSvc) AcknowledgeSession(id string) {
	f.mu.Lock()
	for _, s := range f.sessions {
		if s.ID == id {
			s.Acknowledged = true
		}
	}
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventSessionStatusChanged})
}

func (f *fakeSvc) BindSlot(slot int, sessionID string) error {
	if f.GetSession(sessionID) == nil {
		return errors.New("session not found")
	}
	f.mu.Lock()
	f.slots[slot] = sessionID
	f.mu.Unlock()
	return nil
}

func (f *fakeSvc) UnbindSlot(slot int) error {
	f.mu.Lock()
	delete(f.slots, slot)
	f.mu.Unlock()
	return nil
}

func (f *fakeSvc) PinRepo(path string) error {
	f.mu.Lock()
	f.pinned[path] = true
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventGitInfoChanged})
	return nil
}

func (f *fakeSvc) UnpinRepo(path string) error {
	f.mu.Lock()
	delete(f.pinned, path)
	f.mu.Unlock()
	f.notify(service.Event{Type: service.EventGitInfoChanged})
	return nil
}

func (f *fakeSvc) ListWorkspaces(_ string) ([]workspace.WorkspaceInfo, string, error) {
	return []workspace.WorkspaceInfo{}, "git-worktree", nil
}

func (f *fakeSvc) CreateWorkspace(repoRoot, name, _, newBranch string) (*workspace.WorkspaceInfo, *session.Session, error) {
	info := &workspace.WorkspaceInfo{Name: name, Path: repoRoot + "-" + name, Branch: newBranch}
	sess, err := f.CreateSession(name, info.Path, name)
	if err != nil {
		return info, nil, err
	}
	return info, sess, nil
}

func (f *fakeSvc) DestroyWorkspace(_ string, _ string) error { return nil }

func (f *fakeSvc) SnapshotForUndo(id string) (*session.SessionRow, error) {
	if s := f.GetSession(id); s != nil {
		return &session.SessionRow{ID: s.ID, Title: s.Title, ProjectPath: s.ProjectPath, Status: string(s.Status)}, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeSvc) SoftDelete(id string) (*session.SessionRow, error) {
	row, err := f.SnapshotForUndo(id)
	if err != nil {
		return nil, err
	}
	f.DeleteSession(id)
	return row, nil
}

func (f *fakeSvc) RestoreDeleted(row *session.SessionRow) error {
	if row == nil {
		return errors.New("nil row")
	}
	f.addSession(&session.Session{ID: row.ID, Title: row.Title, ProjectPath: row.ProjectPath, Status: session.Status(row.Status), CreatedAt: time.Now()})
	return nil
}

func (f *fakeSvc) SoftRestore(_ *session.Session, _ *session.SessionRow) error {
	return nil
}

var _ service.Service = (*fakeSvc)(nil)

// ── Test harness ──────────────────────────────────────────────────────────

// startTestClient brings up a real daemonsrv.Server backed by `svc` over
// bufconn, and returns a daemonclient.Client wired to that bufconn. Returns
// the client (already Started) and a cleanup func that stops both.
func startTestClient(t *testing.T, svc service.Service) (*Client, func()) {
	t.Helper()
	lis := bufconn.Listen(64 * 1024)
	srv := grpc.NewServer()
	fleetv1.RegisterFleetServer(srv, daemonsrv.NewServer(svc))
	go func() { _ = srv.Serve(lis) }()

	dialer := func(_ context.Context) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	c := newWithDialer(dialer)
	if _, err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c, func() {
		c.Stop()
		srv.Stop()
	}
}

// countingObserver records every event it sees so tests can assert on the
// fan-out shape. Atomics avoid the need for explicit locking on the small
// surface tests poke.
type countingObserver struct {
	sessions atomic.Int32
	statuses atomic.Int32
	errors   atomic.Int32
}

func (o *countingObserver) OnEvent(e service.Event) {
	switch e.Type {
	case service.EventSessionsChanged:
		o.sessions.Add(1)
	case service.EventSessionStatusChanged:
		o.statuses.Add(1)
	case service.EventError:
		o.errors.Add(1)
	}
}

// waitFor polls the predicate up to timeout, returning true if it ever
// reports done. Used to bridge between the stream goroutine landing a
// delta and the test's assertion against the cache.
func waitFor(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return pred()
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestClient_StartPopulatesSnapshot(t *testing.T) {
	fake := newFakeSvc()
	fake.addSession(&session.Session{ID: "alpha", Title: "alpha", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})
	fake.addSession(&session.Session{ID: "beta", Title: "beta", ProjectPath: "/tmp", Status: session.StatusIdle, CreatedAt: time.Now().Add(time.Second)})

	c, stop := startTestClient(t, fake)
	defer stop()

	if !waitFor(2*time.Second, func() bool { return len(c.Sessions()) == 2 }) {
		t.Fatalf("snapshot never populated: got %d sessions", len(c.Sessions()))
	}
	got := c.GetSession("alpha")
	if got == nil || got.Title != "alpha" {
		t.Errorf("GetSession(alpha): got %+v", got)
	}
}

func TestClient_StreamDelivers_Add_Change_Remove(t *testing.T) {
	fake := newFakeSvc()
	c, stop := startTestClient(t, fake)
	defer stop()

	obs := &countingObserver{}
	c.Subscribe(obs)

	// Initial snapshot is empty — wait for the stream goroutine to be ready
	// before pushing the first ADDED so the stream's stateful inSnapshot
	// branch gets exercised. We can't directly observe "stream attached" so
	// we just poll-add until it sticks.
	fake.addSession(&session.Session{ID: "x", Title: "x", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})
	if !waitFor(2*time.Second, func() bool { return c.GetSession("x") != nil }) {
		t.Fatalf("ADDED never landed")
	}

	fake.renameSession("x", "x-renamed")
	if !waitFor(2*time.Second, func() bool {
		s := c.GetSession("x")
		return s != nil && s.Title == "x-renamed"
	}) {
		t.Fatalf("CHANGED never landed; cache title=%q", maybeTitle(c.GetSession("x")))
	}

	fake.DeleteSession("x")
	if !waitFor(2*time.Second, func() bool { return c.GetSession("x") == nil }) {
		t.Fatalf("REMOVED never landed")
	}

	if obs.sessions.Load() == 0 {
		t.Errorf("observer saw no EventSessionsChanged")
	}
}

func TestClient_Mutation_RoundTrip(t *testing.T) {
	fake := newFakeSvc()
	c, stop := startTestClient(t, fake)
	defer stop()

	sess, err := c.CreateSession("hello", "/tmp/repo", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess == nil || sess.Title != "hello" {
		t.Errorf("returned session: %+v", sess)
	}
	// applySessionImmediate should have surfaced it without waiting for the
	// stream tick.
	if c.GetSession(sess.ID) == nil {
		t.Errorf("cache missing session immediately after CreateSession")
	}
}

// TestClient_SoftRestore_AfterSoftDelete reproduces the exact path the TUI
// takes for `d` then `z` in daemon mode — deferDelete calls SoftDelete to
// stash the row, then undoDelete calls SoftRestore (NOT RestoreDeleted) to
// bring it back. If SoftRestore on the client doesn't reach the daemon, the
// tombstone evicts after 30s and the tmux pane dies even though the UI
// shows the session as restored.
func TestClient_SoftRestore_AfterSoftDelete(t *testing.T) {
	fake := newFakeSvc()
	fake.addSession(&session.Session{ID: "doomed", Title: "doomed", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})

	c, stop := startTestClient(t, fake)
	defer stop()
	if !waitFor(2*time.Second, func() bool { return c.GetSession("doomed") != nil }) {
		t.Fatalf("snapshot of doomed never landed")
	}

	row, err := c.SoftDelete("doomed")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	sess := &session.Session{ID: row.ID, Title: row.Title, ProjectPath: row.ProjectPath}

	// The TUI keeps the live pointer + row in its undo stack and calls
	// SoftRestore on `z`. Both forms (with sess, with row only) must
	// reach the daemon.
	if err := c.SoftRestore(sess, row); err != nil {
		t.Fatalf("SoftRestore: %v", err)
	}
	if c.GetSession("doomed") == nil {
		t.Errorf("session not in cache immediately after SoftRestore")
	}
	if fake.GetSession("doomed") == nil {
		t.Errorf("daemon-side fake never received restore — tombstone would evict")
	}
}

func TestClient_SoftDelete_RestoreRoundTrip(t *testing.T) {
	fake := newFakeSvc()
	fake.addSession(&session.Session{ID: "doomed", Title: "doomed", ProjectPath: "/tmp", Status: session.StatusRunning, CreatedAt: time.Now()})

	c, stop := startTestClient(t, fake)
	defer stop()
	if !waitFor(2*time.Second, func() bool { return c.GetSession("doomed") != nil }) {
		t.Fatalf("snapshot of doomed never landed")
	}

	row, err := c.SoftDelete("doomed")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if row == nil || row.ID != "doomed" {
		t.Fatalf("undo row: %+v", row)
	}
	if !waitFor(2*time.Second, func() bool { return c.GetSession("doomed") == nil }) {
		t.Fatalf("session not removed from cache after SoftDelete")
	}

	if err := c.RestoreDeleted(row); err != nil {
		t.Fatalf("RestoreDeleted: %v", err)
	}
	if c.GetSession("doomed") == nil {
		t.Errorf("session not in cache immediately after RestoreDeleted")
	}
}

func TestClient_PinUnpin_RoundTrip(t *testing.T) {
	fake := newFakeSvc()
	c, stop := startTestClient(t, fake)
	defer stop()

	if err := c.PinRepo("/repo"); err != nil {
		t.Fatalf("PinRepo: %v", err)
	}
	if !waitFor(2*time.Second, func() bool {
		for _, p := range c.PinnedRepos() {
			if p == "/repo" {
				return true
			}
		}
		return false
	}) {
		t.Errorf("pin never reached client cache: pinned=%v", c.PinnedRepos())
	}

	if err := c.UnpinRepo("/repo"); err != nil {
		t.Fatalf("UnpinRepo: %v", err)
	}
	if !waitFor(2*time.Second, func() bool {
		for _, p := range c.PinnedRepos() {
			if p == "/repo" {
				return false
			}
		}
		return true
	}) {
		t.Errorf("unpin never reached client cache: pinned=%v", c.PinnedRepos())
	}
}

func TestClient_SlotBindings(t *testing.T) {
	fake := newFakeSvc()
	fake.addSession(&session.Session{ID: "x", ProjectPath: "/tmp", CreatedAt: time.Now()})
	c, stop := startTestClient(t, fake)
	defer stop()
	if !waitFor(2*time.Second, func() bool { return c.GetSession("x") != nil }) {
		t.Fatalf("snapshot never populated")
	}

	if err := c.BindSlot(3, "x"); err != nil {
		t.Fatalf("BindSlot: %v", err)
	}
	if got := c.SlotBindings()[3]; got != "x" {
		t.Errorf("slot 3: want x, got %q", got)
	}

	if err := c.UnbindSlot(3); err != nil {
		t.Fatalf("UnbindSlot: %v", err)
	}
	if _, ok := c.SlotBindings()[3]; ok {
		t.Errorf("slot 3 still bound after UnbindSlot")
	}
}

// maybeTitle is a small helper for test failure messages — accepts nil.
func maybeTitle(s *session.Session) string {
	if s == nil {
		return "<nil>"
	}
	return s.Title
}
