import Foundation

// ActionEntry mirrors the TUI's actionlog.go ring buffer: one record per user
// action (attach, restart, delete, …). Used by the bug report dialog as
// "steps to reproduce". Success is optional so callers can mark the entry
// pending at dispatch start, then settle it on completion.
struct ActionEntry: Identifiable, Hashable {
    let id: UUID
    let timestamp: Date
    let action: String
    let detail: String
    var success: Bool?

    init(action: String, detail: String, success: Bool? = nil) {
        self.id = UUID()
        self.timestamp = Date()
        self.action = action
        self.detail = detail
        self.success = success
    }
}
