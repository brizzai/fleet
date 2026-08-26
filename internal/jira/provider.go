package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/ticket"
)

// Provider is Jira Cloud as a ticket.Provider.
//
// It is a zero-size value over package-level state rather than a struct with
// fields, for the same reason Linear's is: that state has to be reachable from
// Available(), which the Bubble Tea Update goroutine calls and which therefore
// may not do any work.
type Provider struct{}

// New returns the Jira provider.
func New() Provider { return Provider{} }

var _ ticket.Provider = Provider{}

func (Provider) Kind() string { return kind }
func (Provider) Name() string { return "Jira" }

func (Provider) Available() bool { return Available() }
func (Provider) Resolved() bool  { return Resolved() }

func (Provider) Keys(repoPath string) []string { return ProjectKeys(repoPath) }

func (Provider) Warm(ctx context.Context) { Warm(ctx) }

func (Provider) Fetch(ctx context.Context, id string) (ticket.Ticket, error) {
	return Fetch(ctx, id)
}

func (Provider) Search(ctx context.Context, term string, limit int) ([]ticket.Ticket, error) {
	return Search(ctx, term, limit)
}

func (Provider) Assigned(ctx context.Context, limit int) ([]ticket.Ticket, error) {
	return AssignedIssues(ctx, limit)
}

func (Provider) Account() (ticket.Account, bool) { return accountInfo() }

func (Provider) FetchAccount(ctx context.Context) (ticket.Account, error) {
	return fetchAccount(ctx)
}

func (Provider) ConnectedVia() string { return ConnectedVia() }
func (Provider) Disconnect() error    { return Disconnect() }

// commentFetchLimit is how many comments fleet will pull for one issue. Matches
// Linear's 50: past that a ticket is a discussion thread, and an agent reading
// fifty comments before it reads any code is already at the limit of useful.
const commentFetchLimit = 50

// Document reads the whole issue in one round trip and normalizes it.
//
// A second call happens only when the issue carries more comments than the
// default page returned, which is the case worth a round trip: the newest
// comment is usually the one that changes what the ticket means.
func (Provider) Document(ctx context.Context, id string) (*ticket.Document, error) {
	cred, err := credential()
	if err != nil {
		return nil, err
	}
	key := strings.ToUpper(id)

	var iss issue
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=" + fullFields
	if err := doWith(ctx, cred, ticket.FullTimeout, http.MethodGet, path, nil, &iss); err != nil {
		return nil, err
	}
	if iss.Key == "" {
		return nil, ticket.ErrNotFound
	}

	comments := iss.Fields.Comment
	if comments != nil && comments.Total > len(comments.Comments) {
		var page commentPage
		// -created, not created. Jira's orderBy is ascending by default, so
		// `created` fetches the OLDEST page — the exact opposite of the reason
		// this round trip is worth taking.
		cpath := fmt.Sprintf("/rest/api/3/issue/%s/comment?maxResults=%d&orderBy=-created",
			url.PathEscape(key), commentFetchLimit)
		if err := doWith(ctx, cred, ticket.FullTimeout, http.MethodGet, cpath, nil, &page); err == nil {
			// Replace only when the page is actually bigger. An issue can embed
			// more comments than commentFetchLimit, and swapping 100 embedded
			// for 50 fetched would lose half of them while looking like a fix.
			if len(page.Comments) > len(comments.Comments) {
				// Back to chronological. The page arrives newest-first because
				// that is how the newest ones were selected, but ticket.md must
				// read as a conversation in both trackers — Linear's comments
				// are oldest-first and an agent comparing the two would be
				// reading one of them backwards.
				reverseComments(page.Comments)
				comments = &page
			}
		}
	}

	doc := &ticket.Document{
		Ticket: iss.ticket(cred.Site),
		// The gate is the site the user configured and nothing else. Jira
		// attachment URLs are always on that host, so this is as narrow as
		// Linear's single upload host — and it is what stops an attachment
		// record pointing somewhere else from being handed a Basic header.
		Host: func(u *url.URL) bool { return u.Hostname() == cred.Site },
		Auth: func(context.Context) (string, error) {
			c, err := credential()
			if err != nil {
				return "", err
			}
			return c.authHeader(), nil
		},
		Start: func(ctx context.Context) (string, error) { return moveToStarted(ctx, key) },
	}

	if a := iss.Fields.Assignee; a != nil {
		doc.Assignee = a.DisplayName
	}
	doc.Labels = iss.Fields.Labels
	if p := iss.Fields.Parent; p != nil && p.Key != "" {
		doc.Parent = fmt.Sprintf("%s — %s", strings.ToUpper(p.Key), p.Fields.Summary)
	}
	doc.Body, doc.Images = renderBody(&iss, comments)
	return doc, nil
}

