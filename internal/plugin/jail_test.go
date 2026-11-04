package plugin

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// buildCommand's choice of confinement, for each policy and opt-out, with
// the jail available ("") or not. PlannedConfinement (what plugin list
// --caps shows) must make the same choice.
func TestJailDecision(t *testing.T) {
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	conn := Spec{Name: "p", Kind: KindConnector, Provides: "acme-echo", BinPath: bin, Local: true}
	step := Spec{Name: "e", Kind: KindStep, Provides: "acme-engine", BinPath: bin, Local: true}
	trusted := conn
	trusted.TrustFull = true
	none := conn
	none.Isolation = &config.IsolationConfig{Mode: "none"}
	noJail := func() string { return "no user namespaces here" }

	for _, tc := range []struct {
		name    string
		s       Spec
		policy  JailPolicy
		probe   func() string
		mode    string
		wantErr bool
	}{
		{"trust full", trusted, JailRequired, noJail, "trusted", false},
		{"isolation none", none, JailRequired, noJail, "trusted", false},
		{"off", conn, JailOff, nil, "off", false},
		{"on, no jail here", conn, JailOn, noJail, "unavailable", false},
		{"required, no jail here", conn, JailRequired, noJail, "", true},
		{"off keeps an engine jailed", step, JailOff, noJail, "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd := SandboxDeps{Jail: tc.policy, JailProbe: tc.probe}
			cmd, cleanup, conf, err := buildCommand(tc.s, sd)
			plan := PlannedConfinement(tc.s, sd)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "sandbox.plugins is required") {
					t.Fatalf("want a refusal naming sandbox.plugins, got %v", err)
				}
				if plan.Mode != "refused" {
					t.Fatalf("plan = %q, want refused", plan.Mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if conf.Mode != tc.mode || plan.Mode != tc.mode {
				t.Fatalf("mode = %q (plan %q), want %q", conf.Mode, plan.Mode, tc.mode)
			}
			if cmd.Args[0] != bin {
				t.Fatalf("%s must exec the plugin directly: %v", tc.mode, cmd.Args)
			}
			if tc.mode == "unavailable" && !strings.Contains(conf.String(), "no user namespaces here") {
				t.Fatalf("the reason is not in %q", conf.String())
			}
		})
	}
}

// The jail's network: its own namespace with the proxy as the only way out
// when it declares egress (or network: sets one); the host network when it
// declares none or has an exposes verb; nothing for the describe probe and
// engines beyond what they declare.
func TestJailNetwork(t *testing.T) {
	base := Spec{Name: "p", Kind: KindConnector}
	withEgress := base
	withEgress.Manifest = Manifest{Egress: []string{"api.example.com:443"}}
	narrowed := withEgress
	narrowed.Network = []string{"api.example.com:443"}
	setByOperator := base
	setByOperator.Network = []string{"nas.lan:5000"}
	exposes := withEgress
	exposes.Manifest.Exposes = true
	learned := withEgress
	learned.hostNetwork = true
	probe := withEgress
	probe.Probe = true
	engine := Spec{Name: "e", Kind: KindStep}

	for _, tc := range []struct {
		name  string
		s     Spec
		deny  bool
		allow string
	}{
		{"no egress", base, false, ""},
		{"declared egress", withEgress, true, "api.example.com:443"},
		{"network: narrows", narrowed, true, "api.example.com:443"},
		{"network: on a plugin that declares none", setByOperator, true, "nas.lan:5000"},
		{"exposes (recorded)", exposes, false, ""},
		{"exposes (learned at describe)", learned, false, ""},
		{"describe probe", probe, true, ""},
		{"engine", engine, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := jailNetwork(tc.s)
			if n.deny != tc.deny || strings.Join(n.allow, ",") != tc.allow {
				t.Fatalf("deny=%v allow=%v, want deny=%v allow=%q", n.deny, n.allow, tc.deny, tc.allow)
			}
			d := n.describe()
			switch {
			case !tc.deny && !strings.HasPrefix(d, "host network"):
				t.Fatalf("describe = %q", d)
			case tc.deny && tc.allow == "" && d != "none":
				t.Fatalf("describe = %q", d)
			}
		})
	}
}

// An exposes verb in the declaration is recorded in the manifest at
// install, so the first launch already has the host network.
func TestManifestRecordsExposes(t *testing.T) {
	d := &Decl{Verbs: []Verb{{Name: "share", Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{}}}}}
	if !ManifestFromDecl(d).Exposes {
		t.Fatal("an exposes verb is not recorded")
	}
	if ManifestFromDecl(&Decl{Verbs: []Verb{{Name: "x"}}}).Exposes {
		t.Fatal("a plain verb recorded as exposes")
	}
}

// An isolated instance runs under its own grant only: a sibling's
// trust: full on the shared process does not reach it, and its own does.
func TestInstanceSpecTrustIsPerInstance(t *testing.T) {
	shared := Spec{Name: "p", Kind: KindConnector, TrustFull: true, Instances: map[string]config.ConnectorGrant{
		"trusted":   {TrustFull: true, Isolation: &config.IsolationConfig{Mode: "none"}},
		"jailed":    {Isolate: true},
		"ownTrusts": {Isolate: true, TrustFull: true, Isolation: &config.IsolationConfig{Mode: "none"}},
	}}
	sd := SandboxDeps{Jail: JailOn, JailProbe: func() string { return "" }}
	if m := PlannedConfinement(InstanceSpec(shared, "jailed"), sd).Mode; m != "jail" {
		t.Fatalf("an isolated instance without trust: full got %q from its sibling", m)
	}
	if m := PlannedConfinement(InstanceSpec(shared, "ownTrusts"), sd).Mode; m != "trusted" {
		t.Fatalf("an isolated instance's own trust: full: %q", m)
	}
	if m := PlannedConfinement(InstanceSpec(shared, "unknown"), sd).Mode; m != "jail" {
		t.Fatalf("an instance with no grant: %q", m)
	}
}
