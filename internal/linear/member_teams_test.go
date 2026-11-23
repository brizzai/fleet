package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// TestTeamKeysDefaultsToYourTeams pins issue #373: a connected user's own teams
// are on in a repo that names none, without a per-repo file.
//
// Each override is checked against that default, because the regression worth
// catching is an override that silently stops overriding: a repo naming its team
// must get exactly that team (not the union), and a repo switched off must stay
// off even though a default exists to fall back to.
func TestTeamKeysDefaultsToYourTeams(t *testing.T) {
	SetAccountForTest(ticket.Account{Name: "Astelia", Keys: []string{"MAGMA", "OPS", "PLAT"}, Mine: []string{"MAGMA", "PLAT"}})
	t.Cleanup(func() { SetAccountForTest(ticket.Account{}) })

	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bare := t.TempDir()
	if got := TeamKeys(bare); !slices.Equal(got, []string{"MAGMA", "PLAT"}) {
		t.Errorf("a repo naming no team must track your teams, got %v", got)
	}

	// Your teams, never the whole workspace: OPS exists, you are not on it.
	if slices.Contains(TeamKeys(bare), "OPS") {
		t.Error("the default must be your memberships, not every team in the workspace")
	}

	named := t.TempDir()
	write(named, ".fleet.json", `{"linear":{"team":"ops"}}`)
	if got := TeamKeys(named); !slices.Equal(got, []string{"OPS"}) {
		t.Errorf("a repo naming its own team must track exactly that, got %v", got)
	}

	off := t.TempDir()
	write(off, ".fleet.local.json", `{"linear":{"enabled":false}}`)
	if got := TeamKeys(off); got != nil {
		t.Errorf(`{"linear":{"enabled":false}} must switch the repo off, got %v`, got)
	}

	// Local enabled:true re-enables a repo the committed file switched off.
	reOn := t.TempDir()
	write(reOn, ".fleet.json", `{"linear":{"enabled":false}}`)
	write(reOn, ".fleet.local.json", `{"linear":{"enabled":true}}`)
	if got := TeamKeys(reOn); !slices.Equal(got, []string{"MAGMA", "PLAT"}) {
		t.Errorf("a local enabled:true must override a committed false, got %v", got)
	}

	// A local team re-enables a repo the committed file switched off: naming
	// your team is asking for Linear there.
	localTeam := t.TempDir()
	write(localTeam, ".fleet.json", `{"linear":{"enabled":false}}`)
	write(localTeam, ".fleet.local.json", `{"linear":{"team":"ops"}}`)
	if got := TeamKeys(localTeam); !slices.Equal(got, []string{"OPS"}) {
		t.Errorf("a local team must re-enable a committed enabled:false, got %v", got)
	}

	// A defaulted key yields to a Jira project the repo names explicitly, or
	// the Linear-first registry would send that project's tickets to Linear.
	jiraRepo := t.TempDir()
	write(jiraRepo, ".fleet.json", `{"jira":{"project":"PLAT"}}`)
	if got := TeamKeys(jiraRepo); !slices.Equal(got, []string{"MAGMA"}) {
		t.Errorf("a defaulted key must yield to an explicit Jira project, got %v", got)
	}

	// Disconnected (or before the startup read lands): quiet, not wrong.
	SetAccountForTest(ticket.Account{})
	if got := TeamKeys(bare); got != nil {
		t.Errorf("with no account reading the default must be empty, got %v", got)
	}

	// A caller mutating the returned slice must not edit the cache.
	SetAccountForTest(ticket.Account{Name: "Astelia", Mine: []string{"MAGMA"}})
	TeamKeys(bare)[0] = "XXX"
	if got := TeamKeys(bare); !slices.Equal(got, []string{"MAGMA"}) {
		t.Errorf("TeamKeys must return a copy of the cached teams, got %v", got)
	}
}

// TestAccountReadsYourTeams pins the decode of the viewer's memberships off the
// same query that reads the workspace, so verifying a credential yields them
// without a second round trip.
func TestAccountReadsYourTeams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		if !strings.Contains(req.Query, "viewer") {
			t.Errorf("the workspace query must ask for the viewer's teams:\n%s", req.Query)
		}
		_, _ = io.WriteString(w, `{"data":{
			"organization":{"name":"Astelia"},
			"teams":{"nodes":[{"key":"plat","name":"Platform"},{"key":"MAGMA","name":"Magma"},{"key":"OPS","name":"Ops"}]},
			"viewer":{"teams":{"nodes":[{"key":"plat"},{"key":"MAGMA"}]}}
		}}`)
	}))
	t.Cleanup(srv.Close)
	prev := apiEndpointVar
	apiEndpointVar = srv.URL
	t.Cleanup(func() { apiEndpointVar = prev })

	ws, err := VerifyCredential(context.Background(), Credential{Kind: KindAPIKey, Token: "lin_api_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ws.Keys, []string{"MAGMA", "OPS", "PLAT"}) {
		t.Errorf("Keys = %v", ws.Keys)
	}
	if !slices.Equal(ws.Mine, []string{"MAGMA", "PLAT"}) {
		t.Errorf("Mine = %v, want your teams upper-cased and sorted", ws.Mine)
	}
}
