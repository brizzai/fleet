import Foundation
import GRPCCore
import SwiftProtobuf

// Mutator is the read-write counterpart to the streaming consumers in
// `StreamConsumers.swift`: it wraps a live `FleetFleet.ClientProtocol`
// reference and exposes the mutation RPCs the V1 UI needs as plain async
// throws methods. AppModel holds a `Mutator?` once the gRPC client is open,
// and clears it when the daemon link drops.
//
// Errors are surfaced to the UI by the caller (see AppModel.dispatch*) — we
// don't translate them here so the toast can show the raw daemon message,
// which is the same shape the TUI's error history captures.
final class Mutator: Sendable {
    private let client: any FleetFleet.ClientProtocol

    init(client: any FleetFleet.ClientProtocol) {
        self.client = client
    }

    func sendKeys(sessionID: String, keys: [String], submit: Bool) async throws {
        var req = FleetSendKeysRequest()
        req.sessionID = sessionID
        req.keys = keys
        req.submit = submit
        _ = try await client.sendKeys(req)
    }

    func delete(sessionID: String,
                option: FleetDeleteOption = .sessionOnly,
                deferTmuxKill: Bool = false) async throws {
        var req = FleetDeleteSessionRequest()
        req.id = sessionID
        req.option = option
        req.deferTmuxKill = deferTmuxKill
        _ = try await client.deleteSession(req)
    }

    func restart(sessionID: String) async throws {
        var req = FleetRestartSessionRequest()
        req.id = sessionID
        _ = try await client.restartSession(req)
    }

    func rename(sessionID: String, title: String) async throws {
        var req = FleetRenameSessionRequest()
        req.id = sessionID
        req.title = title
        _ = try await client.renameSession(req)
    }

    func acknowledge(sessionID: String) async throws {
        var req = FleetAcknowledgeSessionRequest()
        req.id = sessionID
        _ = try await client.acknowledgeSession(req)
    }

    func pinRepo(root: String) async throws {
        var req = FleetPinRepoRequest()
        req.root = root
        _ = try await client.pinRepo(req)
    }

    func unpinRepo(root: String) async throws {
        var req = FleetUnpinRepoRequest()
        req.root = root
        _ = try await client.unpinRepo(req)
    }

    // ─── Creation ────────────────────────────────────────────────────
    // CreateSession is synchronous: the daemon allocates the SQLite row,
    // spawns tmux + claude, and returns the populated `Session` proto in
    // <100ms. The new row also arrives via ListSessions stream — caller
    // can ignore the return value if it wants to wait for stream delivery.
    @discardableResult
    func createSession(title: String,
                       projectPath: String,
                       workspaceName: String = "",
                       forkFromID: String = "") async throws -> Session {
        var req = FleetCreateSessionRequest()
        req.title = title
        req.projectPath = projectPath
        req.workspaceName = workspaceName
        req.forkFromID = forkFromID
        let proto = try await client.createSession(req)
        return Convert.toSession(proto)
    }

    struct WorkspaceList {
        var workspaces: [WorkspaceEntry]
        var providerName: String
    }

    struct WorkspaceEntry: Identifiable, Hashable {
        var id: String { name }
        let name: String
        let path: String
        let branch: String
        let status: String
    }

    func listWorkspaces(repoRoot: String) async throws -> WorkspaceList {
        var req = FleetListWorkspacesRequest()
        req.repoRoot = repoRoot
        let resp = try await client.listWorkspaces(req)
        let entries = resp.workspaces.map { w in
            WorkspaceEntry(name: w.name, path: w.path, branch: w.branch, status: w.status)
        }
        return WorkspaceList(workspaces: entries, providerName: resp.providerName)
    }

    // CreateWorkspace returns immediately with a `pending_id` — the actual
    // git-worktree clone + session spawn happens after, and arrives via the
    // session/repo streams. Callers should drop a PendingCreation row in
    // the sidebar and reconcile when the new session shows up.
    @discardableResult
    func createWorkspace(repoRoot: String,
                         name: String,
                         baseBranch: String,
                         newBranch: String) async throws -> String {
        var req = FleetCreateWorkspaceRequest()
        req.repoRoot = repoRoot
        req.name = name
        req.baseBranch = baseBranch
        req.newBranch = newBranch
        let resp = try await client.createWorkspace(req)
        return resp.pendingID
    }

    // DestroyWorkspace asks the daemon to remove the underlying worktree
    // (or run the custom shell-out destroy command). Caller is responsible
    // for stopping/deleting any sessions that point at the workspace first
    // — `git worktree remove --force` succeeds but leaves orphaned panes.
    func destroyWorkspace(repoRoot: String, name: String) async throws {
        var req = FleetDestroyWorkspaceRequest()
        req.repoRoot = repoRoot
        req.name = name
        _ = try await client.destroyWorkspace(req)
    }

    // ─── Slot bindings ───────────────────────────────────────────────
    // Daemon-side semantics (storage.go:348-367): a single bind RPC evicts
    // any prior occupant of that slot AND any prior slot of that session,
    // so callers don't need to clean up before binding.
    func bindSlot(slot: Int, sessionID: String) async throws {
        var req = FleetBindSlotRequest()
        req.slot = Int32(slot)
        req.sessionID = sessionID
        _ = try await client.bindSlot(req)
    }

    func unbindSlot(_ slot: Int) async throws {
        var req = FleetUnbindSlotRequest()
        req.slot = Int32(slot)
        _ = try await client.unbindSlot(req)
    }

    // ─── Config ──────────────────────────────────────────────────────
    // Read-only; used by NewSessionSheet to pre-fill the path field.
    func getConfig() async throws -> FleetConfig {
        try await client.getConfig(Google_Protobuf_Empty())
    }

    // ─── Diagnostics ─────────────────────────────────────────────────
    // GetDiagnostics is read-only and lives here purely so consumers have
    // a single object to call into. Returns the markdown blob the daemon
    // prepared (per-session anti-flicker state, recent worker cycles,
    // hook events, status transitions). Tied to the Mac app's Cmd-Shift-D.
    func diagnostics() async throws -> String {
        let resp = try await client.getDiagnostics(Google_Protobuf_Empty())
        return resp.markdown
    }
}
