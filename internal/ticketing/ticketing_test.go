package ticketing

import (
	"context"
	"errors"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// fake is a provider under test control. Every method the router touches is
// here; the rest satisfy the interface and are never called.
type fake struct {
	kind      string
	name      string
	available bool
	resolved  bool
	keys      []string
	tickets   []ticket.Ticket
	err       error
}

func (f *fake) Kind() string                    { return f.kind }
func (f *fake) Name() string                    { return f.name }
func (f *fake) Available() bool                 { return f.available }
func (f *fake) Resolved() bool                  { return f.resolved }
func (f *fake) Keys(string) []string            { return f.keys }
func (f *fake) Warm(context.Context)            {}
func (f *fake) ConnectedVia() string            { return "" }
func (f *fake) Disconnect() error               { return nil }
func (f *fake) Account() (ticket.Account, bool) { return ticket.Account{}, false }

func (f *fake) FetchAccount(context.Context) (ticket.Account, error) {
	return ticket.Account{}, nil
}
func (f *fake) Fetch(context.Context, string) (ticket.Ticket, error) {
	if f.err != nil {
		return ticket.Ticket{}, f.err
	}
	if len(f.tickets) == 0 {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return f.tickets[0], nil
}
func (f *fake) Search(context.Context, string, int) ([]ticket.Ticket, error) {
	return f.tickets, f.err
}
func (f *fake) Assigned(context.Context, int) ([]ticket.Ticket, error) {
	return f.tickets, f.err
}
func (f *fake) Document(context.Context, string) (*ticket.Document, error) {
	return nil, f.err
}

// swap installs a provider set for the duration of a test.
func swap(t *testing.T, ps ...ticket.Provider) {
	t.Helper()
	orig := providers
	providers = ps
	t.Cleanup(func() { providers = orig })
}

func tkt(id string, state ticket.StateType, pri ticket.Priority) ticket.Ticket {
	return ticket.Ticket{Identifier: id, Title: id, State: state, Priority: pri}
}

// TestForRequiresBothGates is the opt-out-by-absence rule, executable.
//
// A connected provider whose keys this repo does not name must contribute
// nothing, and a repo naming keys for a provider with no credential must too.
// Get either wrong and a user sees ticket chrome under the branch field of
// every unrelated repo on their machine.
func TestForRequiresBothGates(t *testing.T) {
	connectedTracked := &fake{kind: "a", available: true, keys: []string{"AAA"}}
	connectedUntracked := &fake{kind: "b", available: true}
	disconnectedTracked := &fake{kind: "c", keys: []string{"CCC"}}
	swap(t, connectedTracked, connectedUntracked, disconnectedTracked)

	bound := For("/repo")
	if len(bound) != 1 || bound[0].Provider.Kind() != "a" {
		t.Fatalf("For = %+v, want only the connected AND tracked provider", bound)
	}
	if got := Keys("/repo"); len(got) != 1 || got[0] != "AAA" {
		t.Errorf("Keys = %v, want only AAA", got)
	}
}

// TestKeysDedupesAcrossProviders: the union feeds LooksLikeIdentifier, and a
// repeated key there would make a repo's disclosure line read "BRZ BRZ".
func TestKeysDedupesAcrossProviders(t *testing.T) {
	swap(t,
		&fake{kind: "a", available: true, keys: []string{"BRZ", "PRD"}},
		&fake{kind: "b", available: true, keys: []string{"BRZ", "OPS"}},
	)
	got := Keys("/repo")
	want := []string{"BRZ", "PRD", "OPS"}
	if len(got) != len(want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys = %v, want %v (registry order, deduped)", got, want)
		}
	}
}

// TestOwnerIsDecidedByRegistryOrder pins the tie-break.
//
// A repo naming BRZ as both a Linear team and a Jira project is pathological,
// but "first configured wins, and the order never changes" is an answer, where
// "whichever the loop reached first" is a coin flip that could land differently
// between launches.
func TestOwnerIsDecidedByRegistryOrder(t *testing.T) {
	first := &fake{kind: "first", available: true, keys: []string{"BRZ"}}
	second := &fake{kind: "second", available: true, keys: []string{"BRZ", "OPS"}}
	swap(t, first, second)

	p, ok := Owner("/repo", "BRZ-1")
	if !ok || p.Kind() != "first" {
		t.Errorf("Owner(BRZ-1) = %v, want the first-registered provider", p)
	}
	p, ok = Owner("/repo", "ops-42")
	if !ok || p.Kind() != "second" {
		t.Errorf("Owner(ops-42) = %v, want the provider that names OPS", p)
	}
	if _, ok := Owner("/repo", "NOPE-1"); ok {
		t.Error("an unclaimed key must resolve to no provider")
	}
	if _, ok := Owner("/repo", "garbage"); ok {
		t.Error("text with no key prefix must resolve to no provider")
	}
}

// TestResolvedNeedsEveryProvider: anything acting on the ABSENCE of a
// credential must not fire while one provider is still resolving and might be
// about to say yes. The "connect a tracker" tip is exactly that caller.
func TestResolvedNeedsEveryProvider(t *testing.T) {
	swap(t, &fake{kind: "a", resolved: true}, &fake{kind: "b"})
	if Resolved() {
		t.Error("Resolved must be false while any provider is still warming")
	}
	swap(t, &fake{kind: "a", resolved: true}, &fake{kind: "b", resolved: true})
	if !Resolved() {
		t.Error("Resolved must be true once every provider has warmed")
	}
}

// TestAssignedMergesAndReSorts: a merged list must come out in the same order
// either tracker alone would produce, or the tab reorders itself depending on
// how many trackers you happen to have connected.
func TestAssignedMergesAndReSorts(t *testing.T) {
	swap(t,
		&fake{kind: "a", available: true, tickets: []ticket.Ticket{
			tkt("AAA-1", ticket.StateUnstarted, ticket.PriorityLow),
			tkt("AAA-2", ticket.StateStarted, ticket.PriorityNone),
		}},
		&fake{kind: "b", available: true, tickets: []ticket.Ticket{
			tkt("BBB-1", ticket.StateUnstarted, ticket.PriorityUrgent),
			tkt("BBB-2", ticket.StateStarted, ticket.PriorityHigh),
		}},
	)
	got, err := Assigned(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BBB-2", "AAA-2", "BBB-1", "AAA-1"}
	for i := range want {
		if got[i].Identifier != want[i] {
			t.Fatalf("order = %v, want %v (started first, then by priority)", ids(got), want)
		}
	}
}

// TestAssignedSurvivesOneBrokenProvider: half a list is worth more than an
// error. A single revoked credential must not empty the tickets tab.
func TestAssignedSurvivesOneBrokenProvider(t *testing.T) {
	swap(t,
		&fake{kind: "a", available: true, err: ticket.ErrNotAuthenticated},
		&fake{kind: "b", available: true, tickets: []ticket.Ticket{tkt("BBB-1", ticket.StateStarted, ticket.PriorityHigh)}},
	)
	got, err := Assigned(context.Background(), 10)
	if err != nil {
		t.Fatalf("one broken provider must not fail the merge: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "BBB-1" {
		t.Errorf("got %v, want the working provider's rows", ids(got))
	}
}

// TestAssignedReportsTheFirstErrorWhenAllFail: when nothing worked, the caller
// gets a real reason rather than a generic one — a revoked credential names
// itself so the note can say what to do about it.
func TestAssignedReportsTheFirstErrorWhenAllFail(t *testing.T) {
	swap(t,
		&fake{kind: "a", available: true, err: ticket.ErrNotAuthenticated},
		&fake{kind: "b", available: true, err: ticket.ErrNotAuthenticated},
	)
	_, err := Assigned(context.Background(), 10)
	if !errors.Is(err, ticket.ErrNotAuthenticated) {
		t.Errorf("err = %v, want the providers' own sentinel", err)
	}

	// And with nothing connected at all, the resting state — never surfaced.
	swap(t, &fake{kind: "a"}, &fake{kind: "b"})
	if _, err := Assigned(context.Background(), 10); !errors.Is(err, ticket.ErrNotConnected) {
		t.Errorf("err = %v, want ErrNotConnected", err)
	}
}

// TestSearchBoundUsesTheSetItWasGiven pins why SearchBound exists: the worktree
// dialog latches individual providers off as their credentials are refused, and
// re-deriving the set here would search a tracker it has already given up on.
func TestSearchBoundUsesTheSetItWasGiven(t *testing.T) {
	live := &fake{kind: "live", available: true, keys: []string{"AAA"},
		tickets: []ticket.Ticket{tkt("AAA-1", ticket.StateStarted, ticket.PriorityHigh)}}
	latched := &fake{kind: "latched", available: true, keys: []string{"BBB"},
		tickets: []ticket.Ticket{tkt("BBB-1", ticket.StateStarted, ticket.PriorityHigh)}}
	swap(t, live, latched)

	got, err := SearchBound(context.Background(), []Bound{{Provider: live, Keys: live.keys}}, "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Identifier != "AAA-1" {
		t.Errorf("got %v — SearchBound must not reach the provider it was not given", ids(got))
	}
}

func ids(ts []ticket.Ticket) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Identifier)
	}
	return out
}

