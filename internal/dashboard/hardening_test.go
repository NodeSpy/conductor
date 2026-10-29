package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

type sshTestKey struct{ priv, pub string }

func newSSHTestKey(t *testing.T, name string) sshTestKey {
	t.Helper()
	priv := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", priv).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, _ := os.ReadFile(priv + ".pub")
	return sshTestKey{priv: priv, pub: strings.TrimSpace(string(pub))}
}

// browserClient drives a handler like a browser on one connection: it keeps the
// session cookie it is given and sends the CSRF token and JSON content type.
type browserClient struct {
	t      *testing.T
	h      http.Handler
	host   string
	addr   string
	cookie *http.Cookie
	csrf   string
}

func (c *browserClient) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host, r.RemoteAddr = c.host, c.addr
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		r.Header.Set(auth.CSRFHeader, c.csrf)
	}
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, r)
	for _, ck := range rr.Result().Cookies() {
		if strings.Contains(ck.Name, "conductor_session") && ck.Value != "" {
			c.cookie = ck
		}
	}
	return rr
}

// A session cookie sniffed on a plain-HTTP listener carries no step-up. With it
// an attacker must not be able to rewrite who can sign in: swapping an
// operator's ssh_key through the config editor needs a step-up, so the apply is
// refused, the file is unchanged, and the attacker's key does not sign in.
func TestStolenSessionCannotSwapSSHKey(t *testing.T) {
	victim, attacker := newSSHTestKey(t, "victim"), newSSHTestKey(t, "attacker")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgText := "connectors:\n  hooks:\n    use: webhook\n    options:\n      sources: { push: { path: /push } }\n      secret: s3cr3t\n" +
		"triggers:\n  - on: hooks.push\n    steps:\n      - command: [\"true\"]\n" +
		"operators:\n  ops:\n    ssh_key: " + victim.pub + "\n" +
		"dashboard:\n  enabled: true\n  bind:\n    - address: 0.0.0.0:7795\n      hostnames: [box.lan]\n      allow_plain_http: true\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("baseline config must load: %v", err)
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Operators re-reads the live file, as the daemon does after a reload.
	ops := func() map[string]string {
		c, err := config.Load(cfgPath)
		if err != nil {
			return map[string]string{}
		}
		m := map[string]string{}
		for n, o := range c.Operators {
			m[n] = o.SSHKey
		}
		return m
	}
	s := New(Deps{
		Cfg: cfg, Store: st, Events: flow.NewEventHub(), StateDir: dir,
		ConfigPath: cfgPath, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Validate:  func(p string) error { _, err := config.Load(p); return err },
		Reload:    func() error { return nil },
		Operators: ops,
	})
	if s.auth == nil || !s.authEnforced() {
		t.Fatal("auth must be enforced")
	}
	h := s.bindHandler(cfg.Dashboard.Bind[0])

	// The victim's session, replayed by the attacker from another address.
	vr := httptest.NewRequest("POST", "/", nil)
	vr.Host, vr.RemoteAddr = "box.lan:7795", "192.0.2.50:1111"
	vr = auth.WithConn(vr, auth.ConnInfo{Listener: bindListener(cfg.Dashboard.Bind[0]), Transport: auth.TransportHTTP})
	ck, _, err := s.auth.CreateSessionFor(vr, "ops", auth.MethodPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	atk := &browserClient{t: t, h: h, host: "box.lan:7795", addr: "192.0.2.66:2222", cookie: ck}
	var state struct {
		SignedIn bool   `json:"signed_in"`
		CSRF     string `json:"csrf"`
	}
	_ = json.Unmarshal(atk.do("GET", "/api/v1/auth/state", nil).Body.Bytes(), &state)
	if !state.SignedIn {
		t.Fatal("the replayed cookie should be signed in")
	}
	atk.csrf = state.CSRF

	if rr := atk.do("POST", "/api/v1/config/stage", api.StageRequest{Path: []string{"operators", "ops", "ssh_key"}, Value: attacker.pub}); rr.Code != 200 {
		t.Fatalf("stage: %d %s", rr.Code, rr.Body)
	}
	var rev api.ReviewResponse
	_ = json.Unmarshal(atk.do("GET", "/api/v1/config/review", nil).Body.Bytes(), &rev)
	if !rev.Stepup || len(rev.Guarded) == 0 {
		t.Fatalf("the review must say a step-up is needed: %+v", rev)
	}
	rr := atk.do("POST", "/api/v1/config/apply", api.ApplyRequest{Token: rev.Token})
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "stepup_required") {
		t.Fatalf("apply without a step-up: %d %s, want 403 stepup_required", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != cfgText {
		t.Fatal("a refused apply changed the config file")
	}

	// The attacker's own key still does not sign in as ops.
	fresh := &browserClient{t: t, h: h, host: "box.lan:7795", addr: "192.0.2.66:3333"}
	var ch auth.SSHChallengeView
	_ = json.Unmarshal(fresh.do("POST", "/api/v1/auth/ssh/begin", map[string]string{"operator": "ops"}).Body.Bytes(), &ch)
	sig, err := auth.SignSSH(context.Background(), attacker.priv, ch.Namespace, []byte(ch.Text))
	if err != nil {
		t.Fatal(err)
	}
	if rr := fresh.do("POST", "/api/v1/auth/ssh/finish", map[string]string{"challenge_id": ch.ID, "signature": sig}); rr.Code == 200 {
		t.Fatalf("the attacker's key signed in as ops: %s", rr.Body)
	}
}

// The tailnet listener answers only to its MagicDNS name and tailnet IPs. A
// tailnet peer sending Host: localhost would otherwise mint challenges whose
// origin is the localhost listener's — the origin approve-login checks against
// when the operator runs it on the box.
func TestTailnetRefusesLocalhostHost(t *testing.T) {
	cfg := &config.Config{Dashboard: config.DashboardConfig{
		Listen:    []string{"localhost", "tailscale"},
		Tailscale: config.DashboardTailscale{TLS: config.DashboardTLS{Off: true}},
	}}
	s := newAuthServer(t, cfg)
	status := tsStatus{MagicDNSName: "box.tail.ts.net", IPs: []string{"100.64.0.1"}}
	h := s.tailnetGuard(tsFake{who: tsWho{Node: "peer-node", Login: "someone@x"}}, status, 7788)
	for _, host := range []string{"localhost:7788", "127.0.0.1:7788", "[::1]:7788", "app.localhost:7788"} {
		c := &browserClient{t: t, h: h, host: host, addr: "100.64.0.9:4444"}
		if rr := c.do("POST", "/api/v1/auth/ssh/begin", map[string]string{"operator": "ops"}); rr.Code != http.StatusForbidden {
			t.Errorf("tailnet listener with Host %s: %d %s, want 403", host, rr.Code, rr.Body)
		}
	}
	c := &browserClient{t: t, h: h, host: "box.tail.ts.net:7788", addr: "100.64.0.9:4444"}
	if rr := c.do("GET", "/api/v1/auth/state", nil); rr.Code != 200 {
		t.Fatalf("the MagicDNS name must still be served: %d %s", rr.Code, rr.Body)
	}
}

// A loopback bind is a secure context only under a loopback name: a browser
// treats http://box.lan as insecure even when it resolves to 127.0.0.1, so
// passkeys must not be offered there.
func TestLoopbackBindTransport(t *testing.T) {
	lan := config.DashboardBind{Address: "127.0.0.1:7791", Hostnames: []string{"box.lan"}}
	if got := bindTransport(lan); got != auth.TransportHTTP {
		t.Fatalf("loopback bind named box.lan: transport %q, want http", got)
	}
	if rps := bindRPs([]config.DashboardBind{lan}); len(rps) != 0 {
		t.Fatalf("no passkey relying party on an insecure context: %+v", rps)
	}
	local := config.DashboardBind{Address: "127.0.0.1:7792", Hostnames: []string{"localhost", "127.0.0.1"}}
	if got := bindTransport(local); got != auth.TransportLocalhost {
		t.Fatalf("loopback bind named localhost: transport %q, want localhost", got)
	}
	if rps := bindRPs([]config.DashboardBind{local}); len(rps) != 1 || rps[0].ID != "localhost" {
		t.Fatalf("the localhost name is a relying party: %+v", rps)
	}
}
