package ui

import (
	"reflect"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func TestGitRefreshInterval(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		a       repoActivity
		lastHot time.Time
		want    time.Duration
	}{
		{"active repo refreshes every tick", repoActive, time.Time{}, gitActiveInterval},
		{"live repo at rest slows down", repoLive, time.Time{}, gitLiveInterval},
		{"dormant repo slows further", repoDormant, time.Time{}, gitDormantInterval},
		{"recently running stays hot", repoLive, now.Add(-30 * time.Second), gitActiveInterval},
		{"hot window expires", repoLive, now.Add(-2 * gitHotWindow), gitLiveInterval},
		{"recently running dormant repo stays hot", repoDormant, now.Add(-10 * time.Second), gitActiveInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitRefreshInterval(tt.a, tt.lastHot, now); got != tt.want {
				t.Errorf("gitRefreshInterval = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityForStatus(t *testing.T) {
	tests := map[session.Status]repoActivity{
		session.StatusRunning:   repoActive,
		session.StatusWaiting:   repoActive,
		session.StatusStarting:  repoActive,
		session.StatusIdle:      repoLive,
		session.StatusFinished:  repoLive,
		session.StatusSuspended: repoDormant,
		session.StatusError:     repoDormant,
	}
	for st, want := range tests {
		if got := activityForStatus(st); got != want {
			t.Errorf("activityForStatus(%s) = %v, want %v", st, got, want)
		}
	}
}

// TestGitRefreshTargets is the idle-CPU budget for git: on a steady cycle with
// fresh readings, only repos with an active session may be refreshed — the
// regression this guards is "every repo, every 2s".
func TestGitRefreshTargets(t *testing.T) {
	now := time.Now()
	fresh := func(age time.Duration) *git.RepoInfo { return &git.RepoInfo{LastGitRefresh: now.Add(-age)} }
	samples := []repoSample{
		{repo: "/active", status: session.StatusFinished},
		{repo: "/active", status: session.StatusRunning}, // most active session wins
		{repo: "/live", status: session.StatusIdle},
		{repo: "/dormant", status: session.StatusSuspended},
		{repo: "/dormant", status: session.StatusError},
		{repo: "/new", status: session.StatusFinished},
	}
	infos := map[string]*git.RepoInfo{
		"/active":  fresh(tickInterval),
		"/live":    fresh(5 * time.Second),
		"/dormant": fresh(time.Minute),
		// "/new" has never been refreshed.
	}

	got := gitRefreshTargets(samples, infos, nil, now, false)
	if want := []string{"/active", "/new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("steady cycle refreshed %v, want %v", got, want)
	}

	infos["/live"] = fresh(gitLiveInterval)
	infos["/dormant"] = fresh(gitDormantInterval)
	got = gitRefreshTargets(samples, infos, nil, now, false)
	if want := []string{"/active", "/live", "/dormant", "/new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("expired readings refreshed %v, want %v", got, want)
	}
}

func TestGitRefreshTargetsForceRefreshesEverything(t *testing.T) {
	now := time.Now()
	samples := []repoSample{{repo: "/a", status: session.StatusSuspended}, {repo: "/b", status: session.StatusIdle}}
	infos := map[string]*git.RepoInfo{"/a": {LastGitRefresh: now}, "/b": {LastGitRefresh: now}}
	if got := gitRefreshTargets(samples, infos, nil, now, true); len(got) != 2 {
		t.Errorf("forced cycle refreshed %v, want both repos", got)
	}
}

func TestGitRefreshDueAbsorbsTickJitter(t *testing.T) {
	now := time.Now()
	// Refreshed one tick ago minus a few ms of ticker jitter: still due.
	if !gitRefreshDue(now.Add(-tickInterval+20*time.Millisecond), gitActiveInterval, now, false) {
		t.Error("an active repo skipped a beat because the ticker fired a few ms early")
	}
	if gitRefreshDue(now.Add(-10*time.Second), gitLiveInterval, now, false) {
		t.Error("a live repo refreshed 10s after its last reading")
	}
}

func TestPRCacheRowChanged(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	base := git.RepoInfo{Branch: "main", OriginKey: "github.com/a/b", LastPRRefresh: t0}
	with := func(f func(*git.RepoInfo)) *git.RepoInfo { c := base; f(&c); return &c }
	tests := []struct {
		name      string
		old, next *git.RepoInfo
		want      bool
	}{
		{"never persisted", nil, &base, true},
		{"nothing moved", &base, with(func(*git.RepoInfo) {}), false},
		{"dirty flip alone is not persisted state", &base, with(func(i *git.RepoInfo) { i.IsDirty = true }), false},
		{"branch switch", &base, with(func(i *git.RepoInfo) { i.Branch = "feat" }), true},
		{"origin changed", &base, with(func(i *git.RepoInfo) { i.OriginKey = "x" }), true},
		{"PR fetched", &base, with(func(i *git.RepoInfo) { i.LastPRRefresh = t0.Add(time.Minute) }), true},
		{"rate limited", &base, with(func(i *git.RepoInfo) { i.PRRateLimitedAt = t0 }), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prCacheRowChanged(tt.old, tt.next); got != tt.want {
				t.Errorf("prCacheRowChanged = %v, want %v", got, tt.want)
			}
		})
	}
}
