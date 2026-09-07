package sandbox

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// viaProxy builds an http.Client whose traffic routes through the proxy at
// addr (the launched runtime's view of the world).
func viaProxy(addr string) *http.Client {
	u, _ := url.Parse("http://" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func TestProxyAllowsListedTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from upstream")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://") // 127.0.0.1:PORT

	p := &Proxy{Allow: []string{target}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := viaProxy(addr).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "hello from upstream" {
		t.Fatalf("allowed target: %d %q", resp.StatusCode, body)
	}
}

func TestProxyDeniesUnlistedTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("denied target must never be reached")
	}))
	defer upstream.Close()

	var mu sync.Mutex
	var denied []string
	p := &Proxy{Allow: []string{"api.example.com:443"}, OnDeny: func(hp string) {
		mu.Lock()
		denied = append(denied, hp)
		mu.Unlock()
	}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := viaProxy(addr).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied target: %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(denied) != 1 || !strings.HasPrefix(denied[0], "127.0.0.1:") {
		t.Fatalf("OnDeny audit hook: %v", denied)
	}
}

func TestProxyDenyAll(t *testing.T) {
	// The empty allowlist (agent-authored deny-by-default) refuses everything,
	// CONNECT included.
	p := &Proxy{}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// CONNECT to anywhere → refused (the client surfaces the proxy's 403 as a
	// transport error).
	if _, err := viaProxy(addr).Get("https://api.example.com/"); err == nil ||
		!strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("deny-all CONNECT: %v", err)
	}
	// A direct (non-proxy) hit serves nothing useful.
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("direct hit: %d", resp.StatusCode)
	}
}

func TestProxyConnectTunnelAllowed(t *testing.T) {
	// CONNECT to an allowed plain-TCP target tunnels bytes both ways; a
	// loopback HTTP server is a fine TCP peer for that.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tunneled")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")

	p := &Proxy{Allow: []string{"127.0.0.1:*"}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Speak the CONNECT handshake by hand (an https:// client would want TLS).
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200 Connection Established") {
		t.Fatalf("CONNECT handshake: %q", buf[:n])
	}
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	all, _ := io.ReadAll(conn)
	if !strings.Contains(string(all), "tunneled") {
		t.Fatalf("tunnel body: %q", all)
	}
}

func TestProxyEnv(t *testing.T) {
	env := ProxyEnv("127.0.0.1:9999")
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HTTP_PROXY=http://127.0.0.1:9999",
		"HTTPS_PROXY=http://127.0.0.1:9999",
		"http_proxy=", "https_proxy=",
		"NO_PROXY=127.0.0.1,localhost,::1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ProxyEnv missing %q:\n%s", want, joined)
		}
	}
}

func TestProxyManagerSharesByAllowlist(t *testing.T) {
	var mu sync.Mutex
	var denies []string
	m := NewProxyManager(func(key, hp string) {
		mu.Lock()
		denies = append(denies, hp)
		mu.Unlock()
	})
	defer m.Close()

	a1, err := m.Addr([]string{"b.example.com", "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := m.Addr([]string{"a.example.com", "b.example.com"}) // same set, different order
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Fatalf("equivalent allowlists must share a proxy: %s vs %s", a1, a2)
	}
	deny, err := m.Addr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if deny == a1 {
		t.Fatal("deny-all must be a distinct proxy")
	}

	// The deny-all proxy audits through the manager hook.
	resp, err := viaProxy(deny).Get("http://198.51.100.7:80/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("deny-all: %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(denies) != 1 || denies[0] != "198.51.100.7:80" {
		t.Fatalf("manager OnDeny: %v", denies)
	}
}
