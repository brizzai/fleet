package github

import "testing"

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
