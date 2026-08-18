package linear

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// The two credential kinds fleet can hold. They differ in exactly one place —
// the Authorization header form — which is why they share a struct.
const (
	// KindAPIKey is exported because the Connect dialog constructs a Credential
	// directly, to be verified before anything is stored.
	KindAPIKey = "api_key"
	KindOAuth  = "oauth"

	credAPIKey = KindAPIKey
	credOAuth  = KindOAuth
)

// APIKeyEnvVar is the escape hatch, and it is the `linear` CLI's own convention
// so anyone already scripting against Linear needs no migration. It wins over
// the stored credential, so a stale one can always be overridden without
// touching the UI — and so CI, which has no keychain, works at all.
const APIKeyEnvVar = "LINEAR_API_KEY"

// Credential is what fleet needs to talk to Linear.
type Credential struct {
	Kind      string
	Token     string
	Refresh   string
	ExpiresAt time.Time
	Workspace string
}

// authHeader returns the Authorization value for this credential.
//
// The two forms are not interchangeable and getting it wrong reads as a rejected
// credential: a personal API key is sent RAW, with no scheme, while an OAuth
// access token takes the ordinary Bearer prefix. Verified against the live API.
func (c Credential) authHeader() string {
	if c.Kind == credOAuth {
		return "Bearer " + c.Token
	}
	return c.Token
}

func (c Credential) ok() bool { return c.Token != "" }

// credState is the process-wide resolved credential.
//
// It exists because Available() is called from the Bubble Tea Update goroutine
// (ticket.go's branch inference), where a keychain subprocess must never run.
// warmed is an atomic so that read costs nothing; the mutex only guards the
// slower paths that actually load or replace it.
var credState struct {
	mu      sync.Mutex
	cred    Credential
	loaded  bool
	warmed  atomic.Bool
	present atomic.Bool
}

// Available reports whether fleet has a credential to work with.
//
// Free and non-blocking by contract: it reads two atomics and an env var, never
// the keychain. Before Warm has run it answers false, which makes every ticket
// surface inert for the few milliseconds after launch — the honest answer, and
// cheaper than blocking a frame to find out.
func Available() bool {
	if envKey() != "" {
		return true
	}
	return credState.warmed.Load() && credState.present.Load()
}

// Resolved reports whether fleet has finished looking for a credential.
//
// The distinction matters to anything that acts on the ABSENCE of one: before
// Warm runs, Available() answers false because it does not know yet, and a
// caller that reads that as "this user has no Linear" would, for the first
// moments of every launch, be wrong about a connected user.
func Resolved() bool { return credState.warmed.Load() }

func envKey() string { return strings.TrimSpace(getenv(APIKeyEnvVar)) }

// Warm loads the stored credential once, off the Update goroutine.
//
// Called from the TUI's startup batch. Idempotent: a second call after a
// successful load is a no-op, so it is safe to use as a "make sure" before any
// path that needs a definite answer.
func Warm() {
	credState.mu.Lock()
	defer credState.mu.Unlock()
	loadLocked()
}

func loadLocked() Credential {
	if credState.loaded {
		return credState.cred
	}
	if key := envKey(); key != "" {
		credState.cred = Credential{Kind: credAPIKey, Token: key}
	} else if s, ok := loadStored(); ok {
		// stored and Credential are field-identical on purpose: the wire format
		// is named separately because it carries the JSON tags, but there is no
		// mapping to get wrong.
		credState.cred = Credential(s)
	}
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(credState.cred.ok())
	return credState.cred
}

// refreshMargin is how early an OAuth token is renewed. Linear's access tokens
// last 24 hours, so five minutes of slack costs nothing and covers a clock that
// is a little off or a request that takes a while to start.
const refreshMargin = 5 * time.Minute

// refreshMu serializes renewal. Without it a ticket with a dozen screenshots
// starts a dozen concurrent downloads, each finds the token stale, and each
// spends a refresh — with the losers' tokens immediately superseded.
var refreshMu sync.Mutex

