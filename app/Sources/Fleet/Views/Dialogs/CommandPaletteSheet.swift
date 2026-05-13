import AppKit
import SwiftUI

// CommandPaletteSheet is the Cmd-Shift-P target: a Spotlight-style fuzzy
// finder over every Mac-exposed action. Mirrors the TUI's `:` / Ctrl+P
// dialog (`internal/ui/command_palette.go`). Keyboard-driven — ↑↓ navigate,
// Enter dispatches, Esc cancels.
//
// Action set is a subset of the TUI's 19 commands: we include only actions
// that have a Mac implementation today. Fork/editor/branch/open-PR/focus are
// excluded until they ship; "Reload All Sessions" is palette-only.
struct CommandPaletteSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var query: String = ""
    @State private var cursor: Int = 0
    @FocusState private var fieldFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .foregroundStyle(.secondary)
                TextField("Type a command…", text: $query)
                    .textFieldStyle(.plain)
                    .focused($fieldFocused)
                    .font(.title3)
                    .onSubmit { dispatch() }
                    .onKeyPress(.upArrow) { moveCursor(by: -1); return .handled }
                    .onKeyPress(.downArrow) { moveCursor(by: 1); return .handled }
                    .onChange(of: query) { _, _ in cursor = 0 }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)

            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(filtered.enumerated()), id: \.element.id) { idx, cmd in
                            row(cmd: cmd, selected: idx == cursor)
                                .id(cmd.id)
                                .onTapGesture {
                                    cursor = idx
                                    dispatch()
                                }
                        }
                    }
                }
                .frame(maxHeight: 380)
                .onChange(of: cursor) { _, _ in
                    if let item = filtered.indices.contains(cursor) ? filtered[cursor] : nil {
                        withAnimation(.linear(duration: 0.05)) {
                            proxy.scrollTo(item.id, anchor: .center)
                        }
                    }
                }
            }
        }
        .frame(width: 560)
        .onAppear { fieldFocused = true }
    }

    private func row(cmd: PaletteCommand, selected: Bool) -> some View {
        HStack {
            Text(cmd.label)
            Spacer()
            if !cmd.shortcut.isEmpty {
                Text(cmd.shortcut)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
        .background(selected ? model.currentTheme.accentColor.opacity(0.25) : Color.clear)
    }

    private func moveCursor(by delta: Int) {
        let n = filtered.count
        guard n > 0 else { return }
        cursor = max(0, min(n - 1, cursor + delta))
    }

    private func dispatch() {
        guard filtered.indices.contains(cursor) else { return }
        let cmd = filtered[cursor]
        dismiss()
        // Defer to avoid running the action while the sheet is mid-dismiss
        // — important for actions that present another sheet.
        DispatchQueue.main.async {
            cmd.action(model)
        }
    }

    private var filtered: [PaletteCommand] {
        let cmds = PaletteCommand.all
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return cmds }
        let scored = cmds.compactMap { cmd -> (PaletteCommand, Int)? in
            let m = FuzzyMatch.match(query: q, target: cmd.label)
            return m.matches ? (cmd, m.score) : nil
        }
        return scored.sorted { $0.1 > $1.1 }.map { $0.0 }
    }
}

// PaletteCommand is the Mac-side mirror of the TUI's PaletteCommand. The
// `action` closure resolves dispatch inline so the sheet doesn't need a
// switch over command IDs — keeps adding commands a one-line affair.
struct PaletteCommand: Identifiable {
    let id: String
    let label: String
    let shortcut: String
    let enabled: @MainActor (AppModel) -> Bool
    let action: @MainActor (AppModel) -> Void

    @MainActor
    static let all: [PaletteCommand] = [
        PaletteCommand(
            id: "new_session", label: "New Session", shortcut: "⌘N",
            enabled: { _ in true },
            action: { m in Task { await m.dispatchCreateAtCursor() } }
        ),
        PaletteCommand(
            id: "new_repo", label: "New Session at Path…", shortcut: "⌘⇧N",
            enabled: { _ in true },
            action: { m in m.presentingNewSession = true }
        ),
        PaletteCommand(
            id: "new_worktree", label: "New Worktree Session…", shortcut: "⌘⌥N",
            enabled: { m in m.cursorRepoRoot != nil },
            action: { m in
                if m.cursorRepoRoot != nil { m.presentingNewWorktree = true }
            }
        ),
        PaletteCommand(
            id: "approve", label: "Quick Approve", shortcut: "⌘Y",
            enabled: { m in
                guard let id = m.selectedSessionID,
                      let s = m.sessionsByID[id] else { return false }
                return s.status == .waiting
            },
            action: { m in Task { await m.dispatchQuickApprove() } }
        ),
        PaletteCommand(
            id: "restart", label: "Restart Session", shortcut: "⌘R",
            enabled: { m in m.selectedSessionID != nil },
            action: { m in
                guard let id = m.selectedSessionID else { return }
                Task { await m.dispatchRestart(sessionID: id) }
            }
        ),
        PaletteCommand(
            id: "rename", label: "Rename Session", shortcut: "⌘⇧R",
            enabled: { m in m.selectedSessionID != nil },
            action: { m in m.renamingSessionID = m.selectedSessionID }
        ),
        PaletteCommand(
            id: "delete", label: "Delete Session", shortcut: "⌘⌫",
            enabled: { m in m.selectedSessionID != nil },
            action: { m in
                guard let id = m.selectedSessionID,
                      let sess = m.sessionsByID[id] else { return }
                m.pendingDeletion = sess
            }
        ),
        PaletteCommand(
            id: "filter", label: "Filter Sessions", shortcut: "⌘F",
            enabled: { _ in true },
            action: { m in m.requestFilterFocus() }
        ),
        PaletteCommand(
            id: "settings", label: "Settings", shortcut: "⌘,",
            enabled: { _ in true },
            action: { m in m.presentingSettings = true }
        ),
        PaletteCommand(
            id: "bug_report", label: "Bug Report", shortcut: "⌘⇧B",
            enabled: { _ in true },
            action: { m in m.presentingBugReport = true }
        ),
        PaletteCommand(
            id: "help", label: "Keyboard Shortcuts", shortcut: "⌘⇧/",
            enabled: { _ in true },
            action: { m in m.presentingHelp = true }
        ),
        PaletteCommand(
            id: "snapshot", label: "Save Diagnostics Snapshot", shortcut: "⌘⇧D",
            enabled: { _ in true },
            action: { m in Task { await m.dispatchSnapshot() } }
        ),
        // Palette-only: there's no menu/keyboard entry, lives only here.
        PaletteCommand(
            id: "reload_all", label: "Reload All Sessions", shortcut: "",
            enabled: { _ in true },
            action: { m in Task { await m.dispatchReloadAllSessions() } }
        ),
        PaletteCommand(
            id: "quit", label: "Quit Fleet", shortcut: "⌘Q",
            enabled: { _ in true },
            action: { _ in NSApp.terminate(nil) }
        ),
    ]
}
