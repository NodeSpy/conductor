package engine

import (
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// LEGACY — removed with the legacy `integrations:` layer (plugin-contract.md
// §5, step C). A trigger from a legacy integration has no connector instance
// to declare credentials, so it gets the identity model the bundled github
// integration had: the daemon-wide write token as GH_TOKEN, the read token
// (the event's App token, or the identity.read_token override) as
// PC_GH_APP_TOKEN, and the guidance that says which is which.
const (
	legacyWriteEnv = "PC_GH_WRITE_TOKEN"
	legacyAppEnv   = "PC_GH_APP_TOKEN"
)

const legacyIdentityGuidance = "\n\n---\n" +
	"IDENTITY: you act as ME. GH_TOKEN/GITHUB_TOKEN are MY token, so every comment, " +
	"review, reply, and `gh`/API write is attributed to me — and commits and `git push` " +
	"go over SSH as me. NEVER post, submit, approve, or otherwise write anything with the " +
	"App/bot token. If a large read would burn my rate limit you MAY read (only) with the " +
	"App token via `GH_TOKEN=$" + legacyAppEnv + " gh ...`, but never write with it."

func (e *Engine) legacyCredentials(t core.Trigger) dispatch.Credentials {
	appTok, _ := t.Context["app_token"].(string)
	if appTok == "" && e.refreshTok != nil {
		appTok, _ = e.refreshTok(t)
	}
	if e.readTok != nil {
		if tok, err := e.readTok(); err == nil && tok != "" {
			appTok = tok
		}
	}
	userTok := ""
	if e.userTok != nil {
		userTok, _ = e.userTok()
	}
	return dispatch.Credentials{
		Env: map[string]string{
			"GH_TOKEN": userTok, "GITHUB_TOKEN": userTok, legacyWriteEnv: userTok, legacyAppEnv: appTok,
		},
		Templates: map[string]string{"app_token": appTok, "gh_token": userTok},
		Guidance:  legacyIdentityGuidance,
	}
}
