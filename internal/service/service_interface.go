package service

import (
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
)

// Service is the abstract surface both the in-process SessionService and the
// future daemon-client implementation satisfy. Stage 0 PR 5 will introduce a
// second implementation that talks to `fleet daemon` over gRPC; in PR 2 only
// the in-process implementation exists.
type Service interface {
	// Lifecycle.
	LoadFromStorage() error
	Start() (warning string, err error)
	Stop()
	Subscribe(Observer)
	Unsubscribe(Observer)
	TriggerRefresh()
	EnqueuePriority(id string)

	// Queries.
	Sessions() []*session.Session
	GetSession(id string) *session.Session
	GitInfo(repo string) *git.RepoInfo
	GitInfoAll() map[string]*git.RepoInfo
	IsGHAvailable() bool
	PinnedRepos() []string
	SlotBindings() map[int]string
	CapturePreview(id string) (string, error)

	// Mutations.
	CreateSession(title, projectPath, workspaceName string) (*session.Session, error)
	ForkSession(title, projectPath, workspaceName, parentClaudeSessionID string) (*session.Session, error)
	DeleteSession(id string)
	RestartSession(id string) error
	RenameSession(id, newTitle string)
	AcknowledgeSession(id string)
	BindSlot(slot int, sessionID string) error
	UnbindSlot(slot int) error
	PinRepo(path string) error
	UnpinRepo(path string) error

	// Workspaces. CreateWorkspace also spawns a session pointing at the new
	// workspace path — daemon clients can't easily sequence two RPCs without
	// risking an orphan worktree if the second call fails.
	ListWorkspaces(repoRoot string) ([]workspace.WorkspaceInfo, string, error)
	CreateWorkspace(repoRoot, name, baseBranch, newBranch string) (*workspace.WorkspaceInfo, *session.Session, error)
	DestroyWorkspace(repoRoot, name string) error

	// Undo-delete.
	SnapshotForUndo(id string) (*session.SessionRow, error)
	SoftDelete(id string) (*session.SessionRow, error)
	RestoreDeleted(row *session.SessionRow) error
	SoftRestore(sess *session.Session, row *session.SessionRow) error

	// Auto-naming hooks.
	OnFirstPrompt(id, prompt string)
	OnPromptCount(id string, count int)
}

// Compile-time check that *SessionService satisfies Service.
var _ Service = (*SessionService)(nil)
