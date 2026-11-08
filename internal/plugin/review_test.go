package plugin

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func TestReviewAdd(t *testing.T) {
	// A builtin: nothing to fetch or trust, and the snippet names it bare.
	b, err := ReviewAdd(config.UseKindConnector, "webhook", "", nil, nil)
	if err != nil || !b.Builtin || !b.Trusted || b.Instance != "webhook" || !strings.Contains(b.Snippet, "use: webhook") {
		t.Fatalf("builtin: %+v %v", b, err)
	}

	// An official name is trusted by default; nothing installed yet.
	o, err := ReviewAdd(config.UseKindConnector, "widgetsvc", "w1", nil, nil)
	if err != nil || o.Builtin || !o.Official || !o.Trusted || o.Installed != nil {
		t.Fatalf("official: %+v %v", o, err)
	}
	if o.Snippet != "connectors:\n  w1:\n    use: widgetsvc\n" {
		t.Errorf("snippet %q", o.Snippet)
	}

	// A third-party source needs a plugin_trust entry until one admits it.
	ref := "git.example.invalid/acme/widget"
	tp, err := ReviewAdd(config.UseKindRuntime, ref, "", nil, nil)
	if err != nil || tp.Trusted || tp.Official || tp.Source == "" {
		t.Fatalf("third party: %+v %v", tp, err)
	}
	ok, _ := ReviewAdd(config.UseKindRuntime, ref, "", &config.PackTrustConfig{Allow: []string{tp.Source}}, nil)
	if !ok.Trusted {
		t.Errorf("an allow entry admits it: %+v", ok)
	}

	// An engine has no block: the snippet is the step that names it.
	e, _ := ReviewAdd(config.UseKindEngine, "widgetlang", "", nil, nil)
	if !strings.HasPrefix(e.Snippet, "steps:") {
		t.Errorf("engine snippet %q", e.Snippet)
	}

	// An installed build's manifest is the agreement.
	u, _ := config.ParseUse(config.UseKindConnector, "widgetsvc")
	st := &InstallState{Plugins: []Installed{{Key: u.InstallKey(), Kind: "connector", Name: "widgetsvc", Source: u.Source(), Resolved: "v1.0.0", Path: "/x",
		Manifest: Manifest{Egress: []string{"w.example.invalid:443"}}}}}
	in, _ := ReviewAdd(config.UseKindConnector, "widgetsvc", "", nil, st)
	if in.Installed == nil || in.Installed.Resolved != "v1.0.0" || len(in.Permissions) == 0 || !strings.Contains(strings.Join(in.Permissions, "\n"), "w.example.invalid") {
		t.Errorf("installed: %+v", in)
	}
}
