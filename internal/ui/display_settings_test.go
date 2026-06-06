package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// resetDisplayFlags restores the package display globals to their defaults so
// tests don't leak state into each other.
func resetDisplayFlags(t *testing.T) {
	t.Helper()
	ApplyDisplayConfig(&config.Config{})
}

func TestRenderSidebarPreview_ContainsVocabulary(t *testing.T) {
	resetDisplayFlags(t)
	out := RenderSidebarPreview(44, 14)
	// Note: the sidebar shows only the last branch segment, so "feat/preview"
	// renders as "preview" (real product behavior we deliberately preview).
	for _, want := range []string{"fleet", "scratch", "Refactor", "#128", claudeGlyph, codexGlyph} {
		if !strings.Contains(out, want) {
			t.Errorf("preview missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSidebarPreview_AgentGlyphsToggle(t *testing.T) {
	resetDisplayFlags(t)
	off := false
	ApplyDisplayConfig(&config.Config{ShowAgentGlyphs: &off})
	defer resetDisplayFlags(t)

	out := RenderSidebarPreview(44, 14)
	if strings.Contains(out, claudeGlyph) || strings.Contains(out, codexGlyph) {
		t.Errorf("agent glyphs should be hidden when ShowAgentGlyphs is off:\n%s", out)
	}
}

func TestRenderSidebarPreview_PRBadgeToggle(t *testing.T) {
	resetDisplayFlags(t)
	off := false
	ApplyDisplayConfig(&config.Config{ShowPRBadges: &off})
	defer resetDisplayFlags(t)

	out := RenderSidebarPreview(44, 14)
	if strings.Contains(out, "#128") {
		t.Errorf("PR badge should be hidden when ShowPRBadges is off:\n%s", out)
	}
}

func TestChevronGlyphStyles(t *testing.T) {
	resetDisplayFlags(t)
	defer resetDisplayFlags(t)

	ChevronStyle = "triangle"
	if got := chevronGlyph(true); got != "▾" {
		t.Errorf("triangle expanded = %q, want ▾", got)
	}
	ChevronStyle = "plusminus"
	if got := chevronGlyph(false); got != "+" {
		t.Errorf("plusminus collapsed = %q, want +", got)
	}
}

func TestSettingsDialog_AppearanceViewAndToggle(t *testing.T) {
	resetDisplayFlags(t)
	defer resetDisplayFlags(t)

	cfg := &config.Config{}
	d := NewSettingsDialog(cfg)
	d.SetSize(140, 40)
	d.Show()

	// Appearance is the default category and renders without panicking.
	if v := d.View(); v == "" {
		t.Fatal("settings View() returned empty")
	}

	// Dive into the detail pane and flip the first toggle-style row (Agent
	// icons sits after the THEME subheader + Theme + Status style rows).
	d.focus = focusDetail
	for i, r := range d.curRows() {
		if r.label == "Agent icons" {
			d.rowCursor = i
		}
	}
	d.cycleCurrent(1)
	if cfg.IsShowAgentGlyphs() {
		t.Error("cycling Agent icons should have turned it off")
	}
	if ShowAgentGlyphs {
		t.Error("toggling Agent icons should sync the render global live")
	}
}

func TestSettingsDialog_BehaviorHasNoThemeRow(t *testing.T) {
	resetDisplayFlags(t)
	defer resetDisplayFlags(t)

	d := NewSettingsDialog(&config.Config{})
	d.SetSize(140, 40)
	d.Show()
	// Move to Behavior category.
	d.categoryCursor = 1
	for _, r := range d.curRows() {
		if r.label == "Theme" {
			t.Error("Theme should live under Appearance, not Behavior")
		}
	}
}

func TestOnboardingDialog_ConfirmAndSkip(t *testing.T) {
	resetDisplayFlags(t)
	defer resetDisplayFlags(t)

	// Confirm path: picks a non-default theme and marks onboarding seen.
	cfg := &config.Config{}
	d := NewOnboardingDialog(cfg)
	d.SetSize(120, 40)
	d.Show()
	if v := d.View(); !strings.Contains(v, "Welcome to fleet") {
		t.Fatalf("onboarding View() missing title:\n%s", v)
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRight}) // advance theme
	wantTheme := BuiltinPalettes[1].Name
	// Update mutates the shared cfg pointer; we assert on cfg, not the return.
	_, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cfg.Theme != wantTheme {
		t.Errorf("confirm set theme %q, want %q", cfg.Theme, wantTheme)
	}
	if !cfg.DisplayOnboardingSeen {
		t.Error("confirm should mark DisplayOnboardingSeen")
	}

	// Skip path: reverts to the original theme but still marks seen.
	cfg2 := &config.Config{Theme: "nord"}
	d2 := NewOnboardingDialog(cfg2)
	d2.SetSize(120, 40)
	d2.Show()
	d2, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRight})
	_, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cfg2.Theme != "nord" {
		t.Errorf("skip should revert theme to nord, got %q", cfg2.Theme)
	}
	if !cfg2.DisplayOnboardingSeen {
		t.Error("skip should still mark DisplayOnboardingSeen")
	}
}
