package sandbox

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSpecialAllowedExactBinding: a special-range address crosses the SSRF
// guard only for the target that names it exactly as bound.
func TestSpecialAllowedExactBinding(t *testing.T) {
	lo4, lo6, meta := net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("169.254.169.254")
	for _, tc := range []struct {
		allow      []string
		host, port string
		ip         net.IP
		ok         bool
	}{
		{[]string{"localhost:3000"}, "localhost", "3000", lo4, true},
		{[]string{"localhost:3000"}, "localhost", "3000", lo6, true},
		{[]string{"localhost:3000"}, "localhost", "3001", lo4, false},
		{[]string{"localhost:3000"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"localhost:3000"}, "::1", "3000", lo6, false},
		{[]string{"localhost:3000"}, "localhost", "3000", meta, false},
		{[]string{"localhost:*"}, "localhost", "9", lo4, true},
		{[]string{"localhost"}, "localhost", "443", lo4, true},
		{[]string{"localhost"}, "localhost", "80", lo4, false},
		{[]string{"127.0.0.1:3000"}, "127.0.0.1", "3000", lo4, true},
		{[]string{"127.0.0.1:3000"}, "localhost", "3000", lo4, false},
		{[]string{"127.0.0.1:3000"}, "127.0.0.1", "3001", lo4, false},
		{[]string{"127.0.0.1:3000"}, "rebind.example", "3000", lo4, false}, // a name never inherits a literal
		{[]string{"[::1]:3000"}, "::1", "3000", lo6, true},
		{[]string{"[::1]:3000"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"[::1]:3000"}, "localhost", "3000", lo6, false},
		{[]string{"*"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"127.0.0.*:3000"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"local*:3000"}, "localhost", "3000", lo4, false},
		{[]string{"*.localhost:3000"}, "localhost", "3000", lo4, false},
		{[]string{"169.254.169.254:80"}, "169.254.169.254", "80", meta, true},
		{[]string{"169.254.169.254:80"}, "metadata.evil", "80", meta, false},
		// A local mirror: an exact name reaches the private address it
		// resolves to — RFC1918, a tailnet's CGNAT, ULA, loopback — at its
		// port; never through a wildcard, never link-local or metadata.
		{[]string{"athens.lan:3000"}, "athens.lan", "3000", net.ParseIP("10.1.2.3"), true},
		{[]string{"ATHENS.lan.:3000"}, "athens.lan", "3000", net.ParseIP("192.168.1.9"), true},
		{[]string{"mirror.tailnet.ts.net:4873"}, "mirror.tailnet.ts.net", "4873", net.ParseIP("100.101.102.103"), true},
		{[]string{"devpi.lan:3141"}, "devpi.lan", "3141", net.ParseIP("fd00::5"), true},
		{[]string{"verdaccio.local:4873"}, "verdaccio.local", "4873", lo4, true},
		{[]string{"athens.lan:3000"}, "athens.lan", "3001", net.ParseIP("10.1.2.3"), false},
		{[]string{"athens.lan:3000"}, "other.lan", "3000", net.ParseIP("10.1.2.3"), false},
		{[]string{"*.lan:3000"}, "athens.lan", "3000", net.ParseIP("10.1.2.3"), false},
		{[]string{"athens.*:3000"}, "athens.lan", "3000", net.ParseIP("10.1.2.3"), false},
		{[]string{"*:3000"}, "athens.lan", "3000", net.ParseIP("10.1.2.3"), false},
		{[]string{"athens.lan:80"}, "athens.lan", "80", meta, false},
		{[]string{"athens.lan:80"}, "athens.lan", "80", net.ParseIP("fe80::1"), false},
		{[]string{"athens.lan:80"}, "athens.lan", "80", net.ParseIP("64:ff9b::a9fe:a9fe"), false}, // NAT64 metadata
		{[]string{"athens.lan:80"}, "athens.lan", "80", net.ParseIP("0.0.0.0"), false},
		// A CIDR range holds the literal addresses in it.
		{[]string{"100.64.0.0/10:4873"}, "100.101.102.103", "4873", net.ParseIP("100.101.102.103"), true},
		{[]string{"10.0.0.0/8:*"}, "10.9.9.9", "22", net.ParseIP("10.9.9.9"), true},
		{[]string{"10.0.0.0/8:*"}, "athens.lan", "22", net.ParseIP("10.9.9.9"), false},
		{[]string{"10.0.0.0/8:*"}, "192.168.1.1", "22", net.ParseIP("192.168.1.1"), false},
	} {
		if got := specialAllowed(tc.allow, tc.host, tc.port, tc.ip); got != tc.ok {
			t.Errorf("allow %v, %s:%s → %s: got %v", tc.allow, tc.host, tc.port, tc.ip, got)
		}
	}
}

