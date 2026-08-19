package linear

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestImageLinksAreHostGated pins the gate that keeps the Linear credential off
// every host but Linear's.
//
// fetchImage attaches a raw personal API key or an OAuth access token to
// whatever URL it is given, and the URLs come from issue descriptions and
// comments — content anyone who can write to the issue controls, including
// through Linear's customer requests and public intake. An ungated link is a
// one-line credential exfiltration primitive.
func TestImageLinksAreHostGated(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"linear upload", "https://uploads.linear.app/abc/def", true},
		{"linear upload with port", "https://uploads.linear.app:443/abc", true},
		{"plain http to linear", "http://uploads.linear.app/abc", false},
		{"foreign host", "https://evil.example/x.png", false},
		{"foreign host over http", "http://evil.example/x.png", false},

		// The three shapes a prefix or suffix test would wave through.
		{"suffix impersonation", "https://uploads.linear.app.evil.example/x", false},
		{"prefix impersonation", "https://evil-uploads.linear.app.evil.example/x", false},
		{"userinfo impersonation", "https://uploads.linear.app@evil.example/x", false},

		// Other Linear hosts are not upload hosts.
		{"linear api host", "https://api.linear.app/x.png", false},
		{"linear www", "https://linear.app/x.png", false},

		// Non-http schemes.
		{"data uri", "data:image/png;base64,AAAA", false},
		{"file uri", "file:///etc/passwd", false},
		{"relative path", "/local/x.png", false},

		// SSRF targets.
		{"cloud metadata", "http://169.254.169.254/latest/meta-data/", false},
		{"localhost", "http://127.0.0.1:8080/x.png", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := allowedImageURL(c.target); got != c.want {
				t.Errorf("allowedImageURL(%q) = %v, want %v", c.target, got, c.want)
			}
			md := []byte("![x](" + c.target + ")")
			refs := findImages(md)
			if got := len(refs) == 1; got != c.want {
				t.Errorf("findImages kept %q = %v, want %v", c.target, got, c.want)
			}
		})
	}
}

// TestFetchImageNeverSendsCredentialOffHost is the assertion that actually
// matters: not "the link was dropped" but "the secret did not leave".
//
// It stands up a real server playing the attacker's host and asserts fetchImage
// refuses before any request is made. Testing findImages alone would keep
// passing if someone later called fetchImage from a new code path.
func TestFetchImageNeverSendsCredentialOffHost(t *testing.T) {
	// Deliberately no credential is installed. The host check sits ABOVE the
	// credential() call, so the refusal must happen whether or not one is
	// resolvable — and this test must never touch the real keychain.
	t.Setenv(APIKeyEnvVar, "")

	var hits int64
	var sawAuth atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	_, _, err := fetchImage(context.Background(), attacker.URL+"/x.png", t.TempDir(), "x", 0)
	if err == nil {
		t.Fatal("fetchImage accepted a foreign host")
	}
	if !strings.Contains(err.Error(), "refusing to send credentials") {
		t.Errorf("error should name the reason, got %v", err)
	}
	if n := atomic.LoadInt64(&hits); n != 0 {
		t.Errorf("fetchImage contacted the foreign host %d time(s) — it must refuse before dialing", n)
	}
	if sawAuth.Load() {
		t.Error("the Linear credential reached a foreign host")
	}
}

// TestImageClientRefusesRedirects covers the gap the host check alone leaves.
//
// Go strips Authorization across a redirect only when the registrable domain
// changes; it deliberately permits uploads.linear.app -> anything.linear.app.
// A redirect target is a URL allowedImageURL never inspected, so the client
// must not follow one at all.
func TestImageClientRefusesRedirects(t *testing.T) {
	var followed atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	defer dest.Close()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/x.png", http.StatusFound)
	}))
	defer src.Close()

	req, err := http.NewRequest(http.MethodGet, src.URL+"/x.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := imageClient.Do(req)
	if err != nil {
		t.Fatalf("expected the 3xx to be returned, not an error: %v", err)
	}
	defer resp.Body.Close()

	if followed.Load() {
		t.Error("imageClient followed a redirect — the target was never host-checked")
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("want the redirect status surfaced, got %d", resp.StatusCode)
	}
}
