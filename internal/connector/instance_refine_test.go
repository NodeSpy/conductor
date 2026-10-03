package connector

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// fakeInstanceHandler is a minimal contract Handler + InstanceDescriber: its
// type-level Describe() is fixed, and its per-instance decl is whatever the
// test supplies — the same shape rest/graphql/webhook have, driven over the
// REAL in-process wire (plugin.NewClient with Spec.InProcess), so these tests
// exercise the exact code path RegisterExternalConnector/
// RegisterInProcessConnector use, not a shortcut around it.
type fakeInstanceHandler struct {
	typeDecl sdk.Decl
	instance func(instance string, cfg map[string]any) sdk.Decl
}

func (f *fakeInstanceHandler) Describe() sdk.Decl { return f.typeDecl }
func (f *fakeInstanceHandler) Invoke(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{Outputs: map[string]any{}}, nil
}
func (f *fakeInstanceHandler) DescribeInstance(_ context.Context, instance string, cfg map[string]any) (sdk.Decl, error) {
	d := f.instance(instance, cfg)
	if d.Type == "" {
		d.Type = f.typeDecl.Type // the identity anti-forgery check requires it match
	}
	return d, nil
}

// buildFakeInstanceConnector registers h as an EXTERNAL connector (the
// RegisterExternalConnector path a spawned plugin takes — plugin.NewClient
// with an in-process handler drives the identical wire/validation code a
// real subprocess would, just without paying for one) under typ, builds one
// instance named "inst", and returns it. The type is unregistered on
// cleanup.
func buildFakeInstanceConnector(t *testing.T, typ string, h *fakeInstanceHandler) *Instance {
	t.Helper()
	h.typeDecl.Type = typ
	spec := plugin.Spec{Name: typ, Kind: plugin.KindConnector, Provides: typ, InProcess: h}
	cl := plugin.NewClient(spec, plugin.Deps{})
	t.Cleanup(func() { _ = cl.Close() })
	decl, err := cl.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	single := func(string) (*plugin.Client, error) { return cl, nil } // a fixed shared client, as every pre-isolation test expects
	if _, err := RegisterExternalConnector(single, spec, decl); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterExternalType(typ) })
	cfg := mustDecodeConfig(t, fmt.Sprintf("connectors:\n  inst:\n    use: %s\n", typ))
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	in, ok := reg.Get("inst")
	if !ok {
		t.Fatal("instance not registered")
	}
	return in
}

// The reviewer's scenario: the type decl declares a host_only
// mints_credential verb; the instance decl redeclares the SAME verb name
// with no semantics at all (an ordinary verb). Before the Q6 refinement
// check, this REPLACED the type decl wholesale — the instance's "mint" verb
// would have been an ordinary, flow-callable verb, handing a step the minted
// credential as an output. It must now disable the connector instead.
func TestInstanceDeclCannotDropHostOnlyMintsCredential(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Verbs: []sdk.Verb{{Name: "mint", Semantics: &sdk.VerbSemantics{
				HostOnly: true, MintsCredential: &sdk.MintsCredential{Credential: "w"},
			}}},
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{Verbs: []sdk.Verb{{Name: "mint"}}} // no semantics: an ordinary verb now
		},
	}
	in := buildFakeInstanceConnector(t, "fakemint", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that drops a type-level host_only mints_credential verb must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "mint") || !strings.Contains(in.DisabledReason, "refinement") {
		t.Fatalf("DisabledReason = %q, want it to name the verb and the refinement rule", in.DisabledReason)
	}
	// The credential must never have become callable: FlowVerb (what a flow
	// step/skill reaches) must not see an unprotected "mint".
	if _, ok := in.Decl.FlowVerb("mint"); ok {
		t.Fatal("the type-level decl must still govern — \"mint\" must never become flow-callable")
	}
}

// An instance decl that adds a brand-new declared credential (connection
// semantics the type decl never had) must be refused — credentials are
// decided once, at install, from the type-level decl.
func TestInstanceDeclCannotAddCredential(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{Verbs: []sdk.Verb{{Name: "mint", Semantics: &sdk.VerbSemantics{
			HostOnly: true, MintsCredential: &sdk.MintsCredential{Credential: "w"},
		}}}},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Verbs: []sdk.Verb{{Name: "mint", Semantics: &sdk.VerbSemantics{
					HostOnly: true, MintsCredential: &sdk.MintsCredential{Credential: "w"},
				}}},
				Semantics: &sdk.ConnSemantics{Credentials: []sdk.Credential{
					{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "mint"}},
				}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakecred", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that adds a declared credential the type decl never had must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "connection semantics") {
		t.Fatalf("DisabledReason = %q, want it to name the connection semantics mismatch", in.DisabledReason)
	}
}