// claimByName returns the next index for this filename that no media node has
// taken yet, falling back to the first when they have all been claimed.
//
// The fallback matters: the same screenshot is often referenced twice in one
// description, and refusing the second reference would replace a perfectly good
// image with "see Attachments".
func claimByName(byName map[string][]int, used map[int]bool, name string) (int, bool) {
	idx, ok := byName[name]
	if !ok || len(idx) == 0 {
		return 0, false
	}
	for _, n := range idx {
		if !used[n] {
			return n, true
		}
	}
	return idx[0], true
}

// reverseComments flips a page in place.
func reverseComments(cs []comment) {
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
}

// renderBody turns the issue into the markdown an agent will read, and lists
// the images that markdown references.
//
// fleet composes this itself rather than asking for a rendered form, which is
// what lets comments carry their author and time. "Who asked for this and when"
// is usually the part that decides whether a ticket is still current.
func renderBody(iss *issue, comments *commentPage) (string, []ticket.Image) {
	// Every image attachment is downloaded, not only the ones a media node
	// resolves to.
	//
	// The reason is an Atlassian limitation, not a preference: an ADF media
	// node's attrs.id is a Media Services file id and is NOT the attachment id
	// the REST list is keyed by (JRACLOUD-96383). Alt text is the only bridge,
	// and Jira frequently writes none — so matching inline-only would silently
	// drop the screenshot on exactly the bug reports that are mostly screenshot.
	// Downloading everything and listing it under ## Attachments costs at worst
	// an unreferenced image inside caps fleet already enforces.
	// Two indexes, because they answer two different questions and one map
	// cannot do both.
	//
	// byID is how an attachment finds ITS OWN image, and it is keyed on the
	// attachment id because Jira lets one issue carry two attachments called
	// image.png. A filename-keyed map silently collapsed them: both names
	// resolved to the second index, the first became unreachable, and
	// renderAttachments emitted the same placeholder twice — so one screenshot
	// was rendered double and the other never downloaded at all. On a bug
	// report that is mostly screenshots, which is the case this whole "download
	// everything" branch exists for.
	//
	// byName is how a MEDIA NODE finds an image, and there the filename is all
	// Atlassian gives us — so it holds a queue, and each media node consumes
	// the next unclaimed index for that name.
	var images []ticket.Image
	byID := map[string]int{}
	byName := map[string][]int{}
	for _, a := range iss.Fields.Attachment {
		if !a.isImage() || a.Content == "" {
			continue
		}
		n := len(images)
		byID[a.ID] = n
		lower := strings.ToLower(a.Filename)
		byName[lower] = append(byName[lower], n)
		images = append(images, ticket.Image{URL: a.Content, Alt: a.Filename})
	}

	// used marks which attachments a media node placed inline, so the
	// Attachments section lists the rest rather than repeating them all.
	used := make(map[int]bool, len(images))
	media := func(attrs map[string]any) string {
		alt := strings.TrimSpace(attrString(attrs, "alt"))
		if alt == "" {
			alt = strings.TrimSpace(attrString(attrs, "__fileName"))
		}
		if alt == "" {
			// No name to match on. The file is still downloaded and still
			// listed under ## Attachments; say so where the image was, so the
			// agent knows something visual belongs here.
			return "_(image — see Attachments)_"
		}
		n, ok := claimByName(byName, used, strings.ToLower(alt))
		if !ok {
			return "_(image: " + alt + " — see Attachments)_"
		}
		used[n] = true
		return "![" + alt + "](" + ticket.PlaceholderFor(n) + ")"
	}

	var b strings.Builder
	desc := renderADF(iss.Fields.Description, media)
	if desc == "" {
		desc = "_(no description)_"
	}
	b.WriteString("## Description\n\n")
	b.WriteString(desc)
	b.WriteString("\n")

	if comments != nil && len(comments.Comments) > 0 {
		fmt.Fprintf(&b, "\n## Comments (%d)\n", len(comments.Comments))
		for _, c := range comments.Comments {
			author := "someone"
			if c.Author != nil && c.Author.DisplayName != "" {
				author = c.Author.DisplayName
			}
			fmt.Fprintf(&b, "\n### %s — %s\n\n", author, formatJiraTime(c.Created))
			body := renderADF(c.Body, media)
			if body == "" {
				body = "_(empty)_"
			}
			b.WriteString(body)
			b.WriteString("\n")
		}
	}

	if n := len(iss.Fields.Subtasks); n > 0 {
		b.WriteString("\n## Sub-tasks\n\n")
		for _, s := range iss.Fields.Subtasks {
			fmt.Fprintf(&b, "- %s — %s\n", strings.ToUpper(s.Key), s.Fields.Summary)
		}
	}

	if links := renderLinks(iss.Fields.IssueLinks); links != "" {
		b.WriteString("\n## Links\n\n")
		b.WriteString(links)
	}

	if att := renderAttachments(iss.Fields.Attachment, byID, used); att != "" {
		b.WriteString("\n## Attachments\n\n")
		b.WriteString(att)
	}

	return b.String(), images
}

