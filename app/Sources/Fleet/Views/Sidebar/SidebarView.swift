import SwiftUI

// SidebarView renders a flat List rather than nested Sections because we
// need both repo headers AND session rows to be selectable: SwiftUI's
// `.sidebar` style ignores `.tag()` on Section headers, so the only way
// to make Cmd-N target an empty pinned repo is to give the repo its own
// selectable row. Spacing + indentation give the visual hierarchy back.
struct SidebarView: View {
    @Bindable var model: AppModel
    @FocusState private var filterFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            filterBar
            Divider()
            sessionList
        }
        .onChange(of: model.filterFocusRequest) { _, requested in
            if requested {
                filterFocused = true
                model.consumeFilterFocusRequest()
            }
        }
    }

    private var filterBar: some View {
        HStack {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)
            TextField("Filter sessions", text: $model.filterText)
                .textFieldStyle(.plain)
                .focused($filterFocused)
                .onSubmit { filterFocused = false }
                .onKeyPress(.escape) {
                    model.filterText = ""
                    filterFocused = false
                    return .handled
                }
            if !model.filterText.isEmpty {
                Button {
                    model.filterText = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
    }

    private var sessionList: some View {
        List(selection: $model.selection) {
            ForEach(filteredRepos, id: \.id) { repo in
                RepoGroupHeader(repo: repo, model: model)
                    .tag(Selection.repo(repo.id))

                ForEach(repo.sessions, id: \.id) { session in
                    SessionRow(session: session, model: model)
                        .tag(Selection.session(session.id))
                        .padding(.leading, 12)
                }

                ForEach(pendingFor(repoRoot: repo.id), id: \.id) { pending in
                    PendingSessionRow(pending: pending)
                        .padding(.leading, 12)
                }
            }
        }
        .listStyle(.sidebar)
    }

    private func pendingFor(repoRoot: String) -> [PendingCreation] {
        model.pendingCreations.filter { $0.repoRoot == repoRoot }
    }

    private var filteredRepos: [Repo] {
        let needle = model.filterText.lowercased()
        let repos = model.displayedRepos
        guard !needle.isEmpty else {
            return repos.filter { repo in
                !repo.sessions.isEmpty
                    || repo.pinned
                    || model.pendingCreations.contains(where: { $0.repoRoot == repo.id })
            }
        }

        return repos.compactMap { repo in
            let matchedSessions = repo.sessions.filter { sess in
                sess.title.lowercased().contains(needle)
                    || repo.displayName.lowercased().contains(needle)
            }
            guard !matchedSessions.isEmpty else { return nil }
            var copy = repo
            copy.sessions = matchedSessions
            return copy
        }
    }
}
