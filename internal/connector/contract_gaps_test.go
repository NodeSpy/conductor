package connector

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A type whose plugin is not installed yet accepts any event and verb name,
// unchecked — its filter/options/context are checked once the plugin
// describes itself — while an installed type still refuses an unknown name.
func TestUnavailableTypeAcceptsNamesUnchecked(t *testing.T) {
	d := &TypeDecl{Type: "acme", Unavailable: "not installed"}
	ev, ok := d.Event("anything")
	if !ok || !ev.Unchecked || !ev.Dynamic || ev.Name != "anything" {
		t.Fatalf("Event = %+v, %v", ev, ok)
	}
	if v, ok := d.FlowVerb("whatever"); !ok || !v.Open {
		t.Fatalf("FlowVerb = %+v, %v", v, ok)
	}
	real := &TypeDecl{Type: "acme", Events: []EventDecl{{Name: "alert"}}}
	if ev, ok := real.Event("alert"); !ok || ev.Unchecked {
		t.Fatalf("a declared event must be checked: %+v %v", ev, ok)
	}
	if _, ok := real.Event("nope"); ok {
		t.Fatal("an installed type accepted an undeclared event")
	}
}

// A dynamic (config-named) event is taken by its name — but a plugin cannot
// relabel one: a kind differing from its event is not a config name.
func TestKindForDynamicException(t *testing.T) {
	psi := &pluginSourceIntegration{instance: "c1", log: t.Logf,
		declared: map[string]bool{"tick": true},
		dynamic:  &EventDecl{Name: "tick", Dynamic: true}}
	if k, ok := psi.kindFor(pluginEvent{Event: "nightly"}); !ok || k != "nightly" {
		t.Fatalf("config-named event: %q %v", k, ok)
	}
	if k, ok := psi.kindFor(pluginEvent{Event: "nightly", Kind: "_closed"}); ok {
		t.Fatalf("a relabelled dynamic event was taken as %q", k)
	}
	plain := &pluginSourceIntegration{instance: "c1", log: t.Logf, declared: map[string]bool{"tick": true}}
	if _, ok := plain.kindFor(pluginEvent{Event: "nightly"}); ok {
		t.Fatal("an undeclared event was taken without a dynamic declaration")
	}
}

// The revision read runs only for a target the platform assigned to this
// instance: a sender-chosen target reads nothing (no credentialed call made
// on the attacker's say-so).
func TestTargetHeadOnlyForAnAssignedTarget(t *testing.T) {
	decl := mapDecl(&sdk.Decl{Type: "forge", Verbs: []sdk.Verb{{Name: "pr_head", Semantics: &sdk.VerbSemantics{
		HostOnly: true,
		ReadsRevision: &sdk.ReadsRevision{Args: map[string]string{"repo": "{{.repo}}"}, Revision: "sha", State: "state",
			States: map[string][]string{"open": {"open"}}}}}}})
	inv := &countingHead{}
	in := &Instance{Name: "gh", Decl: decl, Enabled: true,
		Impl: &externalImpl{client: inv, decl: decl, instance: "gh", log: t.Logf}}
	tr := core.Trigger{Instance: "gh", Target: core.Target{Repo: "o/r", Number: 1}}
	if h, err := in.TargetHead(context.Background(), tr); err != nil || h.SHA != "" || inv.calls != 0 {
		t.Fatalf("unassigned target: head=%+v err=%v calls=%d", h, err, inv.calls)
	}
	tr.TargetTrusted = true
	if h, err := in.TargetHead(context.Background(), tr); err != nil || h.SHA != "abc" || h.State != "open" || inv.calls != 1 {
		t.Fatalf("assigned target: head=%+v err=%v calls=%d", h, err, inv.calls)
	}
}

type countingHead struct{ calls int }

func (c *countingHead) Invoke(context.Context, plugin.InvokeRequest) (map[string]any, error) {
	c.calls++
	return map[string]any{"sha": "abc", "state": "open"}, nil
}
