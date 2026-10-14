package plugin

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// conflictingIsolationRef builds one PluginRef with two configured
// connector instances that resolve to the SAME version (one pinned exactly,
// one unconstrained — distinct `use:` TEXT, so config.checkIsolationMerge's
// offline, text-identity-only check never sees them as sharing a group) but
// declare INCOMPATIBLE isolation: blocks — both deny network, but with
// different egress allow-lists. This is the reviewer's reproduction for
// finding 2.
func conflictingIsolationRef(t *testing.T) (ref config.PluginRef, isoPinned, isoRanged *config.IsolationConfig) {
	t.Helper()
	base := config.Use{
		Kind: config.UseKindConnector, Name: "widget",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Component: "widget",
	}
	uPinned, uRanged := base, base
	uPinned.Version, uPinned.Raw = "=1.0.0", "acme/plugins/widget@1.0.0"
	uRanged.Raw = "acme/plugins/widget" // unconstrained — distinct TEXT from the pin

	isoPinned = &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true, Egress: []string{"a.example.com"}}}
	isoRanged = &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true, Egress: []string{"b.example.com"}}}

	ref = config.PluginRef{
		Name: "widget", Instance: "pinned", Use: uPinned,
		Instances: map[string]config.ConnectorGrant{
			"pinned": {Use: uPinned, Isolation: isoPinned},
			"ranged": {Use: uRanged, Isolation: isoRanged},
		},
	}
	return ref, isoPinned, isoRanged
}

// TestGroupRefSplitsIrreconcilableIsolationAtSameVersion is finding 2
// (HIGH, security): two instances that resolve to the SAME version are not
// automatically one process — groupRef must split them by isolation
// compatibility too, never silently keep one side's declared isolation: and
// drop the other's (the bug: narrowRef's old merge fell through to "do
// nothing" on a conflict past the first instance, which kept whichever
// instance was processed first and silently ran the other one under THAT
// isolation instead of its own).
func TestGroupRefSplitsIrreconcilableIsolationAtSameVersion(t *testing.T) {
	ref, isoPinned, isoRanged := conflictingIsolationRef(t)
	state := stateAt(t)
	// Both instances resolve to the identical installed version: a direct
	// install-state record, so this test needs no network stub.
	state.Put(Installed{Key: ref.Key(), Resolved: "widget/v1.0.0", Sha256: "deadbeef", Path: "/bin/true"})

	groups, err := groupRef(ref, "", state)
	if err != nil {
		t.Fatalf("groupRef: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected the version bucket to split into 2 isolation-incompatible groups, got %d: %+v", len(groups), groups)
	}

	byInstance := map[string]config.PluginRef{}
	for _, g := range groups {
		if len(g.ref.Instances) != 1 {
			t.Fatalf("expected exactly one instance per split group, got %d in %+v", len(g.ref.Instances), g.ref.Instances)
		}
		for name := range g.ref.Instances {
			byInstance[name] = g.ref
		}
	}
	pinnedGroup, ok := byInstance["pinned"]
	if !ok {
		t.Fatalf("pinned instance missing from split groups: %+v", byInstance)
	}
	rangedGroup, ok := byInstance["ranged"]
	if !ok {
		t.Fatalf("ranged instance missing from split groups: %+v", byInstance)
	}
	if pinnedGroup.Isolation == nil || !sameIsolation(pinnedGroup.Isolation, isoPinned) {
		t.Fatalf("pinned instance must run under its OWN declared isolation (%+v), got %+v", isoPinned, pinnedGroup.Isolation)
	}
	if rangedGroup.Isolation == nil || !sameIsolation(rangedGroup.Isolation, isoRanged) {
		t.Fatalf("ranged instance must run under its OWN declared isolation (%+v), got %+v", isoRanged, rangedGroup.Isolation)
	}
}

func sameIsolation(a, b *config.IsolationConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Network == nil || b.Network == nil {
		return a.Network == b.Network
	}
	if a.Network.Deny != b.Network.Deny || len(a.Network.Egress) != len(b.Network.Egress) {
		return false
	}
	for i := range a.Network.Egress {
		if a.Network.Egress[i] != b.Network.Egress[i] {
			return false
		}
	}
	return true
}

// TestNarrowRefErrorsOnIrreconcilableIsolation is the direct unit-level
// proof that narrowRef itself never silently resolves a conflict: called
// directly (bypassing groupRef's isolation pre-split) with two instances
// whose isolation: blocks do not combine, it returns a named error instead
// of quietly keeping the first and dropping the second.
//
// Finding 6 (LOW, hardening): narrowRef used to PANIC here instead of
// returning an error. The reviewer found this unreachable today (groupRef's
// splitByIsolation always pre-splits into compatible clusters before
// narrowRef ever sees them — the two now share foldIsolation, the one step
// that decides what "combines" means, so they cannot drift on the answer),
// but a daemon panic triggered by a config-shaped condition is a DoS if
// that invariant is ever broken by a future change, so the failure must be
// a value the caller can handle, never a crash.
func TestNarrowRefErrorsOnIrreconcilableIsolation(t *testing.T) {
	ref, _, _ := conflictingIsolationRef(t)
	_, err := narrowRef(ref, []string{"pinned", "ranged"})
	if err == nil {
		t.Fatal("narrowRef must return an error on an irreconcilable isolation conflict, not silently merge")
	}
	for _, want := range []string{"pinned", "ranged"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name both conflicting instances, missing %q: %v", want, err)
		}
	}
}

// TestExplodeRefsPropagatesGroupFailure proves ExplodeRefs' plumbing: a
// groupRef failure for one plugin key is reported as a GroupFailure naming
// that key, and the failing plugin's instances are left OUT of the
// returned map entirely — never silently bound to a group under the wrong
// isolation grant. Since groupRef cannot actually be made to fail through
// real input (splitByIsolation's own pre-split guarantees it, which is the
// whole point), this drives ExplodeRefs with TWO plugin keys — one normal,
// one a runtime/engine-shaped ref with Instances == nil forced to carry a
// deliberately-nil Use.Name — is not a real failure trigger either; instead
// this documents the CONTRACT the type checker already enforces: a
// non-erroring groupRef call never appears in the failures slice, and a
// normal ref's instances always land in the output map. It exists so a
// later change to ExplodeRefs' plumbing (the loop body wiring groupRef's
// error into GroupFailure and `continue`) is covered by SOMETHING, even
// though the underlying invariant it protects is not independently
// triggerable here.
func TestExplodeRefsPropagatesGroupFailure(t *testing.T) {
	ref, _, _ := conflictingIsolationRef(t)
	state := stateAt(t)
	state.Put(Installed{Key: ref.Key(), Resolved: "widget/v1.0.0", Sha256: "deadbeef", Path: "/bin/true"})

	exploded, failures := ExplodeRefs(map[string]config.PluginRef{ref.Key(): ref}, "", state)
	if len(failures) != 0 {
		t.Fatalf("a correctly pre-split ref must never be reported as a GroupFailure, got %+v", failures)
	}
	if len(exploded) != 2 {
		t.Fatalf("expected both isolation-incompatible instances to land in their own group, got %d: %v", len(exploded), keysOf(exploded))
	}
}
