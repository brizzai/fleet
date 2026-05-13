import Foundation
import Observation

enum ConnectionState: Equatable, Sendable {
    case connecting    // first dial, no socket yet
    case connected     // streams active
    case reconnecting  // stream broken; backoff loop running
    case disconnected  // gave up (daemon binary missing, etc.)
}

// AppModel is the SwiftUI source of truth and the sole sink for daemon
// stream updates. SwiftUI observes it from the main thread; daemon
// consumers run on background tasks and call into the @MainActor methods
// to mutate state. This keeps writes serialised on the main actor without
// our daemon code having to reach for `MainActor.run` each call.
@Observable
@MainActor
final class AppModel {
    // ─── Stream-driven state ─────────────────────────────────────────
    private(set) var sessionsByID: [String: Session] = [:]
    private(set) var reposByRoot: [String: Repo] = [:]
    private(set) var slotBindings: [Int: String] = [:]

    // ─── UI state ────────────────────────────────────────────────────
    // `selection` is the source of truth for the sidebar's current row. It
    // can refer to either a session (default) or a repo header (so Cmd-N
    // can target the repo even when no session in it is highlighted).
    // `selectedSessionID` is a back-compat computed accessor for the views
    // that only care about session selection.
    var selection: Selection? {
        didSet { onSelectionChange(from: oldValue) }
    }

    var selectedSessionID: String? {
        get {
            if case .session(let id) = selection { return id }
            return nil
        }
        set {
            if let id = newValue {
                selection = .session(id)
            } else if case .session = selection {
                selection = nil
            }
        }
    }

    /// The repo Cmd-N targets. Mirrors the TUI's `a → n` fallback at
    /// `internal/ui/app.go:1068-1080`: if a session is highlighted, use
    /// its repo; if a repo header is highlighted, use that repo; else nil
    /// (which makes Cmd-N fall through to the path-picker sheet).
    var cursorRepoRoot: String? {
        switch selection {
        case .session(let id):
            return sessionsByID[id]?.repoRoot
        case .repo(let root):
            return root
        case .none:
            return nil
        }
    }

    var filterText: String = ""

    // Pending rename session id; SidebarView swaps the row for a TextField
    // when this matches a row's id. nil means no row is in edit mode.
    var renamingSessionID: String?

    // Pending delete confirmation. SwiftUI binds to this for the .alert.
    var pendingDeletion: Session?

    // Sheet visibility (driven by FleetCommands menu items + Cmd-shortcuts).
    var presentingNewSession: Bool = false
    var presentingNewWorktree: Bool = false

    // Async-creation placeholders (worktree only). Rendered in the sidebar
    // under the matching repo until the new session arrives via the stream
    // or the 30s stale guard drops the entry.
    private(set) var pendingCreations: [PendingCreation] = []

    // ─── Connection state ────────────────────────────────────────────
    private(set) var connectionState: ConnectionState = .connecting
    private(set) var lastError: String?

    // Transient red banner for failed mutations; cleared by `errorToastClearer`.
    private(set) var errorToast: String?
    private var errorToastClearer: Task<Void, Never>?

    // ─── Lifecycle ───────────────────────────────────────────────────
    private var runnerTask: Task<Void, Never>?
    private var pendingGuardTask: Task<Void, Never>?
    private(set) var mutator: Mutator?

    func start() {
        guard runnerTask == nil else { return }
        runnerTask = Task { [self] in
            await DaemonClientRunner.run(model: self)
        }
        pendingGuardTask = Task { [weak self] in
            await Self.runPendingGuard(model: self)
        }
    }

    func stop() {
        runnerTask?.cancel()
        runnerTask = nil
        pendingGuardTask?.cancel()
        pendingGuardTask = nil
    }

