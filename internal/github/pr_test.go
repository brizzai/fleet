package github

import (
	"encoding/json"
	"testing"
)

func TestDeriveCIStatus(t *testing.T) {
	tests := []struct {
		name   string
		checks []statusCheckEntry
		ignore []string
		want   string
	}{
		{"empty", nil, nil, ""},
		{
			"all success",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"},
			},
			nil,
			"SUCCESS",
		},
		{
			"failure",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			nil,
			"FAILURE",
		},
		{
			"pending",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "test", Status: "IN_PROGRESS", Conclusion: ""},
			},
			nil,
			"PENDING",
		},
		{
			"failure takes priority over pending",
			[]statusCheckEntry{
				{Name: "build", Status: "IN_PROGRESS", Conclusion: ""},
				{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			nil,
			"FAILURE",
		},
		{
			"ghost entries ignored",
			[]statusCheckEntry{
				{Name: "", Status: "", Conclusion: ""},
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
			},
			nil,
			"SUCCESS",
		},
		{
			"only ghost entries",
			[]statusCheckEntry{
				{Name: "", Status: "", Conclusion: ""},
			},
			nil,
			"SUCCESS", // all named checks pass (none exist)
		},
		{
			"error conclusion",
			[]statusCheckEntry{
				{Name: "deploy", Status: "COMPLETED", Conclusion: "ERROR"},
			},
			nil,
			"FAILURE",
		},
		{
			"timed out",
			[]statusCheckEntry{
				{Name: "e2e", Status: "COMPLETED", Conclusion: "TIMED_OUT"},
			},
			nil,
			"FAILURE",
		},
		{
			"queued",
			[]statusCheckEntry{
				{Name: "build", Status: "QUEUED", Conclusion: ""},
			},
			nil,
			"PENDING",
		},
		{
			"ignored failure -> SUCCESS",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "minimum-review/default_reviewers", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			[]string{"minimum-review/default_reviewers"},
			"SUCCESS",
		},
		{
			"ignored + real failure -> FAILURE",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"},
				{Name: "minimum-review/default_reviewers", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			[]string{"minimum-review/default_reviewers"},
			"FAILURE",
		},
		{
			"glob wildcard matches",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "minimum-review/default_reviewers", Status: "COMPLETED", Conclusion: "FAILURE"},
				{Name: "minimum-review/codeowners", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			[]string{"minimum-review/*"},
			"SUCCESS",
		},
		{
			"non-matching pattern leaves failure",
			[]statusCheckEntry{
				{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			[]string{"unrelated/*"},
			"FAILURE",
		},
		{
			"bad glob is skipped, others still apply",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "noisy", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			[]string{"[", "noisy"},
			"SUCCESS",
		},
		{
			"ignored pending check ignored",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "noisy", Status: "IN_PROGRESS", Conclusion: ""},
			},
			[]string{"noisy"},
			"SUCCESS",
		},
		{
			// Re-run in place: a check failed, then a later run of the same
			// check passed. Only the latest run should count.
			"rerun supersedes stale failure with success",
			[]statusCheckEntry{
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: "2026-06-03T13:12:50Z"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-06-03T14:26:09Z"},
			},
			nil,
			"SUCCESS",
		},
		{
			"rerun supersedes stale success with failure",
			[]statusCheckEntry{
				{Name: "validate", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-06-03T13:12:50Z"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: "2026-06-03T14:26:09Z"},
			},
			nil,
			"FAILURE",
		},
		{
			"rerun latest run still in progress -> pending",
			[]statusCheckEntry{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: "2026-06-03T13:00:00Z"},
				{Name: "build", Status: "IN_PROGRESS", Conclusion: "", StartedAt: "2026-06-03T14:00:00Z"},
			},
			nil,
			"PENDING",
		},
		{
			// Real PR #3510 shape: validate failed twice, was cancelled, then
			// passed — all on the same commit. Latest run is SUCCESS.
			"multiple reruns, latest success",
			[]statusCheckEntry{
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: "2026-06-03T13:12:50Z"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE", StartedAt: "2026-06-03T13:14:51Z"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "CANCELLED", StartedAt: "2026-06-03T14:25:53Z"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-06-03T14:26:09Z"},
				{Name: "secret-scan", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-06-03T14:26:00Z"},
			},
			nil,
			"SUCCESS",
		},
		{
			// No timestamps (e.g. older API shape): fall back to rollup order,
			// last entry per name wins.
			"no startedAt falls back to last-seen success",
			[]statusCheckEntry{
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE"},
				{Name: "validate", Status: "COMPLETED", Conclusion: "SUCCESS"},
			},
			nil,
			"SUCCESS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveCIStatus(tt.checks, tt.ignore)
			if got != tt.want {
				t.Errorf("deriveCIStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// jobURL is the details URL shape GitHub gives a check run. Run identity comes
// from here, so every fixture below must carry one to mean anything.
func jobURL(run, job string) string {
	return "https://github.com/brizzai/fleet/actions/runs/" + run + "/job/" + job
}

// Run ids of the two real runs this was found on: a push run whose gate went red
// before the suites were triggered, then the run a /e2e comment started.
const (
	pushRun    = "30754985996"
	labeledRun = "30755192426"
)

// A gate job that publishes its verdict before the suites it gates are
// triggered goes red, then goes green in a later run of the same workflow.
// Between the two, the stale red must not read as "this needs you" — the
// workflow is already recomputing it.
//
// The timestamps below are deliberately arranged so that comparing StartedAt
// instead of run id would give the wrong answer in both directions.
func TestDeriveCIStatusSupersededFailure(t *testing.T) {
	tests := []struct {
		name   string
		checks []statusCheckEntry
		ignore []string
		want   string
	}{
		{
			"stale gate failure while a later run of its workflow is going",
			[]statusCheckEntry{
				{Name: "heavy_gate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(pushRun, "91515571601")},
				{Name: "E2E / E2E Tests", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(labeledRun, "91516663251")},
			},
			nil,
			"PENDING",
		},
		{
			// The regression the run id exists for: one run, jobs staggered by
			// `needs:` chains and runner acquisition. A finished failure here is
			// final and actionable no matter what its siblings are still doing.
			"a later-starting sibling of the same run never suppresses",
			[]statusCheckEntry{
				{Name: "golangci-lint", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:44:27Z", WorkflowName: "CI - Merge Gatekeeper",
					DetailsURL: jobURL("30754985868", "91515665758")},
				{Name: "Core Tests", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Merge Gatekeeper",
					DetailsURL: jobURL("30754985868", "91515868760")},
			},
			nil,
			"FAILURE",
		},
		{
			"a failure in the newer run outranks an in-flight job of the older one",
			[]statusCheckEntry{
				{Name: "E2E / E2E Tests", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(labeledRun, "91516663251")},
				{Name: "E2E / Preflight", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(pushRun, "91515571784")},
			},
			nil,
			"FAILURE",
		},
		{
			"another workflow running does not excuse this one's failure",
			[]statusCheckEntry{
				{Name: "heavy_gate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(pushRun, "91515571601")},
				{Name: "Core Tests", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Merge Gatekeeper",
					DetailsURL: jobURL("30754985868", "91515868760")},
			},
			nil,
			"FAILURE",
		},
		{
			// Status contexts carry no workflow and an arbitrary URL, so neither
			// half of the key exists. They must never suppress anything.
			"status contexts share no workflow, so they never suppress",
			[]statusCheckEntry{
				{Name: "validate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z"},
				{Name: "gitStream.cm", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", DetailsURL: "https://docs.gitstream.cm/examples/"},
			},
			nil,
			"FAILURE",
		},
		{
			// An unrecognized URL shape costs a false red, never a missed one.
			"a failure with no parseable run id is never suppressed",
			[]statusCheckEntry{
				{Name: "heavy_gate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: "https://ci.example.com/build/7"},
				{Name: "E2E / E2E Tests", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(labeledRun, "91516663251")},
			},
			nil,
			"FAILURE",
		},
		{
			"suppressed failure does not mask a live one elsewhere",
			[]statusCheckEntry{
				{Name: "heavy_gate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(pushRun, "91515571601")},
				{Name: "E2E / E2E Tests", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(labeledRun, "91516663251")},
				{Name: "Core Tests", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:44:00Z", WorkflowName: "CI - Merge Gatekeeper",
					DetailsURL: jobURL("30754985868", "91515868760")},
			},
			nil,
			"FAILURE",
		},
		{
			"an ignored in-flight check cannot suppress a failure",
			[]statusCheckEntry{
				{Name: "heavy_gate", Status: "COMPLETED", Conclusion: "FAILURE",
					StartedAt: "2026-08-02T15:45:07Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(pushRun, "91515571601")},
				{Name: "E2E / Flaky", Status: "IN_PROGRESS",
					StartedAt: "2026-08-02T15:49:12Z", WorkflowName: "CI - Heavy Tests",
					DetailsURL: jobURL(labeledRun, "91516663252")},
			},
			[]string{"E2E / Flaky"},
			"FAILURE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveCIStatus(tt.checks, tt.ignore); got != tt.want {
				t.Errorf("deriveCIStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// FailedChecks names exactly the checks behind a FAILURE — sorted, with ignored
// and superseded failures left out, the same set the status counted.
func TestSummarizeChecksNamesFailures(t *testing.T) {
	checks := []statusCheckEntry{
		{Name: "lint", Status: "COMPLETED", Conclusion: "FAILURE"},
		{Name: "build", Status: "COMPLETED", Conclusion: "TIMED_OUT"},
		{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "noisy-gate", Status: "COMPLETED", Conclusion: "FAILURE"},
		// Superseded: a later run of the same workflow is still going.
		{Name: "verdict", Status: "COMPLETED", Conclusion: "FAILURE", WorkflowName: "ci",
			DetailsURL: "https://github.com/o/r/actions/runs/100/job/1"},
		{Name: "suite", Status: "IN_PROGRESS", WorkflowName: "ci",
			DetailsURL: "https://github.com/o/r/actions/runs/200/job/2"},
	}
	status, failed := summarizeChecks(checks, []string{"noisy-*"})
	if status != "FAILURE" {
		t.Fatalf("status = %q", status)
	}
	if len(failed) != 2 || failed[0] != "build" || failed[1] != "lint" {
		t.Errorf("failed = %v, want [build lint]", failed)
	}

	status, failed = summarizeChecks(checks[2:3], nil)
	if status != "SUCCESS" || failed != nil {
		t.Errorf("all green: %q %v", status, failed)
	}
}

// A StatusContext row (the commit-status API: policy-bot, external CI) names
// itself with context/state, not name/status/conclusion. Reading only the
// CheckRun fields dropped these rows as nameless ghosts, so a pending
// approval-policy bot left a BLOCKED PR reading green (astelia-pipelines#8487)
// and a failed external CI could never turn the badge red.
func TestDeriveCIStatusCountsStatusContexts(t *testing.T) {
	green := statusCheckEntry{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}
	tests := []struct {
		name   string
		checks []statusCheckEntry
		ignore []string
		want   string
	}{
		{"pending context holds an otherwise green rollup",
			[]statusCheckEntry{green, {Context: "policy-bot: main", State: "PENDING"}}, nil, "PENDING"},
		{"expected context is a required status nobody posted yet",
			[]statusCheckEntry{green, {Context: "ci/jenkins", State: "EXPECTED"}}, nil, "PENDING"},
		{"failed context turns it red",
			[]statusCheckEntry{green, {Context: "ci/jenkins", State: "FAILURE"}}, nil, "FAILURE"},
		{"errored context turns it red",
			[]statusCheckEntry{green, {Context: "ci/jenkins", State: "ERROR"}}, nil, "FAILURE"},
		{"successful context stays green",
			[]statusCheckEntry{green, {Context: "policy-bot: main", State: "SUCCESS"}}, nil, "SUCCESS"},
		{"state is matched case-insensitively",
			[]statusCheckEntry{green, {Context: "ci/jenkins", State: "failure"}}, nil, "FAILURE"},
		{"pr_checks.ignore matches a context by its name",
			[]statusCheckEntry{green, {Context: "policy-bot: main", State: "PENDING"}}, []string{"policy-bot*"}, "SUCCESS"},
		{"a row with neither name nor context is still a ghost",
			[]statusCheckEntry{green, {State: "FAILURE"}}, nil, "SUCCESS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveCIStatus(tt.checks, tt.ignore); got != tt.want {
				t.Errorf("deriveCIStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

// The rollup is decoded straight from gh's JSON, so pin the field names
// against the shape gh actually emits for a StatusContext.
func TestStatusContextDecodesFromGhJSON(t *testing.T) {
	raw := `{"mergeStateStatus":"BLOCKED","statusCheckRollup":[
		{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"StatusContext","context":"policy-bot: main","startedAt":"2026-11-22T14:15:35Z","state":"PENDING","targetUrl":"https://policy-bot.example/details/o/r/1"}]}`
	var resp ghPRResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.MergeStateStatus != "BLOCKED" {
		t.Errorf("MergeStateStatus = %q, want BLOCKED", resp.MergeStateStatus)
	}
	if got := deriveCIStatus(resp.StatusCheckRollup, nil); got != "PENDING" {
		t.Errorf("deriveCIStatus = %q, want PENDING", got)
	}
}

func TestReadyToMergeRespectsMergeState(t *testing.T) {
	for _, tt := range []struct {
		state string
		want  bool
	}{
		{"CLEAN", true},
		{"HAS_HOOKS", true},
		{"UNSTABLE", true}, // only non-required checks failing; GitHub merges it
		{"UNKNOWN", true},  // computed lazily: a cold read must not flicker green off
		{"", true},         // older gh / field absent
		{"BLOCKED", false},
		{"BEHIND", false},
	} {
		pr := &PR{State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS", MergeStateStatus: tt.state}
		if got := pr.ReadyToMerge(); got != tt.want {
			t.Errorf("ReadyToMerge with %q = %v, want %v", tt.state, got, tt.want)
		}
	}
}

// ReadyToMerge stands on its own: an otherwise green PR that is a draft, has
// conflicts, or is no longer open is never ready, whatever the caller checked
// first.
func TestReadyToMergeIsSelfContained(t *testing.T) {
	ready := func() *PR {
		return &PR{State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS", MergeStateStatus: "CLEAN"}
	}
	if !ready().ReadyToMerge() {
		t.Fatal("baseline PR should be ready")
	}
	for name, mutate := range map[string]func(*PR){
		"draft":        func(p *PR) { p.IsDraft = true },
		"conflicts":    func(p *PR) { p.HasConflicts = true },
		"merged":       func(p *PR) { p.State = "MERGED" },
		"closed":       func(p *PR) { p.State = "CLOSED" },
		"no CI":        func(p *PR) { p.CIStatus = "" },
		"not approved": func(p *PR) { p.ReviewDecision = "REVIEW_REQUIRED" },
	} {
		p := ready()
		mutate(p)
		if p.ReadyToMerge() {
			t.Errorf("%s: ReadyToMerge = true, want false", name)
		}
	}
}

// A commit status and a check run can share a name and are still separate
// results. Deduped by name alone, whichever started later replaced the other,
// so a failed status hid behind a passing check (and the reverse) in either
// rollup order.
func TestLatestRunKeepsStatusAndCheckOfTheSameNameApart(t *testing.T) {
	check := func(conclusion, at string) statusCheckEntry {
		return statusCheckEntry{Name: "build", Status: "COMPLETED", Conclusion: conclusion, StartedAt: at}
	}
	status := func(state, at string) statusCheckEntry {
		return statusCheckEntry{Context: "build", State: state, StartedAt: at}
	}
	tests := []struct {
		name   string
		checks []statusCheckEntry
		want   string
	}{
		{"failed status, newer passing check",
			[]statusCheckEntry{status("FAILURE", "2026-11-22T10:00:00Z"), check("SUCCESS", "2026-11-22T11:00:00Z")}, "FAILURE"},
		{"failed status with no timestamp, passing check first",
			[]statusCheckEntry{check("SUCCESS", "2026-11-22T11:00:00Z"), status("FAILURE", "")}, "FAILURE"},
		{"failed check, newer passing status",
			[]statusCheckEntry{check("FAILURE", "2026-11-22T10:00:00Z"), status("SUCCESS", "2026-11-22T11:00:00Z")}, "FAILURE"},
		{"pending status beside a passing check",
			[]statusCheckEntry{check("SUCCESS", "2026-11-22T11:00:00Z"), status("PENDING", "2026-11-22T10:00:00Z")}, "PENDING"},
		{"both kinds passing",
			[]statusCheckEntry{check("SUCCESS", "2026-11-22T11:00:00Z"), status("SUCCESS", "2026-11-22T10:00:00Z")}, "SUCCESS"},
		// Same kind still collapses to the newest run, as before.
		{"check rerun green still hides its older failure",
			[]statusCheckEntry{check("FAILURE", "2026-11-22T10:00:00Z"), check("SUCCESS", "2026-11-22T11:00:00Z")}, "SUCCESS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveCIStatus(tt.checks, nil); got != tt.want {
				t.Errorf("deriveCIStatus = %q, want %q", got, tt.want)
			}
		})
	}
	// Ignore matching still goes by the plain name: one glob covers both kinds.
	both := []statusCheckEntry{check("FAILURE", "2026-11-22T10:00:00Z"), status("FAILURE", "2026-11-22T11:00:00Z")}
	if got := deriveCIStatus(append(both, statusCheckEntry{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"}), []string{"build"}); got != "SUCCESS" {
		t.Errorf("ignore \"build\" should drop both kinds, got %q", got)
	}
}
