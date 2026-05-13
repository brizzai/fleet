import Foundation

// ErrorEntry mirrors the TUI's errors.go ring buffer: a persistent record of
// every error that surfaced as a toast. Toasts auto-clear after 5s; this is
// what the bug report dialog reads to populate "Recent Errors".
struct ErrorEntry: Identifiable, Hashable {
    let id: UUID
    let timestamp: Date
    let message: String
    let context: String

    init(message: String, context: String = "") {
        self.id = UUID()
        self.timestamp = Date()
        self.message = message
        self.context = context
    }
}