// An instance decl that claims a wider capabilities manifest than the
// type-level one (confinement, decided once at install) must be refused.
func TestInstanceDeclCannotChangeCapabilities(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{Capabilities: sdk.Capabilities{Commands: []string{"safe-tool"}}},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{Capabilities: sdk.Capabilities{Commands: []string{"safe-tool", "curl"}}}
		},
	}
	in := buildFakeInstanceConnector(t, "fakecap", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that widens the capabilities manifest must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "capabilities") {
		t.Fatalf("DisabledReason = %q, want it to name the capabilities mismatch", in.DisabledReason)
	}
}

// A legitimate refinement — identical verb semantics, identical connection
// semantics and capabilities, but a DIFFERENT event per instance (Q6's whole
// point, the shape webhook/rest/graphql actually use) — passes, and the
// instance decl (not the type decl) is what the registry and the connector
// actually use, on the EXTERNAL plugin path (RegisterExternalConnector),
// proving the per-instance override is not silently ignored there (it is
// already exercised for in-process builtins by the rest/graphql/webhook
// integration tests).
func TestInstanceDeclLegitimateRefinementIsUsedOnExternalPath(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Verbs: []sdk.Verb{{Name: "mint", Semantics: &sdk.VerbSemantics{
				HostOnly: true, MintsCredential: &sdk.MintsCredential{Credential: "w"},
			}}},
			Semantics: &sdk.ConnSemantics{Credentials: []sdk.Credential{
				{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "mint"}},
			}},
		},
		instance: func(instance string, _ map[string]any) sdk.Decl {
			return sdk.Decl{
				Verbs: []sdk.Verb{
					{Name: "mint", Semantics: &sdk.VerbSemantics{
						HostOnly: true, MintsCredential: &sdk.MintsCredential{Credential: "w"},
					}},
					{Name: "create_issue"}, // a brand-new, plain verb (no semantics) — fine: the type decl never granted this semantic to anyone, and this verb carries none
				},
				Semantics: &sdk.ConnSemantics{Credentials: []sdk.Credential{
					{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "mint"}},
				}},
				Events: []sdk.Event{{Name: "concrete_event_" + instance, Semantics: &sdk.EventSemantics{
					Target: &sdk.TargetSemantics{Assigned: []byte("true")},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakerefine", h)
	if in.DisabledReason != "" {
		t.Fatalf("a legitimate (event-only + new plain verb) refinement must not disable the connector: %s", in.DisabledReason)
	}
	// The instance decl's event ("concrete_event_inst"), absent from the type
	// decl entirely, must be what the registry sees — proof the override was
	// actually applied, not ignored in favor of the type-level decl.
	if _, ok := in.Decl.Event("concrete_event_inst"); !ok {
		t.Fatalf("the instance's own event must be on the effective decl, got events %+v", in.Decl.Events)
	}
	if _, ok := in.Decl.Verb("create_issue"); !ok {
		t.Fatal("the instance's new plain verb must be on the effective decl")
	}
}

// TestInstanceDeclCannotStripOptionScope is finding 7's regression test: the
// original Q6 refinement check compared only a verb's Semantics block, so an
// instance decl could keep Semantics byte-identical while stripping an
// option's Scope — the tag that gates an agent-authored call's VALUE for
// that option (ScopedOptions). Before the fix, this silently handed an
// agent free-form access to a resource dimension install-time review
// believed was gated.
func TestInstanceDeclCannotStripOptionScope(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Verbs: []sdk.Verb{{Name: "post", Options: sdk.Schema{
				"channel": {Type: "string", Scope: "channel"},
				"text":    {Type: "string"},
			}}},
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Verbs: []sdk.Verb{{Name: "post", Options: sdk.Schema{
					"channel": {Type: "string"}, // scope silently dropped
					"text":    {Type: "string"},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakescopestrip", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that drops a type-level option's scope must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "channel") || !strings.Contains(in.DisabledReason, "scope") {
		t.Fatalf("DisabledReason = %q, want it to name the option and the scope mismatch", in.DisabledReason)
	}
}

// TestInstanceDeclCannotFlipOpenToTrue is finding 7's second regression
// test: Open skips output validation entirely; an instance decl flipping it
// to true on a verb the type decl declared closed (a real Options/Outputs
// schema) must be refused even though VerbSemantics never changed.
func TestInstanceDeclCannotFlipOpenToTrue(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Verbs: []sdk.Verb{{Name: "post", Open: false, Options: sdk.Schema{
				"text": {Type: "string", Required: true},
			}}},
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Verbs: []sdk.Verb{{Name: "post", Open: true, Options: sdk.Schema{
					"text": {Type: "string", Required: true},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakeopenflip", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that flips Open to true must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "open") {
		t.Fatalf("DisabledReason = %q, want it to name the open mismatch", in.DisabledReason)
	}
}

// TestInstanceDeclCannotLoosenOutputs: an instance decl that drops a
// type-declared output, or weakens it from required to optional, loosens
// output validation for a verb the type decl already governed.
func TestInstanceDeclCannotLoosenOutputs(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Verbs: []sdk.Verb{{Name: "create", Outputs: sdk.Schema{
				"id": {Type: "string", Required: true},
			}}},
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Verbs: []sdk.Verb{{Name: "create", Outputs: sdk.Schema{
					"id": {Type: "string", Required: false}, // required dropped
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakeoutputloosen", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that loosens a type-level output must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "id") {
		t.Fatalf("DisabledReason = %q, want it to name the loosened output", in.DisabledReason)
	}
}

// TestInstanceDeclCannotAddConversationReplyToExistingEvent and
// TestInstanceDeclCannotWidenEventTargetScope are finding 7's event-side
// regression tests: events are exempt from the refinement check in general
// (Q6's whole point), but a SAME-NAMED event may not escalate what the
// engine does with it beyond the type-level declaration's own same-named
// event.
func TestInstanceDeclCannotAddConversationReplyToExistingEvent(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Events: []sdk.Event{{Name: "comment"}}, // no conversation_reply at the type level
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Events: []sdk.Event{{Name: "comment", Semantics: &sdk.EventSemantics{
					ConversationReply: &sdk.ConversationReply{ID: "thread", Author: "a", Text: "t"},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakeconvreply", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that adds conversation_reply to an existing event must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "comment") || !strings.Contains(in.DisabledReason, "conversation_reply") {
		t.Fatalf("DisabledReason = %q, want it to name the event and conversation_reply", in.DisabledReason)
	}
}

func TestInstanceDeclCannotWidenEventTargetScope(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{
			Events: []sdk.Event{{Name: "comment", Semantics: &sdk.EventSemantics{
				Target: &sdk.TargetSemantics{Scope: []sdk.ScopeFact{{Dimension: "repo", Fact: "repo"}}},
			}}},
		},
		instance: func(string, map[string]any) sdk.Decl {
			return sdk.Decl{
				Events: []sdk.Event{{Name: "comment", Semantics: &sdk.EventSemantics{
					Target: &sdk.TargetSemantics{Scope: []sdk.ScopeFact{
						{Dimension: "repo", Fact: "repo"},
						{Dimension: "channel", Fact: "channel"}, // a dimension the type decl never named
					}},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakescopewiden", h)
	if in.DisabledReason == "" {
		t.Fatal("an instance decl that widens an existing event's target scope dimensions must disable the connector")
	}
	if !strings.Contains(in.DisabledReason, "comment") || !strings.Contains(in.DisabledReason, "scope") {
		t.Fatalf("DisabledReason = %q, want it to name the event and the scope mismatch", in.DisabledReason)
	}
}

// TestInstanceDeclLegitimateEventWithNewTargetAssignedPasses: webhook's
// actual shape — a per-instance event with its own statically-known
// target.assigned, and no same-named event at the type level at all — must
// still pass (the baseline Q6 case, re-asserted here alongside the stricter
// checks above so a future change to this file can't tighten its way into
// breaking the legitimate case).
func TestInstanceDeclLegitimateEventWithNewTargetAssignedPasses(t *testing.T) {
	h := &fakeInstanceHandler{
		typeDecl: sdk.Decl{}, // no events at all at the type level (webhook's shape)
		instance: func(instance string, _ map[string]any) sdk.Decl {
			return sdk.Decl{
				Events: []sdk.Event{{Name: "delivery", Semantics: &sdk.EventSemantics{
					Target:            &sdk.TargetSemantics{Assigned: []byte("true")},
					ConversationReply: &sdk.ConversationReply{ID: "thread", Author: "a", Text: "t"},
				}}},
			}
		},
	}
	in := buildFakeInstanceConnector(t, "fakewebhookshape", h)
	if in.DisabledReason != "" {
		t.Fatalf("a genuinely new per-instance event (absent from the type decl) must not disable the connector: %s", in.DisabledReason)
	}
}
