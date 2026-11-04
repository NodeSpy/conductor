package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
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

// The jail's network: always its own namespace, the egress proxy the only
// way out. The allowlist is network: when the entry sets one, else the
// declared egress plus the connection config's targets; the describe probe
// and engines get only what they declare. network: [host] is the only way
// to the host network (trust: full is decided before the jail).
func TestJailNetwork(t *testing.T) {
	base := Spec{Name: "p", Kind: KindConnector}
	withEgress := base
	withEgress.Manifest = Manifest{Egress: []string{"api.example.com:443"}}
	narrowed := withEgress
	narrowed.Network = []string{"api.example.com:443"}
	setByOperator := base
	setByOperator.Network = []string{"nas.lan:5000"}
	targets := base
	targets.Targets = []string{"ha.lan:8123", "127.0.0.1:9000", "localhost:*"}
	both := withEgress
	both.Targets = []string{"ghe.example.com:443"}
	narrowedTargets := narrowed
	narrowedTargets.Targets = []string{"ghe.example.com:443"}
	host := withEgress
	host.Network = []string{config.HostNetwork}
	probe := withEgress
	probe.Probe = true
	probe.Targets = []string{"ha.lan:8123"}
	engine := Spec{Name: "e", Kind: KindStep}

	for _, tc := range []struct {
		name     string
		s        Spec
		deny     bool
		allow    string
		loopback string
	}{
		{"no egress, no targets", base, true, "", ""},
		{"declared egress", withEgress, true, "api.example.com:443", ""},
		{"network: narrows", narrowed, true, "api.example.com:443", ""},
		{"network: on a plugin that declares none", setByOperator, true, "nas.lan:5000", ""},
		{"config targets", targets, true, "ha.lan:8123,127.0.0.1:9000,localhost:*", "127.0.0.1:9000"},
		{"declared egress plus targets", both, true, "api.example.com:443,ghe.example.com:443", ""},
		{"network: is exact, targets not added", narrowedTargets, true, "api.example.com:443", ""},
		{"network: [host]", host, false, "", ""},
		{"describe probe", probe, true, "", ""},
		{"engine", engine, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := jailNetwork(tc.s)
			if n.deny != tc.deny || strings.Join(n.allow, ",") != tc.allow || strings.Join(n.loopback, ",") != tc.loopback {
				t.Fatalf("deny=%v allow=%v loopback=%v, want deny=%v allow=%q loopback=%q", n.deny, n.allow, n.loopback, tc.deny, tc.allow, tc.loopback)
			}
			d := n.describe()
			switch {
			case !tc.deny && d != "host network, not confined — its entry sets network: [host]":
				t.Fatalf("describe = %q", d)
			case tc.deny && tc.allow == "" && d != "no network":
				t.Fatalf("describe = %q", d)
			}
		})
	}
}

// Red-team regression (phase 4, H1): a connector got the host network by
// declaring no egress, or by declaring an exposes verb. Neither does now.
func TestNoEgressAndExposesGetNoHostNetwork(t *testing.T) {
	bare := Spec{Name: "evil", Kind: KindConnector}
	rt := Spec{Name: "evil", Kind: KindRuntime}
	exposing := Spec{Name: "evil", Kind: KindConnector, Manifest: Manifest{Egress: []string{"api.example.com:443"}}}
	for _, s := range []Spec{bare, rt, exposing} {
		if n := jailNetwork(s); !n.deny {
			t.Fatalf("%s %s: host network (%s)", s.Kind, s.Name, n.why)
		}
	}
	if !strings.Contains(PlannedConfinement(bare, SandboxDeps{Jail: JailOn}).String(), "network: no network") {
		t.Fatalf("--caps does not say it has no network: %s", PlannedConfinement(bare, SandboxDeps{Jail: JailOn}))
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

// Red-team regression (phase 4, finding 4): under sandbox.plugins: on, a
// launch whose jail fails to build is refused, not run unconfined. Only the
// box-level probe may degrade. Here the box "can" jail but this launch
// cannot (no conductor binary wired for the Linux forwarder; on macOS the
// staging dir is a file).
func TestJailBuildErrorRefusesUnderOn(t *testing.T) {
	staging := t.TempDir()
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	s := Spec{Name: "evil", Kind: KindConnector, BinPath: bin}
	if err := os.WriteFile(filepath.Join(staging, "evil"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd := SandboxDeps{Jail: JailOn, StagingRoot: staging, JailProbe: func() string { return "" }}
	cmd, cleanup, conf, err := buildCommand(s, sd)
	if err == nil {
		cleanup()
		t.Fatalf("a launch whose jail cannot be built ran anyway: mode=%q argv=%v", conf.Mode, cmd.Args)
	}
	if !strings.Contains(err.Error(), "building its OS jail failed") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

// A plugin that plants a file or a symlink where its HOME dir goes gets it
// removed (the symlink not followed) and a fresh HOME dir on the next
// launch, so it cannot use that to break its own jail.
func TestJailHomeResetsPlantedEntries(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			staging := t.TempDir()
			stage := filepath.Join(staging, "evil")
			home := filepath.Join(stage, ".home")
			if err := os.MkdirAll(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				err := os.WriteFile(home, []byte("x"), 0o600)
				if err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(victim, home); err != nil {
				t.Fatal(err)
			}
			_, h, _, err := jailFiles(Spec{Name: "evil", Kind: KindConnector}, SandboxDeps{StagingRoot: staging})
			if err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Lstat(h); err != nil || !fi.IsDir() {
				t.Fatalf("HOME %s is not a fresh directory: %v %v", h, fi, err)
			}
			if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
				t.Fatalf("the symlink's target was touched: %q %v", b, err)
			}
		})
	}
}

// A path the plugin must execute (its binary dir, a declared command) is
// never also writable, even when an fs: entry names it :rw.
func TestDedupeBindsExecStaysReadOnly(t *testing.T) {
	out := dedupeBinds([]sandbox.BindMount{
		{Path: "/opt/plugin", RO: true, Exec: true},
		{Path: "/opt/plugin", RO: false, Optional: true},
		{Path: "/srv/data", RO: false},
	})
	if len(out) != 2 || !out[0].Exec || !out[0].RO {
		t.Fatalf("binds = %+v, want /opt/plugin executable and read-only", out)
	}
	if out[1].Exec || out[1].RO {
		t.Fatalf("binds = %+v, want /srv/data writable and not executable", out)
	}
}

// The connection config's targets reach the shared process's spec (the
// union) and an isolated instance's own spec (its own only).
func TestSpecTargetsFromRef(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/lan")
	ref.Targets = []string{"a.lan:80"}
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Targets: []string{"a.lan:80"}},
		"b": {Targets: []string{"b.lan:80"}, Isolate: true},
	}
	s := SpecFromRef(ref, "", Installed{}, false)
	if strings.Join(s.Targets, ",") != "a.lan:80" {
		t.Fatalf("shared spec targets = %v", s.Targets)
	}
	if got := InstanceSpec(s, "b").Targets; strings.Join(got, ",") != "b.lan:80" {
		t.Fatalf("isolated instance targets = %v", got)
	}
}
