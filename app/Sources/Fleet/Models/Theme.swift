import AppKit
import SwiftTerm
import SwiftUI

// ThemePalette mirrors the Go TUI's Palette (internal/ui/palette.go) plus the
// SwiftTerm-side colors (terminal background/foreground/cursor + 16 ANSI).
// Chrome colors are NSColor so the SwiftUI sidebar/banners can use them; ANSI
// is SwiftTerm.Color so the terminal view can install them directly.
struct ThemePalette: Equatable, @unchecked Sendable {
    let name: String         // wire identifier matching the Go side
    let displayName: String

    // Chrome (sidebar / banners / toolbar). Names mirror palette.go fields.
    let bg: NSColor
    let surface: NSColor
    let border: NSColor
    let text: NSColor
    let textDim: NSColor
    let accent: NSColor
    let green: NSColor
    let yellow: NSColor
    let blue: NSColor
    let red: NSColor
    let gray: NSColor
    let orange: NSColor
    let purple: NSColor

    // Terminal (SwiftTerm). Separate from chrome — terminal background often
    // differs slightly from the sidebar surface in the Go theme set.
    let terminalBackground: NSColor
    let terminalForeground: NSColor
    let terminalCursor: NSColor
    let terminalSelectionBackground: NSColor

    // ANSI 16. SwiftTerm.Color is a class (non-Sendable) so this is built on
    // demand — `static let` would require allocations to be ad-hoc.
    let ansi: () -> [SwiftTerm.Color]

    static func == (lhs: ThemePalette, rhs: ThemePalette) -> Bool {
        lhs.name == rhs.name
    }
}

// SwiftUI helpers — every Color in the chrome derives from an NSColor on the
// palette so the same palette table drives both AppKit views and SwiftUI.
extension ThemePalette {
    var bgColor: SwiftUI.Color { SwiftUI.Color(nsColor: bg) }
    var surfaceColor: SwiftUI.Color { SwiftUI.Color(nsColor: surface) }
    var borderColor: SwiftUI.Color { SwiftUI.Color(nsColor: border) }
    var textColor: SwiftUI.Color { SwiftUI.Color(nsColor: text) }
    var textDimColor: SwiftUI.Color { SwiftUI.Color(nsColor: textDim) }
    var accentColor: SwiftUI.Color { SwiftUI.Color(nsColor: accent) }
    var greenColor: SwiftUI.Color { SwiftUI.Color(nsColor: green) }
    var yellowColor: SwiftUI.Color { SwiftUI.Color(nsColor: yellow) }
    var blueColor: SwiftUI.Color { SwiftUI.Color(nsColor: blue) }
    var redColor: SwiftUI.Color { SwiftUI.Color(nsColor: red) }
    var orangeColor: SwiftUI.Color { SwiftUI.Color(nsColor: orange) }
    var purpleColor: SwiftUI.Color { SwiftUI.Color(nsColor: purple) }
}

// Built-in palettes. Hex values copied verbatim from internal/ui/palette.go so
// changing colors there changes them here too via a quick port.
enum Theme {
    static let all: [ThemePalette] = [tokyoNight, catppuccinMocha, rosePine, nord, gruvbox]

    static func byName(_ name: String) -> ThemePalette {
        all.first(where: { $0.name == name }) ?? tokyoNight
    }

    static let tokyoNight = ThemePalette(
        name: "tokyo-night",
        displayName: "Tokyo Night",
        bg: hex(0x1a1b26),
        surface: hex(0x24283b),
        border: hex(0x414868),
        text: hex(0xc0caf5),
        textDim: hex(0x565f89),
        accent: hex(0x7aa2f7),
        green: hex(0x9ece6a),
        yellow: hex(0xe0af68),
        blue: hex(0x7dcfff),
        red: hex(0xf7768e),
        gray: hex(0x565f89),
        orange: hex(0xff9e64),
        purple: hex(0xbb9af7),
        terminalBackground: hex(0x1a1b26),
        terminalForeground: hex(0xc0caf5),
        terminalCursor: hex(0x7aa2f7),
        terminalSelectionBackground: hex(0x33467c),
        ansi: {
            [
                term(0x15161e), term(0xf7768e), term(0x9ece6a), term(0xe0af68),
                term(0x7aa2f7), term(0xbb9af7), term(0x7dcfff), term(0xa9b1d6),
                term(0x414868), term(0xf7768e), term(0x9ece6a), term(0xe0af68),
                term(0x7aa2f7), term(0xbb9af7), term(0x7dcfff), term(0xc0caf5),
            ]
        }
    )

