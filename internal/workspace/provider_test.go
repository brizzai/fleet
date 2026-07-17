package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseWorktreePorcelain(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   int // expected count
		checks func(t *testing.T, result []WorkspaceInfo)
	}{
		{
			"single worktree",
			"worktree /code/myrepo\nbranch refs/heads/main\n\n",
			1,
			func(t *testing.T, result []WorkspaceInfo) {
				if result[0].Path != "/code/myrepo" {
					t.Errorf("path = %q, want /code/myrepo", result[0].Path)
				}
				if result[0].Branch != "main" {
					t.Errorf("branch = %q, want main", result[0].Branch)
				}
				if result[0].Name != "myrepo" {
					t.Errorf("name = %q, want myrepo", result[0].Name)
				}
			},
		},
		{
			"multiple worktrees",
			"worktree /code/myrepo\nbranch refs/heads/main\n\nworktree /code/myrepo-feat\nbranch refs/heads/feat\n\n",
			2,
			func(t *testing.T, result []WorkspaceInfo) {
				if result[0].Branch != "main" {
					t.Errorf("first branch = %q, want main", result[0].Branch)
				}
				if result[1].Branch != "feat" {
					t.Errorf("second branch = %q, want feat", result[1].Branch)
				}
			},
		},
		{
			"no branch (detached HEAD)",
			"worktree /code/myrepo\nHEAD abc123\ndetached\n\n",
			1,
			func(t *testing.T, result []WorkspaceInfo) {
				if result[0].Branch != "" {
					t.Errorf("branch = %q, want empty", result[0].Branch)
				}
			},
		},
		{"empty input", "", 0, nil},
		{"whitespace only", "\n\n", 0, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseWorktreePorcelain(tt.input)
			if len(result) != tt.want {
				t.Fatalf("len(result) = %d, want %d", len(result), tt.want)
			}
			if tt.checks != nil {
				tt.checks(t, result)
			}
		})
	}
}

func TestSanitizeBranchInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"space becomes dash", "my branch", "my-branch"},
		{"multiple spaces", "a b c d", "a-b-c-d"},
		{"strip tilde", "feat~bad", "featbad"},
		{"strip caret", "feat^bad", "featbad"},
		{"strip colon", "feat:bad", "featbad"},
		{"strip question", "feat?bad", "featbad"},
		{"strip star", "feat*bad", "featbad"},
		{"strip bracket", "feat[bad", "featbad"},
		{"strip backslash", "feat\\bad", "featbad"},
		{"strip backtick", "feat`bad", "featbad"},
		{"strip tab control", "feat\tbad", "featbad"},
		{"strip del control", "feat\x7fbad", "featbad"},
		{"keep slash", "feature/login", "feature/login"},
		{"keep dots", "v1.2.3", "v1.2.3"},
		{"keep underscore", "fix_bug_123", "fix_bug_123"},
		{"empty stays empty", "", ""},
		{"mixed", "my cool~branch/v2 ", "my-coolbranch/v2-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeBranchInput(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBranchInput(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// Idempotence.
			if twice := SanitizeBranchInput(got); twice != got {
				t.Errorf("SanitizeBranchInput not idempotent: %q -> %q -> %q", tt.input, got, twice)
			}
		})
	}
}

func TestSanitizeBranchInputWithCursor(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		cursor     int
		wantOut    string
		wantCursor int
	}{
		{"no change", "feat", 2, "feat", 2},
		{"space before cursor replaced (cursor unchanged)", "a b", 3, "a-b", 3},
		{"drop char before cursor", "a~bc", 4, "abc", 3},
		{"drop char after cursor", "ab~c", 2, "abc", 2},
		{"drop char at cursor", "ab~c", 3, "abc", 2},
		{"multiple drops before cursor", "~a~b~c", 6, "abc", 3},
		{"cursor past end clamps", "abc", 10, "abc", 3},
		{"negative cursor clamps to 0", "~abc", -1, "abc", 0},
		{"empty string", "", 0, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOut, gotCursor := SanitizeBranchInputWithCursor(tt.input, tt.cursor)
			if gotOut != tt.wantOut {
				t.Errorf("out = %q, want %q", gotOut, tt.wantOut)
			}
			if gotCursor != tt.wantCursor {
				t.Errorf("cursor = %d, want %d", gotCursor, tt.wantCursor)
			}
		})
	}
}

