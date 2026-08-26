package ticket

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// keychainAccount is required by `security` but carries no meaning: a fleet
// install talks to one workspace per tracker at a time.
const keychainAccount = "fleet"

// storeTimeout bounds a keychain call. These are local IPC — past a couple of
// seconds the agent is wedged, not slow.
const storeTimeout = 5 * time.Second

// errSecItemNotFound is the exit code `security` uses when the Keychain holds no
// item for the service. Borrowed from claudeaccount, where the same distinction
// matters: "no item" is information, every other failure is not.
const errSecItemNotFound = 44

// Store is one provider's credential slot: macOS Keychain, libsecret, or a 0600
// file, whichever is reachable.
//
// Every field is per-provider, and that is the point — the previous version
// hardcoded a single "fleet-linear" service in three places and a fourth that
// disagreed with the other three (the secret-tool branch ignored the service it
// was handed, so the test seam silently didn't work on Linux). Carrying the
// names on a value makes the second provider a construction rather than an edit.
type Store struct {
	Service  string // keychain / secret-tool item name: "fleet-linear"
	Label    string // secret-tool label, shown in the user's keyring UI
	FileName string // last-resort file under ~/.config/fleet
}

// hasTool reports whether a helper binary is on PATH.
func hasTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// useSecretTool reports whether libsecret is usable. Mirrors how
// clipboardCopyCommandFor picks a Linux clipboard tool: probe for what is
// actually there rather than assuming a desktop.
func useSecretTool() bool { return runtime.GOOS == "linux" && hasTool("secret-tool") }

func useKeychain() bool { return runtime.GOOS == "darwin" && hasTool("security") }

// fallbackPath is where the credential lands when no OS keychain is reachable.
func (s Store) fallbackPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "fleet", s.FileName), nil
}

// Load reads the raw record, and whether there was one.
//
// A failure to read is reported as "no credential" rather than as an error: a
// locked keychain and an empty one look the same from here, and the caller's
// only sensible response to either is to treat the tracker as not connected.
func (s Store) Load() ([]byte, bool) {
	switch {
	case useKeychain():
		return readKeychainChunks(s.Service)
	case useSecretTool():
		out, err := runQuiet(storeTimeout, nil, "secret-tool", "lookup", "service", s.Service)
		if err != nil {
			return nil, false
		}
		return bytes.TrimSpace(out), true
	}
	path, err := s.fallbackPath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return bytes.TrimSpace(data), true
}

// Save stores the record without ever putting it in argv.
//
// That constraint is the whole reason this function is shaped the way it is:
// `security -w <value>` and `security -X <hex>` both work, and both publish the
// credential to every `ps` on the machine. `security ... -w` with no value reads
// the secret from stdin — twice, because it is implementing an interactive
// "type it again" prompt and does not care that stdin is a pipe. secret-tool
// reads it once. Both were verified against a live keychain.
func (s Store) Save(data []byte) error {
	switch {
	case useKeychain():
		return writeKeychainChunks(s.Service, data)
	case useSecretTool():
		_, err := runQuiet(storeTimeout, data, "secret-tool", "store",
			"--label="+s.Label, "service", s.Service)
		if err != nil {
			return fmt.Errorf("secret-tool write failed: %w", err)
		}
		return nil
	}
	path, err := s.fallbackPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// 0600 matches accounts.json. Unlike accounts.json this one really does hold
	// a secret, which is why it is the last resort rather than the default.
	return os.WriteFile(path, data, 0600)
}

