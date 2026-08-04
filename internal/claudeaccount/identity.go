package claudeaccount

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Identity naming, and the line this file deliberately does not cross.
//
// A `claude setup-token` credential cannot tell you who it belongs to. That is
// intentional — Anthropic's own wording is that long-lived tokens are
// "limited to inference-only for security reasons" — and fleet does not try to
// defeat it. There is no scope trick here and no attempt at one.
//
// What fleet does instead is entirely local. Claude Code caches the profile of
// whatever account is logged in on this machine, in ~/.claude.json. If a
// token's organization (which the API volunteers in a response header) matches
// that cached organization, then the token belongs to the account whose email
// is already sitting in the user's own file — and telling them so reveals
// nothing they don't have.
//
// The distinction that matters: the scope limit exists so a *leaked* token
// can't identify its owner to whoever holds it. This match only works for
// someone already reading the user's home directory, where the email is in
// plain text regardless. It buys convenience on the user's own machine and
// grants nothing to anyone else. Every other account still gets named by hand.

// LocalProfile is Claude Code's cached record of the account logged in on this
// machine, read from ~/.claude.json.
type LocalProfile struct {
	Email   string
	OrgUUID string
	OrgName string
}

type localConfig struct {
	OAuthAccount struct {
		EmailAddress     string `json:"emailAddress"`
		OrganizationUUID string `json:"organizationUuid"`
		OrganizationName string `json:"organizationName"`
	} `json:"oauthAccount"`
}

// LocalIdentity returns the cached profile of the ambient login, if any.
//
// Best-effort by design: a missing or unparseable file simply means fleet
// cannot name a token automatically, which is the normal case for any account
// other than the one currently logged in.
func LocalIdentity() (LocalProfile, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return LocalProfile{}, false
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return LocalProfile{}, false
	}
	var c localConfig
	if err := json.Unmarshal(data, &c); err != nil {
		debuglog.Logger.Debug("could not parse ~/.claude.json for identity", "err", err)
		return LocalProfile{}, false
	}
	a := c.OAuthAccount
	if a.EmailAddress == "" || a.OrganizationUUID == "" {
		return LocalProfile{}, false
	}
	return LocalProfile{Email: a.EmailAddress, OrgUUID: a.OrganizationUUID, OrgName: a.OrganizationName}, true
}

// NameForOrg returns the email for a token's organization, when that
// organization is the one logged in on this machine. Empty otherwise — and
// empty is the expected answer for a second, different subscription.
func NameForOrg(orgUUID string) string {
	if orgUUID == "" {
		return ""
	}
	p, ok := LocalIdentity()
	if !ok || p.OrgUUID != orgUUID {
		return ""
	}
	return p.Email
}
