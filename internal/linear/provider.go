package linear

import (
	"context"
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/ticket"
)

// kind is Linear's stable id: the config key, the analytics property, and the
// prefix of its keychain service name. Never shown to a user — Name is.
const kind = "linear"

// Provider is Linear as a ticket.Provider.
//
// It is a zero-size value over package-level state rather than a struct with
// fields, because that state has to be reachable from Available(), which the
// Bubble Tea Update goroutine calls and which therefore may not do any work.
// Two atomics behind a package var is what makes that free; an instance the UI
// would have to find first is not.
type Provider struct{}

// New returns the Linear provider.
func New() Provider { return Provider{} }

var _ ticket.Provider = Provider{}

func (Provider) Kind() string { return kind }
func (Provider) Name() string { return "Linear" }

func (Provider) Available() bool { return Available() }
func (Provider) Resolved() bool  { return Resolved() }

func (Provider) Keys(repoPath string) []string { return TeamKeys(repoPath) }

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

// Document reads the whole issue in one query and normalizes it.
//
// The team's workflow states ride along on that same query, which is what lets
// Start be a closure over an already-fetched issue rather than a second round
// trip taken at mutation time.
func (Provider) Document(ctx context.Context, id string) (*ticket.Document, error) {
	var out struct {
		Issue *issueFull `json:"issue"`
	}
	if err := execute(ctx, ticket.FullTimeout, issueFullQuery, map[string]any{"id": strings.ToUpper(id)}, &out); err != nil {
		return nil, err
	}
	i := out.Issue
	if i == nil || i.Identifier == "" {
		return nil, ticket.ErrNotFound
	}

	doc := &ticket.Document{
		Ticket: i.ticket(),
		Host:   allowedImageHost,
		Auth: func(context.Context) (string, error) {
			cred, err := credential()
			if err != nil {
				return "", err
			}
			return cred.authHeader(), nil
		},
		Start: func(ctx context.Context) (string, error) { return moveToStarted(ctx, i) },
	}

	if i.Assignee != nil {
		doc.Assignee = i.Assignee.DisplayName
	}
	for _, l := range i.Labels.Nodes {
		doc.Labels = append(doc.Labels, l.Name)
	}
	if i.Parent != nil && i.Parent.Identifier != "" {
		doc.Parent = fmt.Sprintf("%s — %s", i.Parent.Identifier, i.Parent.Title)
	}
	doc.Body, doc.Images = renderBody(i)
	return doc, nil
}

// renderBody turns the issue into the markdown an agent will read, and lists
// the images that markdown references.
//
// fleet composes this itself rather than asking the API for a rendered form,
// which is what lets comments carry their author and time. "Who asked for this
// and when" is usually the part that decides whether a ticket is still current.
//
// Image links are rewritten to ticket placeholders here rather than left as
// uploads.linear.app URLs, because the file's name on disk is only known after
// its bytes arrive — the extension is recovered from them.
func renderBody(i *issueFull) (string, []ticket.Image) {
	var b strings.Builder
	desc := strings.TrimSpace(i.Description)
	if desc == "" {
		desc = "_(no description)_"
	}
	b.WriteString("## Description\n\n")
	b.WriteString(desc)
	b.WriteString("\n")

	if n := len(i.Comments.Nodes); n > 0 {
		fmt.Fprintf(&b, "\n## Comments (%d)\n", n)
		for _, c := range i.Comments.Nodes {
			author := "someone"
			if c.User != nil && c.User.DisplayName != "" {
				author = c.User.DisplayName
			}
			fmt.Fprintf(&b, "\n### %s — %s\n\n", author, c.CreatedAt.Format("2006-01-02 15:04"))
			b.WriteString(strings.TrimSpace(c.Body))
			b.WriteString("\n")
		}
	}

	if n := len(i.Children.Nodes); n > 0 {
		b.WriteString("\n## Sub-issues\n\n")
		for _, c := range i.Children.Nodes {
			fmt.Fprintf(&b, "- %s — %s\n", c.Identifier, c.Title)
		}
	}

	// Attachments are links (PRs, Figma, Slack threads), not files. Listed
	// rather than downloaded: fleet fetches images because an agent cannot
	// follow a URL, and it deliberately does not go crawling anything else.
	if n := len(i.Attachments.Nodes); n > 0 {
		b.WriteString("\n## Links\n\n")
		for _, a := range i.Attachments.Nodes {
			title := a.Title
			if title == "" {
				title = a.URL
			}
			fmt.Fprintf(&b, "- [%s](%s)\n", title, a.URL)
		}
	}

	body := b.String()
	alts, targets := findImages(body)
	images := make([]ticket.Image, 0, len(targets))
	for n, target := range targets {
		images = append(images, ticket.Image{URL: target, Alt: alts[n]})
		// Replace only the link target, and only inside a link: the same URL
		// can appear twice in one body, and both occurrences are the same file.
		body = strings.ReplaceAll(body, "]("+target+")", "]("+ticket.PlaceholderFor(n)+")")
	}
	return body, images
}
