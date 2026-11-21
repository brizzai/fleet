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

// A hand-written template must stay reachable from itself in both directions,
// not be replaced by the first preset with no way back.
func TestCycleWorktreeLocationKeepsCustom(t *testing.T) {
	custom := "~/code/wt/{{repo}}/{{name}}"
	if got := cycleWorktreeLocation(custom, 1); got != "" {
		t.Errorf("step forward from custom = %q, want default preset", got)
	}
	if got := cycleWorktreeLocation(custom, -1); got != "~/worktrees" {
		t.Errorf("step back from custom = %q, want last preset", got)
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
