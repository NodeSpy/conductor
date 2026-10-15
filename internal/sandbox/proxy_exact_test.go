package sandbox

import (
	"context"
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
		{[]string{"127.0.0.1:3000"}, "rebind.example", "3000", lo4, false},
		{[]string{"[::1]:3000"}, "::1", "3000", lo6, true},
		{[]string{"[::1]:3000"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"[::1]:3000"}, "localhost", "3000", lo6, false},
		{[]string{"*"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"127.0.0.*:3000"}, "127.0.0.1", "3000", lo4, false},
		{[]string{"local*:3000"}, "localhost", "3000", lo4, false},
		{[]string{"*.localhost:3000"}, "localhost", "3000", lo4, false},
		{[]string{"169.254.169.254:80"}, "169.254.169.254", "80", meta, true},
		{[]string{"169.254.169.254:80"}, "metadata.evil", "80", meta, false},
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
	if c := get([]string{"127.0.0.1:" + port, "app.example:" + port}, "http://app.example:"+port+"/", rebind); c != 403 {
		t.Fatalf("a name rebinding to a listed literal must not inherit it: %d", c)
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
