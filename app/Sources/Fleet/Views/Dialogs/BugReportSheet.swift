import AppKit
import SwiftUI

// BugReportSheet is the Cmd-Shift-B target: assemble a bug report (description
// + recent errors + recent actions + Mac diagnostics + daemon GetDiagnostics)
// and submit it via `gh issue create`. Mirrors the TUI's `!` dialog
// (`internal/ui/bugreport.go`).
//
// If `gh` is missing or fails (no network, auth not set up), we fall back to
// copying the full markdown body to the clipboard so the user can paste it
// into a manually-opened issue.
struct BugReportSheet: View {
    @Bindable var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var description: String = ""
    @State private var daemonDiagnostics: String = ""
    @State private var loadingDiagnostics: Bool = true
    @State private var submitting: Bool = false
    @State private var ghAvailable: Bool = CommandRunner.isAvailable("gh")

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("Report a Bug")
                    .font(.title2)
                Spacer()
                if !ghAvailable {
                    Label("gh not found", systemImage: "exclamationmark.triangle")
                        .font(.caption)
                        .foregroundStyle(.orange)
                }
            }

            Text("Describe what happened")
                .font(.subheadline).foregroundStyle(.secondary)
            TextEditor(text: $description)
                .font(.body)
                .frame(minHeight: 80, maxHeight: 120)
                .overlay(
                    RoundedRectangle(cornerRadius: 4)
                        .stroke(model.currentTheme.borderColor, lineWidth: 1)
                )

            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    section("Recent Errors") {
                        if model.errorHistory.isEmpty {
                            Text("None").font(.caption).foregroundStyle(.tertiary)
                        } else {
                            ForEach(model.errorHistory.suffix(5).reversed()) { e in
                                row(time: e.timestamp, message: e.message, detail: e.context)
                            }
                        }
                    }

                    section("Recent Actions") {
                        if model.actionLog.isEmpty {
                            Text("None").font(.caption).foregroundStyle(.tertiary)
                        } else {
                            ForEach(model.actionLog.suffix(10).reversed()) { a in
                                actionRow(a)
                            }
                        }
                    }

                    section("Daemon Diagnostics") {
                        if loadingDiagnostics {
                            HStack {
                                ProgressView().controlSize(.small)
                                Text("Loading…").font(.caption).foregroundStyle(.secondary)
                            }
                        } else if daemonDiagnostics.isEmpty {
                            Text("Unavailable").font(.caption).foregroundStyle(.tertiary)
                        } else {
                            ScrollView {
                                Text(daemonDiagnostics)
                                    .font(.system(size: 11, design: .monospaced))
                                    .textSelection(.enabled)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(8)
                            }
                            .frame(maxHeight: 200)
                            .background(model.currentTheme.surfaceColor)
                            .cornerRadius(4)
                        }
                    }
                }
            }

            Divider()

            HStack {
                Button("Copy to Clipboard") { copyToClipboard() }
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(submitting ? "Submitting…" : "Open GitHub Issue") {
                    Task { await submit() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!ghAvailable || submitting)
            }
        }
        .padding(20)
        .frame(width: 640, height: 720)
        .task { await loadDiagnostics() }
    }

    private func section<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.headline)
            content()
        }
    }

    private func row(time: Date, message: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(formatted(time))
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .frame(width: 60, alignment: .leading)
            VStack(alignment: .leading, spacing: 2) {
                Text(message).font(.caption)
                if !detail.isEmpty {
                    Text(detail).font(.caption2).foregroundStyle(.tertiary)
                }
            }
        }
    }

    private func actionRow(_ a: ActionEntry) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(formatted(a.timestamp))
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .frame(width: 60, alignment: .leading)
            Text(a.action).font(.caption.bold())
            Text(a.detail).font(.caption).foregroundStyle(.secondary)
            Spacer()
            if let s = a.success {
                Image(systemName: s ? "checkmark.circle.fill" : "xmark.circle.fill")
                    .foregroundStyle(s ? model.currentTheme.greenColor : model.currentTheme.redColor)
                    .font(.caption)
            }
        }
    }

    private func formatted(_ d: Date) -> String {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f.string(from: d)
    }

    private func loadDiagnostics() async {
        guard let m = model.mutator else {
            loadingDiagnostics = false
            return
        }
        do {
            daemonDiagnostics = try await m.diagnostics()
        } catch {
            FleetLog.warn("bug report: diagnostics fetch failed err=\(error)")
            daemonDiagnostics = "Could not load daemon diagnostics: \(error)"
        }
        loadingDiagnostics = false
    }

    private func submit() async {
        submitting = true
        defer { submitting = false }
        let body = assembleBody()
        let title = String(description.trimmingCharacters(in: .whitespacesAndNewlines).prefix(70))
        let finalTitle = title.isEmpty ? "Fleet bug report" : title

        do {
            let tmp = try writeTemp(body: body)
            defer { try? FileManager.default.removeItem(at: tmp) }
            let result = try CommandRunner.run("gh", args: [
                "issue", "create",
                "--repo", "brizzai/fleet",
                "--title", finalTitle,
                "--label", "bug",
                "--body-file", tmp.path,
            ])
            if result.ok {
                let url = result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
                FleetLog.info("bug report: issue created url=\(url)")
                if let u = URL(string: url) {
                    NSWorkspace.shared.open(u)
                }
                dismiss()
            } else {
                FleetLog.warn("bug report: gh failed exit=\(result.exitCode) stderr=\(result.stderr)")
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(body, forType: .string)
                model.showErrorToast("gh failed (copied to clipboard): \(result.stderr.prefix(120))",
                                     context: "bug report")
                dismiss()
            }
        } catch {
            FleetLog.error("bug report: submit failed err=\(error)")
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(body, forType: .string)
            model.showErrorToast("submit failed (copied to clipboard): \(error)",
                                 context: "bug report")
            dismiss()
        }
    }

    private func copyToClipboard() {
        let body = assembleBody()
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(body, forType: .string)
        FleetLog.info("bug report: copied to clipboard bytes=\(body.utf8.count)")
        model.showErrorToast("Bug report copied to clipboard", context: "bug report")
        dismiss()
    }

    private func writeTemp(body: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory
        let url = dir.appendingPathComponent("fleet-bug-\(UUID().uuidString).md")
        try body.write(to: url, atomically: true, encoding: .utf8)
        return url
    }

    private func assembleBody() -> String {
        var parts: [String] = []
        parts.append("## Description")
        parts.append(description.isEmpty ? "_(no description)_" : description)

        parts.append("\n## Mac Diagnostics")
        let info = ProcessInfo.processInfo
        parts.append("- OS: \(info.operatingSystemVersionString)")
        parts.append("- Locale: \(Locale.current.identifier)")
        let bundle = Bundle.main
        if let v = bundle.infoDictionary?["CFBundleShortVersionString"] as? String {
            parts.append("- App version: \(v)")
        }
        if let b = bundle.infoDictionary?["CFBundleVersion"] as? String {
            parts.append("- Build: \(b)")
        }
        parts.append("- gh available: \(ghAvailable)")

        if !model.errorHistory.isEmpty {
            parts.append("\n## Recent Errors")
            for e in model.errorHistory.suffix(20).reversed() {
                let ctx = e.context.isEmpty ? "" : " [\(e.context)]"
                parts.append("- `\(formatted(e.timestamp))`\(ctx) \(e.message)")
            }
        }

        if !model.actionLog.isEmpty {
            parts.append("\n## Recent Actions")
            for a in model.actionLog.suffix(20).reversed() {
                let st = a.success.map { $0 ? "✓" : "✗" } ?? "…"
                let detail = a.detail.isEmpty ? "" : " — \(a.detail)"
                parts.append("- `\(formatted(a.timestamp))` \(st) **\(a.action)**\(detail)")
            }
        }

        if !daemonDiagnostics.isEmpty {
            parts.append("\n## Daemon Diagnostics")
            parts.append("```")
            parts.append(daemonDiagnostics)
            parts.append("```")
        }

        parts.append("\n---\n_Generated by Fleet Mac (Cmd-Shift-B)_")
        return parts.joined(separator: "\n")
    }
}