// TestMaterializeHonoursTheTicketsOwnProvider pins the regression this fix
// closes.
//
// Search is deliberately unscoped by team or project, so a repo that names only
// BRZ will happily offer PRD-45 in the `w` dialog. Re-resolving that pick
// against the repo's keys refused it here — after the branch had been named and
// the worktree created — and ticketStatusLine swallowed ErrNotConnected, so the
// user got `prd-45-login-crash` with no ticket, no prompt, and nothing on
// screen saying why. It worked before the router existed.
//
// The repo gate decides WHETHER ticket surfaces appear at all. It is not a rule
// about which issue you may work on, and applying it a second time made it one.
func TestMaterializeHonoursTheTicketsOwnProvider(t *testing.T) {
	// A repo that tracks BRZ only.
	tracked := &fake{kind: "tracked", available: true, keys: []string{"BRZ"}}
	// ...and a connected provider whose keys this repo does not name, which is
	// exactly what an unscoped search can surface.
	other := &fake{kind: "other", available: true}
	swap(t, tracked, other)

	if _, ok := Owner("/repo", "PRD-45"); ok {
		t.Fatal("precondition: the repo must not claim PRD-45 by key")
	}

	// With the provider carried, routing succeeds and reaches that provider.
	p, ok := ByKind("other")
	if !ok || p.Kind() != "other" {
		t.Fatalf("ByKind = %v", p)
	}

	// Document is what Materialize calls; a sentinel error proves which
	// provider it routed to without needing a worktree on disk.
	other.err = errSentinel
	_, err := Materialize(context.Background(), "/repo", ticket.Opts{
		WorktreePath: t.TempDir(),
		Identifier:   "PRD-45",
		Provider:     "other",
	})
	if !errors.Is(err, errSentinel) {
		t.Errorf("err = %v, want the pick to have been routed to its own provider", err)
	}
	if errors.Is(err, ticket.ErrNotConnected) {
		t.Error("the repo gate was applied a second time to a ticket that already had a provider")
	}
}

