package plugin

import (
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestInstanceClientAppliesOwnGrantNotUnion is the low-level proof for
// finding 1 (per-instance grants, not the sibling union): config.PluginRef
// folds two configured connector instances of one plugin into a single
// entry (PluginRefs), carrying each instance's OWN Network/AllowSecrets/
// AllowEnv/Isolation in PluginRef.Instances, separate from the UNIONED
// fields on PluginRef itself. Manager.InstanceClient must build each
// instance's per-process Spec from ITS OWN entry in that map — never from
// the union — so ghA's process never inherits ghB's allow_env/network/
// allow_secrets/isolation, and vice versa.
func TestInstanceClientAppliesOwnGrantNotUnion(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	isoA := &config.IsolationConfig{Mode: "namespace"}
	isoB := &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}}
	ref := refFor(t, config.UseKindConnector, bin)
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Network: []string{"a.example:443"}, AllowSecrets: []string{"secret-a"}, AllowEnv: []string{"VAR_A"}, Isolation: isoA},
		"b": {Network: []string{"b.example:443"}, AllowSecrets: []string{"secret-b"}, AllowEnv: []string{"VAR_B"}, Isolation: isoB},
	}
	// The unioned fields PluginRefs would also carry — confirms InstanceClient
	// does NOT fall back to these for a per-instance (non-SharedProcess) spec.
	ref.Network = []string{"a.example:443", "b.example:443"}
	ref.AllowSecrets = []string{"secret-a", "secret-b"}
	ref.AllowEnv = []string{"VAR_A", "VAR_B"}
	ref.Isolation = isoA

	state := stateAt(t)
	mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", state, Deps{})
	defer mgr.Close()
	key := ref.Key()

	// The TYPE-level spec (what ProbeDescribe runs the throwaway describe
	// probe with) gets the MINIMUM — none of this — not the union: a pure
	// self-description needs no network, no secrets, no env.
	typeSpec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("type-level spec not found")
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
}

// TestSpecFromRefSharedProcessKeepsUnion proves the other half of the
// decision: a shared_process: true plugin's ONE process must get the UNION
// (it serves every instance, so it needs whatever any of them declared),
// not the minimum.
func TestSpecFromRefSharedProcessKeepsUnion(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	ref.SharedProcess = true
	ref.Network = []string{"a.example:443", "b.example:443"}
	ref.AllowSecrets = []string{"secret-a", "secret-b"}
	ref.AllowEnv = []string{"VAR_A", "VAR_B"}
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Network: []string{"a.example:443"}},
		"b": {Network: []string{"b.example:443"}},
	}
	s := SpecFromRef(ref, "", Installed{}, false)
	if !reflect.DeepEqual(s.Network, ref.Network) {
		t.Fatalf("shared_process spec Network = %v, want the union %v", s.Network, ref.Network)
	}
	if !reflect.DeepEqual(s.AllowSecrets, ref.AllowSecrets) {
		t.Fatalf("shared_process spec AllowSecrets = %v, want the union %v", s.AllowSecrets, ref.AllowSecrets)
	}
	if !reflect.DeepEqual(s.AllowEnv, ref.AllowEnv) {
		t.Fatalf("shared_process spec AllowEnv = %v, want the union %v", s.AllowEnv, ref.AllowEnv)
	}
}
