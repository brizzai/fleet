package ticket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onePixelPNG is a real PNG. The bytes matter: detectExt sniffs magic bytes, so
// a placeholder string would take the "not an image response" branch and the
// test would pass for the wrong reason.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// imageServer stands up a TLS server (the gate refuses http) serving one image
// and records what it was asked for.
func imageServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, func(*url.URL) bool) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	// The test server's certificate is self-signed, so the shared imageClient
	// cannot verify it. Swapping the client keeps its ONE load-bearing
	// property — refusing redirects — which is why the CheckRedirect is copied
	// rather than dropped.
	orig := imageClient
	c := srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	imageClient = c
	t.Cleanup(func() { imageClient = orig })

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	return srv, func(got *url.URL) bool { return got.Hostname() == host }
}

// TestFetchImageWritesAUsableFile is the download path end to end: an
// authenticated request, real image bytes, and a file an agent can open.
//
// The extension is the point. A tracker's upload URL carries no filename and
// Linear's default alt text is literally "image.png", so a perfectly downloaded
// screenshot lands unreadable if the extension is not recovered from the bytes.
func TestFetchImageWritesAUsableFile(t *testing.T) {
	var gotAuth string
	srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	})

	dir := t.TempDir()
	doc := &Document{
		Host: host,
		Auth: func(context.Context) (string, error) { return "Basic dGVzdA==", nil },
	}
	name, size, err := fetchImage(context.Background(), doc, srv.URL+"/attachment/content/1", dir, "Login screen", 1)
	if err != nil {
		t.Fatalf("fetchImage: %v", err)
	}
	if gotAuth != "Basic dGVzdA==" {
		t.Errorf("the credential did not reach the tracker: %q", gotAuth)
	}
	if name != "1-login-screen.png" {
		t.Errorf("name = %q, want the alt slugged with a recovered extension", name)
	}
	if size != int64(len(onePixelPNG)) {
		t.Errorf("size = %d, want %d", size, len(onePixelPNG))
	}
	on, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || len(on) != len(onePixelPNG) {
		t.Fatalf("file not written: %v", err)
	}
}

// TestFetchImageRejectsNonImages: a 401 HTML page or a JSON error body must
// never land beside real screenshots looking like one.
func TestFetchImageRejectsNonImages(t *testing.T) {
	srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>401 Unauthorized</body></html>"))
	})
	doc := &Document{
		Host: host,
		Auth: func(context.Context) (string, error) { return "x", nil },
	}
	dir := t.TempDir()
	if _, _, err := fetchImage(context.Background(), doc, srv.URL+"/x", dir, "shot", 1); err == nil {
		t.Fatal("an HTML body was accepted as an image")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a rejected response still wrote %d file(s)", len(entries))
	}
}

// TestCollectImagesRewritesAndDegrades is the whole contract in one run: every
// placeholder either becomes a real relative path or becomes a sentence, and no
// fleet-image: token survives into ticket.md.
func TestCollectImagesRewritesAndDegrades(t *testing.T) {
	srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	})

	doc := &Document{
		Host: host,
		Auth: func(context.Context) (string, error) { return "x", nil },
		Images: []Image{
			{URL: srv.URL + "/ok.png", Alt: "works"},
			{URL: srv.URL + "/missing.png", Alt: "gone"},
			{URL: srv.URL + "/unreferenced.png", Alt: "never linked"},
		},
	}
	doc.Body = "before\n" +
		"![works](" + PlaceholderFor(0) + ")\n" +
		"![gone](" + PlaceholderFor(1) + ")\n" +
		"after\n"

	dir := t.TempDir()
	body, kept, dropped := collectImages(context.Background(), doc, dir)

	if kept != 1 || dropped != 1 {
		t.Errorf("kept=%d dropped=%d, want 1 and 1 (the third is never referenced)", kept, dropped)
	}
	if !strings.Contains(body, "!["+"works"+"](images/1-works.png)") {
		t.Errorf("the downloaded image was not rewritten to its path:\n%s", body)
	}
	if !strings.Contains(body, "(image: gone — not downloaded)") {
		t.Errorf("a failed download must degrade to a sentence, not a dangling link:\n%s", body)
	}
	if strings.Contains(body, ImagePlaceholder) {
		t.Errorf("a placeholder survived into ticket.md:\n%s", body)
	}
	// Only the referenced-and-successful one is on disk: an image the body
	// never mentions is not fetched at all.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "1-works.png" {
		t.Errorf("images/ holds %v, want just the one that was referenced and succeeded", entries)
	}
}

