package daemonsrv

import (
	"sort"

	fleetv1 "github.com/brizzai/fleet/gen/proto/fleet/v1"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/service"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// convertConfig projects the daemon's config.Config onto the proto Config
// message. Proto exposes a subset of fields — AutoUpdate, EnterMode, and
// Telemetry stay client-private since Mac doesn't render them.
func convertConfig(c *config.Config) *fleetv1.Config {
	if c == nil {
		return &fleetv1.Config{}
	}
	return &fleetv1.Config{
		TickIntervalSec:    int32(c.TickIntervalSec),
		DefaultProjectPath: c.DefaultProjectPath,
		Editor:             c.Editor,
		Theme:              c.Theme,
		AutoNameSessions:   c.IsAutoNameEnabled(),
		CopyClaudeSettings: c.IsCopyClaudeSettingsEnabled(),
	}
}

func convertWorkspace(w workspace.WorkspaceInfo) *fleetv1.Workspace {
	return &fleetv1.Workspace{
		Name:   w.Name,
		Path:   w.Path,
		Branch: w.Branch,
		Status: w.Status,
	}
}

// convertSession projects a *session.Session into its proto wire form.
// hook_status is left empty here — the daemon doesn't currently surface the
// HookSnapshot from storage; clients should rely on the typed Status field.
func convertSession(s *session.Session) *fleetv1.Session {
	if s == nil {
		return nil
	}
	return &fleetv1.Session{
		Id:                s.ID,
		Title:             s.Title,
		ProjectPath:       s.ProjectPath,
		Status:            convertStatus(s.Status),
		TmuxSessionName:   s.TmuxSessionName,
		CreatedAt:         timestamppb.New(s.CreatedAt),
		LastAccessedAt:    timestamppb.New(s.LastAccessedAt),
		Acknowledged:      s.Acknowledged,
		ClaudeSessionId:   s.ClaudeSessionID,
		ClaudeSessionName: s.ClaudeSessionName,
		WorkspaceName:     s.WorkspaceName,
		ManuallyRenamed:   s.ManuallyRenamed,
		FirstPrompt:       s.FirstPrompt,
		TitleGenerated:    s.TitleGenerated,
		PromptCount:       int32(s.PromptCount),
		ForkFromId:        s.ForkFromID,
		IsAlive:           safeIsAlive(s),
		RepoRoot:          session.GetRepoRoot(s.ProjectPath),
	}
}

// safeIsAlive guards against sessions that don't have a tmux handle attached
// — production sessions always go through session.NewSession() which sets
// one, but tests can construct bare *session.Session{} literals where
// IsAlive's tmuxSession.Exists() would dereference a nil pointer.
func safeIsAlive(s *session.Session) bool {
	if s == nil || s.GetTmuxSession() == nil {
		return false
	}
	return s.IsAlive()
}

func convertStatus(s session.Status) fleetv1.Status {
	switch s {
	case session.StatusIdle:
		return fleetv1.Status_STATUS_IDLE
	case session.StatusStarting:
		return fleetv1.Status_STATUS_STARTING
	case session.StatusRunning:
		return fleetv1.Status_STATUS_RUNNING
	case session.StatusWaiting:
		return fleetv1.Status_STATUS_WAITING
	case session.StatusFinished:
		return fleetv1.Status_STATUS_FINISHED
	case session.StatusError:
		return fleetv1.Status_STATUS_ERROR
	default:
		return fleetv1.Status_STATUS_UNSPECIFIED
	}
}

// convertRepo composes a Repo message from a repo root and the cached
// *git.RepoInfo. info may be nil if the worker hasn't refreshed yet — that's
// fine; we just emit zero-valued git fields.
func convertRepo(root string, info *git.RepoInfo, pinned bool) *fleetv1.Repo {
	r := &fleetv1.Repo{
		Root:        root,
		DisplayName: displayName(root),
		Pinned:      pinned,
	}
	if info != nil {
		r.Branch = info.Branch
		r.IsDirty = info.IsDirty
		r.IsWorktreeRepo = info.IsWorktreeRepo
		r.Pr = convertPR(info.PR)
		r.LastGitRefresh = timestamppb.New(info.LastGitRefresh)
		r.LastPrRefresh = timestamppb.New(info.LastPRRefresh)
	}
	return r
}

func convertPR(pr *github.PR) *fleetv1.PR {
	if pr == nil {
		return nil
	}
	return &fleetv1.PR{
		Number:            int32(pr.Number),
		Title:             pr.Title,
		Url:               pr.URL,
		State:             pr.State,
		ReviewDecision:    pr.ReviewDecision,
		CiStatus:          pr.CIStatus,
		UnresolvedThreads: int32(pr.UnresolvedThreads),
		HasConflicts:      pr.HasConflicts,
	}
}

// displayName returns a friendly label for a repo root — basename for now;
// can grow into "owner/repo" inference later without breaking callers.
func displayName(root string) string {
	for i := len(root) - 1; i >= 0; i-- {
		if root[i] == '/' {
			return root[i+1:]
		}
	}
	return root
}

// derivedRepos enumerates every repo that should appear in ListRepos:
// the repo root of every live session, plus every pinned repo root. Result
// is sorted by root so snapshots are deterministic.
func derivedRepos(svc service.Service) []*fleetv1.Repo {
	roots := map[string]bool{}
	for _, s := range svc.Sessions() {
		roots[session.GetRepoRoot(s.ProjectPath)] = true
	}
	pinned := map[string]bool{}
	for _, p := range svc.PinnedRepos() {
		roots[p] = true
		pinned[p] = true
	}

	out := make([]*fleetv1.Repo, 0, len(roots))
	for root := range roots {
		out = append(out, convertRepo(root, svc.GitInfo(root), pinned[root]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// convertSlotBindings produces a stable, slot-sorted slice — clients depend
// on consistent ordering when rendering hotkey badges.
func convertSlotBindings(m map[int]string) []*fleetv1.SlotBinding {
	out := make([]*fleetv1.SlotBinding, 0, len(m))
	for slot, id := range m {
		out = append(out, &fleetv1.SlotBinding{Slot: int32(slot), SessionId: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}
