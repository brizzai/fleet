package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/session"
)

// TestPersistRelaunchEndsIndefiniteSnooze: `fleet resume`, `fleet restart` and
// a `fleet send` wake all relaunch through persistRelaunch, and each is the
// user picking the session back up. An "until I wake it" snooze left on the
// row would have the next TUI suspend the session again as soon as it went
// idle. A timed snooze is a reminder and survives.
func TestPersistRelaunchEndsIndefiniteSnooze(t *testing.T) {
	storage, err := session.Open(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer storage.Close()

	now := time.Now().Truncate(time.Second).Add(-time.Hour)
	timedUntil := now.Add(time.Hour)
	cases := []struct {
		id     string
		snooze time.Time
		want   time.Time
	}{
		{"parked-1700000000", session.SnoozeForever, time.Time{}},
		{"timed-1700000000", timedUntil, timedUntil},
	}
	for _, tc := range cases {
		row := &session.SessionRow{
			ID: tc.id, Title: tc.id, ProjectPath: "/tmp/p", Status: "suspended",
			CreatedAt: now, LastAccessed: now, SnoozedUntil: tc.snooze,
		}
		if err := storage.SaveSession(row); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
		persistRelaunch(storage, row, session.FromRow(row))
	}
	rows, _ := storage.LoadSessions()
	for _, r := range rows {
		for _, tc := range cases {
			if r.ID != tc.id {
				continue
			}
			if !r.SnoozedUntil.Equal(tc.want) {
				t.Errorf("%s snooze after relaunch = %v, want %v", tc.id, r.SnoozedUntil, tc.want)
			}
			// The access stamp exempts it from a parked group's sweep.
			if !r.LastAccessed.After(now) {
				t.Errorf("%s last_accessed = %v, want it stamped by the relaunch", tc.id, r.LastAccessed)
			}
		}
	}
}

// TestStatusShowsIndefiniteSnooze: the sentinel must never surface as a date
// ("until Dec 31 9999") — text says "until woken", JSON sets a flag instead of
// a timestamp.
func TestStatusShowsIndefiniteSnooze(t *testing.T) {
	ob := &control.Observation{
		Row:  &session.SessionRow{ID: "p-1", Title: "parked", ProjectPath: "/tmp/p", SnoozedUntil: session.SnoozeForever},
		Slot: -1,
	}
	var b bytes.Buffer
	printDetail(&b, ob)
	if !strings.Contains(b.String(), "until woken") || strings.Contains(b.String(), "9999") {
		t.Errorf("detail block:\n%s\nwant \"until woken\" and no sentinel date", b.String())
	}

	j := toJSON(ob, 100, false)
	if !j.SnoozedIndefinitely || j.SnoozedUntil != nil {
		t.Errorf("json snooze = indefinitely %v, until %v; want true, nil", j.SnoozedIndefinitely, j.SnoozedUntil)
	}
}
