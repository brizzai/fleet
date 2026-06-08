package ui

import (
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

// TestModalOpenCoversEveryDialog guards against drift between renderBody's
// per-dialog early-returns and modalOpen(): if either grows a new full-screen
// dialog and the other forgets it, the sticky bottom-right tip would composite
// on top of that dialog — the exact bug modalOpen exists to prevent.
//
// Rather than hand-list the dialogs (which would drift the same way), it uses
// reflection to enumerate every pointer field on Home that exposes
// `IsVisible() bool` over an unexported `visible` bool (the dialog convention),
// flips each visible in turn, and asserts modalOpen() returns true. A new dialog
// field is picked up automatically, so the guard can't go stale.
func TestModalOpenCoversEveryDialog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "modal.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})

	hv := reflect.ValueOf(h).Elem()
	ht := hv.Type()
	checked := 0
	for i := 0; i < hv.NumField(); i++ {
		f := hv.Field(i)
		if f.Kind() != reflect.Ptr || f.IsNil() {
			continue
		}
		if !f.MethodByName("IsVisible").IsValid() {
			continue
		}
		vis := f.Elem().FieldByName("visible")
		if !vis.IsValid() || vis.Kind() != reflect.Bool {
			// A dialog that doesn't follow the `visible bool` convention can't be
			// driven generically — surface it so the under-coverage is visible.
			t.Logf("skipping %s: no settable `visible` bool field", ht.Field(i).Name)
			continue
		}
		set := func(b bool) {
			reflect.NewAt(vis.Type(), unsafe.Pointer(vis.UnsafeAddr())).Elem().SetBool(b)
		}

		set(true)
		if !h.modalOpen() {
			t.Errorf("modalOpen() is false while %s is visible — add it to modalOpen()", ht.Field(i).Name)
		}
		set(false)
		checked++
	}

	if checked == 0 {
		t.Fatal("reflection found no dialog fields — the guard is exercising nothing")
	}
}
