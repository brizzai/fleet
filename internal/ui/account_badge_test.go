package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestAccountBadge(t *testing.T) {
	h, _ := moveHome(t, nil)
	h.accounts.Upsert(claudeaccount.Account{Email: "breno@x.com", ConfigDir: "/d/3", Label: "work"})

	mk := func(ag agent.Type, acct string) *session.Session {
		s := session.NewSession("t", "/tmp/r")
		s.Agent, s.Account = ag, acct
		return s
	}
	tests := []struct {
		name string
		s    *session.Session
		want string
	}{
		{"label wins", mk(agent.Claude, "breno@x.com"), "work"},
		{"email local part", mk(agent.Claude, "first@x.com"), "first"},
		{"no pin is ambient", mk(agent.Claude, ""), "ambient"},
		{"removed account is ambient", mk(agent.Claude, "gone@x.com"), "ambient"},
		{"legacy empty agent is claude", mk("", "first@x.com"), "first"},
		{"codex has no badge", mk(agent.Codex, "first@x.com"), ""},
		{"nil session", nil, ""},
	}
	for _, tc := range tests {
		if got, _ := h.accountBadge(tc.s); got != tc.want {
			t.Errorf("%s: badge = %q, want %q", tc.name, got, tc.want)
		}
	}

	// One account (or none): every session shares a credential, so silence.
	h.accounts = &claudeaccount.Store{}
	h.accounts.Upsert(claudeaccount.Account{Email: "only@x.com", ConfigDir: "/d/1"})
	if got, _ := h.accountBadge(mk(agent.Claude, "only@x.com")); got != "" {
		t.Errorf("single account badge = %q, want none", got)
	}
}

func TestRenderSessionItem_AccountBadge(t *testing.T) {
	s := session.NewSession("a rather long session title for the sidebar", "/tmp/r")
	s.Agent = agent.Claude
	for _, selected := range []bool{false, true} {
		for _, width := range []int{24, 30, 40, 60} {
			out := renderSessionItem(SidebarItem{Session: s, AccountBadge: "breno"}, width, selected, 3)
			if w := ansi.StringWidth(out); w > width {
				t.Errorf("selected=%v width=%d: row is %d cells wide: %q", selected, width, w, out)
			}
		}
		out := renderSessionItem(SidebarItem{Session: s, AccountBadge: "breno"}, 40, selected, -1)
		if !strings.Contains(ansi.Strip(out), "breno") {
			t.Errorf("selected=%v: badge missing: %q", selected, out)
		}
		// Right-aligned: the badge ends the row.
		if plain := strings.TrimRight(ansi.Strip(out), " "); !strings.HasSuffix(plain, "breno") {
			t.Errorf("selected=%v: badge not at the right edge: %q", selected, plain)
		}
		out = renderSessionItem(SidebarItem{Session: s}, 40, selected, -1)
		if strings.Contains(ansi.Strip(out), "breno") {
			t.Errorf("selected=%v: badge shown with none set: %q", selected, out)
		}
	}
}
