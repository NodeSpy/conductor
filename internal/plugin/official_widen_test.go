package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// widgetDecl is a synthetic connector declaration with the given
// capabilities.
func widgetDecl(c Capabilities) func(context.Context, Spec) (*Decl, error) {
	return func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "widget", Capabilities: c,
			Connection: sdk.Schema{"listen": {Type: "string"}, "key_path": {Type: "string"}}}, nil
	}
}

var (
	widgetNarrow = Capabilities{Egress: []string{"api.widget.example:443"}}
	widgetWide   = Capabilities{
		Egress: []string{"api.widget.example:443"},
		Listen: []string{"config:listen"},
		Read:   []string{"config:key_path"},
	}
)

func logInto(lines *[]string) func(string, ...any) {
	return func(format string, args ...any) { *lines = append(*lines, fmt.Sprintf(format, args...)) }
}

// A verified release of an official plugin that declares more than the
// installed build is installed, not held, and the log names what it added:
// for a plain ref, and for connector instances (the batch path).
func TestOfficialVerifiedWideningUpdateIsNotHeld(t *testing.T) {
	for _, shape := range []struct {
		name string
		ref  func(config.PluginRef) config.PluginRef
	}{{"plain ref", func(r config.PluginRef) config.PluginRef { return r }}, {"connector instances", withInstance}} {
		t.Run(shape.name, func(t *testing.T) { officialVerifiedWideningIsNotHeld(t, shape.ref) })
	}
}

func officialVerifiedWideningIsNotHeld(t *testing.T, shape func(config.PluginRef) config.PluginRef) {
	st := stateAt(t)
	ref := shape(refFor(t, config.UseKindConnector, "NodeSpy/conductor-plugins/connectors/widget"))
	refs := map[string]config.PluginRef{ref.Key(): ref}
	if _, err := Reconcile(refs, st, nil, stubFor("connectors/widget", "connectors/widget/v1.0.0"), Options{Describe: widgetDecl(widgetNarrow)}); err != nil {
		t.Fatal(err)
	}

	var logs []string
	res, err := Reconcile(refs, st, nil, stubFor("connectors/widget", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"),
		Options{Describe: widgetDecl(widgetWide), Log: logInto(&logs)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionUpdated || res[0].Tag != "connectors/widget/v2.0.0" {
		t.Fatalf("resolution = %+v, want updated to v2", res)
	}
	got, _ := LoadInstallState(st.Dir()).Get(ref.Key())
	if got.Resolved != "connectors/widget/v2.0.0" || strings.Join(got.Manifest.Read, ",") != "config:key_path" {
		t.Fatalf("v2 and its wider declaration must be recorded: %+v", got)
	}
	if p := st.PendingUpdates(); len(p) != 0 {
		t.Fatalf("nothing may be held: %+v", p)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "Accepted without a hold") || !strings.Contains(joined, "listen") {
		t.Fatalf("the log must say the widening was accepted and name it:\n%s", joined)
	}
}

// An official build whose release did not verify it is held like any other,
// and the next pass that fetches a verified copy installs it and clears the
// hold: what a conductor that held an official update before this change
// left in its install state.
func TestOfficialUnverifiedWideningUpdateIsHeldUntilVerified(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "NodeSpy/conductor-plugins/connectors/widget")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	if _, err := Reconcile(refs, st, nil, stubFor("connectors/widget", "connectors/widget/v1.0.0"), Options{Describe: widgetDecl(widgetNarrow)}); err != nil {
		t.Fatal(err)
	}

	unverified := stubFor("connectors/widget", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0")
	unverified.noSums = true
	res, err := Reconcile(refs, st, nil, unverified, Options{Describe: widgetDecl(widgetWide)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionHeld {
		t.Fatalf("an unverified official build must be held: %+v", res)
	}
	if p := st.PendingUpdates(); len(p) != 1 {
		t.Fatalf("one held update: %+v", p)
	}

	st = LoadInstallState(st.Dir())
	res, err = Reconcile(refs, st, nil, stubFor("connectors/widget", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionUpdated {
		t.Fatalf("a verified copy must install: %+v", res)
	}
	if p := st.PendingUpdates(); len(p) != 0 {
		t.Fatalf("the hold must be cleared: %+v", p)
	}
}

// The same widening from a third-party source is still held.
func TestThirdPartyWideningUpdateIsStillHeld(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/connectors/widget")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	if _, err := Reconcile(refs, st, trust, stubFor("connectors/widget", "connectors/widget/v1.0.0"), Options{Describe: widgetDecl(widgetNarrow)}); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(refs, st, trust, stubFor("connectors/widget", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionHeld {
		t.Fatalf("a third-party widening must be held: %+v", res)
	}
}

// `plugin update <name>` on a current official build that now declares more
// records the wider declaration instead of holding it.
func TestNamedUpdateRecordsAnOfficialCurrentBuildThatDeclaresMore(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "NodeSpy/conductor-plugins/connectors/widget")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	api := stubFor("connectors/widget", "connectors/widget/v1.0.0")
	if _, err := Reconcile(refs, st, nil, api, Options{Describe: widgetDecl(widgetNarrow)}); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(refs, st, nil, api, Options{Describe: widgetDecl(widgetWide), Only: ref.Name})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action == ActionHeld {
		t.Fatalf("an official current build must not be held: %+v", res)
	}
	got, _ := LoadInstallState(st.Dir()).Get(ref.Key())
	if strings.Join(got.Manifest.Listen, ",") != "config:listen" {
		t.Fatalf("the wider declaration must be recorded: %+v", got.Manifest)
	}
	if p := st.PendingUpdates(); len(p) != 0 {
		t.Fatalf("nothing may be held: %+v", p)
	}
}
