package session

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openGroupTestDB(t *testing.T) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func saveRow(t *testing.T, db *StateDB, id, group string) {
	t.Helper()
	if err := db.SaveSession(&SessionRow{ID: id, Title: id, ProjectPath: "/tmp/" + id, Status: "idle", CreatedAt: time.Now(), GroupID: group}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

func groupOf(t *testing.T, db *StateDB, id string) string {
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
	t.Fatalf("session %s not found", id)
	return ""
}

func TestGroupIDRoundTrips(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "a", "g1")
	if got := groupOf(t, db, "a"); got != "g1" {
		t.Fatalf("group_id = %q, want g1", got)
	}
	if got := FromRow(&SessionRow{ID: "x", GroupID: "g1"}).GroupID(); got != "g1" {
		t.Fatalf("FromRow dropped group id: %q", got)
	}
}

// The parent becomes the lead of a fresh group, moved into it together with
// the child; a second child joins that group rather than minting another.
func TestJoinLeadGroupCreatesOnceAndMovesTheLead(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "parent", "")
	saveRow(t, db, "c1", "")
	saveRow(t, db, "c2", "")
	now := time.Now()

	g1, created, ok, err := db.JoinLeadGroup("parent", "c1", now)
	if err != nil || !ok || !created || g1 == "" {
		t.Fatalf("first child: id=%q created=%v ok=%v err=%v", g1, created, ok, err)
	}
	for _, id := range []string{"parent", "c1"} {
		if got := groupOf(t, db, id); got != g1 {
			t.Fatalf("%s not in the new group: %q", id, got)
		}
	}
	g2, created, ok, err := db.JoinLeadGroup("parent", "c2", now)
	if err != nil || !ok || created || g2 != g1 {
		t.Fatalf("second child: id=%q created=%v ok=%v err=%v, want reuse of %q", g2, created, ok, err, g1)
	}
	if got := groupOf(t, db, "c2"); got != g1 {
		t.Fatalf("second child group = %q", got)
	}
	groups, _ := db.LoadGroups()
	if len(groups) != 1 || groups[0].LeadSessionID != "parent" {
		t.Fatalf("groups = %+v, want one led by parent", groups)
	}
}

// The fan-out case: a parent spawns several children at once, each its own
// process with its own connection. With a deferred BEGIN the losers read the
// parent's empty group_id, then failed to upgrade their stale snapshot to a
// write (SQLITE_BUSY_SNAPSHOT, which busy_timeout doesn't retry) and launched
// ungrouped. Every child must end up in one group.
func TestJoinLeadGroupConcurrentChildrenShareOneGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	main, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	const n = 8
	saveRow(t, main, "parent", "")
	for i := 0; i < n; i++ {
		saveRow(t, main, fmt.Sprintf("c%d", i), "")
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := Open(path) // a separate process's connection pool
			if err != nil {
				errs[i] = err
				return
			}
			defer db.Close()
			ids[i], _, _, errs[i] = db.JoinLeadGroup("parent", fmt.Sprintf("c%d", i), time.Now())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("child %d joined %q, child 0 joined %q", i, ids[i], ids[0])
		}
	}
	if groups, _ := main.LoadGroups(); len(groups) != 1 {
		t.Fatalf("%d groups, want 1", len(groups))
	}
}

// A grandchild's parent is already in a group it does not lead: the
// grandchild joins that group (groups are flat).
func TestJoinLeadGroupReusesAMembersGroup(t *testing.T) {
	db := openGroupTestDB(t)
	if err := db.CreateGroup(&Group{ID: "g", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	saveRow(t, db, "child", "g")
	saveRow(t, db, "grandchild", "")
	id, created, ok, err := db.JoinLeadGroup("child", "grandchild", time.Now())
	if err != nil || !ok || created || id != "g" {
		t.Fatalf("got id=%q created=%v ok=%v err=%v", id, created, ok, err)
	}
}

func TestJoinLeadGroupUnknownLead(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "child", "")
	_, _, ok, err := db.JoinLeadGroup("nope", "child", time.Now())
	if err != nil || ok {
		t.Fatalf("unknown lead: ok=%v err=%v, want ok=false", ok, err)
	}
	if groups, _ := db.LoadGroups(); len(groups) != 0 {
		t.Fatalf("created a group for an unknown lead: %+v", groups)
	}
	if got := groupOf(t, db, "child"); got != "" {
		t.Fatalf("child grouped under an unknown lead: %q", got)
	}
}

// A dangling group id on the lead (its group was dissolved) reads as
// ungrouped, exactly as the sidebar renders it.
func TestJoinLeadGroupReplacesDanglingID(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "parent", "gone")
	id, created, ok, err := db.JoinLeadGroup("parent", "", time.Now())
	if err != nil || !ok || !created || id == "gone" {
		t.Fatalf("got id=%q created=%v ok=%v err=%v", id, created, ok, err)
	}
}

