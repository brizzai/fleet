import AppKit
import SwiftTerm
import SwiftUI

// TerminalPaneView wraps SwiftTerm.LocalProcessTerminalView. It binds to a
// `Session?`; when the session changes, the same widget gets its current
// PTY torn down and a fresh `tmux attach-session` started for the new
// session. One terminal view for the whole window — V1 has no warm cache.
//
// PTY data does NOT travel through the daemon (see docs/desktop/architecture.md).
// We attach directly to the daemon-owned tmux server at
// `~/.config/fleet/tmux.sock`, the same socket the TUI uses, so a session
// opened in either client is reachable from the other.
struct TerminalPaneView: NSViewRepresentable {
    let session: Session?
    let theme: ThemePalette

    // SwiftTerm has no built-in concept of internal padding, so we wrap it
    // in an NSView that insets the terminal by `padding` on every edge.
    // Anything else (caret color, font) is set on the inner view directly.
    private static let padding: CGFloat = 10
    private static let cornerRadius: CGFloat = 4

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> NSView {
        let container = NSView(frame: .zero)
        container.wantsLayer = true
        // Match the terminal's native background so the padding doesn't
        // show as a visible seam if the terminal redraws lazily.
        container.layer?.backgroundColor = theme.terminalBackground.cgColor

        let term = LocalProcessTerminalView(frame: .zero)
        term.translatesAutoresizingMaskIntoConstraints = false
        term.font = Self.preferredFont()
        // Mouse reporting *off* so click+drag does native Mac text selection
        // (and Cmd+C copies). We forward scroll-wheel ourselves via the
        // local NSEvent monitor below, so tmux still scrolls correctly.
        // Losing tmux's click-on-pane-to-focus is fine: Claude is
        // keyboard-driven and we have one pane per session.
        term.allowMouseReporting = false
        term.optionAsMetaKey = true

        // Colors come from the user's selected palette. SwiftTerm doesn't
        // expose a clean live-recolor API, so theme changes recreate the
        // NSViewRepresentable via .id() at the call site.
        term.installColors(theme.ansi())
        term.nativeBackgroundColor = theme.terminalBackground
        term.nativeForegroundColor = theme.terminalForeground
        term.caretColor = theme.terminalCursor
        term.selectedTextBackgroundColor = theme.terminalSelectionBackground

        container.addSubview(term)
        NSLayoutConstraint.activate([
            term.leadingAnchor.constraint(equalTo: container.leadingAnchor, constant: Self.padding),
            term.trailingAnchor.constraint(equalTo: container.trailingAnchor, constant: -Self.padding),
            term.topAnchor.constraint(equalTo: container.topAnchor, constant: Self.padding),
            term.bottomAnchor.constraint(equalTo: container.bottomAnchor, constant: -Self.padding),
        ])

        context.coordinator.terminal = term
        context.coordinator.installScrollMonitor(for: term)
        scheduleAttach(view: term, coordinator: context.coordinator)
        return container
    }

    static func dismantleNSView(_ nsView: NSView, coordinator: Coordinator) {
        coordinator.removeScrollMonitor()
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        guard let term = context.coordinator.terminal else { return }
        scheduleAttach(view: term, coordinator: context.coordinator)
    }

    private static let fontSize: CGFloat = 13.5

    private static func preferredFont() -> NSFont {
        // The bundled font registers under the family "JetBrains Mono"
        // (with space) but its PostScript name is "JetBrainsMono-Regular"
        // (no space). NSFont(name:) takes the PostScript name; if that
        // miss-resolves we fall back to the family lookup via NSFontManager.
        if let f = NSFont(name: "JetBrainsMono-Regular", size: fontSize) {
            return f
        }
        if let f = NSFontManager.shared.font(
            withFamily: "JetBrains Mono",
            traits: [],
            weight: 5,
            size: fontSize
        ) {
            return f
        }
        return NSFont.userFixedPitchFont(ofSize: fontSize) ?? NSFont.systemFont(ofSize: fontSize)
    }

    /// Schedules `startProcess` on the next runloop turn. SwiftUI lays out
    /// representables *after* `makeNSView`/`updateNSView` returns, so the
    /// view's bounds are still `.zero` when we'd otherwise spawn — tmux
    /// would attach with a default 80×24 PTY and redraw on the next
    /// SIGWINCH (a visible 1–2s flash). Bouncing through `DispatchQueue.main`
    /// guarantees the view has its real size before tmux sees its first
    /// pane geometry.
    private func scheduleAttach(
        view: LocalProcessTerminalView,
        coordinator: Coordinator
    ) {
        let target = session?.tmuxName
        guard target != coordinator.attachedTmuxName else { return }
        coordinator.attachedTmuxName = target

        // Tear down the previous client immediately so a stale pane isn't
        // visible while we wait for the next runloop turn. terminate() sends
        // SIGHUP, which tmux maps to a clean client detach.
        view.terminate()

        guard let tmuxName = target else { return }

        DispatchQueue.main.async {
            // The view may have been recycled or selection may have changed
            // again before our turn comes up; re-check that we still want to
            // attach to this name.
            guard coordinator.attachedTmuxName == tmuxName else { return }
            view.startProcess(
                executable: Coordinator.tmuxPath,
                args: ["attach-session", "-t", tmuxName],
                environment: Self.terminalEnvironment(),
                execName: nil
            )
        }
    }

