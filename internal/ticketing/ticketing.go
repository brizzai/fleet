// Package ticketing routes fleet's ticket surfaces to whichever tracker owns
// the ticket in front of them.
//
// It is the only ticket package the UI and cmd/fleet import. That is what keeps
// the providers substitutable: internal/ticket knows nothing about Linear or
// Jira, internal/linear and internal/jira know nothing about each other, and
// this is the one place that knows there is more than one.
//
// Every function here preserves the contract the single-provider version had:
// Available, Resolved and Keys touch no network and no keychain, because the
// Bubble Tea Update goroutine calls them.
package ticketing

import (
	"context"
	"strings"
	"sync"

	"github.com/brizzai/fleet/internal/jira"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/ticket"
)

// providers is the registry, in a fixed order.
//
// The order is not cosmetic: it decides which tracker owns a key that two of
// them claim. A repo naming BRZ as both a Linear team and a Jira project is
// pathological, but "first configured wins, and the order never changes" is an
// answer, where "whichever map iteration reached it first" is a coin flip that
// lands differently between launches.
var providers = []ticket.Provider{linear.New(), jira.New()}

// All returns every provider fleet knows about, connected or not.
func All() []ticket.Provider { return providers }

// ByKind returns the provider with this Kind.
func ByKind(kind string) (ticket.Provider, bool) {
	for _, p := range providers {
		if p.Kind() == kind {
			return p, true
		}
	}
	return nil, false
}

// Available reports whether any tracker is connected.
func Available() bool {
	for _, p := range providers {
		if p.Available() {
			return true
		}
	}
	return false
}

// Resolved reports whether every tracker has finished looking for a credential.
//
// All of them, not any: a caller acting on the ABSENCE of a credential — the
// "connect a tracker" tip is the one that matters — must not fire while one
// provider is still resolving and might be about to say yes.
func Resolved() bool {
	for _, p := range providers {
		if !p.Resolved() {
			return false
		}
	}
	return true
}

// Warm resolves every provider's credential once, off the Update goroutine.
func Warm(ctx context.Context) {
	for _, p := range providers {
		p.Warm(ctx)
	}
}

// ConnectedNames lists the display names of the connected trackers, in registry
// order — for the one line a dialog draws while it is searching.
func ConnectedNames() []string {
	var names []string
	for _, p := range providers {
		if p.Available() {
			names = append(names, p.Name())
		}
	}
	return names
}

// Bound is a provider paired with the keys one repo tracks through it.
type Bound struct {
	Provider ticket.Provider
	Keys     []string
}

// For returns the connected providers this repo tracks, each with its keys.
//
// BOTH gates: a provider with no credential is skipped even when the repo names
// its keys, and a repo naming no keys gets nothing even from a connected
// provider. Empty means every ticket surface stays inert, which is the
// opt-out-by-absence rule in one line.
//
// Free and non-blocking — a couple of small file reads per provider.
func For(repoPath string) []Bound {
	var out []Bound
	for _, p := range providers {
		if !p.Available() {
			continue
		}
		if keys := p.Keys(repoPath); len(keys) > 0 {
			out = append(out, Bound{Provider: p, Keys: keys})
		}
	}
	return out
}

