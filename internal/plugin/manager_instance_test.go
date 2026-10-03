package plugin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Multi-instance isolation (docs/wiki/Plugins.md, docs/design/
// plugin-contract.md): by default every configured connector INSTANCE of an
// external plugin gets its own subprocess, not a process shared by every
// instance of the plugin's type. These tests drive the real acme-echo
// subprocess (buildExamplePlugin, integration_test.go) through a real
// Manager, exactly the path cmd/conductor's loadConnectorPlugins +
// RegisterExternalConnector takes.

// managerWithLocalPlugin builds a Manager with ONE connector plugin reference
// resolved as a local `use: <path>` build (exercising the local-build
// snapshot path too), optionally opted into shared_process:.
func managerWithLocalPlugin(t *testing.T, shared bool) (mgr *Manager, key string) {
	t.Helper()
	bin, _ := buildExamplePlugin(t)
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	ref := refFor(t, config.UseKindConnector, bin)
	ref.SharedProcess = shared
	key = ref.Key()
	state := LoadInstallState(InstallDir())
	mgr = NewManager(map[string]config.PluginRef{key: ref}, "", state, Deps{
		CallTimeout: 5 * time.Second,
		State:       NewStateStore(t.TempDir()),
	})
	return mgr, key
}

func echoFrom(t *testing.T, c *Client, instance, msg string) {
	t.Helper()
	out, err := c.Invoke(context.Background(), InvokeRequest{
		Instance: instance, Verb: "echo", Options: map[string]any{"message": msg},
	})
	if err != nil {
		t.Fatalf("instance %s: invoke: %v", instance, err)
	}
	if out["message"] != msg {
		t.Fatalf("instance %s: echo mismatch: %+v", instance, out)
	}
}

// TestInstanceClientGivesEachInstanceItsOwnProcess is the headline
// multi-instance-isolation guarantee: two configured instances of the SAME
// plugin get two DIFFERENT *Client values, each backed by its own OS process
// (distinct pids) — not one process shared by both.
func TestInstanceClientGivesEachInstanceItsOwnProcess(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, false)
	defer mgr.Close()

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two configured instances must get distinct *Client values by default")
	}
	// Asking again for the same instance returns the SAME client (no second
	// process per call).
	again, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	if again != a {
		t.Fatal("InstanceClient must reuse the same client for the same instance")
	}

	echoFrom(t, a, "a", "hello-a")
	echoFrom(t, b, "b", "hello-b")

	pidA, pidB := a.PID(), b.PID()
	if pidA == 0 || pidB == 0 {
		t.Fatalf("expected two live pids, got a=%d b=%d", pidA, pidB)
	}
	if pidA == pidB {
		t.Fatalf("two instances must run as two distinct processes; both reported pid %d", pidA)
	}

	// Killing one instance's process leaves the other one running untouched.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	echoFrom(t, b, "b", "still-alive")
	if b.PID() != pidB {
		t.Fatalf("instance b's process must survive instance a's teardown unchanged: was %d, now %d", pidB, b.PID())
	}
}

// TestInstanceClientSharedProcessOptOut proves the explicit, documented
// resource trade-off: shared_process: true gives every configured instance
// back the SAME client (and so the same process) — the pre-isolation
// behavior every plugin kind had.
func TestInstanceClientSharedProcessOptOut(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, true)
	defer mgr.Close()

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("shared_process: true must give every instance the SAME *Client")
	}
	// And it must be the Manager's persistent client (Client(key)), not some
	// other instance altogether.
	persistent, ok := mgr.Client(key)
	if !ok || persistent != a {
		t.Fatalf("shared_process must reuse the persistent client: got %v, want %v (ok=%v)", a, persistent, ok)
	}
}

// TestManagerCloseStopsEveryInstance proves teardown reaches every
// per-instance process, not just the first one or a shared one.
func TestManagerCloseStopsEveryInstance(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, false)
	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	echoFrom(t, a, "a", "x")
	echoFrom(t, b, "b", "y")
	if a.PID() == 0 || b.PID() == 0 {
		t.Fatal("expected both instances running before Close")
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	// A subsequent call must re-spawn rather than silently succeed against a
	// dead connection — but more importantly, PID() must now read 0: the
	// client tore its subprocess down.
	if a.PID() != 0 || b.PID() != 0 {
		t.Fatalf("Close must stop every instance's subprocess: a.PID=%d b.PID=%d", a.PID(), b.PID())
	}
}

