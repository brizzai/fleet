package jira

import (
	"net/url"
	"testing"
)

// TestAttachmentHostGateIsTheConfiguredSite pins the predicate Document hands
// to ticket.fetchImage.
//
// That downloader attaches a Basic header carrying the user's Atlassian API
// token to whatever URL it is given, and the URLs come from the issue's
// attachment records — content anyone who can write to the issue influences,
// including through Jira's public service-desk intake. The gate is the site the
// user configured and nothing else.
//
// The comparison must be on url.Hostname(): a suffix test matches
// acme.atlassian.net.evil.example, a prefix test matches
// acme.atlassian.net.evil.example too, and either one hands a live credential to
// whoever registered the domain. Hostname also strips the port, so an explicit
// :443 still matches.
func TestAttachmentHostGateIsTheConfiguredSite(t *testing.T) {
	host := func(u *url.URL) bool { return u.Hostname() == "acme.atlassian.net" }

	cases := []struct {
		target string
		want   bool
	}{
		{"https://acme.atlassian.net/rest/api/3/attachment/content/1", true},
		{"https://acme.atlassian.net:443/x", true},

		{"http://acme.atlassian.net/x", false}, // never in plaintext, even to Jira
		{"https://acme.atlassian.net.evil.example/x", false},
		{"https://evil-acme.atlassian.net/x", false},
		{"https://acme.atlassian.net@evil.example/x", false},

		// A DIFFERENT Atlassian tenant is a different customer. The token would
		// be rejected there, but sending it at all is the leak.
		{"https://other.atlassian.net/x", false},

		{"data:image/png;base64,AAAA", false},
		{"file:///etc/passwd", false},
		{"http://169.254.169.254/latest/meta-data/", false},
		{"http://127.0.0.1:8080/x.png", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.target)
		if err != nil {
			if c.want {
				t.Errorf("%s: %v", c.target, err)
			}
			continue
		}
		got := u.Scheme == "https" && host(u)
		if got != c.want {
			t.Errorf("gate(%q) = %v, want %v", c.target, got, c.want)
		}
	}
}

// TestOffSiteAttachmentIsRefusedAtTheCredential pins where the defence actually
// is.
//
// renderBody trusts Jira's own attachment list and queues whatever content URLs
// it names — it has no business second-guessing the API's own payload. The gate
// is Document.Host, and ticket.fetchImage re-checks it on the line that attaches
// the credential. So a tampered attachment record IS queued and then refused,
// which is the outcome to pin: the download never happens and the token never
// leaves.
func TestOffSiteAttachmentIsRefusedAtTheCredential(t *testing.T) {
	iss := &issue{Key: "BRZ-1", Fields: issueFields{
		Summary: "x",
		Attachment: []attachment{
			{ID: "1", Filename: "ok.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/1"},
			{ID: "2", Filename: "evil.png", MimeType: "image/png", Content: "https://evil.example/a/2"},
			// An attachment with no content URL is not fetchable at all.
			{ID: "3", Filename: "nourl.png", MimeType: "image/png"},
		},
	}}
	_, images := renderBody(iss, nil)

	if len(images) != 2 {
		t.Fatalf("queued %d images, want the two with content URLs", len(images))
	}
	for _, img := range images {
		if img.Alt == "nourl.png" {
			t.Fatal("an attachment with no content URL must not be queued")
		}
	}

	host := func(u *url.URL) bool { return u.Hostname() == "acme.atlassian.net" }
	for _, img := range images {
		u, err := url.Parse(img.URL)
		if err != nil {
			t.Fatalf("queued an unparseable URL %q", img.URL)
		}
		allowed := u.Scheme == "https" && host(u)
		want := img.Alt == "ok.png"
		if allowed != want {
			t.Errorf("gate(%s) = %v, want %v", img.URL, allowed, want)
		}
	}
}
