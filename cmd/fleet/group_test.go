package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
)

func TestGroupFlagRules(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    groupFlags
		wantErr string
	}{
		{name: "default inherits", args: []string{"."}},
		{name: "explicit group", args: []string{".", "--group", " BRZ-12 "}, want: groupFlags{name: "BRZ-12"}},
		{name: "opt out", args: []string{"--no-group", "."}, want: groupFlags{none: true}},
		{name: "empty group is a failed substitution", args: []string{".", "--group", ""}, wantErr: "-group was empty"},
		{name: "both conflict", args: []string{".", "--group", "x", "--no-group"}, wantErr: "conflict"},
	}
	for _, tt := range tests {
		t.Run("add/"+tt.name, func(t *testing.T) {
			o, err := parseAddArgs(tt.args)
			checkGroupParse(t, o.group, err, tt.want, tt.wantErr)
		})
		t.Run("worktree/"+tt.name, func(t *testing.T) {
			// Same flags, with the branch standing where the path stood.
			o, err := parseWorktreeArgs(removeDot(append([]string{"feat"}, tt.args...)))
			checkGroupParse(t, o.group, err, tt.want, tt.wantErr)
		})
	}
}

func removeDot(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "." {
			out = append(out, a)
		}
	}
	return out
}

func checkGroupParse(t *testing.T, got groupFlags, err error, want groupFlags, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("err = %v, want containing %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != want {
		t.Fatalf("group = %+v, want %+v", got, want)
	}
}

func TestWorktreeGroupConflictsWithNoSession(t *testing.T) {
	for _, args := range [][]string{
		{"feat", "--no-session", "--group", "x"},
		{"feat", "--no-session", "--no-group"},
	} {
		if _, err := parseWorktreeArgs(args); err == nil || !strings.Contains(err.Error(), "--no-session") {
			t.Fatalf("%v: err = %v, want a --no-session conflict", args, err)
		}
	}
}

func openTestState(t *testing.T) *session.StateDB {
	t.Helper()
	db, err := session.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestResolveLaunchGroup(t *testing.T) {
	db := openTestState(t)
	if err := db.SaveSession(&session.SessionRow{ID: "parent", Title: "p", ProjectPath: "/p", Status: "idle", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Not launched from a fleet session: ungrouped, nothing printed.
	if id, note := resolveLaunchGroup(db, groupFlags{}, ""); id != "" || note != "" {
		t.Fatalf("no parent: id=%q note=%q", id, note)
	}
	// --no-group wins over a parent.
	if id, _ := resolveLaunchGroup(db, groupFlags{none: true}, "parent"); id != "" {
		t.Fatalf("--no-group: id=%q", id)
	}
	// A stale parent id launches ungrouped rather than failing.
	if id, _ := resolveLaunchGroup(db, groupFlags{}, "deleted"); id != "" {
		t.Fatalf("stale parent: id=%q", id)
	}

	// Inheritance: the first child creates the group, the second joins it.
	first, note := resolveLaunchGroup(db, groupFlags{}, "parent")
	if first == "" || !strings.Contains(note, "Grouped") {
		t.Fatalf("first child: id=%q note=%q", first, note)
	}
	second, note := resolveLaunchGroup(db, groupFlags{}, "parent")
	if second != first || !strings.Contains(note, "Joined") {
		t.Fatalf("second child: id=%q note=%q, want %q", second, note, first)
	}

	// --group creates by name once, then joins it — and overrides the parent.
	named, note := resolveLaunchGroup(db, groupFlags{name: "BRZ-12"}, "parent")
	if named == "" || named == first || !strings.Contains(note, "Created group") {
		t.Fatalf("--group create: id=%q note=%q", named, note)
	}
	again, note := resolveLaunchGroup(db, groupFlags{name: "BRZ-12"}, "")
	if again != named || !strings.Contains(note, "Joined group") {
		t.Fatalf("--group join: id=%q note=%q, want %q", again, note, named)
	}
}
