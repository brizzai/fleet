package ui

import "github.com/charmbracelet/lipgloss"

// Palette defines a complete color theme for the TUI.
type Palette struct {
	Name    string
	Bg      lipgloss.Color
	Surface lipgloss.Color
	Border  lipgloss.Color
	Text    lipgloss.Color
	TextDim lipgloss.Color
	Accent  lipgloss.Color
	Green   lipgloss.Color
	Yellow  lipgloss.Color
	Blue    lipgloss.Color
	Red     lipgloss.Color
	Gray    lipgloss.Color
	Orange  lipgloss.Color
	Purple  lipgloss.Color
}

// Built-in palette definitions.
var (
	// Fleet Pink — vivid candy pink (#ff77c6) accent with sky-blue support.
	// Accent carries the brand; sky blue is the secondary cool tone (branch
	// names, finished status). The supporting status palette sits a notch
	// quieter so semantic colors stay readable without out-shouting the accent.
	PaletteFleetPink = Palette{
		Name:    "fleet-pink",
		Bg:      lipgloss.Color("#17121f"),
		Surface: lipgloss.Color("#241a2e"),
		Border:  lipgloss.Color("#6a4d78"),
		Text:    lipgloss.Color("#f6eaf4"),
		TextDim: lipgloss.Color("#857a8c"),
		Accent:  lipgloss.Color("#ff77c6"),
		Blue:    lipgloss.Color("#7dcfff"),
		Green:   lipgloss.Color("#8ad698"),
		Yellow:  lipgloss.Color("#e8c590"),
		Red:     lipgloss.Color("#e07685"),
		Orange:  lipgloss.Color("#ffa05c"),
		Purple:  lipgloss.Color("#bb9af7"),
		Gray:    lipgloss.Color("#857a8c"),
	}

	PaletteTokyoNight = Palette{
		Name:    "tokyo-night",
		Bg:      lipgloss.Color("#1a1b26"),
		Surface: lipgloss.Color("#24283b"),
		Border:  lipgloss.Color("#414868"),
		Text:    lipgloss.Color("#c0caf5"),
		TextDim: lipgloss.Color("#565f89"),
		Accent:  lipgloss.Color("#7aa2f7"),
		Green:   lipgloss.Color("#9ece6a"),
		Yellow:  lipgloss.Color("#e0af68"),
		Blue:    lipgloss.Color("#7dcfff"),
		Red:     lipgloss.Color("#f7768e"),
		Gray:    lipgloss.Color("#565f89"),
		Orange:  lipgloss.Color("#ff9e64"),
		Purple:  lipgloss.Color("#bb9af7"),
	}

	PaletteCatppuccin = Palette{
		Name:    "catppuccin-mocha",
		Bg:      lipgloss.Color("#1e1e2e"),
		Surface: lipgloss.Color("#313244"),
		Border:  lipgloss.Color("#45475a"),
		Text:    lipgloss.Color("#cdd6f4"),
		TextDim: lipgloss.Color("#6c7086"),
		Accent:  lipgloss.Color("#89b4fa"),
		Green:   lipgloss.Color("#a6e3a1"),
		Yellow:  lipgloss.Color("#f9e2af"),
		Blue:    lipgloss.Color("#94e2d5"),
		Red:     lipgloss.Color("#f38ba8"),
		Gray:    lipgloss.Color("#6c7086"),
		Orange:  lipgloss.Color("#fab387"),
		Purple:  lipgloss.Color("#cba6f7"),
	}

	PaletteRosePine = Palette{
		Name:    "rose-pine",
		Bg:      lipgloss.Color("#191724"),
		Surface: lipgloss.Color("#1f1d2e"),
		Border:  lipgloss.Color("#26233a"),
		Text:    lipgloss.Color("#e0def4"),
		TextDim: lipgloss.Color("#6e6a86"),
		Accent:  lipgloss.Color("#c4a7e7"),
		Green:   lipgloss.Color("#9ccfd8"),
		Yellow:  lipgloss.Color("#f6c177"),
		Blue:    lipgloss.Color("#ebbcba"),
		Red:     lipgloss.Color("#eb6f92"),
		Gray:    lipgloss.Color("#908caa"),
		Orange:  lipgloss.Color("#ebbcba"),
		Purple:  lipgloss.Color("#c4a7e7"),
	}

	PaletteNord = Palette{
		Name:    "nord",
		Bg:      lipgloss.Color("#2e3440"),
		Surface: lipgloss.Color("#3b4252"),
		Border:  lipgloss.Color("#4c566a"),
		Text:    lipgloss.Color("#eceff4"),
		TextDim: lipgloss.Color("#616e88"),
		Accent:  lipgloss.Color("#88c0d0"),
		Green:   lipgloss.Color("#a3be8c"),
		Yellow:  lipgloss.Color("#ebcb8b"),
		Blue:    lipgloss.Color("#81a1c1"),
		Red:     lipgloss.Color("#bf616a"),
		Gray:    lipgloss.Color("#616e88"),
		Orange:  lipgloss.Color("#d08770"),
		Purple:  lipgloss.Color("#b48ead"),
	}

	PaletteGruvbox = Palette{
		Name:    "gruvbox",
		Bg:      lipgloss.Color("#282828"),
		Surface: lipgloss.Color("#3c3836"),
		Border:  lipgloss.Color("#504945"),
		Text:    lipgloss.Color("#ebdbb2"),
		TextDim: lipgloss.Color("#928374"),
		Accent:  lipgloss.Color("#8ec07c"),
		Green:   lipgloss.Color("#b8bb26"),
		Yellow:  lipgloss.Color("#fabd2f"),
		Blue:    lipgloss.Color("#83a598"),
		Red:     lipgloss.Color("#fb4934"),
		Gray:    lipgloss.Color("#928374"),
		Orange:  lipgloss.Color("#fe8019"),
		Purple:  lipgloss.Color("#d3869b"),
	}
)

// BuiltinPalettes lists all available themes. Fleet Pink is the flagship default.
var BuiltinPalettes = []Palette{
	PaletteFleetPink,
	PaletteTokyoNight,
	PaletteCatppuccin,
	PaletteRosePine,
	PaletteNord,
	PaletteGruvbox,
}

// DefaultPaletteName is the theme used when no theme is configured or the
// configured name doesn't match a built-in.
const DefaultPaletteName = "fleet-pink"

// PaletteByName returns the palette matching the given name, or the default as fallback.
func PaletteByName(name string) Palette {
	for _, p := range BuiltinPalettes {
		if p.Name == name {
			return p
		}
	}
	return PaletteFleetPink
}

// PaletteDisplayName returns a human-readable display name for a palette.
func PaletteDisplayName(name string) string {
	switch name {
	case "fleet-pink":
		return "Fleet Pink"
	case "tokyo-night":
		return "Tokyo Night"
	case "catppuccin-mocha":
		return "Catppuccin Mocha"
	case "rose-pine":
		return "Rosé Pine"
	case "nord":
		return "Nord"
	case "gruvbox":
		return "Gruvbox"
	default:
		return name
	}
}
