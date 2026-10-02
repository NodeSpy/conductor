package engine

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// What an agent receives is what its event's connector DECLARES: each
// credential minted through the declared host-only verb with the declared
// args, under the declared env names and template key, plus its guidance —
// and nothing is minted for a target the platform did not assign (an event's
// own fact is still used).
func TestDeclaredCredentials(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	var minted []string
	e.invokeVerb = func(_ context.Context, inst, verb string, opts map[string]any) (map[string]any, error) {
		minted = append(minted, inst+"."+verb+":"+opts["scope"].(string))
		return map[string]any{"secret": "tok-" + verb}, nil
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{
		{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "mint_w", Args: map[string]string{"scope": "{{.project}}"}},
			Value: "secret", Env: []string{"ACME_TOKEN", "ACME_TOKEN_ALIAS"}, Template: "acme_token", Guidance: "|use ACME_TOKEN|"},
		{Name: "r", Role: "read", Mint: sdk.CredentialMint{Verb: "mint_r", Args: map[string]string{"scope": "{{.project}}"}},
			Value: "secret", Env: []string{"ACME_READ"}, Template: "acme_read"},
	}}
	tr := core.Trigger{Instance: "acme1", TargetTrusted: true, Context: map[string]any{"project": "p1"}}
	c := e.declaredCredentials(context.Background(), tr, sem)
	if c.Env["ACME_TOKEN"] != "tok-mint_w" || c.Env["ACME_TOKEN_ALIAS"] != "tok-mint_w" || c.Env["ACME_READ"] != "tok-mint_r" {
		t.Fatalf("env = %v", c.Env)
	}
	if c.Templates["acme_token"] != "tok-mint_w" || c.Guidance != "|use ACME_TOKEN|" {
		t.Fatalf("templates=%v guidance=%q", c.Templates, c.Guidance)
	}
	if len(minted) != 2 || minted[0] != "acme1.mint_w:p1" {
		t.Fatalf("minted = %v", minted)
	}
	// An unassigned target mints nothing; a fact the source stamped is used.
	minted = nil
	forged := core.Trigger{Instance: "acme1", Context: map[string]any{"project": "p1", "acme_read": "from-the-event"}}
	c = e.declaredCredentials(context.Background(), forged, sem)
	if len(minted) != 0 || c.Env["ACME_TOKEN"] != "" || c.Env["ACME_READ"] != "from-the-event" {
		t.Fatalf("forged target: minted=%v env=%v", minted, c.Env)
	}
}
