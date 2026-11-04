//go:build linux || darwin

package plugin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/sandbox"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The official plugins, run under the default jail (sandbox.plugins:
// required) against local fakes. Opt-in: the binaries come from the
// plugins repo, built by hand.
//
//	CONDUCTOR_OFFICIAL_PLUGINS  a directory holding github, sentry, pagerduty, slack
//	CONDUCTOR_OFFICIAL_HOST_IP  a non-loopback address of this box (the fakes
//	                            listen there; loopback bypasses HTTP_PROXY)
//	CONDUCTOR_GHFAKE            (github) the scenario file a running GitHub
//	                            fake wrote: {url, key, repo, pr, app_id, token}
type officialRig struct {
	dir, hostIP, self, stage string
	pm                       *sandbox.ProxyManager
}

func newOfficialRig(t *testing.T) *officialRig {
	t.Helper()
	dir := os.Getenv("CONDUCTOR_OFFICIAL_PLUGINS")
	ip := os.Getenv("CONDUCTOR_OFFICIAL_HOST_IP")
	if dir == "" || ip == "" {
		t.Skip("set CONDUCTOR_OFFICIAL_PLUGINS and CONDUCTOR_OFFICIAL_HOST_IP to run the official plugins jailed")
	}
	requireJail(t)
	pm := sandbox.NewProxyManager(func(key, hostport string) { t.Logf("egress proxy denied %s -> %s", key, hostport) })
	t.Cleanup(pm.Close)
	return &officialRig{dir: dir, hostIP: ip, self: jailSelf(t), stage: realDir(t, "", "official-stage-"), pm: pm}
}

