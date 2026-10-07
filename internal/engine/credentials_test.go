package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// What an agent receives is what its event's connector DECLARES: each
// credential minted through the declared host-only verb with the declared
// args, under the declared env names and template key, plus its guidance —
// and a target the platform did not assign gets nothing: not minted, and not
// the event's own fact of the same name either.
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
	c, _ := e.declaredCredentials(context.Background(), tr, "acme1", sem, false)
	if c.Env["ACME_TOKEN"] != "tok-mint_w" || c.Env["ACME_TOKEN_ALIAS"] != "tok-mint_w" || c.Env["ACME_READ"] != "tok-mint_r" {
		t.Fatalf("env = %v", c.Env)
	}
	if c.Templates["acme_token"] != "tok-mint_w" || c.Guidance != "|use ACME_TOKEN|" {
		t.Fatalf("templates=%v guidance=%q", c.Templates, c.Guidance)
	}
	if len(minted) != 2 || minted[0] != "acme1.mint_w:p1" {
		t.Fatalf("minted = %v", minted)
	}
	// An unassigned target gets nothing — not even a token-named fact the
	// event carries.
	minted = nil
	forged := core.Trigger{Instance: "acme1", Context: map[string]any{"project": "p1", "acme_read": "from-the-event"}}
	c, _ = e.declaredCredentials(context.Background(), forged, "acme1", sem, false)
	if len(minted) != 0 || len(c.Env) != 0 || len(c.Templates) != 0 || c.Guidance != "" {
		t.Fatalf("forged target: minted=%v creds=%+v", minted, c)
	}
	// An assigned target whose mint yields nothing still uses the fact its
	// source stamped.
	stamped := core.Trigger{Instance: "acme1", TargetTrusted: true, Context: map[string]any{"project": "p1", "acme_read": "stamped"}}
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}
	if c, _ = e.declaredCredentials(context.Background(), stamped, "acme1", sem, false); c.Env["ACME_READ"] != "stamped" {
		t.Fatalf("assigned target, stamped fact: env=%v", c.Env)
	}
}

// A conductor.* lifecycle event's work gets the credentials of the connector
// its originating trigger came from (origin_instance), minted for the same
// target only when the platform assigned it; an unknown instance gets none —
// there is no daemon-wide identity to fall back to.
func TestLifecycleWorkGetsTheOriginConnectorsCredentials(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	life := func(origin string, trusted bool) core.Trigger {
		return core.Trigger{Source: "conductor", Instance: "conductor", Kind: "escalate", TargetTrusted: trusted,
			Target:  core.Target{Repo: "a/w", Number: 1},
			Context: map[string]any{"repo": "a/w", "origin_instance": origin}}
	}
	if c, _ := e.credentialsFor(context.Background(), life("i", true)); c.Env["GH_TOKEN"] != "utok" || c.Env["PC_GH_APP_TOKEN"] != "atok" {
		t.Fatalf("lifecycle work for the forge's target: env = %v", c.Env)
	}
	if c, _ := e.credentialsFor(context.Background(), life("i", false)); c.Env["GH_TOKEN"] != "" {
		t.Fatalf("an unassigned origin target mints nothing: env = %v", c.Env)
	}
	if c, _ := e.credentialsFor(context.Background(), life("nope", true)); len(c.Env) != 0 {
		t.Fatalf("an unknown origin instance gets no credentials: env = %v", c.Env)
	}
	if c, _ := e.credentialsFor(context.Background(), core.Trigger{Source: "conductor", Instance: "conductor", Kind: "boot", TargetTrusted: true}); len(c.Env) != 0 {
		t.Fatalf("a lifecycle event with no origin gets no credentials: env = %v", c.Env)
	}
}

// A declared credential that cannot be minted (the plugin is down) is an
// error, never a silent dispatch without it — unless a fact the source
// stamped stands in.
func TestFailedMintIsAnError(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("plugin not running")
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{{Name: "w", Role: "write",
		Mint: sdk.CredentialMint{Verb: "mint_w"}, Env: []string{"ACME_TOKEN"}, Template: "acme_token"}}}
	tr := core.Trigger{Instance: "acme1", TargetTrusted: true, Context: map[string]any{}}
	if c, err := e.declaredCredentials(context.Background(), tr, "acme1", sem, false); err == nil || len(c.Env) != 0 {
		t.Fatalf("failed mint: creds=%+v err=%v", c, err)
	}
	tr.Context["acme_token"] = "stamped"
	if c, err := e.declaredCredentials(context.Background(), tr, "acme1", sem, false); err != nil || c.Env["ACME_TOKEN"] != "stamped" {
		t.Fatalf("a stamped fact stands in: creds=%+v err=%v", c, err)
	}
}

// A mint verb that answers without the declared value is a failure too, not
// a credential silently dropped.
func TestEmptyMintIsAnError(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{{Name: "w", Role: "write",
		Mint: sdk.CredentialMint{Verb: "mint_w"}, Env: []string{"ACME_TOKEN"}}}}
	if c, err := e.declaredCredentials(context.Background(), core.Trigger{Instance: "acme1", TargetTrusted: true}, "acme1", sem, false); err == nil || len(c.Env) != 0 {
		t.Fatalf("an empty mint: creds=%+v err=%v", c, err)
	}
}
