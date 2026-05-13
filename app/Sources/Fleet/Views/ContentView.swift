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
            // Worktree sessions get a second destructive button that also
            // tears down the worktree on disk. Mirrors the TUI's `Y` option
            // on the delete confirm dialog (`internal/ui/app.go:1417-1432`).
            if !session.workspaceName.isEmpty {
                Button("Delete + destroy worktree", role: .destructive) {
                    let id = session.id
                    let root = session.repoRoot
                    let ws = session.workspaceName
                    model.pendingDeletion = nil
                    Task { await model.dispatchDeleteAndDestroyWorkspace(sessionID: id, repoRoot: root, workspaceName: ws) }
                }
            }
        } message: { session in
            if session.workspaceName.isEmpty {
                Text("\(session.title) will be removed. The tmux pane is killed.")
            } else {
                Text("\(session.title) will be removed. The tmux pane is killed. Choose 'Delete + destroy worktree' to also remove the underlying git worktree (\(session.workspaceName)).")
            }
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
        .sheet(isPresented: $model.presentingSettings) {
            SettingsSheet(model: model)
        }
        .sheet(isPresented: $model.presentingPalette) {
            CommandPaletteSheet(model: model)
        }
        .sheet(isPresented: $model.presentingBugReport) {
            BugReportSheet(model: model)
        }
        .sheet(isPresented: $model.presentingHelp) {
            HelpSheet(model: model)
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
            bannerRow(text: msg, tint: model.currentTheme.redColor)
                .transition(.move(edge: .top).combined(with: .opacity))
        }
    }

    @ViewBuilder
    private var disconnectBanner: some View {
        switch model.connectionState {
        case .reconnecting:
            bannerRow(text: "Daemon disconnected — reconnecting…", tint: model.currentTheme.orangeColor)
        case .disconnected:
            bannerRow(text: model.lastError ?? "Daemon unavailable.", tint: model.currentTheme.redColor)
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
            if session.isAlive, session.tmuxName != nil {
                TerminalPaneView(session: session, theme: model.currentTheme)
                    // .id keys force a fresh NSViewRepresentable when EITHER
                    // the session OR the theme changes — SwiftTerm has no
                    // clean live-recolor API, so a recreate is cheaper than
                    // a complex stateful recolor path.
                    .id("\(session.id)-\(model.currentTheme.name)")
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
            Button("Restart Session") {
                Task { await model.dispatchRestart(sessionID: session.id) }
            }
            .keyboardShortcut("r", modifiers: .command)
            .controlSize(.large)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
