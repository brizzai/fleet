package ui

// KeyBinding defines a single keybinding for display purposes.
// The actual key handling logic lives in handleKey() — this is the
// single source of truth for what shows in the help bar and overlay.
type KeyBinding struct {
	Key     string // Display label for overlay (e.g. "j / ↓", "Ctrl+Q")
	BarKey  string // Short key label for footer bar (empty = skip bar)
	BarDesc string // Short description for footer bar
	Desc    string // Full description for overlay
	Section string // "nav", "session", "global", "attach"
}

// allKeyBindings is the single source of truth for keybinding display.
// Add new keybindings here — help bar and overlay auto-update.
var allKeyBindings = []KeyBinding{
	// Navigation.
	{Key: "j / ↓", BarKey: "↑↓", BarDesc: "Nav", Desc: "Move down", Section: "nav"},
	{Key: "k / ↑", Desc: "Move up", Section: "nav"},
	{Key: "PgDn", Desc: "Page down", Section: "nav"},
	{Key: "PgUp", Desc: "Page up", Section: "nav"},

	// Session actions.
	{Key: "Enter", BarKey: "⏎", BarDesc: "Open", Desc: "Attach / toggle group", Section: "session"},
	{Key: "Tab", BarKey: "⇥", BarDesc: "Focus", Desc: "Focus preview / attach (swap)", Section: "session"},
	{Key: "Space", BarKey: "␣", BarDesc: "Next", Desc: "Jump to next waiting/finished", Section: "session"},
	{Key: "← / h", Desc: "Collapse group", Section: "session"},
	{Key: "→ / l", Desc: "Expand group", Section: "session"},
	{Key: "a", BarKey: "a", BarDesc: "New", Desc: "New session", Section: "session"},
	{Key: "n", BarKey: "n", BarDesc: "Repo", Desc: "New session (any repo)", Section: "session"},
	{Key: "w", BarKey: "w", BarDesc: "Wktree", Desc: "New worktree session", Section: "session"},
	{Key: "f", Desc: "Fork session", Section: "session"},
	{Key: "d", BarKey: "d", BarDesc: "Del", Desc: "Delete session", Section: "session"},
	{Key: "z", Desc: "Undo delete", Section: "session"},
	{Key: "r", BarKey: "r", BarDesc: "Restart", Desc: "Restart session", Section: "session"},
	{Key: "R", Desc: "Rename session", Section: "session"},
	{Key: "e", Desc: "Open in editor", Section: "session"},
	{Key: "p", BarKey: "p", BarDesc: "PR", Desc: "Open PR in browser", Section: "session"},
	{Key: "Y", BarKey: "Y", BarDesc: "Approve", Desc: "Quick approve permission", Section: "session"},
	{Key: "b", BarKey: "b", BarDesc: "Branch", Desc: "Switch git branch", Section: "session"},
	{Key: "/", BarKey: "/", BarDesc: "Filter", Desc: "Filter sessions", Section: "session"},
	{Key: "0-9", Desc: "Jump to slot (double-tap to attach)", Section: "session"},
	{Key: "Alt+0-9", Desc: "Bind/unbind slot (re-press same slot clears it)", Section: "session"},
	{Key: "= then digit", Desc: "Bind slot (fallback if Alt unsupported)", Section: "session"},
	{Key: "= = then digit", Desc: "Unbind slot", Section: "session"},

	// Global.
	{Key: ": / Ctrl+P", BarKey: ":", BarDesc: "Cmd", Desc: "Command palette", Section: "global"},
	{Key: "S", BarKey: "S", BarDesc: "Set", Desc: "Open settings", Section: "global"},
	{Key: "!", BarKey: "!", BarDesc: "Bug", Desc: "Bug report / diagnostics", Section: "global"},
	{Key: "?", BarKey: "?", BarDesc: "Help", Desc: "Toggle help", Section: "global"},
	{Key: "q", BarKey: "q", BarDesc: "Quit", Desc: "Quit", Section: "global"},

	// Focus mode (shown in overlay only, separated by blank line).
	{Key: "Esc", Desc: "Unfocus preview", Section: "focus"},
	{Key: "all keys", Desc: "Forwarded to session", Section: "focus"},

	// Attach mode (shown in overlay only, separated by blank line).
	{Key: "Ctrl+Q", Desc: "Detach from session", Section: "attach"},
}

// HelpBarBindings returns the bindings to show in the bottom help bar.
// Returns (contextKeys, globalKeys) as (key, desc) pairs.
func HelpBarBindings() (context, global []struct{ Key, Desc string }) {
	for _, kb := range allKeyBindings {
		if kb.BarKey == "" {
			continue
		}
		entry := struct{ Key, Desc string }{kb.BarKey, kb.BarDesc}
		if kb.Section == "global" {
			global = append(global, entry)
		} else {
			context = append(context, entry)
		}
	}
	return
}

// HelpOverlayBindings returns all bindings for the full help overlay.
// Attach-section bindings are preceded by a blank separator entry.
func HelpOverlayBindings() []struct{ Key, Desc string } {
	var result []struct{ Key, Desc string }
	prevSection := ""
	for _, kb := range allKeyBindings {
		if (kb.Section == "focus" || kb.Section == "attach") && prevSection != kb.Section {
			result = append(result, struct{ Key, Desc string }{"", ""})
		}
		result = append(result, struct{ Key, Desc string }{kb.Key, kb.Desc})
		prevSection = kb.Section
	}
	return result
}
