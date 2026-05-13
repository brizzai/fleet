import SwiftUI

// NewWorktreeSheet is the Cmd-Opt-N target: create a new git worktree off
// the cursor repo. Mirrors the TUI's `w` dialog at
// `internal/ui/workspace_picker.go:44-304`. Existing worktrees are listed
// below the inputs; clicking one switches to its session instead of
// creating a new one.
struct NewWorktreeSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    let repoRoot: String

    @State private var baseBranch: String = ""
    @State private var newBranch: String = ""
    @State private var existing: [Mutator.WorkspaceEntry] = []
    @State private var loadingExisting: Bool = true
    @State private var loadError: String?
    @FocusState private var newBranchFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("New Worktree")
                .font(.headline)

            Text(repoDisplayName)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)

            Form {
                TextField("Base branch", text: $baseBranch)
                TextField("New branch", text: $newBranch)
                    .focused($newBranchFocused)
                    .onSubmit { submit() }
            }
            .formStyle(.columns)

            Divider()

            existingWorktreesSection

            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Create") { submit() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!isValid)
            }
        }
        .padding(20)
        .frame(width: 520, height: 420)
        .task { await loadExisting() }
        .onAppear {
            if baseBranch.isEmpty {
                baseBranch = model.reposByRoot[repoRoot]?.branch ?? "main"
            }
            newBranchFocused = true
        }
    }

    private var repoDisplayName: String {
        model.reposByRoot[repoRoot]?.displayName ?? repoRoot
    }

    private var isValid: Bool {
        !baseBranch.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !newBranch.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    @ViewBuilder
    private var existingWorktreesSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Existing worktrees")
                .font(.caption.bold())
                .foregroundStyle(.secondary)

            if loadingExisting {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Loading…").font(.caption).foregroundStyle(.secondary)
                }
            } else if let err = loadError {
                Text(err).font(.caption).foregroundStyle(.red)
            } else if existing.isEmpty {
                Text("None.").font(.caption).foregroundStyle(.tertiary)
            } else {
                List(existing) { entry in
                    HStack(spacing: 6) {
                        Text(entry.name)
                            .font(.body.monospaced())
                        if !entry.branch.isEmpty {
                            Text("(\(entry.branch))")
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        if !entry.status.isEmpty {
                            Text(entry.status)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .contentShape(Rectangle())
                    .onTapGesture { attach(to: entry) }
                }
                .listStyle(.plain)
                .frame(minHeight: 80)
            }
        }
    }

    private func loadExisting() async {
        guard let m = model.mutator else {
            loadingExisting = false
            return
        }
        do {
            let resp = try await m.listWorkspaces(repoRoot: repoRoot)
            self.existing = resp.workspaces
            self.loadingExisting = false
        } catch {
            self.loadError = "Could not load: \(error)"
            self.loadingExisting = false
        }
    }

    private func attach(to entry: Mutator.WorkspaceEntry) {
        // Picking an existing worktree should "open" it: jump to the live
        // session if one already exists, otherwise spawn a fresh session at
        // the worktree's path (matches TUI `app.go:478-483`). Matching by
        // projectPath rather than repoRoot — `git rev-parse --show-toplevel`
        // inside a worktree returns the worktree path, so a worktree session's
        // `repoRoot` does NOT equal the parent repo we have in `repoRoot`.
        let match = model.allSessions.first(where: { $0.projectPath == entry.path })
        if let session = match {
            FleetLog.info("worktree picker: attaching to existing session id=\(session.id) path=\(entry.path)")
            model.selection = .session(session.id)
            dismiss()
            return
        }
        FleetLog.info("worktree picker: no session for worktree path=\(entry.path) — creating new session")
        dismiss()
        Task { await model.dispatchCreateAtPath(entry.path) }
    }

    private func submit() {
        guard isValid else { return }
        let base = baseBranch
        let new = newBranch
        let root = repoRoot
        dismiss()
        Task { await model.dispatchCreateWorktree(repoRoot: root, baseBranch: base, newBranch: new) }
    }
}
