import Foundation

// Selection identifies what the sidebar's "current row" is, so commands
// like Cmd-N (new session at cursor repo) know which repo to target even
// when no session is highlighted. The TUI's `a` key has the same fallback
// (internal/ui/app.go:1068-1080): if a session is selected, use its repo;
// if a repo header is selected, use that repo; if nothing is selected,
// open the path-picker dialog.
enum Selection: Hashable, Sendable {
    case session(String)  // associated value: session id
    case repo(String)     // associated value: repo root path
}