// TestMaterializeFallsBackToTheRepoGate: a bare identifier nobody has claimed
// yet — the `fleet wt --ticket` path — still resolves by the repo's keys.
func TestMaterializeFallsBackToTheRepoGate(t *testing.T) {
	tracked := &fake{kind: "tracked", available: true, keys: []string{"BRZ"}, err: errSentinel}
	swap(t, tracked)

	_, err := Materialize(context.Background(), "/repo", ticket.Opts{
		WorktreePath: t.TempDir(),
		Identifier:   "BRZ-1",
	})
	if !errors.Is(err, errSentinel) {
		t.Errorf("err = %v, want the repo gate to have resolved BRZ", err)
	}

	// And an identifier no provider claims, with no provider carried, is still
	// the honest "nothing here will read this".
	_, err = Materialize(context.Background(), "/repo", ticket.Opts{
		WorktreePath: t.TempDir(),
		Identifier:   "ZZZ-1",
	})
	if !errors.Is(err, ticket.ErrNotConnected) {
		t.Errorf("err = %v, want ErrNotConnected", err)
	}
}

// TestMaterializeIgnoresAnUnknownProviderKind: a stale kind on a ticket must
// fall through to the repo gate rather than failing outright.
func TestMaterializeIgnoresAnUnknownProviderKind(t *testing.T) {
	tracked := &fake{kind: "tracked", available: true, keys: []string{"BRZ"}, err: errSentinel}
	swap(t, tracked)

	_, err := Materialize(context.Background(), "/repo", ticket.Opts{
		WorktreePath: t.TempDir(),
		Identifier:   "BRZ-1",
		Provider:     "a-tracker-that-was-removed",
	})
	if !errors.Is(err, errSentinel) {
		t.Errorf("err = %v, want the fallback to have resolved BRZ", err)
	}
}

var errSentinel = errors.New("routed here")
