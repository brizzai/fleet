package linear

import (
	"net/url"
	"regexp"
)

// imageLinkRe matches a markdown image: ![alt](target). A narrow regex rather
// than a markdown parser — we only ever need the two capture groups, and the
// input is Linear's own markdown, not an arbitrary document.
var imageLinkRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

// uploadsHost is the only host fleet will send a Linear credential to for an
// image. Linear serves every inline attachment from here.
const uploadsHost = "uploads.linear.app"

// allowedImageHost is the Document's host gate.
//
// The comparison is on url.Hostname(), never a string prefix or suffix: a
// suffix test matches evil-uploads.linear.app.evil.example, and a prefix test
// matches uploads.linear.app.evil.example. Hostname strips the port so an
// explicit :443 still matches. ticket.fetchImage re-checks this on the line
// that attaches the credential.
func allowedImageHost(u *url.URL) bool { return u.Hostname() == uploadsHost }

// findImages returns the remote image links in a body that fleet may fetch,
// paired with the byte range of the link target so the caller can rewrite it.
//
// Anything else — a relative path, a data URI, plain http, or a host that is not
// Linear's — is dropped here rather than downstream, so a body full of hostile
// links costs nothing and reaches no network stack at all.
func findImages(markdown string) (alts, targets []string) {
	for _, m := range imageLinkRe.FindAllStringSubmatch(markdown, -1) {
		u, err := url.Parse(m[2])
		if err != nil || u.Scheme != "https" || !allowedImageHost(u) {
			continue
		}
		alts = append(alts, m[1])
		targets = append(targets, m[2])
	}
	return alts, targets
}
