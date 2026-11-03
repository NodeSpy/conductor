package agentsandbox

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPlanGateways(t *testing.T) {
	// A key in the environment: gatewayed to the provider.
	ps, left, err := planGateways("claude-code", []string{"ANTHROPIC_API_KEY=sk-real"}, nil)
	if err != nil || len(ps) != 1 || len(left) != 0 {
		t.Fatalf("plans %+v left %+v %v", ps, left, err)
	}
	if g := ps[0]; g.key != "sk-real" || g.header != "x-api-key" || g.upstream.String() != "https://api.anthropic.com" || g.loopback != "" || !strings.HasPrefix(g.token, "cgw-") {
		t.Fatalf("plan %+v", g)
	}
	// A bearer token and a loopback router from settings.json; the
	// environment wins over settings.
	ps, _, _ = planGateways("claude-code", []string{"ANTHROPIC_AUTH_TOKEN=env-tok"},
		map[string]string{"ANTHROPIC_AUTH_TOKEN": "settings-tok", "ANTHROPIC_BASE_URL": "http://localhost:3456"})
	if len(ps) != 1 || ps[0].key != "env-tok" || ps[0].header != "bearer" || ps[0].loopback != "127.0.0.1:3456" {
		t.Fatalf("plan %+v", ps)
	}
	// A router that needs no key is still reached only through conductor.
	ps, _, _ = planGateways("claude-code", []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:3456"}, nil)
	if len(ps) != 1 || ps[0].header != "" || ps[0].loopback != "127.0.0.1:3456" {
		t.Fatalf("keyless router plan %+v", ps)
	}
	// An OAuth login (no key, the provider directly): not gatewayed.
	ps, left, _ = planGateways("claude-code", nil, nil)
	if len(ps) != 0 || len(left) != 1 {
		t.Fatalf("oauth: plans %+v left %+v", ps, left)
	}
	// Two launches never share a token.
	a, _, _ := planGateways("codex", []string{"OPENAI_API_KEY=k"}, nil)
	b, _, _ := planGateways("codex", []string{"OPENAI_API_KEY=k"}, nil)
	if a[0].token == b[0].token {
		t.Fatal("token reused")
	}
	// opencode configures its own providers: not gatewayed.
	if ps, _, _ := planGateways("opencode", []string{"ANTHROPIC_API_KEY=k"}, nil); len(ps) != 0 {
		t.Fatal("opencode gatewayed")
	}
}

type seenReq struct {
	path, query string
	hdr         http.Header
	body        string
}

func upstream(t *testing.T) (*httptest.Server, *[]seenReq, *sync.Mutex) {
	var mu sync.Mutex
	var seen []seenReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenReq{r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: ok\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &seen, &mu
}

func TestGatewaySwapsTheTokenForTheKey(t *testing.T) {
	up, seen, mu := upstream(t)
	u, _ := url.Parse(up.URL + "/base")
	for _, hdr := range []string{"x-api-key", "bearer", "x-goog-api-key"} {
		*seen = nil
		g := gatewayPlan{upstream: u, header: hdr, key: "sk-REAL", token: "cgw-launch"}
		var refused []string
		gw := httptest.NewServer(gatewayHandler(g, func(s string) { refused = append(refused, s) }))
		// Without the launch's token (or with the real key, or another
		// launch's token): refused, the upstream never reached.
		for _, h := range []map[string]string{{}, {"X-Api-Key": "sk-REAL"}, {"Authorization": "Bearer cgw-other"}, {"Authorization": "cgw-launch"}} {
			req, _ := http.NewRequest("POST", gw.URL+"/v1/messages", strings.NewReader("{}"))
			for k, v := range h {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %v: status %d", hdr, h, resp.StatusCode)
			}
		}
		if len(*seen) != 0 || len(refused) != 4 {
			t.Fatalf("refused requests reached the upstream: %d, reported %d", len(*seen), len(refused))
		}
		// With it, in any header a model client uses.
		for _, h := range [][2]string{{"X-Api-Key", "cgw-launch"}, {"Authorization", "Bearer cgw-launch"}, {"X-Goog-Api-Key", "cgw-launch"}} {
			req, _ := http.NewRequest("POST", gw.URL+"/v1/messages?beta=true&key=cgw-launch", strings.NewReader(`{"m":1}`))
			req.Header.Set(h[0], h[1])
			req.Header.Set("Anthropic-Version", "2023-06-01")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != "data: ok\n\n" {
				t.Fatalf("%s: %d %q", hdr, resp.StatusCode, body)
			}
		}
		gw.Close()
		mu.Lock()
		if len(*seen) != 3 {
			t.Fatalf("%s: upstream saw %d", hdr, len(*seen))
		}
		for _, s := range *seen {
			if s.path != "/base/v1/messages" || s.query != "beta=true" || s.body != `{"m":1}` || s.hdr.Get("Anthropic-Version") != "2023-06-01" {
				t.Fatalf("%s: forwarded %+v", hdr, s)
			}
			all := strings.Join([]string{s.hdr.Get("Authorization"), s.hdr.Get("X-Api-Key"), s.hdr.Get("X-Goog-Api-Key"), s.query}, " ")
			if strings.Contains(all, "cgw-launch") {
				t.Fatalf("%s: the launch's token reached the upstream: %v", hdr, s.hdr)
			}
			want := map[string]string{"x-api-key": s.hdr.Get("X-Api-Key"), "bearer": strings.TrimPrefix(s.hdr.Get("Authorization"), "Bearer "), "x-goog-api-key": s.hdr.Get("X-Goog-Api-Key")}[hdr]
			if want != "sk-REAL" {
				t.Fatalf("%s: the real key was not sent: %v", hdr, s.hdr)
			}
		}
		mu.Unlock()
	}
}

// A request names a path, never a host: the gateway reaches only its
// upstream.
func TestGatewayReachesOnlyItsUpstream(t *testing.T) {
	up, seen, _ := upstream(t)
	other, otherSeen, _ := upstream(t)
	u, _ := url.Parse(up.URL)
	gw := httptest.NewServer(gatewayHandler(gatewayPlan{upstream: u, header: "bearer", key: "k", token: "t"}, nil))
	defer gw.Close()
	ou, _ := url.Parse(other.URL)
	c, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	// An absolute-form request and a Host header naming the other server.
	io.WriteString(c, "GET "+other.URL+"/steal HTTP/1.1\r\nHost: "+ou.Host+"\r\nAuthorization: Bearer t\r\nConnection: close\r\n\r\n")
	io.ReadAll(c)
	c.Close()
	if len(*otherSeen) != 0 || len(*seen) != 1 || (*seen)[0].path != "/steal" {
		t.Fatalf("other %d, upstream %+v", len(*otherSeen), *seen)
	}
}

func TestAccessOnlyCopy(t *testing.T) {
	in := `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":1,"scopes":["a"]},
	  "tokens":{"access_token":"at2","refresh_token":"rt2","id_token":"id"},"OPENAI_API_KEY":"sk","last_refresh":"x",
	  "anthropic":{"type":"oauth","access":"a3","refresh":"r3"},"list":[{"refresh_token":"r4","x":1}]}`
	b, err := accessOnlyCopy([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, gone := range []string{`"rt"`, `"rt2"`, `"r3"`, `"r4"`, `"sk"`, "refresh", "OPENAI_API_KEY"} {
		if strings.Contains(s, gone) {
			t.Errorf("copy keeps %s: %s", gone, s)
		}
	}
	for _, kept := range []string{`"accessToken":"at"`, `"access_token":"at2"`, `"access":"a3"`, `"expiresAt":1`, `"id_token":"id"`} {
		if !strings.Contains(s, kept) {
			t.Errorf("copy lost %s: %s", kept, s)
		}
	}
	if b, _ := accessOnlyCopy([]byte("not json")); b != nil {
		t.Fatal("a file that is not JSON is not copied")
	}
}

// The default launch: the key stays on the host, the agent gets a launch
// token and the gateway's address, the router and provider hosts are not
// reachable directly, and no login file is placed.
func TestWrapGatewaysTheModel(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			withPlatform(t, goos)
			f := newPolicyFixture(t)
			f.pol.ModelCredential = ""
			f.pol.Egress = []string{"api.anthropic.com", "proxy.golang.org"}
			f.pol.ModelHosts = []string{"api.anthropic.com"}
			// The operator's router: a test server (never whatever listens on
			// this box's loopback).
			router, seen, mu := upstream(t)
			rAddr := strings.TrimPrefix(router.URL, "http://")
			f.pol.Loopback = []string{rAddr}
			for i, kv := range f.deps.Inherited {
				if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
					f.deps.Inherited[i] = "ANTHROPIC_BASE_URL=" + router.URL
				}
			}
			// A stale shared link and a seeded key in settings.json.
			os.MkdirAll(filepath.Join(f.pol.Home, ".claude"), 0o700)
			os.Symlink(f.pol.Logins[0].Src, f.pol.Logins[0].Path)
			os.WriteFile(filepath.Join(f.pol.Home, ".claude", "settings.json"),
				[]byte(`{"model":"opus","apiKeyHelper":"/bin/key","env":{"ANTHROPIC_API_KEY":"sk-seeded","ANTHROPIC_BASE_URL":"http://127.0.0.1:3456","FOO":"1"}}`), 0o600)
			os.WriteFile(filepath.Join(f.pol.Home, ".claude.json"), []byte(`{"userID":"u","primaryApiKey":"sk-stored"}`), 0o600)
			l, err := Wrap(f.pol, []string{"claude"}, f.deps)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Cleanup()
			env := envMap(l.Env)
			for _, kv := range l.Env {
				if strings.Contains(kv, "sk-a") {
					t.Fatalf("the operator's key reached the agent: %s", kv)
				}
			}
			if _, ok := env["ANTHROPIC_API_KEY"]; ok {
				t.Fatal("ANTHROPIC_API_KEY set")
			}
			if !strings.HasPrefix(env["ANTHROPIC_AUTH_TOKEN"], "cgw-") || !strings.HasPrefix(env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:") || env["ANTHROPIC_BASE_URL"] == "http://127.0.0.1:3456" {
				t.Fatalf("model env: %v", env)
			}
			if _, err := os.Lstat(f.pol.Logins[0].Path); !os.IsNotExist(err) {
				t.Fatal("no login file when every route is gatewayed")
			}
			sb, _ := os.ReadFile(filepath.Join(f.pol.Home, ".claude", "settings.json"))
			var st map[string]any
			json.Unmarshal(sb, &st)
			if strings.Contains(string(sb), "sk-seeded") || strings.Contains(string(sb), "3456") || st["apiKeyHelper"] != nil || st["model"] != "opus" || !strings.Contains(string(sb), `"FOO":"1"`) {
				t.Fatalf("settings.json: %s", sb)
			}
			if cj, _ := os.ReadFile(filepath.Join(f.pol.Home, ".claude.json")); strings.Contains(string(cj), "sk-stored") || !strings.Contains(string(cj), `"userID":"u"`) {
				t.Fatalf(".claude.json: %s", cj)
			}
			argv := strings.Join(l.Argv, " ")
			_, rPort, _ := net.SplitHostPort(rAddr)
			if strings.Contains(argv, ":"+rPort) || len(f.relays) != 0 {
				t.Fatalf("the router is reached directly: relays %v\n%s", f.relays, argv)
			}
			if strings.Contains(argv, "api.anthropic.com") {
				t.Fatalf("the provider stays in the allowlist: %s", argv)
			}
			gwPort := strings.TrimPrefix(env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:")
			if goos == "darwin" {
				if !strings.Contains(l.Argv[2], `(remote ip "localhost:`+gwPort+`")`) {
					t.Fatalf("profile lacks the gateway port %s", gwPort)
				}
				if strings.Contains(l.Argv[2], `localhost:`+rPort+`"`) {
					t.Fatal("the profile allows the router's port")
				}
				// Through the gateway: the real key reaches the router.
				checkGatewayReachesRouter(t, env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_AUTH_TOKEN"])
				mu.Lock()
				defer mu.Unlock()
				if len(*seen) != 1 || (*seen)[0].hdr.Get("X-Api-Key") != "sk-a" {
					t.Fatalf("router saw %+v", *seen)
				}
				return
			}
			if !strings.Contains(argv, "--relay 127.0.0.1:"+gwPort+"=") {
				t.Fatalf("no gateway relay:\n%s", argv)
			}
		})
	}
}

// checkGatewayReachesRouter sends a request through a macOS-style gateway
// (a host port): refused without the launch's token, forwarded with it.
func checkGatewayReachesRouter(t *testing.T, base, tok string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/v1/messages", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", base+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with the token the gateway forwards: %d", resp.StatusCode)
	}
}

// An OAuth-only operator: the login is an access-token-only copy, the
// provider stays allowed.
func TestWrapCopiesAnOAuthLoginWithoutItsRefreshToken(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	f.pol.ModelCredential = ""
	f.pol.Loopback = nil
	f.pol.Egress, f.pol.ModelHosts = []string{"api.anthropic.com"}, []string{"api.anthropic.com"}
	f.deps.Inherited = []string{"PATH=/usr/bin"}
	os.MkdirAll(filepath.Dir(f.pol.Logins[0].Src), 0o700)
	os.WriteFile(f.pol.Logins[0].Src, []byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt"}}`), 0o600)
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	b, err := os.ReadFile(f.pol.Logins[0].Path)
	if err != nil || strings.Contains(string(b), "rt") || !strings.Contains(string(b), `"accessToken":"at"`) {
		t.Fatalf("copy %s %v", b, err)
	}
	for _, bm := range jailOf(t, l.Argv) {
		if bm.Src == f.pol.Logins[0].Src {
			t.Fatal("the operator's login file is bound")
		}
	}
	if !strings.Contains(strings.Join(l.Argv, " "), "--listen") {
		t.Fatal("the provider stays reachable for an OAuth login")
	}
	// A planted symlink out of the home is never written through.
	victim := filepath.Join(t.TempDir(), "victim")
	os.MkdirAll(victim, 0o700)
	os.RemoveAll(filepath.Join(f.pol.Home, ".claude"))
	os.Symlink(victim, filepath.Join(f.pol.Home, ".claude"))
	if _, err := Wrap(f.pol, []string{"claude"}, f.deps); err == nil {
		t.Fatal("wrote the login copy through a symlinked dir")
	}
	if ents, _ := os.ReadDir(victim); len(ents) != 0 {
		t.Fatalf("wrote into the symlink's target: %v", ents)
	}
}
