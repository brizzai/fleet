package session

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestClosedSessionsKeepNewestAndRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < ClosedKeep+5; i++ {
		row := &SessionRow{ID: fmt.Sprintf("s%02d", i), Title: fmt.Sprintf("t%d", i), ProjectPath: "/p",
			Agent: "claude", Account: "a@b.c", ClaudeSessionID: fmt.Sprintf("conv-%d", i)}
		if err := db.RecordClosed(row, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.LoadClosed()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != ClosedKeep {
		t.Fatalf("kept %d, want %d", len(got), ClosedKeep)
	}
	newest := fmt.Sprintf("s%02d", ClosedKeep+4)
	if got[0].Row.ID != newest || got[0].Row.ClaudeSessionID != fmt.Sprintf("conv-%d", ClosedKeep+4) || got[0].Row.Account != "a@b.c" {
		t.Fatalf("newest first / full round trip lost: %+v", got[0].Row)
	}
	if got[len(got)-1].Row.ID != "s05" {
		t.Fatalf("oldest kept = %s, want s05", got[len(got)-1].Row.ID)
	}

	if err := db.DeleteClosed(newest); err != nil {
		t.Fatal(err)
	}
	got, _ = db.LoadClosed()
	if got[0].Row.ID == newest {
		t.Fatal("DeleteClosed left the entry behind")
	}
}

func TestClosedSessionClosedTwiceKeepsOneEntry(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	row := &SessionRow{ID: "x", Title: "one", ProjectPath: "/p"}
	_ = db.RecordClosed(row, time.Unix(100, 0))
	row.Title = "two"
	_ = db.RecordClosed(row, time.Unix(200, 0))
	got, _ := db.LoadClosed()
	if len(got) != 1 || got[0].Row.Title != "two" {
		t.Fatalf("want one entry titled two, got %+v", got)
	}
}
