package ui

import "strings"

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
	{Key: "Shift+↑/↓", Desc: "Jump to previous / next group header", Section: "nav"},
	{Key: "Ctrl+Shift+↑/↓", Desc: "Jump to previous / next origin", Section: "nav"},
	{Key: "PgDn", Desc: "Page down", Section: "nav"},
	{Key: "PgUp", Desc: "Page up", Section: "nav"},

	// Session actions.
	{Key: ".", BarKey: ".", BarDesc: "Menu", Desc: "Context menu for the selected row", Section: "session"},
	{Key: "Enter", BarKey: "⏎", BarDesc: "Open", Desc: "Attach / toggle group", Section: "session"},
	{Key: "Tab", BarKey: "⇥", BarDesc: "Focus", Desc: "Focus preview / attach (swap)", Section: "session"},
	{Key: "Space", BarKey: "␣", BarDesc: "Jump", Desc: "Jump to next waiting/finished", Section: "session"},
	{Key: "← / h", Desc: "Collapse group", Section: "session"},
	{Key: "→ / l", Desc: "Expand group", Section: "session"},
	{Key: "a", BarKey: "a", BarDesc: "New", Desc: "New session (default agent)", Section: "session"},
	{Key: "A", Desc: "New session (pick agent)", Section: "session"},
	{Key: "n", BarKey: "n", BarDesc: "Repo", Desc: "New session (any repo)", Section: "session"},
	{Key: "w", BarKey: "w", BarDesc: "Wktree", Desc: "New worktree session", Section: "session"},
	{Key: "f", Desc: "Fork session", Section: "session"},
	{Key: "F", Desc: "Fork to worktree", Section: "session"},
	{Key: "d", BarKey: "d", BarDesc: "Del", Desc: "Delete session / repo / worktree", Section: "session"},
	{Key: "u", Desc: "Undo delete", Section: "session"},
	{Key: "r", BarKey: "r", BarDesc: "Restart", Desc: "Restart session", Section: "session"},
	{Key: "R", Desc: "Rename session", Section: "session"},
	{Key: "m", Desc: "Mark session as unread (idle → finished)", Section: "session"},
	{Key: "z", Desc: "Snooze / wake session / repo / worktree", Section: "session"},
	{Key: "e", Desc: "Open in editor", Section: "session"},
	{Key: "p", BarKey: "p", BarDesc: "PR", Desc: "Open PR in browser", Section: "session"},
	{Key: "Y", BarKey: "Y", BarDesc: "Approve", Desc: "Quick approve permission", Section: "session"},
	{Key: "b", BarKey: "b", BarDesc: "Branch", Desc: "Switch git branch", Section: "session"},
	{Key: "/", BarKey: "/", BarDesc: "Filter", Desc: "Filter sessions", Section: "session"},

	// Session slots. Split out of "session" because they are a distinct concept
	// from acting on the row under the cursor — and because they carry the two
	// longest strings in the table, which used to set the column width for every
	// other binding in the sheet.
	{Key: "0-9", Desc: "Jump to slot (double-tap to attach)", Section: "slots"},
	{Key: "Alt+0-9", Desc: "Bind/unbind slot (re-press to clear)", Section: "slots"},
	{Key: "= then digit", Desc: "Bind slot (if Alt unsupported)", Section: "slots"},
	{Key: "= = then digit", Desc: "Unbind slot", Section: "slots"},

	// Global.
	{Key: "`", BarKey: "`", BarDesc: "Term", Desc: "Toggle terminal drawer", Section: "global"},
	{Key: "Ctrl+K", BarKey: "⌃K", BarDesc: "Cmd", Desc: "Command palette", Section: "global"},
	{Key: "t", Desc: "My tickets", Section: "global"},
	{Key: "c", Desc: "Code reviews awaiting you", Section: "global"},
	{Key: "C", Desc: "Jump to this repo's reviews", Section: "global"},
	{Key: "v", Desc: "Review: diff ↔ agent pane", Section: "global"},
	{Key: "s", Desc: "Review: show hidden files", Section: "global"},
	{Key: "↑↓ ][ .,", Desc: "Reader: scroll, hunk, file", Section: "global"},
	{Key: "c e d", Desc: "Reader: comment, edit, delete", Section: "global"},
	{Key: "/ n N", Desc: "Reader: search, next, previous match", Section: "global"},
	{Key: "⏎ +", Desc: "Reader: expand folded context (step, all)", Section: "global"},
	{Key: "space m", Desc: "Reader: mark read + next file / mark in place", Section: "global"},
	{Key: "⇥", Desc: "Reader: focus file tree ↔ diff", Section: "global"},
	{Key: "| s ? ⌃q", Desc: "Reader: split, folded files, key sheet, quit", Section: "global"},
	{Key: "S", BarKey: "S", BarDesc: "Set", Desc: "Open settings", Section: "global"},
	{Key: "Shift+W", Desc: "What's New / release notes", Section: "global"},
	{Key: "X", Desc: "Dismiss on-screen tip", Section: "global"},
	{Key: "!", BarKey: "!", BarDesc: "Bug", Desc: "Bug report / diagnostics", Section: "global"},
	{Key: "?", BarKey: "?", BarDesc: "Help", Desc: "Toggle help", Section: "global"},
	{Key: "Ctrl+C", BarKey: "⌃C", BarDesc: "Quit", Desc: "Quit", Section: "global"},

	// Terminal drawer (shown in overlay only, separated by blank line). It's
	// always-typing — keys go to the shell; Ctrl chords drive the chrome.
	{Key: "`", Desc: "Open terminal drawer + start typing", Section: "drawer"},
	{Key: "Ctrl+T", Desc: "New shell ($SHELL in repo dir)", Section: "drawer"},
	{Key: "Ctrl+W", Desc: "Close shell (twice if running)", Section: "drawer"},
	{Key: "PgUp / PgDn", Desc: "Switch shell tab", Section: "drawer"},
	{Key: "Ctrl+G", Desc: "Full-screen attach (Ctrl+Q returns)", Section: "drawer"},
	{Key: "Enter", Desc: "Restart shell (when exited)", Section: "drawer"},
	{Key: "`", Desc: "Close drawer → sidebar", Section: "drawer"},

	// Focus mode (shown in overlay only, separated by blank line).
	{Key: "Esc", Desc: "Unfocus preview", Section: "focus"},
	{Key: "all keys", Desc: "Forwarded to session", Section: "focus"},

	// Attach mode (shown in overlay only, separated by blank line).
	{Key: "Ctrl+Q", Desc: "Detach from session", Section: "attach"},
}

