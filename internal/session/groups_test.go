package session

import (
	"path/filepath"
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

// The parent becomes the lead of a fresh group, and a second child spawned
// by the same parent joins that group rather than minting another.
func TestEnsureLeadGroupCreatesOnceAndMovesTheLead(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "parent", "")
	now := time.Now()

	g1, created, ok, err := db.EnsureLeadGroup("parent", now)
	if err != nil || !ok || !created || g1 == "" {
		t.Fatalf("first call: id=%q created=%v ok=%v err=%v", g1, created, ok, err)
	}
	if got := groupOf(t, db, "parent"); got != g1 {
		t.Fatalf("lead not moved into its group: %q", got)
	}
	g2, created, ok, err := db.EnsureLeadGroup("parent", now)
	if err != nil || !ok || created || g2 != g1 {
		t.Fatalf("second call: id=%q created=%v ok=%v err=%v, want reuse of %q", g2, created, ok, err, g1)
	}
	groups, _ := db.LoadGroups()
	if len(groups) != 1 || groups[0].LeadSessionID != "parent" {
		t.Fatalf("groups = %+v, want one led by parent", groups)
	}
}

// A grandchild's parent is already in a group it does not lead: the
// grandchild joins that group (groups are flat).
func TestEnsureLeadGroupReusesAMembersGroup(t *testing.T) {
	db := openGroupTestDB(t)
	if err := db.CreateGroup(&Group{ID: "g", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	saveRow(t, db, "child", "g")
	id, created, ok, err := db.EnsureLeadGroup("child", time.Now())
	if err != nil || !ok || created || id != "g" {
		t.Fatalf("got id=%q created=%v ok=%v err=%v", id, created, ok, err)
	}
}

func TestEnsureLeadGroupUnknownLead(t *testing.T) {
	db := openGroupTestDB(t)
	_, _, ok, err := db.EnsureLeadGroup("nope", time.Now())
	if err != nil || ok {
		t.Fatalf("unknown lead: ok=%v err=%v, want ok=false", ok, err)
	}
	if groups, _ := db.LoadGroups(); len(groups) != 0 {
		t.Fatalf("created a group for an unknown lead: %+v", groups)
	}
}

// A dangling group id on the lead (its group was dissolved) reads as
// ungrouped, exactly as the sidebar renders it.
func TestEnsureLeadGroupReplacesDanglingID(t *testing.T) {
	db := openGroupTestDB(t)
	saveRow(t, db, "parent", "gone")
	id, created, ok, err := db.EnsureLeadGroup("parent", time.Now())
	if err != nil || !ok || !created || id == "gone" {
		t.Fatalf("got id=%q created=%v ok=%v err=%v", id, created, ok, err)
	}
}

func TestFindGroupByNameMatchesExplicitNamesOnly(t *testing.T) {
	db := openGroupTestDB(t)
	_ = db.CreateGroup(&Group{ID: "a", Name: "", CreatedAt: time.Now()})
	_ = db.CreateGroup(&Group{ID: "b", Name: "BRZ-12", CreatedAt: time.Now()})
	g, err := db.FindGroupByName("BRZ-12")
	if err != nil || g == nil || g.ID != "b" {
		t.Fatalf("got %+v err=%v", g, err)
	}
	if g, _ := db.FindGroupByName(""); g != nil {
		t.Fatalf("empty name matched %+v", g)
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