func TestValidateBranchName(t *testing.T) {
	invalid := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"at sign alone", "@"},
		{"leading dash", "-bad"},
		{"leading slash", "/bad"},
		{"leading dot", ".bad"},
		{"component leading dot", "feature/.bad"},
		{"trailing dot", "bad."},
		{"component trailing dot", "a./b"},
		{"trailing slash", "bad/"},
		{"trailing .lock", "feature.lock"},
		{"component .lock", "a.lock/b"},
		{"double dot", "a..b"},
		{"at brace", "a@{b"},
		{"double slash", "a//b"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			if got := ValidateBranchName(tt.input); got == "" {
				t.Errorf("ValidateBranchName(%q) = \"\", want error message", tt.input)
			}
		})
	}

	valid := []string{
		"feature-login",
		"feature/login",
		"fix-bug-123",
		"v1.2.3",
		"release/2026-04-16",
		"a",
	}
	for _, v := range valid {
		t.Run("valid/"+v, func(t *testing.T) {
			if got := ValidateBranchName(v); got != "" {
				t.Errorf("ValidateBranchName(%q) = %q, want \"\"", v, got)
			}
		})
	}
}

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"slashes", "feature/login", "feature-login"},
		{"spaces", "my branch", "my-branch"},
		{"double dots", "v1..v2", "v1-v2"},
		{"clean name", "fix-bug-123", "fix-bug-123"},
		{"multiple slashes", "user/feature/thing", "user-feature-thing"},
		{"mixed", "feat/my branch..v2", "feat-my-branch-v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeBranchName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBranchName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveWorktreePathSiblingDefault(t *testing.T) {
	tests := []struct {
		name     string
		repoPath string
		wtName   string
		wantEnd  string // suffix the result should end with
	}{
		{"basic", "/code/myrepo", "feature-login", "myrepo-feature-login"},
		{"nested repo", "/home/user/projects/app", "hotfix", "app-hotfix"},
		{"whitespace template treated as empty", "/code/myrepo", "x", "myrepo-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := ""
			if tt.name == "whitespace template treated as empty" {
				template = "   "
			}
			got := resolveWorktreePath(tt.repoPath, tt.wtName, template)
			if filepath.Base(got) != tt.wantEnd {
				t.Errorf("resolveWorktreePath(%q, %q) base = %q, want %q", tt.repoPath, tt.wtName, filepath.Base(got), tt.wantEnd)
			}
			// Should be a sibling directory (same parent).
			if filepath.Dir(got) != filepath.Dir(tt.repoPath) {
				t.Errorf("resolveWorktreePath result parent = %q, want %q", filepath.Dir(got), filepath.Dir(tt.repoPath))
			}
		})
	}
}

func TestResolveWorktreePathTemplate(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name     string
		repoPath string
		wtName   string
		template string
		want     string
	}{
		{
			name:     "subfolder",
			repoPath: "/code/myrepo",
			wtName:   "feature-login",
			template: "{{parent}}/{{repo}}.worktrees/{{name}}",
			want:     "/code/myrepo.worktrees/feature-login",
		},
		{
			name:     "absolute central dir",
			repoPath: "/code/myrepo",
			wtName:   "hotfix",
			template: "/var/wt/{{repo}}/{{name}}",
			want:     "/var/wt/myrepo/hotfix",
		},
		{
			name:     "tilde expansion",
			repoPath: "/code/myrepo",
			wtName:   "hotfix",
			template: "~/worktrees/{{repo}}/{{name}}",
			want:     filepath.Join(home, "worktrees", "myrepo", "hotfix"),
		},
		{
			name:     "name only under parent",
			repoPath: "/code/myrepo",
			wtName:   "feat",
			template: "{{parent}}/wt/{{name}}",
			want:     "/code/wt/feat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWorktreePath(tt.repoPath, tt.wtName, tt.template)
			if got != tt.want {
				t.Errorf("resolveWorktreePath(%q, %q, %q) = %q, want %q", tt.repoPath, tt.wtName, tt.template, got, tt.want)
			}
		})
	}
}
