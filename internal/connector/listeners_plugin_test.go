package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestListenersEndToEndThroughARealPluginAndTunnel is the full stack, with a
// REAL subprocess and a REAL (if fake-scripted) tunnel: the acme-listener
// reference plugin (test/plugins/acme-listener) declares the `listeners`
// connection semantic exactly as the github plugin does for its webhook, and
// this test builds it into a connector.Registry the way the daemon does
// (RegisterExternalConnector + Build), opens it through the builtin `tunnel`
// exposure, and proves both halves of the contract:
//
//  1. the public URL the tunnel returns reaches the plugin in its config
//     (echoed back as the first event's context.public_url — the plugin
//     never sees it any other way);
//  2. an HTTP delivery to the bound listener address fires a real event,
//     which comes out the other end as a core.Trigger.
//
// It also proves the lease lifecycle: open while the source runs, released
// once Start returns (ctx cancelled — stop/reload).
func TestListenersEndToEndThroughARealPluginAndTunnel(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bin, sum := buildAcmeListenerPlugin(t)
	addr := freeLoopbackAddr(t)

	spec := plugin.Spec{Name: "acme-listener", Kind: plugin.KindConnector, Provides: "acme-listener", BinPath: bin, Sha256: sum}
	cl := plugin.NewClient(spec, plugin.Deps{})
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	decl, err := cl.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if _, err := RegisterExternalConnector(cl, spec, decl); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer UnregisterExternalType("acme-listener")

	cfg := mustDecodeConfig(t, fmt.Sprintf(`
connectors:
  tun:
    use: tunnel
    command: [sh, -c, "echo serving at https://hook.example/{{.port}}; sleep 30"]
  src:
    use: acme-listener
    listen: %s
    expose: tun
`, addr))
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	in, ok := reg.Get("src")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("src disabled: %+v", in)
	}

	enabled := true
	trigs := []CompiledTrigger{
		{Index: 0, Spec: config.TriggerSpec{On: "src.ready", Enabled: &enabled}},
		{Index: 1, Spec: config.TriggerSpec{On: "src.delivery", Enabled: &enabled}},
	}
	srcIntegration, err := in.Impl.Source(trigs)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if srcIntegration == nil {
		t.Fatal("Source returned nil integration")
	}

	triggers := make(chan core.Trigger, 8)
	emit := func(_ context.Context, tr core.Trigger) { triggers <- tr }

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srcIntegration.Start(runCtx, emit) }()
	// stop cancels the source and waits for it to actually return (so its
	// exposure lease is released) exactly once, however the test exits —
	// including a t.Fatalf partway through, which would otherwise leak this
	// lease (and the subprocess) into whichever test runs next in this
	// process, since Build's `tunnel` builtin handler is a package-wide
	// singleton (inprocessTunnel).
	stop := sync.OnceValue(func() error {
		runCancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return fmt.Errorf("Start did not return after cancel")
		}
	})
	t.Cleanup(func() { _ = stop() })

	// 1. The exposure's public URL reached the plugin's config before it
	// ever emitted anything — proven by its own first event, "ready".
	var port string
	if _, p, err := net.SplitHostPort(addr); err == nil {
		port = p
	}
	wantURL := "https://hook.example/" + port
	select {
	case tr := <-triggers:
		if tr.Kind != "ready" {
			t.Fatalf("first trigger kind = %q, want ready", tr.Kind)
		}
		got, _ := tr.Context["public_url"].(string)
		if got != wantURL {
			t.Fatalf("ready context.public_url = %q, want %q (url_to did not reach the plugin)", got, wantURL)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no ready event — the plugin never started (exposure likely never opened)")
	}

	// The tunnel builtin has exactly one lease open while the source runs.
	tun := mustTunnel(t, reg)
	if n := tun.leaseCount(); n != 1 {
		t.Fatalf("leases = %d while running, want 1", n)
	}

	// 2. A real HTTP POST straight at the bound listener address fires a
	// delivery event — proving the listener itself is live and wired to the
	// engine, independent of whether the (fake, printing-only) tunnel
	// forwards real traffic.
	resp, err := http.Post("http://"+addr+"/", "application/json", strings.NewReader(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("post to listener: %v", err)
	}
	resp.Body.Close()

	select {
	case tr := <-triggers:
		if tr.Kind != "delivery" {
			t.Fatalf("second trigger kind = %q, want delivery", tr.Kind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no delivery event — a POST to the listener did not fire a trigger")
	}

	// Stop (ctx cancelled): Start returns, and the exposure's lease is
	// released — the host releases on stop or reload, never before.
	if err := stop(); err == nil {
		t.Fatal("Start returned nil, want context.Canceled")
	}
	if n := tun.leaseCount(); n != 0 {
		t.Fatalf("leases = %d after stop, want 0 (released)", n)
	}
}

// buildAcmeListenerPlugin compiles the reference acme-listener SOURCE plugin
// (test/plugins/acme-listener), mirroring internal/plugin's
// buildTickerPlugin.
func buildAcmeListenerPlugin(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-listener")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-listener")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-listener plugin: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// freeLoopbackAddr finds an unused loopback TCP port by binding :0 and
// immediately releasing it — the same port is then handed to the plugin's
// `listen` config, which binds it itself a few lines later.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}
