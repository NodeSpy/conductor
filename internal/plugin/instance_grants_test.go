package plugin

import (
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestInstanceClientAppliesOwnGrantNotUnion is the low-level proof for the
// isolate: true path: config.PluginRef folds two configured connector
// instances of one plugin into a single entry (PluginRefs), carrying each
// instance's OWN Network/AllowSecrets/AllowEnv/Isolation/Isolate in
// PluginRef.Instances, separate from the UNIONED fields on PluginRef itself
// (which now reflect only the NON-isolated instances). Manager.InstanceClient
// must build each ISOLATED instance's per-process Spec from ITS OWN entry in
// that map — never from the union — so ghA's process never inherits ghB's
// allow_env/network/allow_secrets/isolation, and vice versa.
func TestInstanceClientAppliesOwnGrantNotUnion(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	isoA := &config.IsolationConfig{Mode: "namespace"}
	isoB := &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}}
	ref := refFor(t, config.UseKindConnector, bin)
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Network: []string{"a.example:443"}, AllowSecrets: []string{"secret-a"}, AllowEnv: []string{"VAR_A"}, Isolation: isoA, Isolate: true},
		"b": {Network: []string{"b.example:443"}, AllowSecrets: []string{"secret-b"}, AllowEnv: []string{"VAR_B"}, Isolation: isoB, Isolate: true},
	}
	// Neither instance is in the union (both isolate) — confirms InstanceClient
	// does NOT fall back to these for an isolated instance's spec.
	ref.Network = nil
	ref.AllowSecrets = nil
	ref.AllowEnv = nil
	ref.Isolation = nil

	state := stateAt(t)
	mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", state, Deps{})
	defer mgr.Close()
	key := ref.Key()

	// Every instance isolates, so there is no shared process at all — the
	// type-level spec is the minimum-grant throwaway probe shape.
	typeSpec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("type-level spec not found")
	}
	if typeSpec.Shared {
		t.Fatalf("a connector ref with every instance isolated must have Shared = false, got %+v", typeSpec)
	}
	if !typeSpec.Probe {
		t.Fatalf("a connector ref with every instance isolated must be the minimum-grant probe spec, got %+v", typeSpec)
	}
	if len(typeSpec.Network) != 0 || len(typeSpec.AllowSecrets) != 0 || len(typeSpec.AllowEnv) != 0 || typeSpec.Isolation != nil {
		t.Fatalf("type-level (ProbeDescribe) spec must carry the MINIMUM grant, got %+v", typeSpec)
	}

	ca, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(ca.spec.Network, []string{"a.example:443"}) {
		t.Fatalf("instance a: Network = %v, want its own [a.example:443]", ca.spec.Network)
	}
	if !reflect.DeepEqual(ca.spec.AllowSecrets, []string{"secret-a"}) {
		t.Fatalf("instance a: AllowSecrets = %v, want its own [secret-a]", ca.spec.AllowSecrets)
	}
	if !reflect.DeepEqual(ca.spec.AllowEnv, []string{"VAR_A"}) {
		t.Fatalf("instance a: AllowEnv = %v, want its own [VAR_A]", ca.spec.AllowEnv)
	}
	if ca.spec.Isolation != isoA {
		t.Fatalf("instance a: Isolation = %+v, want its own isoA", ca.spec.Isolation)
	}

	if !reflect.DeepEqual(cb.spec.Network, []string{"b.example:443"}) {
		t.Fatalf("instance b: Network = %v, want its own [b.example:443]", cb.spec.Network)
	}
	if !reflect.DeepEqual(cb.spec.AllowSecrets, []string{"secret-b"}) {
		t.Fatalf("instance b: AllowSecrets = %v, want its own [secret-b]", cb.spec.AllowSecrets)
	}
	if !reflect.DeepEqual(cb.spec.AllowEnv, []string{"VAR_B"}) {
		t.Fatalf("instance b: AllowEnv = %v, want its own [VAR_B]", cb.spec.AllowEnv)
	}
	if cb.spec.Isolation != isoB {
		t.Fatalf("instance b: Isolation = %+v, want its own isoB", cb.spec.Isolation)
	}

	// Neither instance's egress manifest leaks the other's narrowed network.
	if got := ca.spec.EffectiveManifest().Egress; !reflect.DeepEqual(got, []string{"a.example:443"}) {
		t.Fatalf("instance a: effective egress = %v, want only its own", got)
	}
	if got := cb.spec.EffectiveManifest().Egress; !reflect.DeepEqual(got, []string{"b.example:443"}) {
		t.Fatalf("instance b: effective egress = %v, want only its own", got)
	}

	// cb must never be served by ca's client, and vice versa: two distinct
	// *Client pointers, each confined to exactly one instance.
	if ca == cb {
		t.Fatal("two isolated instances must never share a *Client")
	}
}

