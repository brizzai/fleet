package linear

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// keychainService is the item name fleet stores its Linear credential under. One
// item holds the whole record as JSON, so an OAuth refresh rewrites a single
// entry rather than juggling three.
const keychainService = "fleet-linear"

// keychainAccount is required by `security` but carries no meaning: a fleet
// install talks to one Linear workspace at a time.
const keychainAccount = "fleet"

// storeTimeout bounds a keychain call. These are local IPC — past a couple of
// seconds the agent is wedged, not slow.
const storeTimeout = 5 * time.Second

// errSecItemNotFound is the exit code `security` uses when the Keychain holds no
// item for the service. Borrowed from claudeaccount, where the same distinction
// matters: "no item" is information, every other failure is not.
const errSecItemNotFound = 44

// stored is the on-disk/keychain record. Deliberately a superset of both credential
// kinds so the backend never needs to know which one it is holding.
type stored struct {
	Kind      string    `json:"kind"` // credAPIKey | credOAuth
	Token     string    `json:"token"`
	Refresh   string    `json:"refresh,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
}

// fallbackPath is where the credential lands when no OS keychain is reachable.
func fallbackPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "fleet", "linear.json"), nil
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

// loadStored reads the credential fleet put away, and whether there was one.
//
// A failure to read is reported as "no credential" rather than as an error: a
// locked keychain and an empty one look the same from here, and the caller's
// only sensible response to either is to treat Linear as not connected.
func loadStored() (stored, bool) {
	raw, ok := readSecret()
	if !ok || len(raw) == 0 {
		return stored{}, false
	}
	var s stored
	if err := json.Unmarshal(raw, &s); err != nil {
		return stored{}, false
	}
	if s.Token == "" {
		return stored{}, false
	}
	return s, true
}

func saveStored(s stored) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeSecret(data)
}

func readSecret() ([]byte, bool) { return readSecretFrom(keychainService) }

// readSecretFrom is readSecret against an explicit service name, matching
// writeSecretTo so the PTY regression test can round-trip its own item.
func readSecretFrom(service string) ([]byte, bool) {
	switch {
	case useKeychain():
		out, err := runQuiet(storeTimeout, nil, "security", "find-generic-password", "-w", "-s", service)
		if err != nil {
			return nil, false
		}
		return bytes.TrimSpace(out), true
	case useSecretTool():
		out, err := runQuiet(storeTimeout, nil, "secret-tool", "lookup", "service", service)
		if err != nil {
			return nil, false
		}
		return bytes.TrimSpace(out), true
	}
	path, err := fallbackPath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return bytes.TrimSpace(data), true
}

// writeSecret stores the record without ever putting it in argv.
//
// That constraint is the whole reason this function is shaped the way it is:
// `security -w <value>` and `security -X <hex>` both work, and both publish the
// credential to every `ps` on the machine. `security ... -w` with no value reads
// the secret from stdin — twice, because it is implementing an interactive
// "type it again" prompt and does not care that stdin is a pipe. secret-tool
// reads it once. Both were verified against a live keychain.
func writeSecret(data []byte) error { return writeSecretTo(keychainService, data) }

// writeSecretTo is writeSecret against an explicit service name, so the PTY
// regression test can use a namespaced item instead of the real one.
func writeSecretTo(service string, data []byte) error {
	switch {
	case useKeychain():
		twice := append(append(append([]byte{}, data...), '\n'), append(data, '\n')...)
		_, err := runQuiet(storeTimeout, twice, "security", "add-generic-password",
			"-U", "-s", service, "-a", keychainAccount, "-w")
		if err != nil {
			return fmt.Errorf("keychain write failed: %w", err)
		}
		return nil
	case useSecretTool():
		_, err := runQuiet(storeTimeout, data, "secret-tool", "store",
			"--label=fleet: Linear", "service", keychainService)
		if err != nil {
			return fmt.Errorf("secret-tool write failed: %w", err)
		}
		return nil
	}
	path, err := fallbackPath()
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

func clearStored() error {
	switch {
	case useKeychain():
		_, err := runQuiet(storeTimeout, nil, "security", "delete-generic-password", "-s", keychainService)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == errSecItemNotFound {
			return nil // already gone is the outcome the caller wanted
		}
		return err
	case useSecretTool():
		_, err := runQuiet(storeTimeout, nil, "secret-tool", "clear", "service", keychainService)
		return err
	}
	path, err := fallbackPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// runQuiet runs a helper binary with an optional stdin payload and returns
// stdout. Nothing here is ever logged: every one of these commands carries a
// credential on stdin or returns one on stdout.
func runQuiet(timeout time.Duration, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := contextWithTimeout(timeout)
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
