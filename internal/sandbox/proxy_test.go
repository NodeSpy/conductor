package sandbox

import (
	"context"
	"encoding/base64"
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
func viaProxyCred(addr, cred string) *http.Client {
	u, _ := url.Parse("http://" + proxyUser + ":" + cred + "@" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

// viaProxy mints a fresh credential on p and returns an authenticated client.
func viaProxy(p *Proxy, addr string) *http.Client {
	cred, err := p.MintCred()
	if err != nil {
		panic(err)
	}
	return viaProxyCred(addr, cred)
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

	resp, err := viaProxy(p, addr).Get(upstream.URL)
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

	resp, err := viaProxy(p, addr).Get(upstream.URL)
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
	if _, err := viaProxy(p, addr).Get("https://api.example.com/"); err == nil ||
		!strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("deny-all CONNECT: %v", err)
	}
	// A direct (non-proxy) hit carries no credential → 407 before anything.
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
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
	cred, err := p.MintCred()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte(proxyUser + ":" + cred))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
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
	env := ProxyEnv("127.0.0.1:9999", "tok123")
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HTTP_PROXY=http://conductor:tok123@127.0.0.1:9999",
		"HTTPS_PROXY=http://conductor:tok123@127.0.0.1:9999",
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

	a1, c1, err := m.Endpoint([]string{"b.example.com", "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	a2, c2, err := m.Endpoint([]string{"a.example.com", "b.example.com"}) // same set, different order
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Fatalf("equivalent allowlists must share a proxy: %s vs %s", a1, a2)
	}
	if c1 == c2 || c1 == "" {
		t.Fatal("each dispatch must get its own credential")
	}
	deny, denyCred, err := m.Endpoint(nil)
	if err != nil {
		t.Fatal(err)
	}
	if deny == a1 {
		t.Fatal("deny-all must be a distinct proxy")
	}

	// The deny-all proxy audits through the manager hook.
	resp, err := viaProxyCred(deny, denyCred).Get("http://198.51.100.7:80/")
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

// Regression (#36 iso-review M9): the proxy is a host-wide loopback listener
// — without client auth ANY local process could ride an allowlisted
// profile's egress. Unauthenticated and wrong-credential clients get 407
// before any target matching; only a minted per-dispatch credential passes.
func TestProxyRequiresPerDispatchCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credential must never be forwarded upstream")
		}
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")

	p := &Proxy{Allow: []string{target}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// No credential → 407 (never 403: auth comes before target matching).
	resp, err := viaProxyCred(addr, "").Transport.(*http.Transport).RoundTrip(mustReq(t, upstream.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
	// Wrong credential → 407.
	resp, err = viaProxyCred(addr, "not-a-real-cred").Transport.(*http.Transport).RoundTrip(mustReq(t, upstream.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("wrong credential: %d", resp.StatusCode)
	}
	// A minted credential passes and the allowlist still applies.
	resp, err = viaProxy(p, addr).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("authenticated allowed target: %d %q", resp.StatusCode, body)
	}
	if r2, err := viaProxy(p, addr).Get("http://198.51.100.9:80/"); err == nil {
		r2.Body.Close()
		if r2.StatusCode != http.StatusForbidden {
			t.Fatalf("authenticated but unlisted target: %d", r2.StatusCode)
		}
	}
}

// Regression (#36 iso-review, SSRF round 2): resolution happens daemon-side,
// so an allowlisted *name* that resolves to a special-range address
// (169.254.169.254 cloud metadata, loopback, RFC1918) must be refused — a name
// can never opt into internal space. Only a literal IP in the allowlist opts a
// deliberately-internal target back in. Applies to both CONNECT and plain-HTTP.
func TestProxyRefusesSSRFRebindTarget(t *testing.T) {
	// A resolver that maps a hostname straight at the cloud-metadata address —
	// the classic rebinding payload, without touching real DNS.
	rebind := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "metadata.evil.example" {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}
		return net.DefaultResolver.LookupIPAddr(context.Background(), host)
	}

	// Plain-HTTP path: the allowlisted NAME resolves into link-local → 403+audit,
	// and the connection is refused before any byte reaches the metadata address.
	var mu sync.Mutex
	var denied []string
	p := &Proxy{Allow: []string{"metadata.evil.example:80"}, OnDeny: func(hp string) {
		mu.Lock()
		denied = append(denied, hp)
		mu.Unlock()
	}}
	p.resolveIP = rebind
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := viaProxy(p, addr).Get("http://metadata.evil.example/latest/meta-data/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebind via plain-HTTP must be refused: got %d", resp.StatusCode)
	}
	mu.Lock()
	got := append([]string(nil), denied...)
	mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "metadata.evil.example:") {
		t.Fatalf("rebind refusal must audit the target: %v", got)
	}

	// CONNECT path: same rebinding name, HTTPS tunnel → the proxy's 403 surfaces
	// as a transport error client-side.
	pc := &Proxy{Allow: []string{"metadata.evil.example:443"}}
	pc.resolveIP = rebind
	caddr, err := pc.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := viaProxy(pc, caddr).Get("https://metadata.evil.example/"); err == nil ||
		!strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("rebind via CONNECT must be refused, got %v", err)
	}
}

// Regression companion: a deliberately-internal target the operator listed by
// LITERAL IP is reachable — the opt-in the SSRF guard must not break. A
// loopback httptest server is in a special range (IsLoopback), so allowlisting
// it by its explicit 127.0.0.1 address exercises exactly that opt-in.
func TestProxyAllowsExplicitlyListedInternalIP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "internal-ok")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://") // 127.0.0.1:PORT — literal IP

	p := &Proxy{Allow: []string{target}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := viaProxy(p, addr).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "internal-ok" {
		t.Fatalf("explicit-IP internal target must be allowed: %d %q", resp.StatusCode, body)
	}
}

func mustReq(t *testing.T, u string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Proxy the request by hand: absolute URI at the proxy address.
	return r
}

// Regression (#36 iso-review C1): the full enforced-egress chain in-process
// — a client speaks to the sandbox-side TCP forwarder, which pipes into the
// proxy's unix socket; the allowlist and the per-dispatch credential both
// apply, and DNS/exfil targets outside the list are refused at the proxy.
func TestEnforcedEgressChainOverUnixSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "through the wall")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")

	m := NewProxyManager(nil)
	defer m.Close()
	sock, cred, err := m.UnixEndpoint([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	// Same allowlist → same socket; each dispatch gets its own credential.
	sock2, cred2, err := m.UnixEndpoint([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	if sock2 != sock || cred2 == cred {
		t.Fatalf("socket shared, creds distinct: %q vs %q, %v", sock, sock2, cred2 == cred)
	}

	// The in-sandbox forwarder: TCP on loopback → the unix socket.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveForward(ln, sock)

	client := viaProxyCred(ln.Addr().String(), cred)
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "through the wall" {
		t.Fatalf("allowed target through the chain: %d %q", resp.StatusCode, body)
	}
	// An unlisted target is refused by the proxy on the far side of the wall.
	r2, err := client.Get("http://198.51.100.10:80/")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("unlisted target: %d", r2.StatusCode)
	}
	// No credential → 407 even through the forwarder.
	r3, err := viaProxyCred(ln.Addr().String(), "wrong").Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if r3.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("forwarder must not bypass auth: %d", r3.StatusCode)
	}
}
