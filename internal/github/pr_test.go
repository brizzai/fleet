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