// Keys is the union of every tracker key this repo tracks, deduped in registry
// order — what LooksLikeIdentifier and IdentifierFromBranch gate on.
func Keys(repoPath string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, b := range For(repoPath) {
		for _, k := range b.Keys {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// Owner resolves which provider owns an identifier in this repo, by its key
// prefix. Registry order breaks a tie.
func Owner(repoPath, id string) (ticket.Provider, bool) {
	return OwnerIn(For(repoPath), id)
}

// ClaimedBy reports which provider's repo config names this identifier's key,
// IGNORING whether that provider is connected.
//
// The two gates fail for different reasons and want different advice, and
// Owner collapses them into one "no". Told to materialize OPS-1 in a repo whose
// .fleet.local.json already names OPS, "add its project to .fleet.local.json"
// is not just unhelpful — it is a false statement about the user's own config,
// and they will go and check.
func ClaimedBy(repoPath, id string) (ticket.Provider, bool) {
	prefix, _, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(id)), "-")
	if !ok || prefix == "" {
		return nil, false
	}
	for _, p := range providers {
		for _, k := range p.Keys(repoPath) {
			if strings.EqualFold(k, prefix) {
				return p, true
			}
		}
	}
	return nil, false
}

// Fetch reads one issue through whichever provider owns its key in this repo.
func Fetch(ctx context.Context, repoPath, id string) (ticket.Ticket, error) {
	p, ok := Owner(repoPath, id)
	if !ok {
		return ticket.Ticket{}, ticket.ErrNotConnected
	}
	return p.Fetch(ctx, id)
}

// Materialize writes a ticket into a worktree through the provider that owns it.
func Materialize(ctx context.Context, repoPath string, o ticket.Opts) (ticket.Result, error) {
	p, ok := Owner(repoPath, o.Identifier)
	if !ok {
		return ticket.Result{}, ticket.ErrNotConnected
	}
	return ticket.Materialize(ctx, p, o)
}

// Search fans out across the providers this repo tracks and merges the results.
func Search(ctx context.Context, repoPath, term string, limit int) ([]ticket.Ticket, error) {
	return SearchBound(ctx, For(repoPath), term, limit)
}

// SearchBound is Search against an explicit provider set, for a caller that
// already resolved one and must not silently search a different one.
//
// The worktree dialog is that caller: it resolves its providers once, on the
// worker goroutine, and then latches individual ones off as their credentials
// are refused. Re-deriving the set here would search a tracker the dialog has
// already given up on, and would also make the dialog untestable — its whole
// behaviour would depend on process-wide credential state rather than on what
// it was handed.
//
// Concurrent, because two trackers answering in series would double the
// dialog's already-debounced latency for the repo that tracks both. A provider
// that fails contributes nothing and does not fail the others: the surface this
// feeds is a suggestion list, and half a list is worth more than an error.
func SearchBound(ctx context.Context, bound []Bound, term string, limit int) ([]ticket.Ticket, error) {
	if len(bound) == 0 {
		return nil, ticket.ErrNotConnected
	}
	if len(bound) == 1 {
		return bound[0].Provider.Search(ctx, term, limit)
	}
	return fanOut(bound, func(p ticket.Provider) ([]ticket.Ticket, error) {
		return p.Search(ctx, term, limit)
	})
}

// OwnerIn resolves which provider in an explicit set owns an identifier, by its
// key prefix. Order breaks a tie.
func OwnerIn(bound []Bound, id string) (ticket.Provider, bool) {
	prefix, _, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(id)), "-")
	if !ok || prefix == "" {
		return nil, false
	}
	for _, b := range bound {
		for _, k := range b.Keys {
			if strings.EqualFold(k, prefix) {
				return b.Provider, true
			}
		}
	}
	return nil, false
}

// Assigned fans out across every connected provider and merges the results.
//
// Unlike Search this is not repo-scoped: the tickets tab spans every repo on
// screen, and its rows carry their own key prefixes, so scoping it to the repo
// under the cursor would hide most of your work.
func Assigned(ctx context.Context, limit int) ([]ticket.Ticket, error) {
	var bound []Bound
	for _, p := range providers {
		if p.Available() {
			bound = append(bound, Bound{Provider: p})
		}
	}
	if len(bound) == 0 {
		return nil, ticket.ErrNotConnected
	}
	if len(bound) == 1 {
		return bound[0].Provider.Assigned(ctx, limit)
	}
	return fanOut(bound, func(p ticket.Provider) ([]ticket.Ticket, error) {
		return p.Assigned(ctx, limit)
	})
}

// fanOut runs one read across several providers concurrently and merges what
// came back, re-sorting so a merged list is ordered by the same rule as either
// tracker alone.
//
// An error is returned only when EVERY provider failed — and then it is the
// first one, so a single broken credential still names itself rather than being
// reported as a generic failure. Any success wins, because a list missing one
// tracker is still useful and an error would replace it with nothing.
func fanOut(bound []Bound, read func(ticket.Provider) ([]ticket.Ticket, error)) ([]ticket.Ticket, error) {
	results := make([][]ticket.Ticket, len(bound))
	errs := make([]error, len(bound))

	var wg sync.WaitGroup
	for i, b := range bound {
		wg.Add(1)
		go func(i int, p ticket.Provider) {
			defer wg.Done()
			results[i], errs[i] = read(p)
		}(i, b.Provider)
	}
	wg.Wait()

	var merged []ticket.Ticket
	var firstErr error
	ok := false
	for i := range bound {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		ok = true
		merged = append(merged, results[i]...)
	}
	if !ok {
		return nil, firstErr
	}
	ticket.SortAssigned(merged)
	return merged, nil
}
