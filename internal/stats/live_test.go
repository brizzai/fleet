package stats

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noteOld notes a session created well before the transcript's turns, so the
// ownTurns rule (turns before the session existed are a fork's inherited
// history) keeps them.
func noteOld(s *Store, id, cid string) {
	s.NoteSession(SessionMeta{ID: id, ClaudeSessionID: cid, Agent: "claude", Repo: "repo", CreatedAt: time.Now().Add(-48 * time.Hour)})
	s.Flush(time.Second)
}

// transcript with two priced turns: opus-4-8 is $5/$25 per million in/out.
func writeLiveTranscript(t *testing.T, root, cid string, turns int) string {
	t.Helper()
	p := filepath.Join(root, "-proj", cid+".jsonl")
	base := time.Now().Add(-time.Hour)
	var body string
	for i := 0; i < turns; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		body += humanLine(at)
		body += assistantLine(at.Add(5*time.Second), "m"+string(rune('a'+i)), "claude-opus-4-8", 1_000_000, 100_000, 0, 0, false)
		body += durationLine(at.Add(10*time.Second), 10*time.Second, false)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanConversationMatchesStatsHeroAndIsIncremental(t *testing.T) {
	s, root := openTest(t)
	noteOld(s, "fleet-1", "conv-1")
	p := writeLiveTranscript(t, root, "conv-1", 2)

	got, err := s.ScanConversation(context.Background(), "conv-1", p)
	if err != nil {
		t.Fatal(err)
	}
	// 2 turns × (1M in × $5 + 0.1M out × $25) = 2 × (5 + 2.5) = $15.
	if got.Turns != 2 || math.Abs(got.USD-15) > 1e-9 || got.Unpriced != 0 {
		t.Fatalf("got %+v, want 2 turns / $15", got)
	}

	// The Stats screen reads the same rows: its hero must show the same dollars.
	if err := s.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Report(RangeAll, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rep.Hero.CostUSD-got.USD) > 1e-9 {
		t.Fatalf("live $%.4f vs Stats hero $%.4f: they must agree", got.USD, rep.Hero.CostUSD)
	}

	// A third turn lands: only the new bytes are read, and the total grows once.
	p = writeLiveTranscript(t, root, "conv-1", 3)
	got, err = s.ScanConversation(context.Background(), "conv-1", p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns != 3 || math.Abs(got.USD-22.5) > 1e-9 {
		t.Fatalf("after a third turn got %+v, want 3 turns / $22.50 (no double count)", got)
	}
}

func TestScanConversationUnnotedAndUnpriced(t *testing.T) {
	s, root := openTest(t)
	p := writeLiveTranscript(t, root, "ghost", 1)
	if _, err := s.ScanConversation(context.Background(), "ghost", p); !errors.Is(err, ErrNotNoted) {
		t.Fatalf("an unknown conversation must say so, got %v", err)
	}

	// A model the table doesn't know: tokens counted, dollars not — flagged, not zero-silent.
	noteOld(s, "fleet-2", "conv-2")
	at := time.Now().Add(-time.Hour)
	body := humanLine(at) + assistantLine(at.Add(time.Second), "m1", "claude-mystery-9", 1000, 1000, 0, 0, false) + durationLine(at.Add(2*time.Second), 2*time.Second, false)
	p2 := filepath.Join(root, "-proj", "conv-2.jsonl")
	if err := os.WriteFile(p2, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScanConversation(context.Background(), "conv-2", p2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unpriced != 1 || got.USD != 0 {
		t.Fatalf("got %+v, want 1 unpriced turn and $0", got)
	}
}

func TestScanConversationNilStore(t *testing.T) {
	var s *Store
	if c, err := s.ScanConversation(context.Background(), "x", "/nope"); err != nil || c != (LiveCost{}) {
		t.Fatalf("nil store: %+v %v", c, err)
	}
}

// A fork's transcript starts with a copy of its parent's history; turns older
// than the fork itself are the parent's money, not the fork's.
func TestScanConversationDropsHistoryOlderThanTheSession(t *testing.T) {
	s, root := openTest(t)
	s.NoteSession(SessionMeta{ID: "fork-1", ClaudeSessionID: "conv-f", Agent: "claude", Repo: "repo", CreatedAt: time.Now()})
	s.Flush(time.Second)
	p := writeLiveTranscript(t, root, "conv-f", 2) // turns an hour ago
	got, err := s.ScanConversation(context.Background(), "conv-f", p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns != 0 || got.USD != 0 {
		t.Fatalf("inherited history was charged to the fork: %+v", got)
	}
}

// Prices checked against ccusage on real transcripts: the same tokens must cost
// the same dollars. A model that falls through to an older family's row (cache
// reads are most of a long session) overstates the bill by half.
func TestPriceForNewestModelsMatchesCcusage(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  price
	}{
		{"claude-sonnet-5-5", price{2, 10, 0.10}},
		{"claude-sonnet-5-5[1m]", price{2, 10, 0.10}},
		{"claude-fable-5-2", price{8, 40, 0.20}},
		{"claude-opus-5-5", price{4, 20, 0.20}},
		{"claude-sonnet-5", price{2, 10, 0.20}}, // the older 5.0 row is untouched
	} {
		got, ok := priceFor(tc.model)
		if !ok || got != tc.want {
			t.Errorf("priceFor(%q) = %+v ok=%v, want %+v", tc.model, got, ok, tc.want)
		}
	}
	// One real session's Sonnet 5.5 tokens (216f7857…): ccusage reports $46.12.
	usd, ok := turnCost("claude-sonnet-5-5", 2614, 599922, 260465052, 0, 3516606)
	if !ok || math.Abs(usd-46.1174) > 0.01 {
		t.Errorf("session cost = $%.4f, want ≈ $46.12 (ccusage)", usd)
	}
}
