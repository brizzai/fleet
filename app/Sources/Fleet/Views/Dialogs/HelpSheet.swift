import SwiftUI

// HelpSheet is the Cmd-Shift-/ target: a static cheatsheet of every Mac
// keyboard shortcut grouped by category. The TUI's `?` dialog is a
// scrollable text dump; on Mac we use a structured layout so the user can
// scan visually instead of reading prose.
struct HelpSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Keyboard Shortcuts")
                    .font(.title2)
                Spacer()
                Button("Close") { dismiss() }
                    .keyboardShortcut(.cancelAction)
            }

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(Self.groups, id: \.title) { group in
                        section(group)
                    }
                }
            }
        }
        .padding(20)
        .frame(width: 520, height: 560)
    }

    private func section(_ group: ShortcutGroup) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(group.title)
                .font(.headline)
                .foregroundStyle(model.currentTheme.accentColor)
            ForEach(group.entries, id: \.label) { entry in
                HStack {
                    Text(entry.label)
                    Spacer()
                    Text(entry.shortcut)
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    struct ShortcutGroup {
        let title: String
        let entries: [(label: String, shortcut: String)]
    }

    static let groups: [ShortcutGroup] = [
        ShortcutGroup(title: "Sessions", entries: [
            ("New session at cursor", "⌘N"),
            ("New session at path…", "⌘⇧N"),
            ("New worktree session…", "⌘⌥N"),
            ("Quick approve", "⌘Y"),
            ("Restart session", "⌘R"),
            ("Rename session", "⌘⇧R"),
            ("Delete session", "⌘⌫"),
        ]),
        ShortcutGroup(title: "Slots", entries: [
            ("Jump to slot 1–9, 0", "⌘1…⌘9, ⌘0"),
            ("Bind selected to slot", "⌘⌥1…⌘⌥9, ⌘⌥0"),
        ]),
        ShortcutGroup(title: "View", entries: [
            ("Command palette", "⌘⇧P"),
            ("Filter sessions", "⌘F"),
            ("Settings", "⌘,"),
            ("Bug report", "⌘⇧B"),
            ("Keyboard shortcuts (this sheet)", "⌘⇧/"),
            ("Save diagnostics snapshot", "⌘⇧D"),
        ]),
        ShortcutGroup(title: "App", entries: [
            ("Hide Fleet", "⌘H"),
            ("Quit Fleet", "⌘Q"),
        ]),
    ]
}
