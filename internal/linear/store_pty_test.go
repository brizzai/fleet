package linear

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestKeychainWriteWorksUnderATTY is a regression test that must run under a
// real controlling terminal, because that is the only place the bug exists.
//
// `security ... -w` implements an interactive prompt. When a controlling tty is
// present it opens /dev/tty and reads the password from there, ignoring piped
// stdin — so inside the TUI the prompt painted over fleet's own screen and the
// process blocked until the context killed it ("keychain write failed: signal:
// killed"), storing nothing. From a pipe-only shell the same code passes, which
// is exactly why this test allocates a PTY: verifying it any other way reports
// success while the app is broken.
//
// The fix is Setsid in runQuiet. If that is removed, the child below times out
// at storeTimeout instead of finishing in tens of milliseconds.
func TestKeychainWriteWorksUnderATTY(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses the macOS `security` keychain helper")
	}
	if !hasTool("security") {
		t.Skip("no security binary")
	}

	if os.Getenv("FLEET_KEYCHAIN_PTY_CHILD") != "" {
		runKeychainTTYChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestKeychainWriteWorksUnderATTY", "-test.v")
	cmd.Env = append(os.Environ(), "FLEET_KEYCHAIN_PTY_CHILD=1")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("could not allocate a pty: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	<-done

	got := out.String()
	if waitErr != nil || !strings.Contains(got, "KEYCHAIN_TTY_OK") {
		t.Fatalf("keychain write failed under a tty (err=%v). Setsid missing from runQuiet?\n%s", waitErr, got)
	}
	// The prompt reaching the terminal is the visible half of the same bug: it
	// paints over the TUI. Its absence proves the child never went to /dev/tty.
	if strings.Contains(got, "password data for new item") {
		t.Errorf("`security` prompted on the terminal — it is still reading /dev/tty, "+
			"which corrupts the TUI's screen:\n%s", got)
	}
}

func runKeychainTTYChild(t *testing.T) {
	service := fmt.Sprintf("fleet-linear-ttytest-%d", os.Getpid())
	defer func() { _, _ = runQuiet(storeTimeout, nil, "security", "delete-generic-password", "-s", service) }()

	const payload = `{"kind":"api_key","token":"probe-not-a-real-key"}`
	start := time.Now()
	if err := writeSecretTo(service, []byte(payload)); err != nil {
		t.Fatalf("write: %v (after %s)", err, time.Since(start).Round(time.Millisecond))
	}
	// A write that only just beat the deadline is the bug in slow motion.
	if elapsed := time.Since(start); elapsed > storeTimeout/2 {
		t.Fatalf("write took %s, near the %s deadline — it is waiting on something", elapsed, storeTimeout)
	}
	got, ok := readSecretFrom(service)
	if !ok || strings.TrimSpace(string(got)) != payload {
		t.Fatalf("read back %q (ok=%v), want the payload", got, ok)
	}
	fmt.Println("KEYCHAIN_TTY_OK")
}

// TestKeychainStoresRecordsPastThePasswordBufferLimit is the guard for a bug
// that silently ate the credential of every OAuth user on every launch.
//
// `security add-generic-password -w` reads its value with readpassphrase(3),
// whose buffer is _PASSWORD_LEN = 128 bytes. Past that it does not fail, warn or
// return non-zero: it stores the first 128 bytes and exits 0. The record then
// reads back as a JSON prefix that cannot parse, loadStored reports "no
// credential", and the user reconnects on every launch with nothing anywhere
// saying why.
//
// The split is what made it survive review: an API-key record is ~85 bytes and
// fits, so the paste path worked perfectly while the browser path — access token
// plus refresh token plus expiry plus workspace — lost everything, every time.
// A round-trip test with a short value passes either way, which is exactly what
// the original one did.
//
// 129 is the first byte over the cliff; the larger sizes cover multi-chunk
// records, where an aliasing bug in the writer corrupted the first byte of every
// chunk after the first while each write still reported success.
func TestKeychainStoresRecordsPastThePasswordBufferLimit(t *testing.T) {
	if !useKeychain() {
		t.Skip("no macOS keychain here")
	}
	const svc = "fleet-linear-sizetest"
	t.Cleanup(func() { clearKeychainChunks(svc) })

	for _, n := range []int{1, 64, 128, 129, 200, 512, 1024} {
		payload := []byte(strings.Repeat("x", n))
		if err := writeSecretTo(svc, payload); err != nil {
			t.Fatalf("len=%d: write failed: %v", n, err)
		}
		got, ok := readSecretFrom(svc)
		if !ok {
			t.Errorf("len=%d: record did not read back", n)
			continue
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("len=%d: record came back changed — got %d bytes, want %d", n, len(got), n)
		}
	}
}

// TestKeychainShrinkingARecordLeavesNoTail covers the other half of chunking: a
// long record replaced by a short one must not leave old chunks behind for the
// reader to reassemble into something that never existed.
func TestKeychainShrinkingARecordLeavesNoTail(t *testing.T) {
	if !useKeychain() {
		t.Skip("no macOS keychain here")
	}
	const svc = "fleet-linear-shrinktest"
	t.Cleanup(func() { clearKeychainChunks(svc) })

	long := []byte(strings.Repeat("y", 600))
	if err := writeSecretTo(svc, long); err != nil {
		t.Fatalf("write long: %v", err)
	}
	short := []byte("z")
	if err := writeSecretTo(svc, short); err != nil {
		t.Fatalf("write short: %v", err)
	}
	got, ok := readSecretFrom(svc)
	if !ok || !bytes.Equal(got, short) {
		t.Errorf("a shrunk record must read back as itself, got ok=%v len=%d", ok, len(got))
	}
}
