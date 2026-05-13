import Foundation

// AppConfig is the Swift mirror of the daemon's proto Config message. The Mac
// app holds a single live copy on AppModel; SettingsSheet edits a local copy
// and persists via Mutator.updateConfig.
//
// Defaults match the Go side: theme tokyo-night, tick 2s, both toggles on. The
// daemon's GetConfig returns proto3 zero values for unset fields — we promote
// those to sensible defaults at the boundary so the UI doesn't render empty.
struct AppConfig: Equatable {
    var tickIntervalSec: Int = 2
    var defaultProjectPath: String = ""
    var editor: String = ""
    var theme: String = "tokyo-night"
    var autoNameSessions: Bool = true
    var copyClaudeSettings: Bool = true

    static let defaults = AppConfig()

    // From the proto wire shape. Empty/zero proto fields stay as Swift
    // defaults so users opening Settings for the first time see sensible
    // values instead of zeros.
    init(proto: FleetConfig) {
        self.tickIntervalSec = max(1, Int(proto.tickIntervalSec))
        self.defaultProjectPath = proto.defaultProjectPath
        self.editor = proto.editor
        self.theme = proto.theme.isEmpty ? "tokyo-night" : proto.theme
        self.autoNameSessions = proto.autoNameSessions
        self.copyClaudeSettings = proto.copyClaudeSettings
    }

    init() {}

    var toProto: FleetConfig {
        var p = FleetConfig()
        p.tickIntervalSec = Int32(tickIntervalSec)
        p.defaultProjectPath = defaultProjectPath
        p.editor = editor
        p.theme = theme
        p.autoNameSessions = autoNameSessions
        p.copyClaudeSettings = copyClaudeSettings
        return p
    }
}
