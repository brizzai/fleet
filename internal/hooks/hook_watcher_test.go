package hooks

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestHookWatcherChangesNotifiesOnProcessFile(t *testing.T) {
	dir := t.TempDir()

	// Write a status file before creating the watcher.
	sf := &StatusFile{
		Status:    "running",
		SessionID: "sess-123",
		Event:     "UserPromptSubmit",
		Timestamp: time.Now().Unix(),
	}
	if err := WriteStatusFile(dir, "test-instance", sf); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}

	w, err := newHookWatcherWithDir(dir)
	if err != nil {
		t.Fatalf("newHookWatcherWithDir: %v", err)
	}
	defer w.Stop()

	// processFile should send a notification on Changes().
	w.processFile(dir + "/test-instance.json")

	select {
	case <-w.Changes():
		// OK — notification received.
	case <-time.After(time.Second):
		t.Fatal("expected notification on Changes() after processFile, got none")
	}

	// Verify the status was stored.
	hs := w.GetStatus("test-instance")
	if hs == nil {
		t.Fatal("expected non-nil HookStatus")
		return
	}
	if hs.Status != "running" {
		t.Errorf("expected status 'running', got %q", hs.Status)
	}
	if hs.SessionID != "sess-123" {
		t.Errorf("expected session ID 'sess-123', got %q", hs.SessionID)
	}
}

func TestHookWatcherChangesCoalescesRapidWrites(t *testing.T) {
	dir := t.TempDir()

	w, err := newHookWatcherWithDir(dir)
	if err != nil {
		t.Fatalf("newHookWatcherWithDir: %v", err)
	}
	defer w.Stop()

	// Write multiple status files rapidly.
	for i := 0; i < 5; i++ {
		sf := &StatusFile{
			Status:    "running",
			Event:     "UserPromptSubmit",
			Timestamp: time.Now().Unix(),
		}
		id := "instance-" + string(rune('a'+i))
		if err := WriteStatusFile(dir, id, sf); err != nil {
			t.Fatalf("WriteStatusFile %d: %v", i, err)
		}
		w.processFile(dir + "/" + id + ".json")
	}

	// Should get at least one notification (buffered channel coalesces extras).
	select {
	case <-w.Changes():
		// OK.
	case <-time.After(time.Second):
		t.Fatal("expected at least one notification after rapid writes")
	}

	// Drain any remaining notification.
	select {
	case <-w.Changes():
	default:
	}

	// Channel should now be empty — no more pending.
	select {
	case <-w.Changes():
		t.Fatal("expected no more notifications after draining")
	default:
		// OK — empty as expected.
	}
}

func TestHookWatcherLoadExistingNotifies(t *testing.T) {
	dir := t.TempDir()

	// Write a status file before creating the watcher.
	sf := &StatusFile{
		Status:    "waiting",
		Event:     "PermissionRequest",
		Timestamp: time.Now().Unix(),
	}
	if err := WriteStatusFile(dir, "pre-existing", sf); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}

	w, err := newHookWatcherWithDir(dir)
	if err != nil {
		t.Fatalf("newHookWatcherWithDir: %v", err)
	}
	defer w.Stop()

	// loadExisting is called during construction; processFile sends notifications.
	// Drain all notifications from loadExisting's processFile calls.
	select {
	case <-w.Changes():
	case <-time.After(time.Second):
		t.Fatal("expected notification after loadExisting")
	}

	// Verify the pre-existing status was loaded.
	hs := w.GetStatus("pre-existing")
	if hs == nil {
		t.Fatal("expected non-nil HookStatus for pre-existing file")
		return
	}
	if hs.Status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", hs.Status)
	}
}

func TestHookWatcherNotificationLatency(t *testing.T) {
	dir := t.TempDir()

	// Create a real HookWatcher with fsnotify (not the mock helper).
	w, err := NewHookWatcher()
	if err != nil {
		t.Fatalf("NewHookWatcher: %v", err)
	}
	// Override hooks dir to our temp dir.
	w.hooksDir = dir
	go w.Start()
	defer w.Stop()

	// Give fsnotify a moment to set up the watch.
	time.Sleep(50 * time.Millisecond)

	// Write a status file and measure notification latency.
	start := time.Now()
	sf := &StatusFile{
		Status:    "waiting",
		Event:     "PermissionRequest",
		Timestamp: time.Now().Unix(),
	}
	if err := WriteStatusFile(dir, "latency-test", sf); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}

	select {
	case <-w.Changes():
		elapsed := time.Since(start)
		t.Logf("notification latency: %v", elapsed)
		if elapsed > 250*time.Millisecond {
			t.Errorf("notification too slow: %v (want <250ms)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification received within 2s")
	}
}

// forget must drop the cached status so a stale entry (e.g. a suspended
// session's "dead" death-rattle) isn't served after its status file is cleared.
func TestHookWatcherForgetPurgesStatus(t *testing.T) {
	dir := t.TempDir()
	w, err := newHookWatcherWithDir(dir)
	if err != nil {
		t.Fatalf("newHookWatcherWithDir: %v", err)
	}
	defer w.Stop()

	sf := &StatusFile{Status: "dead", Event: "SessionEnd", Timestamp: time.Now().Unix()}
	if err := WriteStatusFile(dir, "gone-soon", sf); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	w.processFile(dir + "/gone-soon.json")
	if w.GetStatus("gone-soon") == nil {
		t.Fatal("precondition: expected status to be cached")
	}
	// Drain the processFile notification so we can assert forget sends its own.
	select {
	case <-w.Changes():
	default:
	}

	w.forget(dir + "/gone-soon.json")
	if hs := w.GetStatus("gone-soon"); hs != nil {
		t.Fatalf("forget did not purge cached status: %+v", hs)
	}
	select {
	case <-w.Changes():
	case <-time.After(time.Second):
		t.Fatal("expected notification after a real purge")
	}
}

// End-to-end regression: deleting a status file (as clearHookState does on
// suspend/resume/restart) must purge the watcher's cache via the fsnotify
// Remove path, so the worker never replays a resumed session's old "dead"
// hook and flips the freshly-running row to error.
func TestHookWatcherRemoveEventPurgesStatus(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHookWatcher()
	if err != nil {
		t.Fatalf("NewHookWatcher: %v", err)
	}
	w.hooksDir = dir
	go w.Start()
	defer w.Stop()
	time.Sleep(50 * time.Millisecond) // let fsnotify establish the watch

	path := dir + "/purge-me.json"
	sf := &StatusFile{Status: "dead", Event: "SessionEnd", Timestamp: time.Now().Unix()}
	if err := WriteStatusFile(dir, "purge-me", sf); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return w.GetStatus("purge-me") != nil },
		"status was never cached after write")

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return w.GetStatus("purge-me") == nil },
		"delete did not purge the cached status")
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout after %v: %s", timeout, msg)
}

// newHookWatcherWithDir creates a HookWatcher pointing at a custom directory
// (for testing without touching the real hooks dir). It loads existing files
// but does NOT start the fsnotify event loop.
func newHookWatcherWithDir(dir string) (*HookWatcher, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	w := &HookWatcher{
		hooksDir: dir,
		statuses: make(map[string]*HookStatus),
		onChange: make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
		// watcher left nil — we call processFile directly in tests.
	}
	w.loadExisting()
	return w, nil
}
