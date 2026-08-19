package linear

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPKCEChallengeMatchesVerifier pins the one calculation the whole flow rests
// on. Get it wrong — pad the base64, hash the wrong string, use the wrong
// encoding — and Linear rejects the exchange with a message about the client
// secret fleet deliberately does not have.
func TestPKCEChallengeMatchesVerifier(t *testing.T) {
	p, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	// RFC 7636 §4.1: 43-128 characters from the unreserved set.
	if n := len(p.verifier); n < 43 || n > 128 {
		t.Errorf("verifier is %d characters, RFC 7636 requires 43-128", n)
	}
	if strings.ContainsAny(p.verifier, "+/=") {
		t.Errorf("verifier %q must be base64url without padding, not standard base64", p.verifier)
	}
	if strings.ContainsAny(p.challenge, "+/=") {
		t.Errorf("challenge %q must be base64url without padding", p.challenge)
	}

	sum := sha256.Sum256([]byte(p.verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); p.challenge != want {
		t.Errorf("challenge = %q, want S256(verifier) = %q", p.challenge, want)
	}

	// Two attempts must never share a verifier, or a replayed code would work.
	other, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if other.verifier == p.verifier {
		t.Error("two sign-in attempts produced the same verifier")
	}
}

// TestOAuthStateMismatchRejected covers the CSRF check.
//
// The callback listens on a fixed loopback port, so during the sign-in window
// anything else on the machine can reach it. The state parameter is the only
// thing that distinguishes Linear's answer from someone else's, and it has to be
// checked before the code is used for anything.
func TestOAuthStateMismatchRejected(t *testing.T) {
	const want = "the-real-state"
	var got struct {
		code string
		err  error
	}

	// Mirrors the handler in SignIn: state first, everything else after.
	handler := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != want {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			got.err = errStateMismatch
			return
		}
		got.code = q.Get("code")
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?state=forged&code=attacker-code")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("forged state returned http %d, want 400", resp.StatusCode)
	}
	if got.code != "" {
		t.Errorf("a forged state got as far as reading the code (%q)", got.code)
	}
	if got.err == nil {
		t.Error("a forged state must be reported, not silently ignored")
	}
}

// TestExchangeSendsVerifierNotSecret pins that fleet authenticates the token
// exchange with the PKCE verifier. Sending a client_secret would mean embedding
// one in a public binary, which is the reason PKCE was chosen.
func TestExchangeSendsVerifierNotSecret(t *testing.T) {
	var body url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		body = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","expires_in":86399,"refresh_token":"rt"}`))
	}))
	defer srv.Close()

	orig := tokenURLVar
	tokenURLVar = srv.URL
	t.Cleanup(func() { tokenURLVar = orig })

	cred, err := exchangeCode(t.Context(), "the-code", "http://localhost:53682/oauth/callback", "the-verifier")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if body.Get("code_verifier") != "the-verifier" {
		t.Error("the exchange must carry the PKCE verifier")
	}
	if body.Get("client_secret") != "" {
		t.Error("the exchange must NOT carry a client secret — fleet has none by design")
	}
	if body.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", body.Get("grant_type"))
	}
	if cred.Kind != credOAuth || cred.Token != "at" || cred.Refresh != "rt" {
		t.Errorf("credential = %+v", cred)
	}
	if until := time.Until(cred.ExpiresAt); until < 23*time.Hour || until > 25*time.Hour {
		t.Errorf("expiry is %v away, want ~24h from expires_in", until)
	}
}

// TestNeedsRefreshOnlyForRenewableOAuth pins when renewal fires. An API key
// never expires; an OAuth credential with no refresh token cannot be renewed and
// must be used until the API itself refuses it, rather than being thrown away.
func TestNeedsRefreshOnlyForRenewableOAuth(t *testing.T) {
	soon := time.Now().Add(time.Minute)
	later := time.Now().Add(12 * time.Hour)

	cases := []struct {
		name string
		c    Credential
		want bool
	}{
		{"api key never refreshes", Credential{Kind: credAPIKey, Token: "k", ExpiresAt: soon}, false},
		{"oauth near expiry", Credential{Kind: credOAuth, Token: "t", Refresh: "r", ExpiresAt: soon}, true},
		{"oauth with time left", Credential{Kind: credOAuth, Token: "t", Refresh: "r", ExpiresAt: later}, false},
		{"oauth with no refresh token", Credential{Kind: credOAuth, Token: "t", ExpiresAt: soon}, false},
		{"oauth with no expiry recorded", Credential{Kind: credOAuth, Token: "t", Refresh: "r"}, false},
	}
	for _, c := range cases {
		if got := c.c.needsRefresh(); got != c.want {
			t.Errorf("%s: needsRefresh = %v, want %v", c.name, got, c.want)
		}
	}
}
