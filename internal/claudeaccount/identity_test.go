package claudeaccount

import (
	"errors"
	"testing"
)

// `claude auth status` exits 1 when logged out while still printing perfectly
// good JSON. Treating a non-zero exit as failure threw that answer away and
// reported the ordinary "needs logging in" case as "fleet could not run claude"
// — which is not actionable, and which Select must not confuse with a broken
// install.
func TestNotLoggedInIsReadFromTheBodyNotTheExitCode(t *testing.T) {
	id, err := parseAuthStatus([]byte(`{"loggedIn":false,"authMethod":"none"}`), errors.New("exit status 1"))
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
	if id.LoggedIn {
		t.Error("reported as logged in")
	}
}

func TestLoggedInIdentityIsReadInFull(t *testing.T) {
	id, err := parseAuthStatus([]byte(`{
		"loggedIn":true,"authMethod":"claude.ai","email":"a@x.com",
		"orgId":"org-1","orgName":"A Org","subscriptionType":"max"}`), nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if id.Email != "a@x.com" || id.OrgUUID != "org-1" || id.Plan != "max" || id.OrgName != "A Org" {
		t.Errorf("identity = %+v, want every field populated", id)
	}
}

// Unparseable output means fleet could not get an answer at all. That is fleet's
// problem, not a verdict on the account, and it must stay distinguishable —
// otherwise a broken install would silently empty the candidate list.
func TestUnparseableOutputIsNotALoggedOutVerdict(t *testing.T) {
	_, err := parseAuthStatus([]byte("command not found"), errors.New("exit status 127"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrNotLoggedIn) {
		t.Error("a failure to run claude was reported as the account being logged out")
	}
}
