import SwiftUI

// FleetCommands wires the macOS menu bar's "Session" + "Slots" menus and
// exposes the V1 keyboard shortcuts. Items follow the gating used by the
// TUI: Approve only when Waiting; Bind/Restart/Rename/Delete only when a
// session is selected; New Session always available (falls back to the
// path-picker sheet if no cursor repo is set).
struct FleetCommands: Commands {
    @Bindable var model: AppModel

    var body: some Commands {
        // Empty File menu (drop the default New / Open / Save chrome — the
        // app has no documents, only sessions, and creation lives in the
        // Session menu).
        CommandGroup(replacing: .newItem) {}

        CommandMenu("Session") {
            Button("New Session") {
                Task { await model.dispatchCreateAtCursor() }
            }
            .keyboardShortcut("n", modifiers: .command)

            Button("New Session at Path…") {
                model.presentingNewSession = true
            }
            .keyboardShortcut("n", modifiers: [.command, .shift])

            Button("New Worktree Session…") {
                model.presentingNewWorktree = true
            }
            .keyboardShortcut("n", modifiers: [.command, .option])
            .disabled(model.cursorRepoRoot == nil)

            Divider()

            Button("Approve") {
                Task { await model.dispatchQuickApprove() }
            }
            .keyboardShortcut("y", modifiers: .command)
            .disabled(!canApprove)

            Divider()

            Button("Rename…") { startRename() }
                .keyboardShortcut("r", modifiers: [.command, .shift])
                .disabled(!hasSelection)

            Button("Restart") { restart() }
                .keyboardShortcut("r", modifiers: .command)
                .disabled(!hasSelection)

            Divider()

            Button("Delete…") { promptDelete() }
                .keyboardShortcut(.delete, modifiers: .command)
                .disabled(!hasSelection)

            Divider()

            Button("Save Diagnostics Snapshot") {
                Task { await model.dispatchSnapshot() }
            }
            .keyboardShortcut("d", modifiers: [.command, .shift])
        }

        CommandGroup(after: .appSettings) {
            Button("Settings…") {
                model.presentingSettings = true
            }
            .keyboardShortcut(",", modifiers: .command)
        }

        CommandMenu("View") {
            Button("Command Palette…") {
                model.presentingPalette = true
            }
            .keyboardShortcut("p", modifiers: [.command, .shift])

            Button("Filter Sessions") {
                model.requestFilterFocus()
            }
            .keyboardShortcut("f", modifiers: .command)

            Divider()

            Button("Bug Report…") {
                model.presentingBugReport = true
            }
            .keyboardShortcut("b", modifiers: [.command, .shift])

            Button("Keyboard Shortcuts") {
                model.presentingHelp = true
            }
            .keyboardShortcut("/", modifiers: [.command, .shift])
        }

        CommandMenu("Slots") {
            // Cmd+0..9 — jump to whatever session is bound to that slot.
            // No-op silently when the slot is unbound (matches TUI).
            Section("Jump") {
                ForEach(slotOrder, id: \.self) { slot in
                    Button(jumpLabel(for: slot)) {
                        model.dispatchJumpToSlot(slot)
                    }
                    .keyboardShortcut(KeyEquivalent(slotKeyChar(slot)),
                                      modifiers: .command)
                }
            }

            // Cmd-Opt+0..9 — bind selected session to that slot. Re-press
            // on the same slot toggles unbind. Daemon evicts conflicts.
            Section("Bind Selected To") {
                ForEach(slotOrder, id: \.self) { slot in
                    Button(bindLabel(for: slot)) {
                        Task { await model.dispatchBindSelected(toSlot: slot) }
                    }
                    .keyboardShortcut(KeyEquivalent(slotKeyChar(slot)),
                                      modifiers: [.command, .option])
                    .disabled(!hasSelection)
                }
            }
        }
    }

    // Slots are displayed 1..9 then 0 to match Cmd+1..9, Cmd+0 conventions
    // (Safari / Chrome tab switching).
    private let slotOrder: [Int] = [1, 2, 3, 4, 5, 6, 7, 8, 9, 0]

    private func slotKeyChar(_ slot: Int) -> Character {
        Character("\(slot)")
    }

    private func jumpLabel(for slot: Int) -> String {
        if let id = model.slotBindings[slot],
           let session = model.sessionsByID[id] {
            return "Slot \(slot) — \(session.title)"
        }
        return "Slot \(slot) (unbound)"
    }

    private func bindLabel(for slot: Int) -> String {
        if model.slotBindings[slot] != nil,
           model.slotBindings[slot] == model.selectedSessionID {
            return "Slot \(slot) (unbind)"
        }
        return "Slot \(slot)"
    }

    private var hasSelection: Bool {
        model.selectedSessionID != nil
    }

    private var canApprove: Bool {
        guard let id = model.selectedSessionID,
              let session = model.sessionsByID[id]
        else { return false }
        return session.status == .waiting
    }

    private func startRename() {
        guard let id = model.selectedSessionID else { return }
        model.renamingSessionID = id
    }

    private func restart() {
        guard let id = model.selectedSessionID else { return }
        Task { await model.dispatchRestart(sessionID: id) }
    }

    private func promptDelete() {
        guard let id = model.selectedSessionID,
              let session = model.sessionsByID[id]
        else { return }
        model.pendingDeletion = session
    }
}
