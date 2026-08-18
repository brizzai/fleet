package linear

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
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

// findImages returns the remote image links in a body.
//
// Only http(s) targets are collected. Linear's own markdown carries absolute
// uploads.linear.app URLs, and anything else — a relative path, a data URI —
// is not something fleet has any business fetching.
func findImages(markdown []byte) []imageRef {
	var refs []imageRef
	for _, m := range imageLinkRe.FindAllSubmatch(markdown, -1) {
		target := string(m[2])
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
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

	resp, err := httpClient.Do(req)
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