// renderLinks lists an issue's relationships, phrased with Jira's own words for
// them ("blocks", "is blocked by") — the direction is the whole content of a
// link, and a bare list of keys would drop it.
func renderLinks(links []issueLink) string {
	var b strings.Builder
	for _, l := range links {
		switch {
		case l.OutwardIssue != nil && l.OutwardIssue.Key != "":
			fmt.Fprintf(&b, "- %s %s — %s\n", l.Type.Outward,
				strings.ToUpper(l.OutwardIssue.Key), l.OutwardIssue.Fields.Summary)
		case l.InwardIssue != nil && l.InwardIssue.Key != "":
			fmt.Fprintf(&b, "- %s %s — %s\n", l.Type.Inward,
				strings.ToUpper(l.InwardIssue.Key), l.InwardIssue.Fields.Summary)
		}
	}
	return b.String()
}

// renderAttachments lists every attachment, inlining the images no media node
// placed and naming the non-images rather than fetching them.
//
// Non-images are deliberately not downloaded: fleet fetches images because an
// agent cannot follow a URL to look at one, and a 40MB video or a customer's
// spreadsheet is neither readable by the agent nor something to pull into a
// worktree unasked.
func renderAttachments(atts []attachment, byID map[string]int, used map[int]bool) string {
	var b strings.Builder
	for _, a := range atts {
		if a.Filename == "" {
			continue
		}
		// By id, never by filename: two attachments can share a name, and a
		// name lookup would give them both the same placeholder.
		n, isImage := byID[a.ID]
		switch {
		case isImage && used[n]:
			fmt.Fprintf(&b, "- %s (shown above)\n", a.Filename)
		case isImage:
			fmt.Fprintf(&b, "- ![%s](%s)\n", a.Filename, ticket.PlaceholderFor(n))
		default:
			fmt.Fprintf(&b, "- %s (%s, not downloaded)\n", a.Filename, a.MimeType)
		}
	}
	return b.String()
}

// formatJiraTime renders Jira's timestamp the way Linear's comments render, so
// the two trackers produce the same ticket.md shape.
//
// Jira sends "2026-08-25T09:41:22.113+0000" — RFC 3339 except for the missing
// colon in the zone, which time.RFC3339 refuses. An unparseable value is passed
// through rather than dropped: a raw timestamp still tells the reader when.
func formatJiraTime(s string) string {
	for _, layout := range []string{"2006-01-02T15:04:05.999-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	return s
}
