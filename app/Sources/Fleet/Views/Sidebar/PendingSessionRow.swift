import SwiftUI

// PendingSessionRow is the "Creating *<branch>*…" placeholder rendered
// under a repo while the daemon is provisioning a worktree session. The
// row is inert (no selection target, no context menu) so users can't act
// on a session that doesn't exist yet.
struct PendingSessionRow: View {
    let pending: PendingCreation

    var body: some View {
        HStack(spacing: 8) {
            ProgressView()
                .controlSize(.small)
                .frame(width: 12, alignment: .center)

            Text("Creating \(pending.displayName)…")
                .lineLimit(1)
                .truncationMode(.tail)
                .foregroundStyle(.secondary)

            Spacer(minLength: 0)
        }
        .contentShape(Rectangle())
        .allowsHitTesting(false)
    }
}
