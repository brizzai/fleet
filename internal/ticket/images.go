package ticket

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// knownImageExt is the set of extensions we treat as already-correct, used both
// when recovering an extension and when stripping one off alt text.
var knownImageExt = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".svg": {},
}

// extByContentType maps the types trackers actually serve for inline images.
var extByContentType = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/bmp":     ".bmp",
	"image/svg+xml": ".svg",
}

// imageClient fetches issue attachments. It refuses to follow redirects at all,
// because the request carries a live tracker credential and a redirect target is
// a URL that no Host gate ever saw. Go's own protection stops at a domain
// change and deliberately permits uploads.linear.app -> anything.linear.app.
//
// http.ErrUseLastResponse makes Do return the 3xx instead of an error, which
// then fails the StatusOK check with the redirect's own status — a more honest
// message than a synthetic one.
var imageClient = &http.Client{
	Timeout: 90 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// allowedImageURL reports whether an image target is one fleet may fetch: https,
// and passing the provider's own host gate.
//
// This is the whole defence, and it is narrow on purpose. fetchImage attaches a
// live credential — a raw personal API key, an OAuth access token, a Basic
// header carrying an Atlassian API token — to whatever URL it is handed. Issue
// descriptions, comments and attachment records are attacker-influenced
// content: anyone who can write to an issue fleet later materializes (including
// through Linear's customer requests and Jira's public service-desk intake)
// could point it at `http://evil.example/` and be sent the credential directly.
//
// http is refused as well as foreign hosts, so the credential can never cross
// the network in plaintext even to the tracker itself.
//
// The host comparison every provider implements is on url.Hostname(), never a
// string prefix or suffix: a suffix test matches
// evil-uploads.linear.app.evil.example, and a prefix test matches
// uploads.linear.app.evil.example. Hostname strips the port so an explicit :443
// still matches.
func allowedImageURL(target string, host func(*url.URL) bool) bool {
	if host == nil {
		return false
	}
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && host(u)
}

// detectExt recovers a file extension for image bytes.
//
// This is not cosmetic. Linear's default alt text is literally "image.png" and
// its upload URLs carry no filename at all, so a real PNG would land on disk
// unnamed and unextensioned — and an agent's file-read tool dispatches on
// extension, making a perfectly downloaded screenshot unreadable. Recovering the
// extension is the difference between "we fetched it" and "the agent can see it".
//
// http.DetectContentType sniffs magic bytes, so it is the backstop when the
// response headers say nothing useful. Jira does send a filename, but the sniff
// still runs as a check rather than a rescue: it is what stops a 401 HTML page
// landing beside real screenshots under a .png name the server volunteered.
func detectExt(name string, body []byte) (string, bool) {
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		for _, known := range extByContentType {
			if ext == known {
				return ext, true
			}
		}
		if ext == ".jpeg" {
			return ".jpg", true
		}
	}
	ct := http.DetectContentType(body)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ext, ok := extByContentType[strings.ToLower(strings.TrimSpace(ct))]
	return ext, ok
}

// fetchImage downloads one attachment into destDir.
//
// These URLs are 401 unauthenticated, which is the whole reason this exists: an
// agent handed the raw markdown could not open a single screenshot. The
// credential is read per request and never written anywhere.
func fetchImage(ctx context.Context, doc *Document, target, destDir, alt string, index int) (string, int64, error) {
	// Re-checked here even though the provider already filtered, because this
	// is the line that attaches the credential. A gate one call away from the
	// thing it protects is a gate that a later refactor removes without
	// noticing; this one cannot be separated from the header it guards.
	if !allowedImageURL(target, doc.Host) {
		return "", 0, fmt.Errorf("refusing to send credentials to %q", target)
	}
	if doc.Auth == nil {
		return "", 0, ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, ImageFetchTimeout)
	defer cancel()

	auth, err := doc.Auth(ctx)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", auth)

	resp, err := imageClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", 0, err
	}
	if len(body) == 0 {
		return "", 0, fmt.Errorf("empty response")
	}
	if len(body) > maxImageBytes {
		return "", 0, fmt.Errorf("over per-image cap")
	}

	// Reject anything that isn't an image, so a 401 HTML page or a JSON error
	// body can never land beside real screenshots looking like one.
	ext, ok := extFromHeaders(resp.Header)
	if !ok {
		if ext, ok = detectExt(alt, body); !ok {
			return "", 0, fmt.Errorf("not an image response")
		}
	}

	name := sanitizeFilename(alt, index) + ext
	if err := os.WriteFile(filepath.Join(destDir, name), body, 0644); err != nil {
		return "", 0, err
	}
	return name, int64(len(body)), nil
}

func extFromHeaders(h http.Header) (string, bool) {
	if cd := h.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				if ext := strings.ToLower(filepath.Ext(fn)); ext != "" {
					if ext == ".jpeg" {
						return ".jpg", true
					}
					for _, known := range extByContentType {
						if ext == known {
							return ext, true
						}
					}
				}
			}
		}
	}
	ct := h.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ext, ok := extByContentType[strings.ToLower(strings.TrimSpace(ct))]
	return ext, ok
}
