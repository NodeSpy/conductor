package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// The single_process capability (pkg/plugin/wire.go Capabilities.
// SingleProcess) is a plugin's own override of multi-instance isolation
// (manager_instance_test.go): a plugin that keeps a box-global resource every
// configured instance must agree on (the tailscale exposure plugin's funnel
// lease refcount is the motivating case, docs/design/plugin-contract.md)
// gets the shared-process path regardless of the operator's own
// shared_process: setting. These tests drive the real acme-echo subprocess,
// built with its singleProcess build flag set (buildExamplePluginSingleProcess,
// integration_test.go), through the COLD-START promotion path
// (Manager.PromoteSharedProcess) cmd/conductor's loadConnectorPlugins takes
// the first time a boot ever learns the capability live — exactly the path a
// freshly-added or locally-built single_process plugin takes.

// managerWithLocalSingleProcessPlugin builds a Manager for one connector
// plugin reference resolved as a local `use:` build, WITHOUT shared_process:
// set — the default per-instance shape SpecFromRef gives it, since a local
// reference never gets a recorded manifest (single_process is not knowable
// ahead of a live describe for one). singleProcess selects whether the
// fixture's own Decl declares the capability.
func managerWithLocalSingleProcessPlugin(t *testing.T, singleProcess bool) (mgr *Manager, ref config.PluginRef, key string) {
	t.Helper()
	var bin string
	if singleProcess {
		bin, _ = buildExamplePluginSingleProcess(t)
	} else {
		bin, _ = buildExamplePlugin(t)
	}
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	ref = refFor(t, config.UseKindConnector, bin)
	key = ref.Key()
	state := LoadInstallState(InstallDir())
	mgr = NewManager(map[string]config.PluginRef{key: ref}, "", state, Deps{
		CallTimeout: 5 * time.Second,
		State:       NewStateStore(t.TempDir()),
	})
	return mgr, ref, key
}

// TestPromoteSharedProcessFoldsSingleProcessPluginIntoOneProcess is the
// headline guarantee: a plugin that declares single_process, configured as
// TWO instances with no shared_process: set anywhere, ends up served by ONE
// process (one pid) once the host follows the same cold-start sequence
// cmd/conductor's loadConnectorPlugins does — ProbeDescribe (learns the
// capability), PromoteSharedProcess (flips the key's shape), StartAndDescribe
// (starts the real, kept-running process) — and BOTH configured instances
// are served by that same client, never a per-instance one.
func TestPromoteSharedProcessFoldsSingleProcessPluginIntoOneProcess(t *testing.T) {
	mgr, ref, key := managerWithLocalSingleProcessPlugin(t, true)
	defer mgr.Close()
	ctx := context.Background()

	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if spec.SharedProcess {
		t.Fatal("a local plugin with no recorded manifest must start in the default per-instance shape, not already shared")
	}

	decl, err := mgr.ProbeDescribe(ctx, key)
	if err != nil {
		t.Fatalf("ProbeDescribe: %v", err)
	}
	if !decl.Capabilities.SingleProcess {
		t.Fatal("fixture built with singleProcess=true must declare Capabilities.SingleProcess")
	}

	// Build the shared Spec exactly as loadConnectorPlugins' promotion does:
	// the type-level spec, reshaped to the SharedProcess branch's fields.
	shared := spec
	shared.SharedProcess = true
	shared.Instances, shared.Probe = nil, false
	shared.Isolation, shared.IsolationDefaulted = ref.Isolation, ref.IsolationDefaulted
	shared.Network, shared.AllowSecrets, shared.AllowEnv = ref.Network, ref.AllowSecrets, ref.AllowEnv

	promoted, err := mgr.PromoteSharedProcess(key, shared)
	if err != nil {
		t.Fatalf("PromoteSharedProcess: %v", err)
	}
	if _, err := mgr.StartAndDescribe(ctx, key); err != nil {
		t.Fatalf("StartAndDescribe after promotion: %v", err)
	}

	// The Manager's own spec now reflects the shared shape too.
	if s, _ := mgr.Spec(key); !s.SharedProcess {
		t.Fatalf("Manager.Spec must reflect the promotion, got %+v", s)
	}

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a != promoted || b != promoted {
		t.Fatalf("every instance must be served by the SAME promoted client, got a=%v b=%v want %v", a, b, promoted)
	}
	if a != b {
		t.Fatal("two configured instances must get the SAME *Client once promoted")
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

	// No per-instance client was ever created — both instances were served
	// by the promoted persistent client from the start.
	if n := len(mgr.InstanceClients(key)); n != 0 {
		t.Fatalf("single_process must never create a per-instance client; got %d", n)
	}
}

// TestPromoteSharedProcessRefusesOncePerInstanceClientsExist: folding
// already-split per-instance processes back into one mid-flight is
// disruptive (stopping some, keeping another, redirecting in-flight calls)
// and out of scope — PromoteSharedProcess must refuse once this key already
// has a live per-instance client, rather than silently discarding it.
func TestPromoteSharedProcessRefusesOncePerInstanceClientsExist(t *testing.T) {
	mgr, _, key := managerWithLocalSingleProcessPlugin(t, false)
	defer mgr.Close()

	if _, err := mgr.InstanceClient(key, "a"); err != nil {
		t.Fatal(err)
	}
	spec, _ := mgr.Spec(key)
	shared := spec
	shared.SharedProcess = true
	if _, err := mgr.PromoteSharedProcess(key, shared); err == nil {
		t.Fatal("PromoteSharedProcess must refuse once a per-instance client already exists")
	}
}

// TestPromoteSharedProcessUnknownKey: a key the Manager never resolved
// (misuse, or a stale key from a reload race) must be refused, not silently
// create a client for nothing.
func TestPromoteSharedProcessUnknownKey(t *testing.T) {
	mgr, _, _ := managerWithLocalSingleProcessPlugin(t, false)
	defer mgr.Close()
	if _, err := mgr.PromoteSharedProcess("connectors/does-not-exist", Spec{}); err == nil {
		t.Fatal("PromoteSharedProcess must refuse an unknown key")
	}
}

// TestPluginWithoutSingleProcessStaysPerInstance is the negative case this
// whole mechanism must leave alone: a plugin that does NOT declare
// single_process (the ordinary acme-echo build) still gets the default
// per-instance-isolated shape end to end — ProbeDescribe reports no
// capability, nothing promotes it, and InstanceClient keeps handing out a
// distinct *Client (and process) per configured instance, exactly as
// TestInstanceClientGivesEachInstanceItsOwnProcess (manager_instance_test.go)
// already proves for the plain default path.
func TestPluginWithoutSingleProcessStaysPerInstance(t *testing.T) {
	mgr, _, key := managerWithLocalSingleProcessPlugin(t, false)
	defer mgr.Close()
	ctx := context.Background()

	decl, err := mgr.ProbeDescribe(ctx, key)
	if err != nil {
		t.Fatalf("ProbeDescribe: %v", err)
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
		t.Fatal("without single_process, two configured instances must still get distinct *Client values")
	}
	echoFrom(t, a, "a", "hello-a")
	echoFrom(t, b, "b", "hello-b")
	if a.PID() == b.PID() {
		t.Fatalf("without single_process, two instances must run as two distinct processes; both reported pid %d", a.PID())
	}
}
