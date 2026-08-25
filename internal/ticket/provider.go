package ticket

import (
	"context"
	"net/url"
)

// Provider is one ticket tracker.
//
// It covers reading only. Connecting is deliberately absent: Linear has two
// flows and a workspace, Jira has three fields and a site, and forcing those
// through one signature would produce a method every implementation half
// ignores. Each provider package exports its own connect surface, used by its
// own dialog.
type Provider interface {
	// Kind is the stable id used in config, analytics, and the credential's
	// keychain service name: "linear" / "jira". Never shown to a user.
	Kind() string

	// Name is what the user reads: "Linear" / "Jira".
	Name() string

	// Available reports whether fleet has a credential to work with.
	//
	// Free and non-blocking BY CONTRACT: atomics and an env var, never the
	// keychain and never the network. The UI calls this from the Bubble Tea
	// Update goroutine.
	Available() bool

	// Resolved reports whether fleet has finished looking for a credential.
	//
	// The distinction matters to anything acting on the ABSENCE of one: before
	// Warm runs, Available answers false because it does not know yet, and a
	// caller reading that as "this user has no tracker" would be wrong about a
	// connected user for the first moments of every launch.
	Resolved() bool

	// Keys returns the tracker keys this repo tracks — Linear team keys, Jira
	// project keys — or nil if it tracks none.
	//
	// Nil is the answer that keeps an unrelated repo silent for a connected
	// user, so there is deliberately NO fallback to "everything in the
	// workspace": that would put ticket suggestions under the branch field of
	// every repo on the machine.
	//
	// Free and non-blocking — two small file reads, no network — because the
	// dialog paths call it while a frame is being built.
	Keys(repoPath string) []string

	// Warm loads the stored credential once, off the Update goroutine.
	// Idempotent, so it is safe as a "make sure" before any path that needs a
	// definite answer.
	Warm(ctx context.Context)

	// Fetch returns an issue's metadata: enough to name a branch and draw a row.
	Fetch(ctx context.Context, id string) (Ticket, error)

	// Search returns issues matching a full-text term, for the dialog's
	// suggestions.
	Search(ctx context.Context, term string, limit int) ([]Ticket, error)

	// Assigned returns your open assigned issues.
	Assigned(ctx context.Context, limit int) ([]Ticket, error)

	// Document is the one full fetch: everything ticket.md needs, already
	// normalized to markdown, plus the closure that performs the single
	// mutation fleet ever makes.
	Document(ctx context.Context, id string) (*Document, error)

	// Account returns the cached workspace/site reading, and whether one has
	// been taken. Free and non-blocking.
	Account() (Account, bool)

	// FetchAccount reads it fresh, caching the result.
	FetchAccount(ctx context.Context) (Account, error)

	// ConnectedVia reports how fleet is authenticating, for the Connect
	// dialog. "" when nothing is connected.
	ConnectedVia() string

	// Disconnect forgets the credential.
	Disconnect() error
}

// Image is one remote file a Document wants pulled into the worktree.
//
// The provider decides what qualifies — Linear scrapes its own markdown, Jira
// enumerates the issue's attachments — because only the provider knows which
// URLs its credential may be sent to.
type Image struct {
	// URL is absolute and must pass the Document's Host gate.
	URL string

	// Alt names the file on disk. May be empty; sanitizeFilename copes.
	Alt string
}

// ImagePlaceholder is what a Document's Body carries in place of an image link
// it wants resolved: ![alt](fleet-image:<n>), where n indexes Document.Images.
//
// The indirection exists because the two providers find their images in
// incompatible ways and neither can write a final path: the file's name is only
// known after the bytes arrive, since the extension is recovered from them.
// collectImages rewrites every placeholder it resolved and drops the rest, so a
// capped or failed download can never leave a link to a file that isn't there.
const ImagePlaceholder = "fleet-image:"

// Document is one issue, fully read and already normalized.
//
// Everything ticket.md renders comes from here, so a provider's decoding types
// never escape its own package.
type Document struct {
	Ticket

	Assignee string
	Labels   []string
	Parent   string // "BRZ-1 — title"; empty when there is none

	// Body is the markdown an agent will read: description, comments with
	// author and time, sub-issues, links. Image links are ImagePlaceholder
	// references into Images.
	Body string

	Images []Image

	// Host gates every image URL. Exactly one host per provider — Linear's
	// upload bucket, or the configured Jira site — and it is re-checked inside
	// fetchImage on the line that attaches the credential, because a gate one
	// call away from the thing it protects is a gate a later refactor removes
	// without noticing.
	Host func(u *url.URL) bool

	// Auth returns the Authorization header for an image fetch. A closure
	// rather than a value so a token that renews mid-download is picked up,
	// and so no credential ever has to live on this struct.
	Auth func(ctx context.Context) (string, error)

	// Start moves the issue to its first started state and returns the
	// resulting state name.
	//
	// nil when the provider cannot move state at all; ("", nil) when there is
	// no started state to move to, which is not an error — it is a workflow
	// fleet has nothing to say about.
	Start func(ctx context.Context) (string, error)
}
