import SwiftUI

struct ContentView: View {
    @Bindable var model: AppModel

    var body: some View {
        VStack(spacing: 0) {
            disconnectBanner
            errorToast
            NavigationSplitView {
                SidebarView(model: model)
                    .navigationSplitViewColumnWidth(min: 200, ideal: 260, max: 360)
            } detail: {
                detailPane
            }
        }
        .alert("Delete session?",
               isPresented: deletionBinding,
               presenting: model.pendingDeletion) { session in
            Button("Cancel", role: .cancel) { model.pendingDeletion = nil }
            Button("Delete", role: .destructive) {
                let id = session.id
                model.pendingDeletion = nil
                Task { await model.dispatchDelete(sessionID: id) }
            }
        } message: { session in
            Text("\(session.title) will be removed. The tmux pane is killed.")
        }
        .sheet(isPresented: $model.presentingNewSession) {
            NewSessionSheet(model: model)
        }
        .sheet(isPresented: $model.presentingNewWorktree) {
            if let root = model.cursorRepoRoot {
                NewWorktreeSheet(model: model, repoRoot: root)
            } else {
                // Defensive: the menu item is gated on cursorRepoRoot, but
                // a hotkey collision could still trigger this branch.
                VStack(spacing: 12) {
                    Text("Select a repo first.")
                    Button("OK") { model.presentingNewWorktree = false }
                }
                .padding(40)
            }
        }
    }

    private var deletionBinding: Binding<Bool> {
        Binding(
            get: { model.pendingDeletion != nil },
            set: { if !$0 { model.pendingDeletion = nil } }
        )
    }

    @ViewBuilder
    private var errorToast: some View {
        if let msg = model.errorToast {
            bannerRow(text: msg, tint: .red)
                .transition(.move(edge: .top).combined(with: .opacity))
        }
    }

    @ViewBuilder
    private var disconnectBanner: some View {
        switch model.connectionState {
        case .reconnecting:
            bannerRow(text: "Daemon disconnected — reconnecting…", tint: .orange)
        case .disconnected:
            bannerRow(text: model.lastError ?? "Daemon unavailable.", tint: .red)
        case .connecting, .connected:
            EmptyView()
        }
    }

    private func bannerRow(text: String, tint: Color) -> some View {
        HStack {
            Image(systemName: "exclamationmark.triangle.fill")
            Text(text)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer()
        }
        .font(.caption)
        .foregroundStyle(tint)
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(tint.opacity(0.12))
    }

    @ViewBuilder
    private var detailPane: some View {
        if let id = model.selectedSessionID,
           let session = model.sessionsByID[id] {
            if session.isAlive, let _ = session.tmuxName {
                TerminalPaneView(session: session)
                    .id(session.id)  // force a fresh NSViewRepresentable on session swap
            } else {
                deadSessionPlaceholder(session: session)
            }
        } else {
            EmptyStateView()
        }
    }

    private func deadSessionPlaceholder(session: Session) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "xmark.octagon")
                .font(.system(size: 40))
                .foregroundStyle(.red.opacity(0.7))
            Text(session.title)
                .font(.title3)
            Text("This session's tmux pane is no longer alive.")
                .foregroundStyle(.secondary)
            Text("Restart will be available in the next slice.")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