// TestInstanceClientDefaultSharesOneProcess is the new default's proof: two
// NON-isolated instances of one plugin must resolve to the SAME *Client (one
// shared process), carrying the UNION of their grants — the opposite of the
// isolated case above.
func TestInstanceClientDefaultSharesOneProcess(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	ref := refFor(t, config.UseKindConnector, bin)
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Network: []string{"a.example:443"}, AllowSecrets: []string{"secret-a"}},
		"b": {Network: []string{"b.example:443"}, AllowSecrets: []string{"secret-b"}},
	}
	ref.Network = []string{"a.example:443", "b.example:443"}
	ref.AllowSecrets = []string{"secret-a", "secret-b"}

	state := stateAt(t)
	mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", state, Deps{})
	defer mgr.Close()
	key := ref.Key()

	typeSpec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("type-level spec not found")
	}
	if !typeSpec.Shared {
		t.Fatalf("a connector ref with no isolated instance must have Shared = true, got %+v", typeSpec)
	}
	if typeSpec.Probe {
		t.Fatalf("a shared connector spec must not be marked as the minimum-grant probe, got %+v", typeSpec)
	}

	ca, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatal("two non-isolated instances must share the same *Client")
	}
	if !reflect.DeepEqual(ca.spec.Network, ref.Network) {
		t.Fatalf("shared client Network = %v, want the union %v", ca.spec.Network, ref.Network)
	}
	if !reflect.DeepEqual(ca.spec.AllowSecrets, ref.AllowSecrets) {
		t.Fatalf("shared client AllowSecrets = %v, want the union %v", ca.spec.AllowSecrets, ref.AllowSecrets)
	}
}

// TestSpecFromRefMixedSharedAndIsolated proves a connector ref can carry BOTH
// shapes at once: one non-isolated instance shares the default process (the
// union of itself and no one else, since it is the ref's only non-isolated
// instance), and a sibling isolated instance gets its own.
func TestSpecFromRefMixedSharedAndIsolated(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	ref.Instances = map[string]config.ConnectorGrant{
		"shared":   {Network: []string{"a.example:443"}},
		"isolated": {Network: []string{"b.example:443"}, Isolate: true},
	}
	ref.Network = []string{"a.example:443"} // the union excludes the isolated instance

	s := SpecFromRef(ref, "", Installed{}, false)
	if !s.Shared {
		t.Fatalf("a ref with one non-isolated instance must have Shared = true, got %+v", s)
	}
	if s.Probe {
		t.Fatal("a ref with a shared process must not be marked as the minimum-grant probe")
	}
	if !reflect.DeepEqual(s.Network, []string{"a.example:443"}) {
		t.Fatalf("shared spec Network = %v, want the non-isolated instance's own [a.example:443], not the isolated sibling's", s.Network)
	}
	if g, ok := s.Instances["isolated"]; !ok || !g.Isolate {
		t.Fatalf("Instances[isolated] must be carried through with Isolate = true, got %+v", s.Instances)
	}
}

