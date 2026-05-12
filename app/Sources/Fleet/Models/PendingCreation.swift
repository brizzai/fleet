import Foundation

// PendingCreation is the Mac-side mirror of the TUI's `PendingWorkspace`
// (internal/ui/workspace_create.go): a placeholder row that appears in the
// sidebar the moment the user submits the New Worktree sheet, and gets
// removed when the daemon's session/repo streams deliver the real session
// row for the new worktree.
//
// Plain session creation (`Cmd-N` / Cmd-Shift-N) is synchronous and doesn't
// need a placeholder — the daemon returns the populated `Session` proto in
// <100ms and the stream delivers the row right after. Worktree creation
// can take several seconds (git worktree add + tmux + claude warmup), so
// the placeholder closes the feedback gap.
struct PendingCreation: Identifiable, Hashable, Sendable {
    let id: String              // client-generated UUID
    let repoRoot: String        // for sidebar grouping
    let displayName: String     // shown in placeholder row (typically the new branch name)
    let kind: Kind
    let startedAt: Date

    enum Kind: Hashable, Sendable {
        case worktree
    }
}