    static let catppuccinMocha = ThemePalette(
        name: "catppuccin-mocha",
        displayName: "Catppuccin Mocha",
        bg: hex(0x1e1e2e),
        surface: hex(0x313244),
        border: hex(0x45475a),
        text: hex(0xcdd6f4),
        textDim: hex(0x6c7086),
        accent: hex(0x89b4fa),
        green: hex(0xa6e3a1),
        yellow: hex(0xf9e2af),
        blue: hex(0x94e2d5),
        red: hex(0xf38ba8),
        gray: hex(0x6c7086),
        orange: hex(0xfab387),
        purple: hex(0xcba6f7),
        terminalBackground: hex(0x1e1e2e),
        terminalForeground: hex(0xcdd6f4),
        terminalCursor: hex(0x89b4fa),
        terminalSelectionBackground: hex(0x45475a),
        ansi: {
            [
                term(0x45475a), term(0xf38ba8), term(0xa6e3a1), term(0xf9e2af),
                term(0x89b4fa), term(0xcba6f7), term(0x94e2d5), term(0xbac2de),
                term(0x585b70), term(0xf38ba8), term(0xa6e3a1), term(0xf9e2af),
                term(0x89b4fa), term(0xcba6f7), term(0x94e2d5), term(0xa6adc8),
            ]
        }
    )

    static let rosePine = ThemePalette(
        name: "rose-pine",
        displayName: "Rosé Pine",
        bg: hex(0x191724),
        surface: hex(0x1f1d2e),
        border: hex(0x26233a),
        text: hex(0xe0def4),
        textDim: hex(0x6e6a86),
        accent: hex(0xc4a7e7),
        green: hex(0x9ccfd8),
        yellow: hex(0xf6c177),
        blue: hex(0xebbcba),
        red: hex(0xeb6f92),
        gray: hex(0x908caa),
        orange: hex(0xebbcba),
        purple: hex(0xc4a7e7),
        terminalBackground: hex(0x191724),
        terminalForeground: hex(0xe0def4),
        terminalCursor: hex(0xc4a7e7),
        terminalSelectionBackground: hex(0x26233a),
        ansi: {
            [
                term(0x26233a), term(0xeb6f92), term(0x9ccfd8), term(0xf6c177),
                term(0xebbcba), term(0xc4a7e7), term(0x31748f), term(0xe0def4),
                term(0x6e6a86), term(0xeb6f92), term(0x9ccfd8), term(0xf6c177),
                term(0xebbcba), term(0xc4a7e7), term(0x31748f), term(0xe0def4),
            ]
        }
    )

    static let nord = ThemePalette(
        name: "nord",
        displayName: "Nord",
        bg: hex(0x2e3440),
        surface: hex(0x3b4252),
        border: hex(0x4c566a),
        text: hex(0xeceff4),
        textDim: hex(0x616e88),
        accent: hex(0x88c0d0),
        green: hex(0xa3be8c),
        yellow: hex(0xebcb8b),
        blue: hex(0x81a1c1),
        red: hex(0xbf616a),
        gray: hex(0x616e88),
        orange: hex(0xd08770),
        purple: hex(0xb48ead),
        terminalBackground: hex(0x2e3440),
        terminalForeground: hex(0xeceff4),
        terminalCursor: hex(0x88c0d0),
        terminalSelectionBackground: hex(0x434c5e),
        ansi: {
            [
                term(0x3b4252), term(0xbf616a), term(0xa3be8c), term(0xebcb8b),
                term(0x81a1c1), term(0xb48ead), term(0x88c0d0), term(0xe5e9f0),
                term(0x4c566a), term(0xbf616a), term(0xa3be8c), term(0xebcb8b),
                term(0x81a1c1), term(0xb48ead), term(0x8fbcbb), term(0xeceff4),
            ]
        }
    )

    static let gruvbox = ThemePalette(
        name: "gruvbox",
        displayName: "Gruvbox",
        bg: hex(0x282828),
        surface: hex(0x3c3836),
        border: hex(0x504945),
        text: hex(0xebdbb2),
        textDim: hex(0x928374),
        accent: hex(0x8ec07c),
        green: hex(0xb8bb26),
        yellow: hex(0xfabd2f),
        blue: hex(0x83a598),
        red: hex(0xfb4934),
        gray: hex(0x928374),
        orange: hex(0xfe8019),
        purple: hex(0xd3869b),
        terminalBackground: hex(0x282828),
        terminalForeground: hex(0xebdbb2),
        terminalCursor: hex(0x8ec07c),
        terminalSelectionBackground: hex(0x504945),
        ansi: {
            [
                term(0x282828), term(0xfb4934), term(0xb8bb26), term(0xfabd2f),
                term(0x83a598), term(0xd3869b), term(0x8ec07c), term(0xebdbb2),
                term(0x928374), term(0xfb4934), term(0xb8bb26), term(0xfabd2f),
                term(0x83a598), term(0xd3869b), term(0x8ec07c), term(0xfbf1c7),
            ]
        }
    )

    private static func hex(_ value: UInt32) -> NSColor {
        let r = CGFloat((value >> 16) & 0xff) / 255
        let g = CGFloat((value >> 8) & 0xff) / 255
        let b = CGFloat(value & 0xff) / 255
        return NSColor(srgbRed: r, green: g, blue: b, alpha: 1)
    }

    private static func term(_ value: UInt32) -> SwiftTerm.Color {
        // SwiftTerm.Color takes 0..65535 components — `byte * 0x101` maps 8-bit
        // back into the right 16-bit value.
        let r = UInt16((value >> 16) & 0xff) * 0x101
        let g = UInt16((value >> 8) & 0xff) * 0x101
        let b = UInt16(value & 0xff) * 0x101
        return SwiftTerm.Color(red: r, green: g, blue: b)
    }
}
