import SwiftUI
import AppKit

// NewSessionSheet is the Cmd-Shift-N target: pick any directory and spawn
// a session in it. Mirrors the TUI's `n` dialog at
// `internal/ui/dialogs.go:29-266`, minus the Tab-cycling autocomplete (the
// Browse… button covers the discoverability gap; autocomplete can land
// later if it feels missing).
struct NewSessionSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var path: String = ""
    @State private var pathLoadAttempted: Bool = false
    @FocusState private var pathFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("New Session")
                .font(.headline)

            Text("Pick a directory. The session is named after the directory's basename.")
                .font(.caption)
                .foregroundStyle(.secondary)

            HStack(spacing: 8) {
                TextField("~/code/my-project", text: $path)
                    .textFieldStyle(.roundedBorder)
                    .focused($pathFocused)
                    .onSubmit { submit() }

                Button("Browse…") { browse() }
            }

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
        .frame(width: 460)
        .task {
            guard !pathLoadAttempted else { return }
            pathLoadAttempted = true
            // Pre-fill from the daemon config's default_project_path. Falls
            // back to empty silently if GetConfig fails or the field is
            // unset — the user can still type or Browse.
            if let m = model.mutator, path.isEmpty {
                if let cfg = try? await m.getConfig(),
                   !cfg.defaultProjectPath.isEmpty {
                    path = cfg.defaultProjectPath
                }
            }
            pathFocused = true
        }
    }

    private var isValid: Bool {
        let trimmed = path.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return false }
        let resolved = (trimmed as NSString).expandingTildeInPath
        var isDir: ObjCBool = false
        let exists = FileManager.default.fileExists(atPath: resolved, isDirectory: &isDir)
        return exists && isDir.boolValue
    }

    private func submit() {
        guard isValid else { return }
        let value = path
        dismiss()
        Task { await model.dispatchCreateAtPath(value) }
    }

    private func browse() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = false
        panel.prompt = "Select Project"
        if let initial = currentDirectoryURL() {
            panel.directoryURL = initial
        }
        if panel.runModal() == .OK, let url = panel.url {
            path = url.path
            pathFocused = true
        }
    }

    private func currentDirectoryURL() -> URL? {
        let trimmed = path.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let resolved = (trimmed as NSString).expandingTildeInPath
        var isDir: ObjCBool = false
        if FileManager.default.fileExists(atPath: resolved, isDirectory: &isDir), isDir.boolValue {
            return URL(fileURLWithPath: resolved, isDirectory: true)
        }
        return nil
    }
}