    @MainActor
    final class Coordinator {
        var terminal: LocalProcessTerminalView?
        var attachedTmuxName: String?
        private var scrollMonitor: Any?

        // SwiftTerm's `scrollWheel` is `public override` (not `open`), so
        // we can't subclass it. Its default scrolls SwiftTerm's own
        // (mostly empty) scrollback buffer instead of forwarding the
        // event upstream — when we're attached to a full-screen tmux app
        // like Claude Code, that paints stale scrollback over the live
        // alt-screen ("scrolling looks messed up") and often appears to
        // do nothing because the scrollback is blank.
        //
        // The same monitor also rewrites a few Mac-standard keyboard
        // shortcuts that SwiftTerm doesn't translate by default:
        //   Cmd+Left   → ^A (start of line)
        //   Cmd+Right  → ^E (end of line)
        //   Cmd+Backspace → ^U (delete to start of line)
        //   Cmd+Delete (forward) → ^K (delete to end of line)
        //
        // We install a local NSEvent monitor that catches these events
        // targeted at the terminal *before* SwiftTerm sees them and
        // returns nil to swallow them. For scroll, the wheel event is
        // forwarded as an SGR (mode 1006) escape sequence (tmux has
        // `mouse on` for fleet sessions). For Cmd-shortcuts, the
        // matching control byte is sent.
        func installScrollMonitor(for term: LocalProcessTerminalView) {
            removeScrollMonitor()
            scrollMonitor = NSEvent.addLocalMonitorForEvents(
                matching: [.scrollWheel, .keyDown]
            ) { [weak self, weak term] event in
                // Local event monitors are always delivered on the main
                // thread but AppKit hasn't annotated them as @MainActor.
                // We do all the AppKit work inside `assumeIsolated` and
                // only flow a Bool back so NSEvent (non-Sendable) doesn't
                // need to cross the actor boundary.
                var consumed = false
                MainActor.assumeIsolated {
                    consumed = self?.handleMonitoredEvent(event, term: term) ?? false
                }
                return consumed ? nil : event
            }
        }

        @MainActor
        private func handleMonitoredEvent(_ event: NSEvent, term: LocalProcessTerminalView?) -> Bool {
            guard let term, term.window === event.window else { return false }

            switch event.type {
            case .scrollWheel:
                let pointInTerm = term.convert(event.locationInWindow, from: nil)
                guard term.bounds.contains(pointInTerm) else { return false }
                let dy = event.scrollingDeltaY
                guard dy != 0 else { return true }   // consume zero-delta events too
                let absTicks = max(1, min(8, Int(abs(dy / 4))))
                let button = dy > 0 ? 64 : 65   // SGR: 64 = wheel up, 65 = wheel down
                let bytes = ArraySlice(Array("\u{1B}[<\(button);1;1M".utf8))
                for _ in 0..<absTicks {
                    term.send(source: term, data: bytes)
                }
                return true

            case .keyDown:
                guard term.window?.firstResponder === term else { return false }
                guard event.modifierFlags.contains(.command) else { return false }
                guard let bytes = mapCommandKey(event) else { return false }
                term.send(source: term, data: ArraySlice(bytes))
                return true

            default:
                return false
            }
        }

        nonisolated private func mapCommandKey(_ event: NSEvent) -> [UInt8]? {
            guard let chars = event.charactersIgnoringModifiers,
                  let scalar = chars.unicodeScalars.first else { return nil }
            switch Int(scalar.value) {
            case NSLeftArrowFunctionKey:    return [0x01]   // ^A
            case NSRightArrowFunctionKey:   return [0x05]   // ^E
            case NSDeleteCharacter,         // backspace key on Mac is "delete"
                 0x7F:                      return [0x15]   // ^U
            case NSDeleteFunctionKey:       return [0x0B]   // ^K (Fn+Delete / Cmd+Delete forward)
            default:                        return nil
            }
        }

        func removeScrollMonitor() {
            if let m = scrollMonitor {
                NSEvent.removeMonitor(m)
                scrollMonitor = nil
            }
        }
        // Cleanup is driven by `dismantleNSView` (SwiftUI lifecycle hook),
        // not deinit — Swift 6 strict concurrency rejects accessing
        // non-Sendable state from a non-isolated deinit, and the monitor
        // ref is `Any?` (not Sendable). dismantleNSView runs on the main
        // actor so it can call removeScrollMonitor() safely.

        // Resolve once at coordinator creation (i.e. first view materialise).
        // tmux is required for any session to be useful; if it's missing the
        // app should fail loudly elsewhere — this is a sane fallback.
        static let tmuxPath: String = {
            let parentPath = ProcessInfo.processInfo.environment["PATH"] ?? ""
            for dir in parentPath.split(separator: ":") {
                let candidate = "\(dir)/tmux"
                if FileManager.default.isExecutableFile(atPath: candidate) {
                    return candidate
                }
            }
            return "/opt/homebrew/bin/tmux"
        }()
    }

    private static func terminalEnvironment() -> [String] {
        var env = ProcessInfo.processInfo.environment
        env["TERM"] = "xterm-256color"
        env["LANG"] = env["LANG"] ?? "en_US.UTF-8"
        env["COLORTERM"] = "truecolor"
        return env.map { "\($0.key)=\($0.value)" }
    }
}
