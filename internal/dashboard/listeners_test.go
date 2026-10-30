package dashboard

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// newAuthServer builds a Server with auth wired (an operator configured and a
// temp auth store) over cfg.
func newAuthServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	cfg.Dashboard.Enabled = true
	s := New(Deps{
		Cfg:      cfg,
		StateDir: t.TempDir(),
		Operators: func() map[string]string {
			return map[string]string{"ops": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake ops@box"}
		},
	})
	if s.auth == nil {
		t.Fatal("auth must be wired")
	}
	return s
}

type authState struct {
	Transport string   `json:"transport"`
	Methods   []string `json:"methods"`
}

func getState(t *testing.T, h http.Handler, host string) (int, authState) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/auth/state", nil)
	r.Host = host
	r.RemoteAddr = "192.0.2.7:4444"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	var st authState
	if rr.Code == http.StatusOK {
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
	}
	return rr.Code, st
}

func hasMethod(st authState, m string) bool {
	for _, x := range st.Methods {
		if x == m {
			return true
		}
	}
	return false
}

func TestBindListenerHostAllowlist(t *testing.T) {
	cfg := &config.Config{Dashboard: config.DashboardConfig{Bind: []config.DashboardBind{
		{Address: "192.0.2.1:7790", Hostnames: []string{"box.lan", "192.0.2.1"}, AllowPlainHTTP: true},
	}}}
	s := newAuthServer(t, cfg)
	h := s.bindHandler(cfg.Dashboard.Bind[0])
	for _, bad := range []string{"evil.example:7790", "box.lan.evil.example", "localhost:7790", ""} {
		if code, _ := getState(t, h, bad); code != http.StatusForbidden {
			t.Errorf("Host %q must be refused (rebinding defence), got %d", bad, code)
		}
	}
	code, st := getState(t, h, "BOX.lan:7790")
	if code != http.StatusOK {
		t.Fatalf("an allow-listed Host must pass, got %d", code)
	}
	if st.Transport != "http" || hasMethod(st, "passkey") || !hasMethod(st, "ssh") || !hasMethod(st, "password") {
		t.Fatalf("plain HTTP off loopback: transport http, no passkey; got %+v", st)
	}
	// Security headers apply on every listener.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "box.lan"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if !strings.Contains(rr.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("bind listener must carry the security headers")
	}
	if rr.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("a plain-HTTP listener sends no HSTS")
	}
}

// A TLS bind sends HSTS (no includeSubDomains) unless hsts: false.
func TestBindListenerHSTS(t *testing.T) {
	off := false
	for _, c := range []struct {
		hsts *bool
		want string
	}{{nil, "max-age=31536000"}, {&off, ""}} {
		b := config.DashboardBind{Address: "0.0.0.0:7790", Hostnames: []string{"box.lan"}, CertFile: "c", KeyFile: "k", HSTS: c.hsts}
		cfg := &config.Config{Dashboard: config.DashboardConfig{Bind: []config.DashboardBind{b}}}
		h := newAuthServer(t, cfg).bindHandler(b)
		for _, host := range []string{"box.lan", "evil.example"} {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = host
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if got := rr.Header().Get("Strict-Transport-Security"); got != c.want {
				t.Errorf("hsts=%v Host %s: Strict-Transport-Security %q, want %q", c.hsts, host, got, c.want)
			}
		}
	}
}

func TestBindListenerTLSIsPasskeyRP(t *testing.T) {
	cfg := &config.Config{Dashboard: config.DashboardConfig{Bind: []config.DashboardBind{
		{Address: "0.0.0.0:7790", Hostnames: []string{"box.lan", "192.0.2.1"}, CertFile: "c", KeyFile: "k"},
	}}}
	s := newAuthServer(t, cfg)
	h := s.bindHandler(cfg.Dashboard.Bind[0])
	_, st := getState(t, h, "box.lan:7790")
	if st.Transport != "https" || !hasMethod(st, "passkey") {
		t.Fatalf("an HTTPS hostname is a passkey RP: %+v", st)
	}
	_, st = getState(t, h, "192.0.2.1:7790")
	if hasMethod(st, "passkey") {
		t.Fatalf("an IP host can't be a passkey RP: %+v", st)
	}
}

func TestPlainHTTPBindRefusedWithoutAck(t *testing.T) {
	cfg := &config.Config{Dashboard: config.DashboardConfig{Bind: []config.DashboardBind{
		{Address: "192.0.2.1:7790", Hostnames: []string{"box.lan"}},
	}}}
	s := newAuthServer(t, cfg)
	if err := s.startBind(context.Background(), cfg.Dashboard.Bind[0]); err == nil || !strings.Contains(err.Error(), "allow_plain_http") {
		t.Fatalf("a plain non-loopback bind without the acknowledgment must not start: %v", err)
	}
}

func TestLocalhostListenerHostAllowlist(t *testing.T) {
	s := newAuthServer(t, &config.Config{})
	h := s.listenerHandler("localhost")
	if code, _ := getState(t, h, "rebind.evil.example:7788"); code != http.StatusForbidden {
		t.Fatalf("the localhost listener must refuse a foreign Host, got %d", code)
	}
	// Host names are case-insensitive here as on every listener.
	for _, good := range []string{"localhost:7788", "127.0.0.1:7788", "[::1]:7788", "LOCALHOST:7788", "LocalHost.:7788"} {
		if code, _ := getState(t, h, good); code != http.StatusOK {
			t.Errorf("Host %q must pass on localhost, got %d", good, code)
		}
	}
	_, st := getState(t, h, "localhost:7788")
	if st.Transport != "localhost" || !hasMethod(st, "passkey") {
		t.Fatalf("http://localhost is a secure context with an RP: %+v", st)
	}
}

// tsFake is a LocalAPI for the tailnet guard: a fixed WhoIs answer.
type tsFake struct{ who tsWho }

func (f tsFake) Status(context.Context) (tsStatus, error)     { return tsStatus{}, nil }
func (f tsFake) WhoIs(context.Context, string) (tsWho, error) { return f.who, nil }
func (f tsFake) CertPair(context.Context, string) (tls.Certificate, error) {
	return tls.Certificate{}, nil
}

func TestTailnetTLSOff(t *testing.T) {
	cfg := &config.Config{Dashboard: config.DashboardConfig{
		Listen:    []string{"localhost", "tailscale"},
		Tailscale: config.DashboardTailscale{TLS: config.DashboardTLS{Off: true}},
	}}
	s := newAuthServer(t, cfg)
	status := tsStatus{MagicDNSName: "box.tail.ts.net", IPs: []string{"100.64.0.1"}}
	h := s.tailnetGuard(tsFake{who: tsWho{Node: "n1", Login: "you@x"}}, status, 7788)
	code, st := getState(t, h, "box.tail.ts.net:7788")
	if code != http.StatusOK || st.Transport != "http" || hasMethod(st, "passkey") || !hasMethod(st, "ssh") {
		t.Fatalf("tls: off is plain HTTP without passkeys: %d %+v", code, st)
	}
	if code, _ := getState(t, h, "evil.example:7788"); code != http.StatusForbidden {
		t.Fatal("the Host allowlist still applies with tls: off")
	}
	tagged := s.tailnetGuard(tsFake{who: tsWho{Node: "n2", Tagged: true}}, status, 7788)
	if code, _ := getState(t, tagged, "box.tail.ts.net:7788"); code != http.StatusForbidden {
		t.Fatal("WhoIs is still enforced with tls: off")
	}
}