// TestProxyHostsExactlyAsBound: through the real proxy, localhost, 127.0.0.1
// and [::1] are three bindings, and a name never inherits a listed literal.
func TestProxyHostsExactlyAsBound(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "up") }))
	defer up.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	get := func(allow []string, target string, resolve func(context.Context, string) ([]net.IPAddr, error)) int {
		t.Helper()
		p := &Proxy{Allow: allow, resolveIP: resolve}
		addr, err := p.Start()
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		resp, err := viaProxy(p, addr).Get(target)
		if err != nil {
			return -1
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 200 && string(b) != "up" {
			t.Fatalf("%s: %q", target, b)
		}
		return resp.StatusCode
	}
	if c := get([]string{"localhost:" + port}, "http://localhost:"+port+"/", nil); c != 200 {
		t.Fatalf("localhost bound, localhost reached: %d", c)
	}
	if c := get([]string{"localhost:" + port}, "http://127.0.0.1:"+port+"/", nil); c != 403 {
		t.Fatalf("localhost bound must not imply 127.0.0.1: %d", c)
	}
	if c := get([]string{"127.0.0.1:" + port}, "http://localhost:"+port+"/", nil); c != 403 {
		t.Fatalf("127.0.0.1 bound must not imply localhost: %d", c)
	}
	if c := get([]string{"127.0.0.1:" + port}, "http://127.0.0.1:"+port+"/", nil); c != 200 {
		t.Fatalf("127.0.0.1 bound, reached: %d", c)
	}
	rebind := func(_ context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if c := get([]string{"127.0.0.1:" + port, "*.example:" + port}, "http://app.example:"+port+"/", rebind); c != 403 {
		t.Fatalf("a wildcard name resolving to loopback must not reach it: %d", c)
	}
	if c := get([]string{"127.0.0.1:" + port}, "http://127.0.0.1:"+port+"/", rebind); c != 200 {
		t.Fatalf("literal: %d", c)
	}
	// localhost is loopback, whatever a resolver (a hosts file, a DNS
	// answer) says.
	hosts := func(_ context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
	}
	if c := get([]string{"localhost:" + port}, "http://localhost:"+port+"/", hosts); c != 200 {
		t.Fatalf("localhost resolved by the resolver: %d", c)
	}
	if c := get([]string{"[::1]:" + port}, "http://127.0.0.1:"+port+"/", nil); c != 403 {
		t.Fatalf("[::1] bound must not imply 127.0.0.1: %d", c)
	}
	// [::1] reaches only a server on ::1 (where the box has IPv6 loopback).
	if ln, err := net.Listen("tcp", "[::1]:0"); err == nil {
		up6 := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "up") })}}
		up6.Start()
		defer up6.Close()
		_, p6, _ := net.SplitHostPort(ln.Addr().String())
		if c := get([]string{"[::1]:" + p6}, "http://[::1]:"+p6+"/", nil); c != 200 {
			t.Fatalf("[::1] bound, reached: %d", c)
		}
		if c := get([]string{"[::1]:" + p6}, "http://localhost:"+p6+"/", nil); c != 403 {
			t.Fatalf("[::1] bound must not imply localhost: %d", c)
		}
	}
}

// A local package mirror the operator lists by name: reached over plain
// HTTP (absolute-URI proxying) and over HTTPS (a checked CONNECT), on
// whatever private address the name resolves to — and refused when only a
// wildcard covers it.
func TestLocalMirrorReachableWhenListedExactly(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "mod list") }))
	defer plain.Close()
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tarball") }))
	defer tlsUp.Close()
	_, pport, _ := net.SplitHostPort(strings.TrimPrefix(plain.URL, "http://"))
	_, tport, _ := net.SplitHostPort(strings.TrimPrefix(tlsUp.URL, "https://"))
	lan := func(_ context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil // the mirror's name, on this box
	}
	fetch := func(allow []string, url string) (int, string) {
		t.Helper()
		p := &Proxy{Allow: allow, resolveIP: lan}
		addr, err := p.Start()
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		cred, _ := p.MintSession(SessionOpts{Run: "m", RequireTLS: true})
		c := viaProxyCred(addr, cred)
		c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		resp, err := c.Get(url)
		if err != nil {
			return -1, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, b := fetch([]string{"athens.lan:" + pport}, "http://athens.lan:"+pport+"/github.com/x/@v/list"); c != 200 || b != "mod list" {
		t.Fatalf("plain-HTTP mirror listed by name: %d %q", c, b)
	}
	if c, b := fetch([]string{"verdaccio.lan:" + tport}, "https://verdaccio.lan:"+tport+"/pkg"); c != 200 || b != "tarball" {
		t.Fatalf("HTTPS mirror listed by name: %d %q", c, b)
	}
	if c, _ := fetch([]string{"*.lan:" + pport}, "http://athens.lan:"+pport+"/"); c != 403 {
		t.Fatalf("a wildcard reached a private mirror: %d", c)
	}
	if c, _ := fetch([]string{"*.lan:" + tport}, "https://verdaccio.lan:"+tport+"/pkg"); c != -1 {
		t.Fatalf("a wildcard tunnelled to a private mirror: %d", c)
	}
}