// client describes the plugin once (unjailed, as install does) for its
// manifest, then returns a client that runs it jailed with spec's grant.
func (r *officialRig) client(t *testing.T, name string, grant func(*Spec)) (*Client, *[]string) {
	t.Helper()
	bin := filepath.Join(r.dir, name)
	probe := NewClient(Spec{Name: name, Kind: KindConnector, Provides: name, BinPath: bin, Local: true}, Deps{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	decl, err := probe.Describe(ctx)
	probe.Close()
	if err != nil {
		t.Fatalf("%s describe: %v", name, err)
	}
	s := Spec{Name: name, Kind: KindConnector, Provides: name, BinPath: bin, Local: true, Manifest: ManifestFromDecl(decl)}
	grant(&s)
	var mu sync.Mutex
	logs := &[]string{}
	c := NewClient(s, Deps{
		Log: func(f string, a ...any) {
			mu.Lock()
			*logs = append(*logs, fmt.Sprintf(f, a...))
			mu.Unlock()
		},
		Sandbox: SandboxDeps{
			Self: r.self, StagingRoot: r.stage,
			EgressUnix: r.pm.UnixEndpoint, EgressAddr: r.pm.Endpoint,
			Jail: JailRequired, JailProbe: agentsandbox.Probe,
		},
	})
	t.Cleanup(func() {
		c.Close()
		t.Logf("%s spawn log:\n  %s", name, strings.Join(*logs, "\n  "))
	})
	return c, logs
}

func (r *officialRig) listenAddr(t *testing.T) string {
	t.Helper()
	return freeLoopbackAddr(t)
}

func (r *officialRig) serve(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(r.hostIP, "0"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// direct is an HTTP client that ignores the environment's proxy settings:
// the test side talks to the fakes and the plugin's listeners directly.
var direct = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// post delivers body to url until the plugin's listener answers (it starts
// asynchronously), and returns the status.
func post(t *testing.T, url string, headers map[string]string, body []byte) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := direct.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return resp.StatusCode
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("POST %s: never answered (last: %v)", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

type sink struct {
	mu  sync.Mutex
	evs []map[string]any
	got chan struct{}
}

func newSink() *sink { return &sink{got: make(chan struct{}, 64)} }

func (s *sink) emit(raw json.RawMessage) {
	var ev map[string]any
	_ = json.Unmarshal(raw, &ev)
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	s.got <- struct{}{}
}

func (s *sink) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-s.got:
	case <-time.After(20 * time.Second):
		t.Fatal("no event streamed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evs[len(s.evs)-1]
}

func assertJailed(t *testing.T, logs []string) {
	t.Helper()
	for _, l := range logs {
		if strings.Contains(l, "OS jail") {
			return
		}
	}
	t.Fatalf("no spawn log line says OS jail: %v", logs)
}

// Sentry and PagerDuty: webhook sources. Each listens on the address its
// config sets; inside the jail that is forwarded from the host.
func TestOfficialWebhookSourcesJailed(t *testing.T) {
	r := newOfficialRig(t)
	for _, tc := range []struct {
		name, path, secretKey, event string
		headers                      func(secret string, body []byte) map[string]string
		body                         string
	}{
		{"sentry", "/sentry", "client_secret", "issue_alert",
			func(s string, b []byte) map[string]string {
				return map[string]string{"Sentry-Hook-Resource": "issue", "Sentry-Hook-Signature": hmacHex(s, b)}
			},
			`{"action":"created","data":{"issue":{"title":"NPE","level":"error","shortID":"WEB-1","culprit":"c.go","permalink":"https://sentry.example/x","project":{"slug":"web"}}}}`},
		{"pagerduty", "/pagerduty", "signing_secret", "incident",
			func(s string, b []byte) map[string]string {
				return map[string]string{"X-PagerDuty-Signature": "v1=" + hmacHex(s, b)}
			},
			`{"event":{"event_type":"incident.triggered","data":{"id":"PD1","number":42,"status":"triggered","title":"DB down","html_url":"https://pd.example/x","urgency":"high","priority":{"summary":"P1"},"service":{"id":"SVC1","summary":"api"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := r.listenAddr(t)
			c, logs := r.client(t, tc.name, func(s *Spec) { s.Listen = []string{addr} })
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			sk := newSink()
			secret := "s3cret"
			if err := c.StartSource(ctx, StartSourceRequest{Instance: tc.name + "1", Config: map[string]any{
				"listen": addr, "path": tc.path, tc.secretKey: secret,
			}}, sk.emit); err != nil {
				t.Fatalf("StartSource: %v", err)
			}
			body := []byte(tc.body)
			if code := post(t, "http://"+addr+tc.path, tc.headers(secret, body), body); code >= 300 {
				t.Fatalf("delivery answered %d", code)
			}
			if ev := sk.wait(t); ev["event"] != tc.event {
				t.Fatalf("event = %v", ev)
			}
			assertJailed(t, *logs)
		})
	}
}

// Slack: a verb against a Web API fake its network: allows, and the same
// verb pointed at a host it does not allow, refused by the egress proxy.
func TestOfficialSlackJailed(t *testing.T) {
	r := newOfficialRig(t)
	var mu sync.Mutex
	var calls []string
	api := r.serve(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"channel":"C1","ts":"1.000001"}`)
	}))
	other := r.serve(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("the undeclared host was reached: %s", req.URL.Path)
	}))
	c, logs := r.client(t, "slack", func(s *Spec) { s.Network = []string{api} })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	post := func(instance, base string) (map[string]any, error) {
		return c.Invoke(ctx, InvokeRequest{Instance: instance, Verb: "post",
			Connection: map[string]any{"bot_token": "xoxb-1", "api_base": "http://" + base + "/api"},
			Options:    map[string]any{"channel": "C1", "text": "from the jail"}})
	}
	out, err := post("slack1", api)
	if err != nil {
		t.Fatalf("post through the allowed host: %v", err)
	}
	t.Logf("post: %v", out)
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n == 0 {
		t.Fatal("the Web API fake saw no call")
	}
	if _, err := post("slack2", other); err == nil {
		t.Fatal("a post to a host outside network: succeeded")
	} else {
		t.Logf("post to an undeclared host: %v", err)
	}
	assertJailed(t, *logs)
}

// GitHub against the GitHub fake: App auth from a key file the config
// points at, a dry-run sweep that finds the review, a verb call, and a
// webhook delivery to its listen address.
func TestOfficialGithubJailed(t *testing.T) {
	r := newOfficialRig(t)
	p := os.Getenv("CONDUCTOR_GHFAKE")
	if p == "" {
		t.Skip("set CONDUCTOR_GHFAKE to a running GitHub fake's scenario file")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		URL, Key, Repo, Token string
		PR                    int
		AppID                 int `json:"app_id"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	apiHost := strings.TrimPrefix(sc.URL, "http://")
	addr := r.listenAddr(t)
	c, logs := r.client(t, "github", func(s *Spec) {
		s.Network = []string{apiHost}
		s.Paths = []string{sc.Key}
		s.Listen = []string{addr}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn := map[string]any{
		"api_base": sc.URL,
		"app":      map[string]any{"app_id": sc.AppID, "private_key_path": sc.Key},
		"me":       map[string]any{"logins": []any{"me"}},
		"sweep":    map[string]any{"enabled": true, "repos": []any{sc.Repo}, "interval": "1h"},
		"webhook":  map[string]any{"listen": addr, "path": "/github", "secret": "hooksecret"},
	}
	sk := newSink()
	if err := c.StartSource(ctx, StartSourceRequest{Instance: "gh1", Config: conn,
		Triggers: []sdk.SourceTrigger{
			{ID: "0:gh.changes_requested", Name: "fix", Event: "changes_requested"},
			{ID: "1:gh.new_comment", Name: "reply", Event: "new_comment"},
		}}, sk.emit); err != nil {
		t.Fatalf("StartSource: %v", err)
	}

	evs, err := c.Poll(ctx, sdk.PollRequest{Instance: "gh1", Mode: sdk.PollDryRun})
	if err != nil {
		t.Fatalf("dry-run sweep: %v", err)
	}
	var found bool
	for _, e := range evs {
		t.Logf("swept: %s %s", e.Event, e.Trigger)
		if e.Event == "changes_requested" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the sweep did not find the review: %+v", evs)
	}

	out, err := c.Invoke(ctx, InvokeRequest{Instance: "gh1", Verb: "comment", Connection: conn,
		Options: map[string]any{"repo": sc.Repo, "number": sc.PR, "body": "from the jail", "as": "bot"}})
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	t.Logf("comment: %v", out)
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/issues/%d/comments", sc.URL, sc.Repo, sc.PR), nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token)
	resp, err := direct.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "from the jail") {
		t.Fatalf("the fake has no comment from the jail: %s", b)
	}

	body := []byte(fmt.Sprintf(`{"action":"created","repository":{"full_name":%q,"owner":{"login":"acme"},"name":"w"},"issue":{"number":%d,"state":"open","html_url":"https://github.example/acme/w/pull/%d","pull_request":{},"user":{"login":"me"}},"comment":{"id":555,"body":"looks good","user":{"login":"rev","type":"User"}}}`, sc.Repo, sc.PR, sc.PR))
	post(t, "http://"+addr+"/github", map[string]string{
		"X-GitHub-Event": "issue_comment", "X-GitHub-Delivery": "d-1",
		"X-Hub-Signature-256": "sha256=" + hmacHex("hooksecret", body),
	}, body)
	if ev := sk.wait(t); ev["event"] != "new_comment" {
		t.Fatalf("webhook event = %v", ev)
	}
	assertJailed(t, *logs)
}
