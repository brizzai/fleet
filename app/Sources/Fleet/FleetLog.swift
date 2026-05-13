import Foundation
import OSLog

// FleetLog is a thin façade over Apple's unified logging that also mirrors
// to the daemon's debug.log so a single file (`~/.config/fleet/debug.log`)
// captures both the Go side (daemon, hook-handler) and the Mac client. That
// makes triage one tail away — no juggling Console.app and a terminal.
//
// Levels follow `slog`/`os.Logger` conventions: debug for verbose tracing,
// info for normal lifecycle events, warn for recoverable issues, error for
// failures the user can see. Each call writes one line. Format:
//   YYYY-MM-DDTHH:MM:SS.sss±HH:MM level=info component=mac msg="…"
enum FleetLog {
    private static let osLog = Logger(subsystem: "com.brizzai.fleet", category: "mac")

    private static let logFileURL: URL = {
        let home = FileManager.default.homeDirectoryForCurrentUser
        return home.appendingPathComponent(".config/fleet/debug.log")
    }()

    private static let queue = DispatchQueue(label: "com.brizzai.fleet.log", qos: .utility)

    static func debug(_ message: @autoclosure () -> String, file: StaticString = #fileID, line: UInt = #line) {
        let m = message()
        osLog.debug("\(m, privacy: .public)")
        write("debug", m, file: file, line: line)
    }

    static func info(_ message: @autoclosure () -> String, file: StaticString = #fileID, line: UInt = #line) {
        let m = message()
        osLog.info("\(m, privacy: .public)")
        write("info", m, file: file, line: line)
    }

    static func warn(_ message: @autoclosure () -> String, file: StaticString = #fileID, line: UInt = #line) {
        let m = message()
        osLog.warning("\(m, privacy: .public)")
        write("warn", m, file: file, line: line)
    }

    static func error(_ message: @autoclosure () -> String, file: StaticString = #fileID, line: UInt = #line) {
        let m = message()
        osLog.error("\(m, privacy: .public)")
        write("error", m, file: file, line: line)
    }

    private static func makeFormatter() -> ISO8601DateFormatter {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds, .withColonSeparatorInTimeZone]
        return f
    }

    private static func write(_ level: String, _ message: String, file: StaticString, line: UInt) {
        // Per-call formatter avoids the Sendable headaches of a shared
        // ISO8601DateFormatter; cost is negligible at log volumes we care about.
        let ts = makeFormatter().string(from: Date())
        let escaped = message.replacingOccurrences(of: "\"", with: "\\\"")
        let loc = "\(file):\(line)"
        let line = "time=\(ts) level=\(level.uppercased()) component=mac loc=\(loc) msg=\"\(escaped)\"\n"
        queue.async { append(line) }
        FileHandle.standardError.write(Data(line.utf8))
    }

    private static func append(_ line: String) {
        let data = Data(line.utf8)
        let dir = logFileURL.deletingLastPathComponent()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        if let handle = try? FileHandle(forWritingTo: logFileURL) {
            defer { try? handle.close() }
            try? handle.seekToEnd()
            try? handle.write(contentsOf: data)
        } else {
            try? data.write(to: logFileURL, options: .atomic)
        }
    }
}
