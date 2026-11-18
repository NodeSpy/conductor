package sandbox

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testGitSecret = "gh-secret-0123456789"

// gitRig is a forge (example.com, an echo server) behind a proxy whose
// session holds a git fetch credential for its org's repositories.
// broad: the allowlist also names the forge's host (any path).
func gitRig(t *testing.T, broad bool) (*inspectRig, string) {
	t.Helper()
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "upstream "+r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization")+" body="+string(body))
	})
	rig := &inspectRig{mirror: httptest.NewTLSServer(echo)}
	t.Cleanup(rig.mirror.Close)
	_, port, _ := net.SplitHostPort(rig.mirror.Listener.Addr().String())
	in, err := NewInspector([]InspectHost{{Host: "example.com", CA: serverPEM(t, rig.mirror)}})
	if err != nil {
		t.Fatal(err)
	}
	rig.in = in
	// Not broad: the allowlist names the host for one unrelated path only
	// (and so lets its name reach the test's loopback upstream).
	allow := []string{"https://example.com:" + port + "/elsewhere/"}
	if broad {
		allow = []string{"example.com:" + port}
	}
	rig.p = &Proxy{Allow: allow,
		resolveIP: func(_ context.Context, h string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		OnConn: func(c ConnRecord) { rig.mu.Lock(); rig.recs = append(rig.recs, c); rig.mu.Unlock() },
	}
	addr, err := rig.p.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rig.p.Close)
	auth := GitAuth{Host: "example.com:" + port, Repos: []string{"/org/"}, Username: "x-user", Secret: testGitSecret}
	if err := auth.Validate(); err != nil {
		t.Fatal(err)
	}
	cred, err := rig.p.MintSession(SessionOpts{Run: "r1", RequireTLS: true, Inspect: in, Git: []GitAuth{auth}, Limits: NoEgressLimits})
	if err != nil {
		t.Fatal(err)
	}
	rig.proxyURL, _ = url.Parse("http://" + proxyUser + ":" + cred + "@" + addr)
	return rig, "Basic " + base64.StdEncoding.EncodeToString([]byte("x-user:"+testGitSecret))
}

func do(t *testing.T, c *http.Client, method, u string, h http.Header) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader("0000"))
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

