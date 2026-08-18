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
// input is the CLI's own generated output, not arbitrary user markdown.
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

// imageRef is one image link found in the CLI's markdown.
type imageRef struct {
	alt    string
	target string // absolute local path (CLI downloaded it) or a remote URL
	remote bool
}

func findImages(markdown []byte) []imageRef {
	var refs []imageRef
	for _, m := range imageLinkRe.FindAllSubmatch(markdown, -1) {
		target := string(m[2])
		refs = append(refs, imageRef{
			alt:    string(m[1]),
			target: target,
			remote: strings.Contains(target, uploadsHost) || strings.HasPrefix(target, "http"),
		})
	}
	return refs
}

// detectExt recovers a file extension for image bytes.
//
// This is not cosmetic. The CLI names downloads after sanitize(alt), so a real
// PNG lands on disk as "Filter bar renders cramped (screenshot)" with no
// extension — and an agent's file-read tool dispatches on extension, so a
// perfectly downloaded screenshot is unreadable. Recovering the extension is
// the difference between "we fetched it" and "the agent can see it".
//
// http.DetectContentType sniffs magic bytes, so it works for the
// already-downloaded case where the response headers are long gone.
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

// copyLocalImage reads an image the CLI already downloaded and writes it into
// destDir with a recovered extension. Returns the destination's base name.
func copyLocalImage(src, destDir string, index int) (string, int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", 0, err
	}
	// A zero-byte file is the v1.7.0 symptom: the CLI created the directory,
	// the download failed, and nothing was written. Treat it as a miss so the
	// caller can fall back to fetching it directly.
	if info.Size() == 0 {
		return "", 0, fmt.Errorf("empty file")
	}
	if info.Size() > maxImageBytes {
		return "", 0, fmt.Errorf("over per-image cap (%d bytes)", info.Size())
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return "", 0, err
	}
	ext, ok := detectExt(filepath.Base(src), body)
	if !ok {
		return "", 0, fmt.Errorf("not a recognised image")
	}
	name := sanitizeFilename(filepath.Base(src), index) + ext
	if err := os.WriteFile(filepath.Join(destDir, name), body, 0644); err != nil {
		return "", 0, err
	}
	return name, int64(len(body)), nil
}

// fetchRemoteImage downloads an uploads.linear.app asset the CLI failed to get.
//
// Reached when the markdown still carries a remote URL, which means the CLI's
// downloader threw and swallowed the error — the signature of the v1.7.0 build
// compiled without --allow-net=uploads.linear.app. Those URLs are 401 without
// auth, so the token is borrowed for the request and never kept.
func fetchRemoteImage(ctx context.Context, url, token, destDir, alt string, index int) (string, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	// Raw token, not "Bearer <token>" — this matches how the CLI itself sets
	// the header for uploads.linear.app.
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
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