// TestCollectImagesIndexesAreNotPrefixes pins a bug the placeholder scheme
// invites: fleet-image:1 is a prefix of fleet-image:10, so matching the bare
// token would let image 1 succeeding rewrite half of image 10's link and leave
// the rest as literal text. The closing paren is what disambiguates.
func TestCollectImagesIndexesAreNotPrefixes(t *testing.T) {
	srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	})

	doc := &Document{Host: host, Auth: func(context.Context) (string, error) { return "x", nil }}
	var b strings.Builder
	for i := 0; i < 12; i++ {
		doc.Images = append(doc.Images, Image{URL: srv.URL + "/x.png", Alt: "shot"})
		b.WriteString("![shot](" + PlaceholderFor(i) + ")\n")
	}
	doc.Body = b.String()

	body, kept, _ := collectImages(context.Background(), doc, t.TempDir())
	if kept != 12 {
		t.Errorf("kept = %d, want all 12", kept)
	}
	if strings.Contains(body, ImagePlaceholder) {
		t.Errorf("a placeholder survived — index 1 likely ate the prefix of index 10:\n%s", body)
	}
	if n := strings.Count(body, "](images/"); n != 12 {
		t.Errorf("rewrote %d links, want 12:\n%s", n, body)
	}
}

// TestBytesBeatHeaders is the check the reviewer showed was not there.
//
// Jira sends a filename AND a Content-Type on every attachment, so a
// header-first rule short-circuited on both and never looked at the body: a 200
// carrying an HTML interstitial for screenshot.png wrote an HTML file called
// 1-screenshot.png, past a check whose stated purpose was to stop exactly that.
// The old TestFetchImageRejectsNonImages passed only because its fixture also
// sent a wrong Content-Type.
func TestBytesBeatHeaders(t *testing.T) {
	t.Run("convincing headers over an HTML body are refused", func(t *testing.T) {
		srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
			// Everything a server could say to make this look like a PNG.
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="screenshot.png"`)
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Sign in to continue</body></html>"))
		})
		doc := &Document{Host: host, Auth: func(context.Context) (string, error) { return "x", nil }}
		dir := t.TempDir()
		if _, _, err := fetchImage(context.Background(), doc, srv.URL+"/x", dir, "screenshot.png", 1); err == nil {
			t.Fatal("an HTML body was accepted because the headers claimed PNG")
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("wrote %d file(s) for a non-image", len(entries))
		}
	})

	t.Run("bytes win over a wrong header", func(t *testing.T) {
		// A real PNG announced as a GIF. The extension has to follow the bytes,
		// since that is what an agent's file-read tool will actually parse.
		srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(onePixelPNG)
		})
		doc := &Document{Host: host, Auth: func(context.Context) (string, error) { return "x", nil }}
		name, _, err := fetchImage(context.Background(), doc, srv.URL+"/x", t.TempDir(), "shot", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, ".png") {
			t.Errorf("name = %q, want the extension the BYTES imply", name)
		}
	})

	t.Run("svg is the one type the headers may vouch for", func(t *testing.T) {
		// Go's sniffer has no SVG rule — <?xml reads as text/xml — so a
		// bytes-only rule would reject every valid SVG. Trusting the header is
		// safe here because the body still has to look like the text an SVG is.
		const svg = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`
		srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(svg))
		})
		doc := &Document{Host: host, Auth: func(context.Context) (string, error) { return "x", nil }}
		name, _, err := fetchImage(context.Background(), doc, srv.URL+"/x", t.TempDir(), "diagram", 1)
		if err != nil {
			t.Fatalf("a valid SVG was rejected: %v", err)
		}
		if !strings.HasSuffix(name, ".svg") {
			t.Errorf("name = %q, want .svg", name)
		}
	})

	t.Run("an svg header cannot launder HTML", func(t *testing.T) {
		srv, host := imageServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>nope</body></html>"))
		})
		doc := &Document{Host: host, Auth: func(context.Context) (string, error) { return "x", nil }}
		if _, _, err := fetchImage(context.Background(), doc, srv.URL+"/x", t.TempDir(), "x", 1); err == nil {
			t.Fatal("HTML was accepted under an SVG header — the body sniff is the backstop")
		}
	})
}