func (s Store) Clear() error {
	switch {
	case useKeychain():
		// Every chunk, not just the first — a partial delete would leave a tail
		// that the next read reassembles into a length-mismatched record.
		//
		// No error is returned: clearKeychainChunks already treats a missing
		// chunk 0 as "already gone", which is the outcome the caller wanted, and
		// there is nothing else a failure here would let them do.
		clearKeychainChunks(s.Service)
		return nil
	case useSecretTool():
		_, err := runQuiet(storeTimeout, nil, "secret-tool", "clear", "service", s.Service)
		return err
	}
	path, err := s.fallbackPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// keychainChunkMax is how many bytes of secret go into one keychain item.
//
// `security add-generic-password -w` reads the value with readpassphrase(3),
// whose buffer is _PASSWORD_LEN = 128 bytes. Past that it does not fail, warn,
// or return non-zero — it stores the first 128 bytes and exits 0. The record
// then reads back as a JSON prefix that cannot parse, the load reports "no
// credential", and the user reconnects on every launch with nothing anywhere
// saying why.
//
// It bit OAuth and not API keys, which is the worst possible split: an API-key
// record is ~85 bytes and fits, so the paste path looked fine while the browser
// path silently lost its credential every single time.
//
// 96 rather than 128 to leave headroom, since the limit is a property of a tool
// we do not control and a value one byte under a silent cliff is not a margin.
const keychainChunkMax = 96

// chunkService names the item holding chunk i. Chunk 0 keeps the plain service
// name so an existing single-item record is still found by the same key.
func chunkService(service string, i int) string {
	if i == 0 {
		return service
	}
	return fmt.Sprintf("%s.%d", service, i)
}

// writeKeychainChunks stores data across as many keychain items as it takes.
//
// Chunk 0 carries a "<total>:" header so a torn write is DETECTED rather than
// silently short — which is the entire failure this function exists to end. A
// short read must report "no credential", never a truncated one.
//
// Old chunks are removed first, so a shorter record cannot leave a stale tail
// behind for the reader to pick up.
func writeKeychainChunks(service string, data []byte) error {
	clearKeychainChunks(service)

	body := append([]byte(fmt.Sprintf("%d:", len(data))), data...)
	for i := 0; len(body) > 0; i++ {
		n := keychainChunkMax
		if n > len(body) {
			n = len(body)
		}
		if err := writeOneKeychainItem(chunkService(service, i), body[:n]); err != nil {
			return err
		}
		body = body[n:]
	}
	return nil
}

// writeOneKeychainItem writes a single item, never putting the value in argv.
//
// `security ... -w` with no value reads from stdin — twice, because it is
// implementing an interactive "type it again" prompt and does not care that
// stdin is a pipe. Setsid is what stops it opening /dev/tty instead.
func writeOneKeychainItem(service string, data []byte) error {
	// Built into a fresh buffer, never with append(data, …). data is a SLICE OF
	// THE RECORD here, so appending to it writes into its spare capacity — which
	// is the first byte of the next chunk. That corrupted every multi-chunk
	// write while each individual call still returned success.
	twice := make([]byte, 0, 2*len(data)+2)
	twice = append(twice, data...)
	twice = append(twice, '\n')
	twice = append(twice, data...)
	twice = append(twice, '\n')
	if _, err := runQuiet(storeTimeout, twice, "security", "add-generic-password",
		"-U", "-s", service, "-a", keychainAccount, "-w"); err != nil {
		return fmt.Errorf("keychain write failed: %w", err)
	}
	return nil
}

// readKeychainChunks reassembles a chunked record, verifying the length header.
//
// A record whose parts do not add up is reported as absent. Reconnecting is a
// mild annoyance; handing the caller half a credential is a confusing failure
// somewhere further away.
func readKeychainChunks(service string) ([]byte, bool) {
	var body []byte
	for i := 0; ; i++ {
		out, err := runQuiet(storeTimeout, nil, "security", "find-generic-password", "-w", "-s", chunkService(service, i))
		if err != nil {
			break
		}
		body = append(body, bytes.TrimSpace(out)...)
		if i > maxKeychainChunks {
			return nil, false
		}
	}
	if len(body) == 0 {
		return nil, false
	}
	sep := bytes.IndexByte(body, ':')
	if sep < 0 {
		return nil, false
	}
	want, err := strconv.Atoi(string(body[:sep]))
	if err != nil {
		return nil, false
	}
	got := body[sep+1:]
	if len(got) != want {
		return nil, false
	}
	return got, true
}

// maxKeychainChunks stops a malformed store from looping forever. Far above any
// real credential: 64 chunks is ~6KB.
const maxKeychainChunks = 64

// clearKeychainChunks deletes every item of a chunked record.
func clearKeychainChunks(service string) {
	for i := 0; i <= maxKeychainChunks; i++ {
		_, err := runQuiet(storeTimeout, nil, "security", "delete-generic-password", "-s", chunkService(service, i))
		if err == nil {
			continue
		}
		// A missing item ends the record. Anything else is a real failure, and
		// stopping on it is still right: continuing would spend a `security`
		// call per remaining slot for a keychain that is not answering.
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == errSecItemNotFound {
			if i > 0 {
				return
			}
			continue
		}
		return
	}
}

// runQuiet runs a helper binary with an optional stdin payload and returns
// stdout. Nothing here is ever logged: every one of these commands carries a
// credential on stdin or returns one on stdout.
func runQuiet(timeout time.Duration, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := ContextWithTimeout(timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stderr = nil
	// Detach from the controlling terminal. This is not hygiene, it is the
	// difference between working and hanging: `security ... -w` implements an
	// interactive prompt, and when a controlling tty exists it opens /dev/tty
	// and reads the password from THERE, ignoring the stdin we piped it. Inside
	// the TUI that means the prompt is painted over fleet's own screen and the
	// process blocks until the context kills it — "keychain write failed:
	// signal: killed", with nothing stored.
	//
	// Setsid puts the child in a new session with no controlling terminal, so
	// /dev/tty cannot be opened and it falls back to stdin.
	//
	// This reproduces ONLY under a real tty, which is why it has a PTY-based
	// test: verifying it from a pipe-only shell passes while the app is broken.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Output()
}