// TestInstanceHostStateScopedToItsOwnProcess proves the structural half of
// the "instance A can't reach B's host.state/host.auth" guarantee: a
// per-instance client only ever marks ITS OWN instance active, so a
// host.state request naming a SIBLING instance — exactly what a buggy or
// malicious plugin process behind instance "a" might try, since it is the
// identical binary/type as "b" — is refused on a's own client, never
// answered, even with no live process involved (handleRequest is pure
// request routing).
func TestInstanceHostStateScopedToItsOwnProcess(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, false)
	defer mgr.Close()

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	echoFrom(t, a, "a", "x") // marks "a" (and only "a") active on this client

	p, _ := json.Marshal(sdk.HostStateRequest{Instance: "b", Op: "get", Key: "k"})
	res, rpcErr := a.handleRequest(context.Background(), sdk.MethodHostState, p)
	if rpcErr != nil {
		t.Fatalf("unexpected transport-level error: %v", rpcErr)
	}
	hr, ok := res.(sdk.HostStateResult)
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	if hr.OK || hr.Error == "" {
		t.Fatalf("host.state for instance %q must be refused on instance %q's own process, got %+v", "b", "a", hr)
	}

	// Its OWN instance, naturally, is unaffected.
	own, _ := json.Marshal(sdk.HostStateRequest{Instance: "a", Op: "get", Key: "k"})
	res2, rpcErr2 := a.handleRequest(context.Background(), sdk.MethodHostState, own)
	if rpcErr2 != nil {
		t.Fatalf("unexpected transport-level error: %v", rpcErr2)
	}
	hr2 := res2.(sdk.HostStateResult)
	if !hr2.OK {
		t.Fatalf("host.state for instance %q's own client must be answered (State store nil just means empty), got %+v", "a", hr2)
	}
}

// TestManagerReloadSwapsEveryInstance proves the reload half: a moved plugin
// binary is applied to EVERY live per-instance client, not just one.
func TestManagerReloadSwapsEveryInstance(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, false)
	defer mgr.Close()

	a, err := mgr.InstanceClient(key, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "b")
	if err != nil {
		t.Fatal(err)
	}
	echoFrom(t, a, "a", "x")
	echoFrom(t, b, "b", "y")
	pidA, pidB := a.PID(), b.PID()

	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if err := mgr.Reload(key, spec); err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Reload tears the old process down; the next call re-dials a FRESH one —
	// for BOTH instances.
	echoFrom(t, a, "a", "x2")
	echoFrom(t, b, "b", "y2")
	if a.PID() == 0 || a.PID() == pidA {
		t.Fatalf("instance a must be running a NEW process after reload: was %d, now %d", pidA, a.PID())
	}
	if b.PID() == 0 || b.PID() == pidB {
		t.Fatalf("instance b must be running a NEW process after reload: was %d, now %d", pidB, b.PID())
	}
}

// TestInstanceClientRuntimeKindTakesSharedPath is a test gap from finding
// 4(d): a runtime (or engine) key has no "several configured instances of
// one plugin" multiplicity to isolate — InstanceClient must fall through to
// the ONE persistent client Client(key) already holds for it, the same
// path a shared_process: true connector takes, regardless of what
// "instance" string is asked for.
func TestInstanceClientRuntimeKindTakesSharedPath(t *testing.T) {
	bin, _ := buildExamplePlugin(t)
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	ref := refFor(t, config.UseKindRuntime, bin)
	key := ref.Key()
	state := LoadInstallState(InstallDir())
	mgr := NewManager(map[string]config.PluginRef{key: ref}, "", state, Deps{})
	defer mgr.Close()

	persistent, ok := mgr.Client(key)
	if !ok {
		t.Fatal("a runtime key must get its persistent client eagerly at construction")
	}
	a, err := mgr.InstanceClient(key, "whatever-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.InstanceClient(key, "whatever-b")
	if err != nil {
		t.Fatal(err)
	}
	if a != persistent || b != persistent {
		t.Fatalf("a runtime key's InstanceClient must always return the ONE persistent client regardless of instance name: persistent=%v a=%v b=%v", persistent, a, b)
	}
	// And it must never have created any per-instance client at all.
	if n := len(mgr.InstanceClients(key)); n != 0 {
		t.Fatalf("a runtime key must never populate instClients: got %d", n)
	}
}