// TestGitFetchCredentialOnlyOnFetchesInScope: the proxy adds the
// credential to smart-HTTP fetches of the org's repositories (replacing
// whatever the agent sent), on HTTP/1.1 and HTTP/2; a push is refused; a
// host reached for its git credential alone passes nothing else; and the
// secret is in no record.
func TestGitFetchCredentialOnlyOnFetchesInScope(t *testing.T) {
	for _, broad := range []bool{false, true} {
		rig, want := gitRig(t, broad)
		roots := caPool(rig.in.CAPEM())
		for _, h2 := range []bool{false, true} {
			c := rig.client(roots, h2)
			mine := http.Header{"Authorization": {"Bearer agent-own"}}
			for _, f := range []struct{ method, path string }{
				{"GET", "/org/dep.git/info/refs?service=git-upload-pack"},
				{"GET", "/org/dep/info/refs?service=git-upload-pack"},
				{"POST", "/org/dep.git/git-upload-pack"},
				{"POST", "/org/sub/deep.git/git-upload-pack"},
			} {
				code, body := do(t, c, f.method, rig.mirrorURL(f.path), mine)
				if code != 200 || !strings.Contains(body, "auth="+want+" ") {
					t.Fatalf("broad=%v h2=%v %s %s: %d %q", broad, h2, f.method, f.path, code, body)
				}
			}
			// A push, to any repository on the host: refused before upstream.
			for _, f := range []struct{ method, path string }{
				{"GET", "/org/dep.git/info/refs?service=git-receive-pack"},
				{"POST", "/org/dep.git/git-receive-pack"},
				{"POST", "/other/x.git/git-receive-pack"},
				{"GET", "/org/dep.git/git-receive-pack"},
			} {
				if code, body := do(t, c, f.method, rig.mirrorURL(f.path), mine); code != 403 || strings.Contains(body, "upstream") {
					t.Fatalf("broad=%v push %s %s: %d %q", broad, f.method, f.path, code, body)
				}
			}
			// Not a fetch in scope: never the credential; refused unless
			// the allowlist names the host itself.
			for _, f := range []struct{ method, path string }{
				{"GET", "/other/secret.git/info/refs?service=git-upload-pack"},
				{"POST", "/other/secret.git/git-upload-pack"},
				{"GET", "/organization/x.git/info/refs?service=git-upload-pack"},
				{"GET", "/org/../other/secret.git/info/refs?service=git-upload-pack"},
				{"GET", "/org/%2e%2e/other/secret.git/info/refs?service=git-upload-pack"},
				{"GET", "/org/dep.git/info/refs"},                         // dumb HTTP
				{"POST", "/org/dep.git/info/refs?service=git-upload-pack"}, // wrong method
				{"GET", "/org/dep.git/git-upload-pack"},                    // wrong method
				{"GET", "/org/dep"},
				{"GET", "/org/dep.git/info/lfs/objects/batch"},
				{"GET", "/login"},
			} {
				code, body := do(t, c, f.method, rig.mirrorURL(f.path), mine)
				if strings.Contains(body, testGitSecret) || strings.Contains(body, want) {
					t.Fatalf("broad=%v %s %s carried the credential: %q", broad, f.method, f.path, body)
				}
				dot := strings.Contains(f.path, "..") || strings.Contains(f.path, "%2e")
				switch {
				case !broad && code != 403:
					t.Fatalf("git-only host: %s %s: %d %q", f.method, f.path, code, body)
				case broad && !dot && (code != 200 || !strings.Contains(body, "auth=Bearer agent-own ")):
					t.Fatalf("broad host: %s %s: %d %q (want the agent's own request)", f.method, f.path, code, body)
				}
			}
		}
		var credited int
		for _, r := range rig.records() {
			js, _ := json.Marshal(r)
			if strings.Contains(string(js), testGitSecret) || strings.Contains(string(js), "Basic ") {
				t.Fatalf("a record carries the credential: %s", js)
			}
			if r.AllowedBy == "git fetch credential" {
				credited++
			}
		}
		if credited != 8 {
			t.Fatalf("broad=%v: %d credited records, want 8: %+v", broad, credited, rig.records())
		}
	}
}

// TestGitHostAdmittedForGitAlone: a host the allowlist does not name is
// reached only by a session holding a git credential for it, and that
// session only for git fetches in scope (anything else on the host is
// refused inside the inspected tunnel); a session without one, or one that
// does not inspect the host, gets no tunnel.
func TestGitHostAdmittedForGitAlone(t *testing.T) {
	p := &Proxy{Allow: []string{"elsewhere.example:443"},
		resolveIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}}
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	in, err := NewInspector([]InspectHost{{Host: "forge.example"}})
	if err != nil {
		t.Fatal(err)
	}
	auth := GitAuth{Host: "forge.example", Repos: []string{"/o/"}, Username: "u", Secret: "s"}
	client := func(o SessionOpts) *http.Client {
		cred, _ := p.MintSession(o)
		pu, _ := url.Parse("http://" + proxyUser + ":" + cred + "@" + addr)
		return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: caPool(in.CAPEM())}}}
	}
	for name, o := range map[string]SessionOpts{
		"no credential":   {Run: "r", Inspect: in},
		"not inspected":   {Run: "r", Git: []GitAuth{auth}},
		"other host cred": {Run: "r", Inspect: in, Git: []GitAuth{{Host: "forge.example:8443", Repos: []string{"/o/"}, Username: "u", Secret: "s"}}},
	} {
		if _, err := client(o).Get("https://forge.example/o/r.git/info/refs?service=git-upload-pack"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatalf("%s: CONNECT allowed: %v", name, err)
		}
	}
	c := client(SessionOpts{Run: "r", Inspect: in, Git: []GitAuth{auth}})
	for _, path := range []string{"/o/r", "/x/r.git/info/refs?service=git-upload-pack", "/o/r.git/git-receive-pack"} {
		resp, err := c.Get("https://forge.example" + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 || !strings.Contains(string(b), "refused") {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, b)
		}
	}
}