// TestSpecFromRefProbeFlag: SpecFromRef must mark Spec.Probe true ONLY when
// there is no shared process at all (every configured connector instance
// isolates) — never when at least one instance shares the default process,
// and never for a runtime or engine ref, neither of which has a
// several-instances-of-one-plugin shape to isolate in the first place.
func TestSpecFromRefProbeFlag(t *testing.T) {
	allIsolated := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	allIsolated.Instances = map[string]config.ConnectorGrant{
		"a": {Isolate: true},
	}
	if s := SpecFromRef(allIsolated, "", Installed{}, false); !s.Probe || s.Shared {
		t.Fatalf("a connector with every instance isolated must have Probe = true, Shared = false, got %+v", s)
	}

	shared := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	shared.Instances = map[string]config.ConnectorGrant{
		"a": {},
	}
	if s := SpecFromRef(shared, "", Installed{}, false); s.Probe || !s.Shared {
		t.Fatalf("a connector with a non-isolated instance must have Probe = false, Shared = true, got %+v", s)
	}

	runtime := refFor(t, config.UseKindRuntime, "acme/plugins/node")
	if s := SpecFromRef(runtime, "", Installed{}, false); s.Probe || !s.Shared {
		t.Fatalf("a runtime spec must have Probe = false, Shared = true, got %+v", s)
	}

	engine := refFor(t, config.UseKindEngine, "acme/plugins/js")
	if s := SpecFromRef(engine, "", Installed{}, false); s.Probe || !s.Shared {
		t.Fatalf("an engine spec must have Probe = false, Shared = true, got %+v", s)
	}
}

// TestForbidIsolatedRefusesInstance proves the single_process late-discovery
// path: once ForbidIsolated has recorded a reason for (key, instance),
// InstanceClient must refuse that instance — never silently fall back to the
// shared client, and never spawn the instance's own process anyway.
func TestForbidIsolatedRefusesInstance(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	ref := refFor(t, config.UseKindConnector, bin)
	ref.Instances = map[string]config.ConnectorGrant{
		"shared":   {},
		"isolated": {Isolate: true},
	}
	state := stateAt(t)
	mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", state, Deps{})
	defer mgr.Close()
	key := ref.Key()

	mgr.ForbidIsolated(key, "isolated", "plugin declares single_process")

	if _, err := mgr.InstanceClient(key, "isolated"); err == nil {
		t.Fatal("a forbidden isolated instance must be refused, got no error")
	}
	// The shared instance is unaffected.
	if _, err := mgr.InstanceClient(key, "shared"); err != nil {
		t.Fatalf("a non-isolated sibling must still resolve: %v", err)
	}
}

// TestSpecIdentityNamesWhatAProcessServes is the log-line proof e2e's K4-pid
// check relies on (test/e2e/run.sh): an isolated instance's process names
// itself by that one instance, and the shared process names every
// non-isolated instance it serves — never the ambiguous bare plugin name for
// either, since "conductor connectors ls" deliberately shows no pid at all
// and this log line is the one place an operator can tell which connector
// names share a process.
func TestSpecIdentityNamesWhatAProcessServes(t *testing.T) {
	shared := Spec{Name: "github", Kind: KindConnector, Instances: map[string]config.ConnectorGrant{
		"gh":      {},
		"ghshare": {},
		"solo":    {Isolate: true},
	}}
	if got, want := shared.Identity(), "github (shared: gh, ghshare)"; got != want {
		t.Fatalf("shared Identity() = %q, want %q", got, want)
	}

	isolated := shared
	isolated.Instance = "solo"
	if got, want := isolated.Identity(), "github instance solo"; got != want {
		t.Fatalf("isolated Identity() = %q, want %q", got, want)
	}

	// A connector with no configured instances at all (the minimal-grant
	// type-level probe, or a not-yet-populated Spec) falls back to the bare
	// name.
	bare := Spec{Name: "github", Kind: KindConnector}
	if got, want := bare.Identity(), "github"; got != want {
		t.Fatalf("bare Identity() = %q, want %q", got, want)
	}
}
