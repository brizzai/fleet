package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/brizzai/fleet/internal/ticket"
)

// The environment escape hatch. All three or none: a site with no token is not
// a usable credential, and silently falling back to the stored one while the
// user has JIRA_SITE exported would authenticate against a different site than
// the variable names.
//
// Named for the convention Atlassian's own tooling uses, so anyone already
// scripting against Jira needs no migration. Wins over the stored credential,
// so a stale one can always be overridden without touching the UI — and so CI,
// which has no keychain, works at all.
const (
	SiteEnvVar  = "JIRA_SITE"
	EmailEnvVar = "JIRA_EMAIL"
	TokenEnvVar = "JIRA_API_TOKEN"
)

// store is Jira's slot in the OS keychain. Separate service from Linear's, so
// connecting one never disturbs the other.
var store = ticket.Store{
	Service:  "fleet-jira",
	Label:    "fleet: Jira",
	FileName: "jira.json",
}

// getenv is a seam for tests; production always reads the real environment.
var getenv = func(k string) string { return strings.TrimSpace(os.Getenv(k)) }

// Credential is what fleet needs to talk to a Jira site.
//
// Three fields rather than one, because Jira Cloud's API token is only half a
// credential: it identifies nothing on its own, and it is scoped to an Atlassian
// account rather than to a site. Site says which host to ask, Email says who is
// asking. All three are needed to form a single header.
type Credential struct {
	Site  string `json:"site"`  // host only: "acme.atlassian.net"
	Email string `json:"email"` // the Atlassian account's address
	Token string `json:"token"` // an API token from id.atlassian.com
}

// authHeader returns the Authorization value for this credential.
//
// Jira Cloud takes HTTP Basic with the account's email as the username and the
// API token as the password. Not a bearer: an API token sent as a bearer is
// rejected exactly like a wrong password, with no hint that the form was the
// problem.
func (c Credential) authHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

func (c Credential) ok() bool { return c.Site != "" && c.Email != "" && c.Token != "" }

// baseURL is the site's API root. Always rebuilt from the stored host rather
// than from anything a user typed, so a pasted URL carrying a path, a port, or
// userinfo cannot survive into a request.
func (c Credential) baseURL() string { return "https://" + c.Site }

// NormalizeSite turns whatever the user pasted into a bare host.
//
// Accepts the four things people actually have to hand: a bare tenant name
// (acme), a host (acme.atlassian.net), a URL (https://acme.atlassian.net), and a
// URL with the path they were looking at when they copied it
// (https://acme.atlassian.net/jira/software/projects/BRZ/boards/1).
//
// A bare word with no dot gets .atlassian.net appended, since that is the only
// thing it could mean. Anything containing a dot is taken as the host verbatim —
// deliberately NOT restricted to *.atlassian.net, because Atlassian now serves
// Jira Cloud on customer-owned domains and rejecting those would refuse to
// connect to a perfectly ordinary site. The Cloud-only decision is enforced by
// the API version fleet asks for, not by the hostname.
func NormalizeSite(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("no site given")
	}
	if !strings.Contains(s, "://") {
		// url.Parse reads "acme.atlassian.net/x" as a path with no host, so a
		// scheme goes on before parsing rather than after.
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("not a site address: %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("not a site address: %q", raw)
	}
	if !strings.Contains(host, ".") {
		host += ".atlassian.net"
	}
	return host, nil
}

// credState is the process-wide resolved credential.
//
// It exists because Available() is called from the Bubble Tea Update goroutine,
// where a keychain subprocess must never run. warmed is an atomic so that read
// costs nothing; the mutex only guards the slower paths that actually load or
// replace it.
var credState struct {
	mu      sync.Mutex
	cred    Credential
	loaded  bool
	warmed  atomic.Bool
	present atomic.Bool
}

