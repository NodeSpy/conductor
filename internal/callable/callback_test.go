package callable

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// resolveTo builds a resolver that maps any host to a fixed IP — so the SSRF
// guard can be exercised without real DNS and without a name that happens to
// resolve publicly.
func resolveTo(ip string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
}

// TestCallbackSSRFRefusedBeforeDial — a callback_url whose host resolves into a
// blocked range (cloud metadata, loopback, RFC1918, CGNAT) is refused inside
// the dialer, so no request is ever sent to the internal address (#36 §13
// review, item 1). This is the exact hole: the old poster dialed
// http.DefaultClient.Do with zero validation.
func TestCallbackSSRFRefusedBeforeDial(t *testing.T) {
	cases := []struct {
		name, ip string
	}{
		{"cloud-metadata", "169.254.169.254"},
		{"loopback", "127.0.0.1"},
		{"rfc1918", "10.1.2.3"},
		{"cgnat", "100.64.1.1"},
		{"linklocal-v6", "fe80::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &callbackPoster{resolve: resolveTo(c.ip)}
			err := p.post(context.Background(), "https://attacker.example/cb", []byte(`{}`))
			if !errors.Is(err, errCallbackBlockedIP) {
				t.Fatalf("post to host resolving to %s: err = %v, want errCallbackBlockedIP", c.ip, err)
			}
		})
	}
}

// TestCallbackHTTPSByDefault — a plain http:// callback is refused unless the
// operator explicitly opted into http.
func TestCallbackHTTPSByDefault(t *testing.T) {
	p := newCallbackPoster(config.CallableConfig{})
	err := p.post(context.Background(), "http://cb.example/x", []byte(`{}`))
	if !errors.Is(err, errCallbackScheme) {
		t.Fatalf("http callback err = %v, want errCallbackScheme (https required by default)", err)
	}
}

// TestCallbackAllowHTTPOptIn — with callback_allow_http, an http URL is allowed
// through the scheme gate (it still fails at the blocked-IP guard below, which
// proves the scheme gate passed rather than the request being sent).
func TestCallbackAllowHTTPOptIn(t *testing.T) {
	p := newCallbackPoster(config.CallableConfig{CallbackAllowHTTP: true})
	p.resolve = resolveTo("10.0.0.9")
	err := p.post(context.Background(), "http://cb.internal/x", []byte(`{}`))
	if errors.Is(err, errCallbackScheme) {
		t.Fatalf("http callback with allow_http still hit scheme gate: %v", err)
	}
	if !errors.Is(err, errCallbackBlockedIP) {
		t.Fatalf("err = %v, want errCallbackBlockedIP (scheme passed, IP guard still blocks)", err)
	}
}

// TestCallbackAllowHostsOptsIPBackIn — a literal IP in callback_allow_hosts
// opts that exact address back in, so a callback to it is delivered.
func TestCallbackAllowHostsOptsIPBackIn(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		got <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, _, _ := net.SplitHostPort(srv.Listener.Addr().String())

	// Without the opt-in, the loopback test server is blocked.
	p := newCallbackPoster(config.CallableConfig{CallbackAllowHTTP: true})
	if err := p.post(context.Background(), srv.URL, []byte(`{"ok":1}`)); !errors.Is(err, errCallbackBlockedIP) {
		t.Fatalf("loopback callback without opt-in: err = %v, want errCallbackBlockedIP", err)
	}
	// With the exact IP opted in, it is delivered.
	p = newCallbackPoster(config.CallableConfig{CallbackAllowHTTP: true, CallbackAllowHosts: []string{host}})
	if err := p.post(context.Background(), srv.URL, []byte(`{"ok":1}`)); err != nil {
		t.Fatalf("callback to opted-in IP %s failed: %v", host, err)
	}
	select {
	case <-got:
	default:
		t.Fatal("opted-in callback was not delivered")
	}
}

// TestCallbackAllowHostsByName — a HOSTNAME in callback_allow_hosts opts the
// host in by name: whatever it resolves to at dial time (here a private/LAN
// address) is permitted. This is the n8n-on-your-own-network case, where the
// callback sink's IP is DHCP/Docker-assigned and can't be pinned. A host that
// is NOT listed stays blocked.
func TestCallbackAllowHostsByName(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		got <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// The URL uses a name that resolves (via the injected resolver) to the
	// loopback test server — an address the guard blocks by default.
	cbURL := "http://n8n.internal:" + port + "/cb"
	resolveHost := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}

	// A different name that is not listed stays blocked even though allow-list
	// is non-empty.
	p := newCallbackPoster(config.CallableConfig{CallbackAllowHTTP: true, CallbackAllowHosts: []string{"other.internal"}})
	p.resolve = resolveHost
	if err := p.post(context.Background(), cbURL, []byte(`{"ok":1}`)); !errors.Is(err, errCallbackBlockedIP) {
		t.Fatalf("unlisted host callback: err = %v, want errCallbackBlockedIP", err)
	}

	// The exact host, listed by name, is delivered.
	p = newCallbackPoster(config.CallableConfig{CallbackAllowHTTP: true, CallbackAllowHosts: []string{"n8n.internal"}})
	p.resolve = resolveHost
	if err := p.post(context.Background(), cbURL, []byte(`{"ok":1}`)); err != nil {
		t.Fatalf("callback to host opted in by name failed: %v", err)
	}
	select {
	case <-got:
	default:
		t.Fatal("name-opted-in callback was not delivered")
	}
}

// TestCallbackBadURL — a non-absolute or schemeless URL is refused up front.
func TestCallbackBadURL(t *testing.T) {
	p := newCallbackPoster(config.CallableConfig{})
	for _, u := range []string{"", "/just/a/path", "notaurl", "ftp://x/y"} {
		err := p.post(context.Background(), u, []byte(`{}`))
		if err == nil {
			t.Fatalf("post(%q) = nil, want refusal", u)
		}
	}
}