// Named joins match explicit names only, create once, and put the child in.
func TestJoinNamedGroup(t *testing.T) {
	db := openGroupTestDB(t)
	_ = db.CreateGroup(&Group{ID: "unnamed", CreatedAt: time.Now()})
	saveRow(t, db, "a", "")
	saveRow(t, db, "b", "")

	id, created, err := db.JoinNamedGroup("BRZ-12", "a", time.Now())
	if err != nil || !created || id == "unnamed" {
		t.Fatalf("create: id=%q created=%v err=%v", id, created, err)
	}
	again, created, err := db.JoinNamedGroup("BRZ-12", "b", time.Now())
	if err != nil || created || again != id {
		t.Fatalf("join: id=%q created=%v err=%v, want %q", again, created, err, id)
	}
	if groupOf(t, db, "a") != id || groupOf(t, db, "b") != id {
		t.Fatal("children not moved into the named group")
	}
	if _, _, err := db.JoinNamedGroup("", "a", time.Now()); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestCreateGroupForMovesTheSession(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "s", "")
	if err := db.CreateGroupFor(&Group{ID: "g", LeadSessionID: "s", CreatedAt: time.Now()}, "s"); err != nil {
		t.Fatal(err)
	}
	if got := groupOf(t, db, "s"); got != "g" {
		t.Fatalf("session group = %q", got)
	}
}

func TestGroupLabelFromLead(t *testing.T) {
	for _, tc := range []struct{ title, id, want string }{
		{"Fix login", "", "Fix login"},
		{"Fix login", "BRZ-12", "BRZ-12 · Fix login"},
		{"brz-12 fix login", "BRZ-12", "brz-12 fix login"}, // already leads with it
	} {
		if got := GroupLabelFromLead(tc.title, tc.id); got != tc.want {
			t.Errorf("GroupLabelFromLead(%q, %q) = %q, want %q", tc.title, tc.id, got, tc.want)
		}
	}
}

func TestDeleteGroupUngroupsMembers(t *testing.T) {
	db := openGroupTestDB(t)
	_ = db.CreateGroup(&Group{ID: "g", CreatedAt: time.Now()})
	saveRow(t, db, "a", "g")
	if err := db.DeleteGroup("g"); err != nil {
		t.Fatal(err)
	}
	if got := groupOf(t, db, "a"); got != "" {
		t.Fatalf("member still grouped: %q", got)
	}
	if groups, _ := db.LoadGroups(); len(groups) != 0 {
		t.Fatalf("group survived: %+v", groups)
	}
}

func TestDeleteEmptyGroupsHonoursGraceAndKeep(t *testing.T) {
	db := openGroupTestDB(t)
	old := time.Now().Add(-time.Hour)
	_ = db.CreateGroup(&Group{ID: "used", CreatedAt: old})
	_ = db.CreateGroup(&Group{ID: "empty", CreatedAt: old})
	_ = db.CreateGroup(&Group{ID: "kept", CreatedAt: old})
	_ = db.CreateGroup(&Group{ID: "fresh", CreatedAt: time.Now()})
	saveRow(t, db, "a", "used")

	gone, err := db.DeleteEmptyGroups(time.Now().Add(-time.Minute), map[string]bool{"kept": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0] != "empty" {
		t.Fatalf("collected %v, want [empty]", gone)
	}
	groups, _ := db.LoadGroups()
	if len(groups) != 3 {
		t.Fatalf("left %d groups, want 3", len(groups))
	}
}

func TestRetireGroupLeadFreezesOnlyAnUnnamedLabel(t *testing.T) {
	db := openGroupTestDB(t)
	_ = db.CreateGroup(&Group{ID: "u", LeadSessionID: "l", CreatedAt: time.Now()})
	_ = db.CreateGroup(&Group{ID: "n", Name: "mine", LeadSessionID: "l", CreatedAt: time.Now().Add(time.Second)})
	_ = db.RetireGroupLead("u", "Fix login")
	_ = db.RetireGroupLead("n", "Fix login")
	groups, _ := db.LoadGroups()
	if groups[0].Name != "Fix login" || groups[0].LeadSessionID != "" {
		t.Fatalf("unnamed group: %+v", groups[0])
	}
	if groups[1].Name != "mine" || groups[1].LeadSessionID != "" {
		t.Fatalf("named group: %+v", groups[1])
	}
}
