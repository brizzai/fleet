import AppKit
import Foundation

// CommandRunner wraps Foundation.Process for spawning short-lived CLI tools
// (gh, etc.) from the Mac app. Mac GUI apps inherit a minimal $PATH that
// excludes /opt/homebrew/bin and /usr/local/bin — Homebrew-installed tools
// like `gh` would otherwise fail to resolve. We prepend both directories
// before invoking so the binary lookup matches what the user's shell sees.
enum CommandRunner {
    struct Result {
        let stdout: String
        let stderr: String
        let exitCode: Int32
        var ok: Bool { exitCode == 0 }
    }

    enum RunError: Error, LocalizedError {
        case launchFailed(String)
        case timedOut

        var errorDescription: String? {
            switch self {
            case .launchFailed(let msg): return msg
            case .timedOut: return "command timed out"
            }
        }
    }

    /// Runs `command` with `args`, returning captured stdout/stderr and exit
    /// code. Throws `launchFailed` if the binary can't be resolved or the
    /// process can't be launched. Inherits environment but augments PATH so
    /// Homebrew-managed tools resolve.
    static func run(_ command: String, args: [String]) throws -> Result {
        FleetLog.info("CommandRunner: run \(command) \(args.joined(separator: " "))")
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: resolveBinary(command))
        proc.arguments = args
        proc.environment = augmentedEnvironment()

        let outPipe = Pipe()
        let errPipe = Pipe()
        proc.standardOutput = outPipe
        proc.standardError = errPipe

        do {
            try proc.run()
        } catch {
            FleetLog.error("CommandRunner: launch failed cmd=\(command) err=\(error)")
            throw RunError.launchFailed("\(error)")
        }
        proc.waitUntilExit()

        let outData = outPipe.fileHandleForReading.readDataToEndOfFile()
        let errData = errPipe.fileHandleForReading.readDataToEndOfFile()
        let stdout = String(data: outData, encoding: .utf8) ?? ""
        let stderr = String(data: errData, encoding: .utf8) ?? ""
        FleetLog.info("CommandRunner: exit=\(proc.terminationStatus) cmd=\(command)")
        return Result(stdout: stdout, stderr: stderr, exitCode: proc.terminationStatus)
    }

    /// Returns true if `binary` resolves on the augmented PATH. Useful for
    /// gating UI ("Open issue" button disabled when gh is missing) without
    /// having to actually invoke the tool.
    static func isAvailable(_ binary: String) -> Bool {
        let path = resolveBinary(binary)
        return FileManager.default.isExecutableFile(atPath: path)
    }

    private static func resolveBinary(_ command: String) -> String {
        if command.hasPrefix("/") { return command }
        let dirs = augmentedPath().split(separator: ":")
        for dir in dirs {
            let candidate = "\(dir)/\(command)"
            if FileManager.default.isExecutableFile(atPath: candidate) {
                return candidate
            }
        }
        // Fall back to bare name; Process will throw `launchFailed` if it
        // doesn't resolve at exec time.
        return command
    }

    private static func augmentedEnvironment() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = augmentedPath()
        return env
    }

    private static func augmentedPath() -> String {
        let existing = ProcessInfo.processInfo.environment["PATH"] ?? ""
        let extras = ["/opt/homebrew/bin", "/usr/local/bin"]
        var parts = extras
        for component in existing.split(separator: ":") {
            parts.append(String(component))
        }
        return parts.joined(separator: ":")
    }
}
