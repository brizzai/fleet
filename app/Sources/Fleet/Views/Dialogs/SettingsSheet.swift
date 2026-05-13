import SwiftUI

// SettingsSheet is the Cmd-, target: edit theme + editor + auto-name +
// copy-claude-settings + tick interval, persist via the daemon's UpdateConfig
// RPC. Mirrors the TUI's S-key dialog (`internal/ui/settings.go`). Theme
// preview is live — selecting a new theme updates currentConfig on the model
// immediately so the sidebar and terminal both re-render before the user
// commits via Done. If they Cancel we revert to the original.
struct SettingsSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var working: AppConfig
    @State private var original: AppConfig
    @State private var saving: Bool = false

    init(model: AppModel) {
        self.model = model
        let cfg = model.currentConfig
        _working = State(initialValue: cfg)
        _original = State(initialValue: cfg)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Settings")
                .font(.title2)

            Form {
                Picker("Theme", selection: $working.theme) {
                    ForEach(Theme.all, id: \.name) { palette in
                        Text(palette.displayName).tag(palette.name)
                    }
                }
                .onChange(of: working.theme) { _, _ in
                    // Live preview: poke currentConfig so the sidebar and
                    // SwiftTerm pane both re-render with the new palette.
                    var preview = working
                    preview.theme = working.theme
                    Task { await model.previewTheme(preview.theme) }
                }

                TextField("Editor", text: $working.editor, prompt: Text("code, vim, etc."))

                Stepper(value: $working.tickIntervalSec, in: 1...30) {
                    Text("Status refresh interval: \(working.tickIntervalSec)s")
                }

                Toggle("Auto-name sessions from first prompt", isOn: $working.autoNameSessions)

                Toggle("Copy .claude/settings.local.json to new worktrees", isOn: $working.copyClaudeSettings)
            }
            .formStyle(.grouped)

            HStack {
                Spacer()
                Button("Cancel", role: .cancel) {
                    revertAndDismiss()
                }
                .keyboardShortcut(.cancelAction)

                Button(saving ? "Saving…" : "Done") {
                    Task { await save() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving)
            }
        }
        .padding(20)
        .frame(width: 520)
    }

    private func save() async {
        saving = true
        await model.dispatchUpdateConfig(working)
        saving = false
        dismiss()
    }

    private func revertAndDismiss() {
        // Theme is the only field that has live side effects; restore it so
        // closing without saving doesn't leave the chrome on a preview value.
        if original.theme != working.theme {
            Task { await model.previewTheme(original.theme) }
        }
        dismiss()
    }
}