// BarContext identifies what the cursor is on, so the footer can show only
// the keys that actually apply to the current row.
type BarContext int

const (
	BarContextEmpty    BarContext = iota // no items, or unknown
	BarContextOrigin                     // cursor on an origin header (▾ brizzai 16)
	BarContextCheckout                   // cursor on a checkout header (▾ new-ui #100)
	BarContextSession                    // cursor on a real session row
)

// HelpBarBindings returns ALL bar bindings; kept for places that want the
// full unfiltered list (settings dialog, debug). New callers should prefer
// HelpBarBindingsFor for the context-aware subset.
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

// HelpBarBindingsFor returns a curated subset of bar bindings for the row
// type currently under the cursor. Keeps the footer scannable (5-6 keys
// per context) instead of dumping the whole keymap on every paint.
// `enterMode` is the active Config.EnterMode ("attach" or "split"). In
// split mode handleKey swaps Enter↔Tab, so the session footer needs to
// follow suit or it points users at the wrong action.
func HelpBarBindingsFor(ctx BarContext, enterMode string) (context, global []struct{ Key, Desc string }) {
	// Each list stays at 6 keys — the footer doesn't truncate, so a longer one
	// would bleed past a narrow terminal. `.` earns its slot by being the gateway
	// to every other action on the row, so it displaces the one key it most
	// cheaply replaces (Restart / PR are both one keystroke away inside the menu).
	// `.` is omitted from the empty context, where it has nothing to act on.
	switch ctx {
	case BarContextSession:
		if enterMode == "split" {
			context = []struct{ Key, Desc string }{
				{"⇥", "Attach"}, {"⏎", "Focus"}, {"␣", "Jump"},
				{"Y", "Approve"}, {"d", "Del"}, {".", "Menu"},
			}
		} else {
			context = []struct{ Key, Desc string }{
				{"⏎", "Attach"}, {"␣", "Jump"}, {"Y", "Approve"},
				{"d", "Del"}, {"p", "PR"}, {".", "Menu"},
			}
		}
	case BarContextCheckout:
		context = []struct{ Key, Desc string }{
			{"⏎", "Expand"}, {"d", "Del"}, {"w", "Wktree"},
			{"F", "Fork"}, {"b", "Branch"}, {".", "Menu"},
		}
	case BarContextOrigin:
		context = []struct{ Key, Desc string }{
			{"⏎", "Expand"}, {"d", "Forget"}, {"w", "Wktree"}, {".", "Menu"},
		}
	default: // empty
		context = []struct{ Key, Desc string }{
			{"a", "New"}, {"n", "Repo"}, {"w", "Wktree"},
		}
	}
	global = []struct{ Key, Desc string }{
		{"`", "Term"}, {"⌃K", "Cmd"}, {"/", "Filter"}, {"?", "Help"}, {"⌃C", "Quit"},
	}
	return
}

// HelpEntry is one row of the help overlay: a binding plus the section it
// belongs to. The overlay draws section headers, so Section has to survive the
// flatten — HelpOverlayBindings used to drop it and emit blank separator rows
// instead, which the overlay then threw away as well.
type HelpEntry struct {
	Key     string
	Desc    string
	Section string
}

// helpSections names each section for the overlay's headers. Order comes from
// allKeyBindings, not from this list — this only supplies the label, so a
// reordering of the bindings can't silently reorder the sheet.
// TestHelpSectionsAreAllLabelled pins the two sets against each other.
var helpSections = map[string]string{
	"nav":     "NAVIGATION",
	"session": "SESSIONS",
	"slots":   "SLOTS",
	"global":  "GLOBAL",
	"drawer":  "TERMINAL DRAWER",
	"focus":   "FOCUS MODE",
	"attach":  "ATTACH MODE",
}

// helpSectionLabel falls back to the raw id so an unlabelled section renders as
// something rather than as an empty header row.
func helpSectionLabel(id string) string {
	if label, ok := helpSections[id]; ok {
		return label
	}
	return strings.ToUpper(id)
}

// HelpOverlayBindings returns every binding for the full help overlay, in
// source order, each carrying its section.
func HelpOverlayBindings() []HelpEntry {
	out := make([]HelpEntry, 0, len(allKeyBindings))
	for _, kb := range allKeyBindings {
		out = append(out, HelpEntry{Key: kb.Key, Desc: kb.Desc, Section: kb.Section})
	}
	return out
}