func TestGitOp(t *testing.T) {
	for _, c := range []struct {
		method, target, op, repo string
	}{
		{"GET", "/o/r.git/info/refs?service=git-upload-pack", gitFetch, "/o/r"},
		{"HEAD", "/o/r/info/refs?service=git-upload-pack", gitFetch, "/o/r"},
		{"GET", "/o/r.git/info/refs?service=git-receive-pack", gitPush, "/o/r"},
		{"GET", "/o/r.git/info/refs?service=git-upload-pack&service=git-receive-pack", gitFetch, "/o/r"},
		{"POST", "/o/r.git/git-upload-pack", gitFetch, "/o/r"},
		{"POST", "/o/r.git/git-receive-pack", gitPush, "/o/r"},
		{"PUT", "/o/r.git/git-receive-pack", gitPush, "/o/r"},
		{"GET", "/o/r.git/git-upload-pack", gitNone, ""},
		{"POST", "/o/r.git/info/refs?service=git-upload-pack", gitNone, ""},
		{"GET", "/o/r.git/info/refs", gitNone, ""},
		{"GET", "/o/r.git/info/refs?service=git-upload-archive", gitNone, ""},
		{"POST", "/o/r.git/git-upload-archive", gitNone, ""},
		{"GET", "/o/r.git/HEAD", gitNone, ""},
	} {
		u, _ := url.Parse("https://h" + c.target)
		r := &http.Request{Method: c.method, URL: u}
		op, repo := gitOp(r, u.Path)
		if op != c.op || repo != c.repo {
			t.Errorf("%s %s: %q %q, want %q %q", c.method, c.target, op, repo, c.op, c.repo)
		}
	}
}

func TestGitAuthValidate(t *testing.T) {
	ok := GitAuth{Host: "github.com", Repos: []string{"/org/"}, Username: "u", Secret: "s"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []GitAuth{
		{Host: "*.github.com", Repos: []string{"/o/"}, Username: "u", Secret: "s"},
		{Host: "10.0.0.1", Repos: []string{"/o/"}, Username: "u", Secret: "s"},
		{Host: "github.com:0", Repos: []string{"/o/"}, Username: "u", Secret: "s"},
		{Host: "github.com:x", Repos: []string{"/o/"}, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: nil, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: []string{"/"}, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: []string{"o/"}, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: []string{"/o/../x"}, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: []string{"/o//x"}, Username: "u", Secret: "s"},
		{Host: "github.com", Repos: []string{"/o/"}, Username: "", Secret: "s"},
		{Host: "github.com", Repos: []string{"/o/"}, Username: "a:b", Secret: "s"},
		{Host: "github.com", Repos: []string{"/o/"}, Username: "u", Secret: ""},
		{Host: "github.com", Repos: []string{"/o/"}, Username: "u", Secret: "a\nb"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	b, _ := json.Marshal(ok)
	if strings.Contains(string(b), `"s"`) {
		t.Fatalf("the secret is serialized: %s", b)
	}
}

func TestGitRewriteEnv(t *testing.T) {
	env := GitRewriteEnv([]string{"A=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y"},
		[]GitAuth{{Host: "github.com"}, {Host: "forge.test:8443"}, {Host: "github.com"}})
	got := strings.Join(env, "\n")
	for _, w := range []string{
		"GIT_CONFIG_KEY_0=core.x",
		"GIT_CONFIG_KEY_1=url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_1=ssh://git@github.com/",
		"GIT_CONFIG_VALUE_2=git+ssh://git@github.com/", "GIT_CONFIG_VALUE_3=ssh://github.com/", "GIT_CONFIG_VALUE_4=git@github.com:",
		"GIT_CONFIG_KEY_5=url.https://forge.test:8443/.insteadOf", "GIT_CONFIG_VALUE_5=ssh://git@forge.test/",
		"GIT_CONFIG_VALUE_8=git@forge.test:",
		"GIT_CONFIG_COUNT=9",
	} {
		if !strings.Contains(got, w+"\n") && !strings.HasSuffix(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
	if strings.Count(got, "GIT_CONFIG_COUNT=") != 1 {
		t.Fatalf("count set twice:\n%s", got)
	}
}