// credential returns the resolved credential, loading and renewing it as needed.
//
// Only ever called from the API paths, which all run off the Update goroutine,
// so it is allowed to block on the keychain and on the network.
func credential() (Credential, error) {
	credState.mu.Lock()
	c := loadLocked()
	credState.mu.Unlock()

	if !c.ok() {
		return Credential{}, ErrNotConnected
	}
	if !c.needsRefresh() {
		return c, nil
	}
	return renew(c)
}

// needsRefresh reports whether an OAuth credential is at or near expiry. An API
// key never expires, and an OAuth credential with no refresh token cannot be
// renewed — in both cases the honest move is to use what we have and let the API
// be the judge.
func (c Credential) needsRefresh() bool {
	if c.Kind != credOAuth || c.Refresh == "" || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Until(c.ExpiresAt) < refreshMargin
}

func renew(c Credential) (Credential, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	// Re-read under the refresh lock: another goroutine may have renewed while
	// this one waited, and spending a second refresh would invalidate the first.
	credState.mu.Lock()
	current := credState.cred
	credState.mu.Unlock()
	if current.ok() && !current.needsRefresh() {
		return current, nil
	}

	ctx, cancel := contextWithTimeout(metaTimeout)
	defer cancel()

	fresh, err := refresh(ctx, c.Refresh)
	if err != nil {
		// A refused refresh is terminal: the grant is gone and every subsequent
		// request would 401 with no explanation anywhere. Clearing it is what
		// makes the dialog and the tip say "not connected" instead of leaving
		// the user with a fleet that silently stopped fetching tickets.
		if errors.Is(err, ErrNotAuthenticated) {
			debuglog.Logger.Warn("linear: refresh was refused — disconnecting")
			_ = Disconnect()
			return Credential{}, ErrNotAuthenticated
		}
		// Anything else (offline, endpoint down) is not evidence against the
		// grant. Keep it and let the caller fail this one request.
		return Credential{}, err
	}

	fresh.Workspace = c.Workspace
	if fresh.Refresh == "" {
		fresh.Refresh = c.Refresh // Linear may not reissue one
	}
	if err := SetCredential(fresh); err != nil {
		debuglog.Logger.Debug("linear: refreshed token could not be persisted", "error", err)
	}
	return fresh, nil
}

// SetCredential stores a credential and makes it live immediately.
//
// The in-memory copy is replaced before the write is attempted so that a
// keychain that refuses to store still leaves this session working — the user
// pasted a key that we verified against the API, and failing to persist it is
// not a reason to act as though they hadn't.
func SetCredential(c Credential) error {
	credState.mu.Lock()
	credState.cred = c
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(c.ok())
	credState.mu.Unlock()

	return saveStored(stored(c))
}

// Disconnect forgets the credential.
//
// The cache is cleared even if the backing store refuses, for the same reason
// SetCredential updates it first: the user's instruction is about this fleet,
// and a keychain error must not leave a session still talking to Linear after
// being told to stop.
func Disconnect() error {
	credState.mu.Lock()
	credState.cred = Credential{}
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(false)
	credState.mu.Unlock()

	resetWorkspaceCache()
	return clearStored()
}

// StoredWorkspace returns the workspace name behind the current credential, for
// display only. Empty when nothing is connected or the name was never recorded.
func StoredWorkspace() string {
	credState.mu.Lock()
	defer credState.mu.Unlock()
	return credState.cred.Workspace
}

// ConnectedVia reports how fleet is authenticating, for the Connect dialog.
// The environment case is called out because it cannot be disconnected from
// inside fleet — the user has to unset the variable.
func ConnectedVia() string {
	if envKey() != "" {
		return "environment (" + APIKeyEnvVar + ")"
	}
	credState.mu.Lock()
	defer credState.mu.Unlock()
	switch credState.cred.Kind {
	case credOAuth:
		return "browser sign-in"
	case credAPIKey:
		return "API key"
	}
	return ""
}
