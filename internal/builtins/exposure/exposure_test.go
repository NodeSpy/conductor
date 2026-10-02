package exposure

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

func open(t *Tunnel, inst string, conn map[string]any) (plugin.InvokeResult, error) {
	return t.Invoke(plugin.InvokeRequest{Instance: inst, Verb: "open", Options: map[string]any{"local_addr": "127.0.0.1:8099"}, Connection: conn})
}

func TestTunnelReadsTheURLAndReleases(t *testing.T) {
	tn := NewTunnel()
	res, err := open(tn, "a", map[string]any{
		"command":     []any{"sh", "-c", "echo noise https://ignored.example; echo 'ready: tcp://x.example:{{.port}}'; sleep 30"},
		"url_pattern": `tcp://\S+`,
	})
	if err != nil || res.Outputs["public_url"] != "tcp://x.example:8099" {
		t.Fatalf("open: %v %v", res.Outputs, err)
	}
	if _, err := open(tn, "b", map[string]any{"command": []any{"sh", "-c", "echo https://b.example; sleep 30"}}); err != nil {
		t.Fatal(err)
	}
	if tn.Leases() != 2 {
		t.Fatalf("leases = %d", tn.Leases())
	}
	// plugin.stop releases exactly that instance's leases.
	_ = tn.Stop(context.Background(), plugin.StopRequest{Instance: "a"})
	if tn.Leases() != 1 {
		t.Fatalf("stop a: leases = %d, want 1", tn.Leases())
	}
	_, _ = tn.Invoke(plugin.InvokeRequest{Verb: "close", Options: map[string]any{"lease": firstLease(tn)}})
	if tn.Leases() != 0 {
		t.Fatalf("close: leases = %d", tn.Leases())
	}
}

func TestTunnelFailures(t *testing.T) {
	tn := NewTunnel()
	tn.Start = 300 * time.Millisecond
	for name, conn := range map[string]map[string]any{
		"missing binary": {"command": []any{"no-such-tunnel-binary-xyz"}},
		"no url":         {"command": []any{"sh", "-c", "sleep 5"}},
		"no command":     {},
		"bad pattern":    {"command": []any{"sh"}, "url_pattern": "("},
	} {
		if _, err := open(tn, "a", conn); err == nil {
			t.Fatalf("%s: open succeeded", name)
		}
	}
	if tn.Leases() != 0 {
		t.Fatal("a failed open left a lease")
	}
}

func TestLANHostAndDeclarations(t *testing.T) {
	res, err := LAN{}.Invoke(plugin.InvokeRequest{Verb: "open", Options: map[string]any{"local_addr": "0.0.0.0:9"}, Connection: map[string]any{"host": "10.0.0.5"}})
	if err != nil || res.Outputs["public_url"] != "http://10.0.0.5:9" {
		t.Fatalf("lan: %v %v", res.Outputs, err)
	}
	for _, d := range []plugin.Decl{LAN{}.Describe(), NewTunnel().Describe()} {
		if p := plugin.ValidateSemantics(d); len(p) > 0 {
			t.Fatalf("%s: %v", d.Type, p)
		}
		if strings.Contains(strings.ToLower(d.Desc), "cloudflare") {
			t.Fatalf("%s names a tunnel vendor", d.Type)
		}
	}
}

func firstLease(t *Tunnel) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.leases {
		return id
	}
	return ""
}
