package linear

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Linear's OAuth endpoints. The authorize URL is on the app domain and the token
// endpoint on the API domain — they are deliberately not the same host.
const (
	authorizeURL = "https://linear.app/oauth/authorize"
	tokenURL     = "https://api.linear.app/oauth/token"
)

// oauthScopes is the minimum fleet needs: read everything it materializes, and
// write for the single issueUpdate that moves a ticket to started. Deliberately
// not `admin`, and not the issues:create / comments:create scopes — fleet never
// creates anything in Linear.
const oauthScopes = "read,write"

// clientID identifies fleet's OAuth application.
//
// Public by design and safe to embed, the same way the PostHog project key is:
// with PKCE there is no client secret, and the registered redirect URIs are what
// actually stop someone else using it. Overridable so a fork or a self-hosted
// setup can point at its own registration.
var clientID = defaultClientID

const defaultClientID = ""

// callbackPorts are the loopback ports fleet will listen on, in order.
//
// Fixed rather than ephemeral because Linear matches the redirect_uri against
// the app's registered list, so a random port would simply be rejected. Three of
// them, because a developer machine running a dozen services will occasionally
// have one taken, and losing the whole sign-in to a port collision would be a
// silly way to fail.
var callbackPorts = []int{53682, 53683, 53684}

const callbackPath = "/oauth/callback"

// oauthTimeout bounds the whole browser round trip. Long enough to find the
// window, log in, and pick a workspace; short enough that an abandoned attempt
// releases the port.
const oauthTimeout = 3 * time.Minute

// ErrOAuthUnavailable means fleet cannot run a browser sign-in on this machine —
// no registered port free, or nothing to open a browser with.
//
// It is a routing error, not a failure: the Connect dialog's answer is to point
// at the paste-a-key path, which is exactly why both exist.
var ErrOAuthUnavailable = errors.New("linear: browser sign-in unavailable here")

// errStateMismatch is what a callback that did not come from fleet's own request
// produces. Named so the guard test can assert on identity rather than prose.
var errStateMismatch = errors.New("linear: sign-in state mismatch — the response did not come from the request fleet made")

// OAuthConfigured reports whether this build carries a client ID.
func OAuthConfigured() bool { return clientIDValue() != "" }

func clientIDValue() string {
	if v := strings.TrimSpace(getenv("FLEET_LINEAR_CLIENT_ID")); v != "" {
		return v
	}
	return clientID
}

// pkce is one sign-in attempt's proof-of-possession pair.
type pkce struct {
	verifier  string
	challenge string
}

func newPKCE() (pkce, error) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return pkce{}, err
	}
	// RFC 7636: the verifier is 43-128 unreserved characters. base64url of 64
	// random bytes is 86, comfortably inside that.
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return pkce{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// listenOnRegisteredPort binds the first free registered port.
func listenOnRegisteredPort() (net.Listener, string, error) {
	for _, port := range callbackPorts {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		return ln, fmt.Sprintf("http://localhost:%d%s", port, callbackPath), nil
	}
	return nil, "", ErrOAuthUnavailable
}

// openBrowser hands the URL to the desktop.
//
// A failure here is reported rather than swallowed: over SSH there is no browser
// at all, and silently "starting" a sign-in the user can never complete would
// leave them staring at a spinner until the timeout.
func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return ErrOAuthUnavailable
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", ErrOAuthUnavailable, err)
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// SignIn runs the full browser sign-in and returns a credential.
//
// Blocking, and meant to be called from a tea.Cmd. It never stores anything —
// the caller verifies the credential and decides, which keeps the "nothing is
// saved until it is proven to work" rule in one place.
func SignIn(ctx context.Context) (Credential, error) {
	if !OAuthConfigured() {
		return Credential{}, ErrOAuthUnavailable
	}

	ln, redirect, err := listenOnRegisteredPort()
	if err != nil {
		return Credential{}, err
	}
	defer ln.Close()

	p, err := newPKCE()
	if err != nil {
		return Credential{}, err
	}
	state, err := randomState()
	if err != nil {
		return Credential{}, err
	}

	type callback struct {
		code string
		err  error
	}
	results := make(chan callback, 1)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		// The state check is the CSRF defence, and it must run before the code
		// is touched: without it anyone who can reach this loopback port during
		// the window can feed fleet an authorization code of their choosing.
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			results <- callback{err: errStateMismatch}
			return
		}
		if e := q.Get("error"); e != "" {
			writeCallbackPage(w, "Sign-in was declined.", "You can close this tab.")
			results <- callback{err: fmt.Errorf("linear: sign-in declined (%s)", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, "Something went wrong.", "No authorization code came back.")
			results <- callback{err: errors.New("linear: sign-in returned no code")}
			return
		}
		writeCallbackPage(w, "fleet is connected to Linear.", "You can close this tab and go back to your terminal.")
		results <- callback{code: code}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientIDValue())
	q.Set("redirect_uri", redirect)
	q.Set("scope", oauthScopes)
	q.Set("state", state)
	q.Set("code_challenge", p.challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("actor", "user")

	if err := openBrowser(authorizeURL + "?" + q.Encode()); err != nil {
		return Credential{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()

	select {
	case <-ctx.Done():
		return Credential{}, fmt.Errorf("linear: sign-in timed out")
	case res := <-results:
		if res.err != nil {
			return Credential{}, res.err
		}
		return exchangeCode(ctx, res.code, redirect, p.verifier)
	}
}

func exchangeCode(ctx context.Context, code, redirect, verifier string) (Credential, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", clientIDValue())
	// PKCE: the verifier stands in for the client secret fleet deliberately does
	// not have, which is what makes shipping the client ID in a public binary
	// safe.
	form.Set("code_verifier", verifier)
	return postToken(ctx, form)
}

// refresh exchanges a refresh token for a fresh access token.
func refresh(ctx context.Context, refreshToken string) (Credential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientIDValue())
	return postToken(ctx, form)
}

func postToken(ctx context.Context, form url.Values) (Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURLVar, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Credential{}, fmt.Errorf("linear: token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Credential{}, fmt.Errorf("%w (token endpoint returned http %d)", ErrNotAuthenticated, resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return Credential{}, fmt.Errorf("linear: unreadable token response: %w", err)
	}
	if tr.AccessToken == "" {
		return Credential{}, ErrNotAuthenticated
	}

	c := Credential{Kind: credOAuth, Token: tr.AccessToken, Refresh: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return c, nil
}

var tokenURLVar = tokenURL

// writeCallbackPage is the only HTML fleet serves. Deliberately tiny and
// self-contained: it exists so the browser tab says something true instead of
// leaving the user wondering whether it worked.
func writeCallbackPage(w http.ResponseWriter, heading, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>fleet</title>`+
		`<div style="font:16px/1.6 system-ui,sans-serif;max-width:32rem;margin:20vh auto;padding:0 1.5rem">`+
		`<h1 style="font-size:1.25rem;margin:0 0 .5rem">%s</h1><p style="opacity:.7;margin:0">%s</p></div>`,
		heading, detail)
}