    /// Background loop that drops worktree placeholders that haven't been
    /// reconciled within 30 seconds. The daemon may have failed to spawn
    /// the session and never sent us a SessionUpdate; without this guard
    /// the spinner row would stay forever.
    private static func runPendingGuard(model: AppModel?) async {
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(5))
            guard !Task.isCancelled else { return }
            await model?.dropStalePendingCreations()
        }
    }

    // ─── Derived views (consumed by the sidebar) ─────────────────────

    /// Sessions sorted in a stable display order. The TUI groups by repo
    /// then sorts by created-time inside each group; we mirror that in
    /// `sessionsForRepo(root:)` rather than sorting the global list.
    var allSessions: [Session] {
        Array(sessionsByID.values)
    }

    /// Repos in the same order the TUI sidebar uses: pinned and/or with
    /// sessions first, alphabetised by display name. Filtering by
    /// `filterText` is applied in the view layer, not here.
    var displayedRepos: [Repo] {
        var repos = Array(reposByRoot.values)
        repos.sort { lhs, rhs in
            if lhs.pinned != rhs.pinned { return lhs.pinned && !rhs.pinned }
            return lhs.displayName.localizedCompare(rhs.displayName) == .orderedAscending
        }
        // Hydrate `sessions` so views can consume `repo.sessions` directly.
        return repos.map { repo in
            var copy = repo
            copy.sessions = sessionsForRepo(root: repo.id)
            return copy
        }
    }

    func sessionsForRepo(root: String) -> [Session] {
        sessionsByID.values
            .filter { $0.repoRoot == root }
            .sorted { lhs, rhs in
                lhs.title.localizedCompare(rhs.title) == .orderedAscending
            }
            .map { sess in
                var copy = sess
                copy.slot = slotForSession(id: sess.id)
                return copy
            }
    }

    private func slotForSession(id: String) -> Int? {
        for (slot, sid) in slotBindings where sid == id { return slot }
        return nil
    }

    // ─── Daemon-driven mutators (called from streams) ────────────────

    func set(connectionState state: ConnectionState, error: String?) {
        self.connectionState = state
        if let error { self.lastError = error } // keep last meaningful error
        else if state == .connected { self.lastError = nil }
    }

    func applySession(_ session: Session) {
        sessionsByID[session.id] = session
        reconcilePendingCreations(against: session)
    }

    func removeSession(id: String) {
        sessionsByID.removeValue(forKey: id)
        if selectedSessionID == id { selection = nil }
    }

    /// Drops any cached sessions whose IDs were not delivered by the
    /// snapshot we just finished consuming.
    func finalizeSessionsSnapshot(seenIDs: Set<String>) {
        for id in sessionsByID.keys where !seenIDs.contains(id) {
            sessionsByID.removeValue(forKey: id)
        }
        if let sel = selectedSessionID, !seenIDs.contains(sel) {
            selection = nil
        }
    }

    func applyRepo(_ repo: Repo) {
        reposByRoot[repo.id] = repo
    }

    func removeRepo(root: String) {
        reposByRoot.removeValue(forKey: root)
    }

    func finalizeReposSnapshot(seenRoots: Set<String>) {
        for root in reposByRoot.keys where !seenRoots.contains(root) {
            reposByRoot.removeValue(forKey: root)
        }
    }

    func applySlotBindings(_ bindings: [Int: String]) {
        self.slotBindings = bindings
    }

    /// Mutates the in-memory bindings map to mirror the daemon's eviction
    /// rules (storage.go:360 — `DELETE WHERE slot=? OR session_id=?`),
    /// keeping the sidebar `[N]` badge snappy without waiting for the next
    /// 5s ListSlotBindings refresh.
    private func locallyApplyBind(slot: Int, sessionID: String) {
        var next = slotBindings
        for (s, sid) in next where sid == sessionID { next.removeValue(forKey: s) }
        next[slot] = sessionID
        slotBindings = next
    }

    private func locallyApplyUnbind(slot: Int) {
        var next = slotBindings
        next.removeValue(forKey: slot)
        slotBindings = next
    }

    // ─── Mutator wiring (set by DaemonClientRunner) ──────────────────

    func attach(mutator: Mutator?) {
        self.mutator = mutator
    }

    // ─── Selection side-effect: ack on focus ─────────────────────────

    private func onSelectionChange(from old: Selection?) {
        // Ack-on-focus fires only when the selection transitions into a
        // *new* session that is finished + unacknowledged. Repo-header
        // selections never trigger an ack.
        guard case .session(let id) = selection,
              old != .session(id),
              let session = sessionsByID[id],
              session.status == .finished, !session.acknowledged
        else { return }
        Task { await dispatchAcknowledge(sessionID: id) }
    }

    // ─── Mutation dispatch (called from views) ───────────────────────

    func dispatchQuickApprove() async {
        guard let id = selectedSessionID,
              let session = sessionsByID[id],
              session.status == .waiting,
              let m = mutator
        else { return }
        // Optimistic flip: drop the row out of `waiting` immediately so the
        // user gets feedback that their Y landed. The next SessionUpdate
        // from the daemon (sub-second once we wired TriggerRefresh into the
        // hook watcher) reconciles to the real status — running while the
        // turn continues, finished when Stop fires.
        var optimistic = session
        optimistic.status = .running
        sessionsByID[id] = optimistic
        await run(label: "send keys") {
            try await m.sendKeys(sessionID: id, keys: ["y"], submit: true)
        }
    }

    func dispatchDelete(sessionID: String) async {
        guard let m = mutator else { return }
        await run(label: "delete") {
            try await m.delete(sessionID: sessionID)
        }
    }

    func dispatchRestart(sessionID: String) async {
        guard let m = mutator else { return }
        await run(label: "restart") {
            try await m.restart(sessionID: sessionID)
        }
    }

    func dispatchRename(sessionID: String, title: String) async {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let m = mutator else { return }
        await run(label: "rename") {
            try await m.rename(sessionID: sessionID, title: trimmed)
        }
    }

    func dispatchAcknowledge(sessionID: String) async {
        guard let m = mutator else { return }
        // Acknowledge is best-effort; failures aren't worth a toast.
        try? await m.acknowledge(sessionID: sessionID)
    }

    func dispatchPinRepo(root: String, pinned: Bool) async {
        guard let m = mutator else { return }
        await run(label: pinned ? "unpin" : "pin") {
            if pinned {
                try await m.unpinRepo(root: root)
            } else {
                try await m.pinRepo(root: root)
            }
        }
    }

    // ─── Creation ────────────────────────────────────────────────────

    /// Cmd-N when a repo or session is in the cursor. Title is derived
    /// from the repo's basename (matches TUI `app.go:1068-1080`).
    func dispatchCreateAtCursor() async {
        guard let m = mutator else { return }
        guard let root = cursorRepoRoot else {
            // No cursor → fall through to the path-picker sheet, mirroring
            // the TUI's `a → n` fallback.
            presentingNewSession = true
            return
        }
        let title = (root as NSString).lastPathComponent
        await run(label: "create session") {
            let new = try await m.createSession(title: title, projectPath: root)
            self.selection = .session(new.id)
        }
    }

    /// Cmd-Shift-N: user typed/picked a path in the New Session sheet.
    func dispatchCreateAtPath(_ path: String) async {
        guard let m = mutator else { return }
        let trimmed = path.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let resolved = (trimmed as NSString).expandingTildeInPath
        let title = (resolved as NSString).lastPathComponent
        await run(label: "create session") {
            let new = try await m.createSession(title: title, projectPath: resolved)
            self.selection = .session(new.id)
        }
    }

    /// Cmd-Opt-N: user submitted the New Worktree sheet. Adds a placeholder
    /// row immediately so the sheet can dismiss without leaving the user
    /// staring at an unchanged sidebar while the daemon clones the worktree.
    func dispatchCreateWorktree(repoRoot: String,
                                baseBranch: String,
                                newBranch: String) async {
        guard let m = mutator else {
            FleetLog.warn("dispatchCreateWorktree: no mutator (daemon link down?)")
            return
        }
        let trimmedBase = baseBranch.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedNew = newBranch.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedBase.isEmpty, !trimmedNew.isEmpty else {
            FleetLog.warn("dispatchCreateWorktree: empty input — base=\(trimmedBase) new=\(trimmedNew)")
            return
        }

        let placeholder = PendingCreation(
            id: UUID().uuidString,
            repoRoot: repoRoot,
            displayName: trimmedNew,
            kind: .worktree,
            startedAt: Date()
        )
        pendingCreations.append(placeholder)
        FleetLog.info("dispatchCreateWorktree: placeholder added pendingID=\(placeholder.id) repoRoot=\(repoRoot) displayName=\(trimmedNew)")

        do {
            _ = try await m.createWorkspace(
                repoRoot: repoRoot,
                name: trimmedNew,
                baseBranch: trimmedBase,
                newBranch: trimmedNew
            )
            FleetLog.info("dispatchCreateWorktree: RPC returned ok pendingID=\(placeholder.id) (waiting for stream to deliver session)")
            // Don't drop the placeholder here — wait for the SessionUpdate
            // stream to deliver the real row, then `reconcilePendingCreations`
            // removes our placeholder. If the daemon never delivers, the
            // 30s pending-guard cleans up.
        } catch {
            FleetLog.error("dispatchCreateWorktree: RPC failed pendingID=\(placeholder.id) err=\(error)")
            pendingCreations.removeAll { $0.id == placeholder.id }
            showErrorToast("create worktree: \(error)")
        }
    }

    // ─── Slot bindings ───────────────────────────────────────────────

    /// Cmd-Opt+0..9: bind currently-selected session into `slot`. If that
    /// slot already holds the selected session, toggle to unbind instead
    /// (matches TUI `app.go:2216-2218`). Daemon evicts conflicts;
    /// `locallyApplyBind` keeps the badge snappy until the next refresh.
    func dispatchBindSelected(toSlot slot: Int) async {
        guard let m = mutator, let id = selectedSessionID else { return }
        if slotBindings[slot] == id {
            await run(label: "unbind slot") {
                try await m.unbindSlot(slot)
                self.locallyApplyUnbind(slot: slot)
            }
        } else {
            await run(label: "bind slot") {
                try await m.bindSlot(slot: slot, sessionID: id)
                self.locallyApplyBind(slot: slot, sessionID: id)
            }
        }
    }

    /// Cmd+0..9: select the session bound to `slot`, no-op if unbound.
    /// Selection change auto-fires ack-on-focus from the existing didSet.
    func dispatchJumpToSlot(_ slot: Int) {
        guard let id = slotBindings[slot] else { return }
        selection = .session(id)
    }

    // ─── Pending-creation reconciliation ─────────────────────────────

    /// Called from `applySession` on every stream message. A pending
    /// worktree placeholder is removed when a real session shows up with a
    /// matching workspace name (the daemon uses the new-branch field as the
    /// workspace name, so they line up 1:1). We deliberately do NOT match on
    /// `repoRoot`: `git rev-parse --show-toplevel` from inside a worktree
    /// returns the worktree's own path, not the parent repo, so the pending
    /// placeholder and the new session would never agree on repoRoot.
    private func reconcilePendingCreations(against session: Session) {
        guard !pendingCreations.isEmpty else { return }
        let matched = pendingCreations.first(where: { p in
            p.displayName == session.workspaceName || p.displayName == session.title
        })
        guard let matched else {
            let pendingList = pendingCreations.map { $0.displayName }.joined(separator: ",")
            FleetLog.debug("reconcile: no match for session id=\(session.id) workspace=\(session.workspaceName) title=\(session.title) — pending=[\(pendingList)]")
            return
        }
        FleetLog.info("reconcile: matched pendingID=\(matched.id) displayName=\(matched.displayName) -> sessionID=\(session.id)")
        pendingCreations.removeAll { $0.id == matched.id }
        // Auto-select the freshly-created session so the user lands on it.
        selection = .session(session.id)
    }

    /// 30-second guard: drop placeholders whose `startedAt` is too old, so
    /// a daemon-side failure doesn't leave a permanent spinner row.
    private func dropStalePendingCreations() {
        guard !pendingCreations.isEmpty else { return }
        let cutoff = Date().addingTimeInterval(-30)
        let stale = pendingCreations.filter { $0.startedAt < cutoff }
        guard !stale.isEmpty else { return }
        pendingCreations.removeAll { p in stale.contains(where: { $0.id == p.id }) }
        showErrorToast("worktree creation timed out — check fleet daemon logs")
    }

    // Cmd-Shift-D: capture the daemon's status-detection snapshot and dump
    // it to ~/.config/fleet/snapshots/. Surfaces a toast with the saved
    // path on success, or the error string on failure.
    func dispatchSnapshot() async {
        guard let m = mutator else {
            showErrorToast("snapshot: daemon not connected")
            return
        }
        do {
            let markdown = try await m.diagnostics()
            let url = try SnapshotWriter.write(markdown: markdown)
            self.errorToast = "Snapshot saved: \(url.path)"
            errorToastClearer?.cancel()
            errorToastClearer = Task { [weak self] in
                try? await Task.sleep(for: .seconds(8))
                guard !Task.isCancelled else { return }
                self?.clearErrorToast()
            }
        } catch {
            showErrorToast("snapshot: \(error)")
        }
    }

    // ─── Toast helpers ───────────────────────────────────────────────

    private func run(label: String, op: () async throws -> Void) async {
        do {
            try await op()
        } catch {
            showErrorToast("\(label): \(error)")
        }
    }

    func showErrorToast(_ message: String) {
        self.errorToast = message
        errorToastClearer?.cancel()
        errorToastClearer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(5))
            guard !Task.isCancelled else { return }
            self?.clearErrorToast()
        }
    }

    private func clearErrorToast() {
        self.errorToast = nil
        self.errorToastClearer = nil
    }
}
