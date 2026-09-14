package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// installExtractedPlugins registers the sentry and pagerduty connector types
// the way an external plugin does at daemon boot (connector.RegisterExternalType,
// driven by the plugins: block the migration now emits), and unregisters them
// on cleanup.
//
// The migration tests need this because a migrated legacy sentry/pagerduty
// config names a type this binary no longer bundles: it is REAL that such a
// config only validates once the plugin is installed. This helper is the
// "installed" half of that, so the tests still prove the transform end to end.
//
// The decls mirror the removed bundled decls' events and filter keys exactly —
// that is the point of the extraction: the migrated triggers, filters, and
// context keys are unchanged, only the provider moved out of process.
func installExtractedPlugins(t *testing.T) {
	t.Helper()
	decls := []*connector.TypeDecl{
		{
			Type: "sentry",
			Desc: "Sentry alerts (external plugin)",
			Connection: connector.Schema{
				"listen":        {Type: connector.TString},
				"smee_url":      {Type: connector.TString},
				"path":          {Type: connector.TString},
				"client_secret": {Type: connector.TString},
			},
			Events: []connector.EventDecl{{
				Name: "alert",
				Filters: connector.Schema{
					"projects":     {Type: connector.TList},
					"levels":       {Type: connector.TList},
					"environments": {Type: connector.TList},
					"exclude":      {Type: connector.TList},
				},
			}},
		},
		{
			Type: "pagerduty",
			Desc: "PagerDuty incidents (external plugin)",
			Connection: connector.Schema{
				"listen":         {Type: connector.TString},
				"smee_url":       {Type: connector.TString},
				"path":           {Type: connector.TString},
				"signing_secret": {Type: connector.TString},
			},
			Events: []connector.EventDecl{{
				Name: "incident",
				Filters: connector.Schema{
					"event_types": {Type: connector.TList},
					"services":    {Type: connector.TList},
					"urgencies":   {Type: connector.TList},
					"priorities":  {Type: connector.TList},
					"exclude":     {Type: connector.TList},
				},
			}},
		},
	}
	for _, d := range decls {
		decl := d
		build := func(name string, ref config.ConnectorRef, deps connector.Deps) (connector.Impl, error) {
			return stubExternalImpl{}, nil
		}
		if err := connector.RegisterExternalType(decl, build); err != nil {
			t.Fatalf("a plugin must be able to provide the formerly-bundled type %q: %v", decl.Type, err)
		}
		t.Cleanup(func() { connector.UnregisterExternalType(decl.Type) })
	}
}

// stubExternalImpl stands in for the plugin-backed connector.Impl. Source
// returns nil: these migration tests validate config semantics, not event
// delivery (the plugin's own event streaming is tested in the
// conductor-plugins repo).
type stubExternalImpl struct{}

func (stubExternalImpl) Validate() error          { return nil }
func (stubExternalImpl) DeclaredEvents() []string { return nil }
func (stubExternalImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (stubExternalImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

// TestPluginCanProvideFormerlyBundledType is the protection-relaxation proof.
//
// Before the extraction, connector.RegisterExternalType REFUSED any type the
// binary bundled ("connector type %q is bundled and cannot be replaced by a
// plugin"), so `plugins: { sentry: … }` was impossible. sentry and pagerduty
// are no longer bundled, so a plugin may now provide them — while a type that
// IS still bundled (github) stays protected, and two plugins claiming the same
// type still collide.
func TestPluginCanProvideFormerlyBundledType(t *testing.T) {
	for _, typ := range []string{"sentry", "pagerduty"} {
		if _, ok := connector.TypeDeclFor(typ); ok {
			t.Fatalf("%q must NOT be a bundled connector type anymore", typ)
		}
	}

	installExtractedPlugins(t) // fails the test if the registration is refused

	for _, typ := range []string{"sentry", "pagerduty"} {
		if _, ok := connector.TypeDeclFor(typ); !ok {
			t.Fatalf("%q should be registered after the plugin provides it", typ)
		}
		if !connector.IsExternalType(typ) {
			t.Errorf("%q should be tagged as plugin-backed", typ)
		}
	}

	// Still bundled → still protected.
	err := connector.RegisterExternalType(
		&connector.TypeDecl{Type: "github"},
		func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "bundled and cannot be replaced") {
		t.Fatalf("github is still bundled and must stay unreplaceable, got %v", err)
	}

	// Two plugins claiming one type still collide.
	err = connector.RegisterExternalType(
		&connector.TypeDecl{Type: "sentry"},
		func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "already provided by another plugin") {
		t.Fatalf("a second plugin providing sentry must be refused, got %v", err)
	}
}