// Available reports whether fleet has a credential to work with.
//
// Free and non-blocking by contract: it reads two atomics and three env vars,
// never the keychain. Before Warm has run it answers false, which makes every
// ticket surface inert for the few milliseconds after launch — the honest
// answer, and cheaper than blocking a frame to find out.
func Available() bool {
	if envCredential().ok() {
		return true
	}
	return credState.warmed.Load() && credState.present.Load()
}

// Resolved reports whether fleet has finished looking for a credential.
//
// The distinction matters to anything that acts on the ABSENCE of one: before
// Warm runs, Available() answers false because it does not know yet, and a
// caller that reads that as "this user has no Jira" would, for the first
// moments of every launch, be wrong about a connected user.
func Resolved() bool { return credState.warmed.Load() }

func envCredential() Credential {
	site, err := NormalizeSite(getenv(SiteEnvVar))
	if err != nil {
		return Credential{}
	}
	return Credential{Site: site, Email: getenv(EmailEnvVar), Token: getenv(TokenEnvVar)}
}

// Warm loads the stored credential once, off the Update goroutine.
//
// Called from the TUI's startup batch. Idempotent: a second call after a
// successful load is a no-op, so it is safe to use as a "make sure" before any
// path that needs a definite answer.
//
// The context is part of ticket.Provider's signature and is unused here: this
// reads a local keychain, which runQuiet already bounds with its own deadline.
func Warm(context.Context) {
	credState.mu.Lock()
	defer credState.mu.Unlock()
	loadLocked()
}

func loadLocked() Credential {
	if credState.loaded {
		return credState.cred
	}
	if c := envCredential(); c.ok() {
		credState.cred = c
	} else if raw, ok := store.Load(); ok && len(raw) > 0 {
		var s Credential
		if err := json.Unmarshal(raw, &s); err == nil && s.ok() {
			credState.cred = s
		}
	}
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(credState.cred.ok())
	return credState.cred
}

// credential returns the resolved credential.
//
// Only ever called from the API paths, which all run off the Update goroutine,
// so it is allowed to block on the keychain. Unlike Linear's there is nothing
// to renew: an Atlassian API token does not expire on a timer, so a rejected
// one is a revoked one and the honest move is to report it rather than retry.
func credential() (Credential, error) {
	credState.mu.Lock()
	c := loadLocked()
	credState.mu.Unlock()

	if !c.ok() {
		return Credential{}, ticket.ErrNotConnected
	}
	return c, nil
}

// SetCredential stores a credential and makes it live immediately.
//
// The in-memory copy is replaced before the write is attempted so that a
// keychain that refuses to store still leaves this session working — the user
// pasted a token that we verified against the API, and failing to persist it is
// not a reason to act as though they hadn't.
func SetCredential(c Credential) error {
	credState.mu.Lock()
	credState.cred = c
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(c.ok())
	credState.mu.Unlock()

	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return store.Save(data)
}

// Disconnect forgets the credential.
//
// The cache is cleared even if the backing store refuses, for the same reason
// SetCredential updates it first: the user's instruction is about this fleet,
// and a keychain error must not leave a session still talking to Jira after
// being told to stop.
func Disconnect() error {
	credState.mu.Lock()
	credState.cred = Credential{}
	credState.loaded = true
	credState.warmed.Store(true)
	credState.present.Store(false)
	credState.mu.Unlock()

	resetAccountCache()
	return store.Clear()
}

// StoredSite returns the host behind the current credential, for display only.
func StoredSite() string {
	if c := envCredential(); c.ok() {
		return c.Site
	}
	credState.mu.Lock()
	defer credState.mu.Unlock()
	return credState.cred.Site
}

// ConnectedVia reports how fleet is authenticating, for the Connect dialog.
// The environment case is called out because it cannot be disconnected from
// inside fleet — the user has to unset the variables.
func ConnectedVia() string {
	if envCredential().ok() {
		return "environment (" + SiteEnvVar + ")"
	}
	credState.mu.Lock()
	defer credState.mu.Unlock()
	if credState.cred.ok() {
		return "API token"
	}
	return ""
}
