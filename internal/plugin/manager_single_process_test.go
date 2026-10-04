package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// The single_process capability (pkg/plugin/wire.go Capabilities.
// SingleProcess) is a plugin's own declaration that it keeps a box-global
// resource every configured instance must agree on (the tailscale exposure
// plugin's funnel lease refcount is the motivating case,
// docs/design/plugin-contract.md). Since sharing one process is now the
// DEFAULT (docs/wiki/Plugins.md "Multi-instance isolation"), a single_process
// plugin configured with no isolate: true anywhere needs NO special handling
// at all — it is already served by the one shared process every other
// plugin's non-isolated instances share. The capability only matters once an
// operator asks for isolate: true on one of its instances: that request
// cannot be honored, and must be refused rather than silently folded back
// into the shared process (Manager.ForbidIsolated) — see cmd/conductor's
// loadConnectorPlugins for the full sequence (including the hard config
// validation error for the case the capability is already KNOWN from a
// recorded install manifest, which this package has no install-manifest
// fixture to drive end to end).

// managerWithLocalSingleProcessPlugin builds a Manager for one connector
// plugin reference resolved as a local `use:` build. singleProcess selects
// whether the fixture's own Decl declares the capability; isolated names the
// configured instances (if any) that set isolate: true.
func managerWithLocalSingleProcessPlugin(t *testing.T, singleProcess bool, isolated ...string) (mgr *Manager, key string) {
	t.Helper()
	var bin string
	if singleProcess {
		bin, _ = buildExamplePluginSingleProcess(t)
	} else {
		bin, _ = buildExamplePlugin(t)
	}
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	ref := refFor(t, config.UseKindConnector, bin)
	iso := map[string]bool{}
	for _, n := range isolated {
		iso[n] = true
	}
	ref.Instances = map[string]config.ConnectorGrant{
		"a": {Isolate: iso["a"]},
		"b": {Isolate: iso["b"]},
	}
	key = ref.Key()
	state := LoadInstallState(InstallDir())
	mgr = NewManager(map[string]config.PluginRef{key: ref}, "", state, Deps{
		CallTimeout: 5 * time.Second,
		State:       NewStateStore(t.TempDir()),
	})
	return mgr, key
}

// TestSingleProcessPluginWithNoIsolatedInstanceNeedsNoPromotion proves the
// new default needs no special-case machinery at all: a plugin that declares
// single_process, configured as TWO instances with neither isolate: true, is
// already served by ONE shared process (Spec.Shared, the default) — no
// cold-start promotion step required, since there was never a per-instance
// shape to fold back from.
func TestSingleProcessPluginWithNoIsolatedInstanceNeedsNoPromotion(t *testing.T) {
	mgr, key := managerWithLocalSingleProcessPlugin(t, true)
	defer mgr.Close()
	ctx := context.Background()

	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if !spec.Shared {
		t.Fatal("a connector with no isolated instance must already be Shared = true, single_process or not")
	}

	decl, err := mgr.StartAndDescribe(ctx, key)
	if err != nil {
		t.Fatalf("StartAndDescribe: %v", err)
	}
	if !decl.Capabilities.SingleProcess {
		t.Fatal("fixture built with singleProcess=true must declare Capabilities.SingleProcess")
	}

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("both instances must be served by the SAME shared client")
	}

	echoFrom(t, a, "a", "hello-a")
	echoFrom(t, b, "b", "hello-b")

	pidA, pidB := a.PID(), b.PID()
	if pidA == 0 || pidB == 0 {
		t.Fatalf("expected a live pid serving both instances, got a=%d b=%d", pidA, pidB)
	}
	if pidA != pidB {
		t.Fatalf("single_process must fold both instances onto ONE process; got two pids %d and %d", pidA, pidB)
	}
	if n := len(mgr.InstanceClients(key)); n != 0 {
		t.Fatalf("no isolated instance was ever configured; expected no per-instance client, got %d", n)
	}
}

// TestForbidIsolatedRefusesSingleProcessPluginsIsolatedInstance is the
// late-discovery path end to end against the real fixture: an operator
// configures one shared instance and one isolate: true instance of a plugin
// that (only discoverable by describing it) requires single_process. The
// shared instance keeps working normally; the isolated one must be refused,
// never silently served by the shared process it never agreed to share
// grants with.
func TestForbidIsolatedRefusesSingleProcessPluginsIsolatedInstance(t *testing.T) {
	mgr, key := managerWithLocalSingleProcessPlugin(t, true, "b")
	defer mgr.Close()
	ctx := context.Background()

	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if !spec.Shared {
		t.Fatal("instance a does not isolate, so this key must still have a shared process")
	}

	decl, err := mgr.StartAndDescribe(ctx, key)
	if err != nil {
		t.Fatalf("StartAndDescribe: %v", err)
	}
	if !decl.Capabilities.SingleProcess {
		t.Fatal("fixture built with singleProcess=true must declare Capabilities.SingleProcess")
	}

	// The sequence cmd/conductor's loadConnectorPlugins runs once it sees
	// Capabilities.SingleProcess: forbid every instance that asked for its
	// own process.
	mgr.ForbidIsolated(key, "b", "plugin declares single_process — isolate: true is not possible for it")

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatalf("the non-isolated instance must still resolve: %v", err)
	}
	echoFrom(t, a, "a", "hello-a")

	if _, err := mgr.InstanceClient(key, "b"); err == nil {
		t.Fatal("the isolated instance must be refused once single_process is discovered, got no error")
	}
	if n := len(mgr.InstanceClients(key)); n != 0 {
		t.Fatalf("a refused isolated instance must never get a client; got %d", n)
	}
}

// TestPluginWithoutSingleProcessHonorsIsolate is the negative case this whole
// mechanism must leave alone: a plugin that does NOT declare single_process
// (the ordinary acme-echo build) honors isolate: true normally — the
// isolated instance gets its own distinct process, the non-isolated one
// shares the default.
func TestPluginWithoutSingleProcessHonorsIsolate(t *testing.T) {
	mgr, key := managerWithLocalSingleProcessPlugin(t, false, "b")
	defer mgr.Close()
	ctx := context.Background()

	decl, err := mgr.StartAndDescribe(ctx, key)
	if err != nil {
		t.Fatalf("StartAndDescribe: %v", err)
	}
	if decl.Capabilities.SingleProcess {
		t.Fatal("the plain fixture build must not declare single_process")
	}

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("instance b isolates; it must get its own *Client, distinct from a's shared one")
	}
	echoFrom(t, a, "a", "hello-a")
	echoFrom(t, b, "b", "hello-b")
	if a.PID() == b.PID() {
		t.Fatalf("instance b must run as its own distinct process; both reported pid %d", a.PID())
	}
}
