package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/workspace"
)

func TestWorktreeLocationLabel(t *testing.T) {
	tests := []struct{ tmpl, want string }{
		{"", "Beside repo"},
		{workspace.DefaultWorktreePathTemplate, "Beside repo"},
		{"../{{repo}}.worktrees", "<repo>.worktrees/"},
		{" ~/worktrees ", "~/worktrees/"},
		{"~/code/wt/{{repo}}/{{name}}", "Custom"},
	}
	for _, tt := range tests {
		if got := worktreeLocationLabel(tt.tmpl); got != tt.want {
			t.Errorf("worktreeLocationLabel(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

// A hand-written template must stay reachable for the whole Settings visit:
// stepping onto a preset and back (either way) restores it, rather than the
// ring forgetting it after the first press and saving a preset over it.
func TestCycleWorktreeLocationKeepsCustom(t *testing.T) {
	custom := "~/code/wt/{{repo}}/{{name}}"
	tests := []struct {
		name  string
		steps []int
		want  string
	}{
		{"forward", []int{1}, ""},
		{"back", []int{-1}, "~/worktrees"},
		{"forward then back", []int{1, -1}, custom},
		{"back then forward", []int{-1, 1}, custom},
		{"full lap", []int{1, 1, 1, 1}, custom},
	}
	for _, tt := range tests {
		cur := custom
		for _, dir := range tt.steps {
			cur = cycleWorktreeLocation(cur, custom, dir)
		}
		if cur != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, cur, tt.want)
		}
	}
	// No custom value: the ring is just the presets.
	if got := cycleWorktreeLocation("", "", -1); got != "~/worktrees" {
		t.Errorf("presets only, back from default = %q", got)
	}
}

// Through the dialog itself: Settings opened on a custom template, → then ←
// must leave the custom template in the config that Esc saves.
func TestSettingsWorktreeLocationRoundTripKeepsCustom(t *testing.T) {
	defer workspace.SetGlobalWorktreePath("")
	custom := "~/code/wt/{{repo}}/{{name}}"
	cfg := &config.Config{WorktreePath: custom}
	d := NewSettingsDialog(cfg)
	d.SetSize(140, 40)
	d.Show()
	d.categoryCursor = 1
	d.focus = focusDetail
	for i, r := range d.curRows() {
		if r.label == "Worktree location" {
			d.rowCursor = i
		}
	}
	d.cycleCurrent(1)
	d.cycleCurrent(-1)
	if cfg.WorktreePath != custom {
		t.Errorf("after → ← worktree_path = %q, want the custom %q", cfg.WorktreePath, custom)
	}
}

func TestSettingsWorktreeLocationRowAppliesLive(t *testing.T) {
	defer workspace.SetGlobalWorktreePath("")
	cfg := &config.Config{}
	d := NewSettingsDialog(cfg)
	d.SetSize(140, 40)
	d.Show()
	d.categoryCursor = 1
	d.focus = focusDetail

	row := -1
	for i, r := range d.curRows() {
		if r.label == "Worktree location" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("Worktree location row missing from the Behavior category")
	}
	d.rowCursor = row

	want := []string{"<repo>.worktrees/", "~/worktrees/", "Beside repo"}
	for i, w := range want {
		d.cycleCurrent(1)
		if got := d.curRows()[row].value(cfg); got != w {
			t.Errorf("step %d: value = %q, want %q", i+1, got, w)
		}
		if got := workspace.WorktreePathTemplate(t.TempDir()); cfg.WorktreePath != "" && got != cfg.WorktreePath {
			t.Errorf("step %d: global template %q not applied (config %q)", i+1, got, cfg.WorktreePath)
		}
	}
}
