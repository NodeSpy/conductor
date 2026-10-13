package exposure

import (
	"context"
	"net"
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

// LAN-IP detection, for a `lan` exposure with no `host:` (restored from the
// former tunnel provider's tests).
func TestIsPrivateIPv4(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"10.0.0.5", true}, {"172.16.0.1", true}, {"172.31.255.255", true}, {"172.32.0.1", false},
		{"192.168.1.1", true}, {"8.8.8.8", false},
		{"127.0.0.1", false}, // loopback is not a LAN address here
		{"203.0.113.5", false}, {"::1", false},
	} {
		if got := isPrivateIPv4(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("isPrivateIPv4(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

// The real detector may legitimately fail in an offline sandbox; it must
// never return a non-private or unparseable address.
func TestDetectLANIPReturnsPrivateOrErrorsCleanly(t *testing.T) {
	ip, err := detectLANIP()
	if err != nil {
		t.Logf("no LAN IP in this environment: %v", err)
		return
	}
	if p := net.ParseIP(ip); p == nil || !isPrivateIPv4(p) {
		t.Fatalf("detectLANIP returned %q", ip)
	}
}
