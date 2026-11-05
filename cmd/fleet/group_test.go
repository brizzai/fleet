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
	save := func(id string) {
		t.Helper()
		if err := db.SaveSession(&session.SessionRow{ID: id, Title: id, ProjectPath: "/" + id, Status: "idle", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	groupOf := func(id string) string {
		t.Helper()
		rows, err := db.LoadSessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				return r.GroupID
			}
		}
		t.Fatalf("no session %s", id)
		return ""
	}
	for _, id := range []string{"parent", "a", "b", "c", "d", "e", "f"} {
		save(id)
	}

	// Not launched from a fleet session: ungrouped, nothing printed.
	if note := resolveLaunchGroup(db, groupFlags{}, "", "a"); note != "" || groupOf("a") != "" {
		t.Fatalf("no parent: note=%q group=%q", note, groupOf("a"))
	}
	// --no-group wins over a parent, and leaves the parent alone too.
	resolveLaunchGroup(db, groupFlags{none: true}, "parent", "b")
	if groupOf("b") != "" || groupOf("parent") != "" {
		t.Fatal("--no-group grouped something")
	}
	// A stale parent id leaves the child ungrouped rather than failing.
	if resolveLaunchGroup(db, groupFlags{}, "deleted", "c"); groupOf("c") != "" {
		t.Fatal("stale parent grouped the child")
	}

	// Inheritance: the first child creates the group (moving the parent in
	// with it), the second joins it.
	if note := resolveLaunchGroup(db, groupFlags{}, "parent", "d"); !strings.Contains(note, "Grouped") {
		t.Fatalf("first child note = %q", note)
	}
	first := groupOf("d")
	if first == "" || groupOf("parent") != first {
		t.Fatalf("first child %q, parent %q — want both in one new group", first, groupOf("parent"))
	}
	if note := resolveLaunchGroup(db, groupFlags{}, "parent", "e"); !strings.Contains(note, "Joined") || groupOf("e") != first {
		t.Fatalf("second child: note=%q group=%q, want %q", note, groupOf("e"), first)
	}

	// --group creates by name, overriding the parent.
	if note := resolveLaunchGroup(db, groupFlags{name: "BRZ-12"}, "parent", "f"); !strings.Contains(note, "Created group") {
		t.Fatalf("--group note = %q", note)
	}
	if named := groupOf("f"); named == "" || named == first {
		t.Fatalf("--group put the child in %q", named)
	}
}
