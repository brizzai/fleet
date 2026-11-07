package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/github"
)

const (
	// reviewListTimeout bounds the whole fetch. It is generous relative to
	// ghTimeout because this runs off the Update goroutine and a slow answer
	// is better than an empty tab.
	reviewListTimeout = 20 * time.Second

	// botFoldID is the synthetic row standing in for every bot-authored PR.
	// It is a fold, never a batch action: expanding is the only thing it does,
	// because a key that approved thirty dependency bumps at once is a
	// sentence that appears in a postmortem exactly once.
	botFoldID = "reviews:bots"
)

// paletteReviewsMsg carries the loaded review queue back to Update.
type paletteReviewsMsg struct {
	reviews []github.ReviewRequest
	err     error
}

// loadPaletteReviews fetches the PRs awaiting this user's review.
//
// One search covers every repo, including ones fleet has never opened. That is
// the point of the surface: the queue you cannot see is the one that grows.
func loadPaletteReviews() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewListTimeout)
		defer cancel()
		reviews, err := github.ReviewsRequested(ctx)
		return paletteReviewsMsg{reviews: reviews, err: err}
	}
}

// maybeLoadPaletteReviews skips the fetch when gh cannot answer, so the tab
// degrades to empty rather than to an error the user can do nothing about.
func (h *Home) maybeLoadPaletteReviews() tea.Cmd {
	if !github.IsGHAvailable() {
		return nil
	}
	return loadPaletteReviews()
}

// reviewPaletteItems turns the queue into palette rows, with every
// bot-authored PR folded into one counted row at the bottom.
//
// The fold is the whole reason this tab corrects anything: the raw queue is
// dominated by dependabot, brizz-bot and sentry, and a list that shows them
// inline reads as "48 things I owe" when the real answer is 18.
func (h *Home) reviewPaletteItems(reviews []github.ReviewRequest) []PaletteItem {
	humans := make([]github.ReviewRequest, 0, len(reviews))
	bots := make([]github.ReviewRequest, 0, len(reviews))
	for _, r := range reviews {
		if r.IsBot {
			bots = append(bots, r)
			continue
		}
		humans = append(humans, r)
	}

	// Newest activity first: a PR whose author just pushed or replied is the
	// one most likely to be waiting on you right now.
	sort.SliceStable(humans, func(i, j int) bool {
		return humans[i].UpdatedAt.After(humans[j].UpdatedAt)
	})

	// Numbers vary in width (#993 vs #5157), so pad them to the widest in the
	// set — otherwise every title starts at a slightly different column and
	// the list reads as ragged rather than as a table.
	numWidth := 0
	for _, r := range humans {
		if n := len(fmt.Sprintf("#%d", r.Number)); n > numWidth {
			numWidth = n
		}
	}

	items := make([]PaletteItem, 0, len(humans)+1)
	for _, r := range humans {
		name := fmt.Sprintf("%-*s  %s", numWidth, fmt.Sprintf("#%d", r.Number), r.Title)
		right := reviewRightColumn(r)
		items = append(items, PaletteItem{
			Kind: PaletteKindReview,
			ID:   r.URL,
			Name: name,
			// The author is the section header, so it is not repeated on the
			// row. Six people own your whole queue; grouping by them is what
			// makes "I owe Dennis six" legible at a glance.
			Group: r.Author,
			// Haystack must be exactly Name + " " + <right column>: the
			// renderer maps matched indexes back onto those two strings by
			// offset, so composing it any other way lights up the wrong runes.
			Haystack: name + " " + right,
			Detail:   right,
		})
	}

	if len(bots) > 0 {
		label := fmt.Sprintf("%d bot PRs", len(bots))
		authors := botAuthorSummary(bots)
		items = append(items, PaletteItem{
			Kind:     PaletteKindReview,
			ID:       botFoldID,
			Name:     label,
			Group:    "folded",
			Detail:   authors,
			Haystack: label + " " + authors,
		})
	}

	return items
}

// reviewRightColumn is what the title does not say: how big the change is and
// how long it has been sitting there.
func reviewRightColumn(r github.ReviewRequest) string {
	parts := make([]string, 0, 3)
	if r.IsDraft {
		parts = append(parts, "draft")
	}
	if r.Decision == "CHANGES_REQUESTED" {
		parts = append(parts, "changes requested")
	}
	parts = append(parts, fmt.Sprintf("%df +%d −%d", r.ChangedFiles, r.Additions, r.Deletions))
	if age := reviewAge(r.UpdatedAt); age != "" {
		parts = append(parts, age)
	}
	return strings.Join(parts, "  ")
}

// reviewAge renders how long a PR has been waiting, in the coarsest unit that
// is still true — an exact minute count on a four-day-old PR is noise.
func reviewAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// botAuthorSummary names the bots behind the fold, deduped and in a stable
// order, so the folded row says what it is hiding rather than just how much.
func botAuthorSummary(bots []github.ReviewRequest) string {
	seen := map[string]bool{}
	names := make([]string, 0, 4)
	for _, b := range bots {
		name := strings.TrimSuffix(b.Author, "[bot]")
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 4 {
		names = append(names[:4], "…")
	}
	return strings.Join(names, " · ")
}

// openReviewFromPalette acts on a chosen review row: it starts the review.
//
// Enter starts rather than opens because starting is the expensive half — the
// fetch, the worktree, the agent — and the browser is one key away from the
// session once it exists. Ctrl+P opens the PR in Chrome without starting
// anything; plain `p` cannot be used here because the palette is a filter box
// and a bare letter is text.
func (h *Home) openReviewFromPalette(id string) (tea.Model, tea.Cmd) {
	if id == botFoldID {
		h.setInfo("Bot PRs are folded — expanding them is not wired up yet")
		return h, nil
	}
	return h.startReview(id)
}
