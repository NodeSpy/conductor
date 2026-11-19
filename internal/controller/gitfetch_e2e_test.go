//go:build linux || darwin

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitwt"
	"github.com/NodeSpy/conductor/internal/keycache"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The git fetch credential's secret, and the user it is sent as.
const (
	forgeUser   = "fetcher"
	forgeSecret = "s3cr3t-forge-token"
)

// fakeForge is a git-over-HTTPS server (git http-backend) on localhost
// that serves a repository only to a request carrying the credential, and
// records what each request carried.
type fakeForge struct {
	srv    *httptest.Server
	root   string
	caFile string
	port   string

	mu   sync.Mutex
	seen []forgeHit
}

type forgeHit struct {
	method, path, query string
	auth                bool
}

// reset forgets what the forge saw (the test's own setup).
func (f *fakeForge) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

func (f *fakeForge) hits() []forgeHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgeHit(nil), f.seen...)
}

func newFakeForge(t *testing.T, root string) *fakeForge {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("no git-http-backend: %v", err)
		}
		t.Skipf("no git-http-backend: %v", err)
	}
	f := &fakeForge{root: filepath.Join(root, "forge")}
	// org/dep: the private dependency; other/secret: a repository of
	// another owner the same credential could read. Pushes are on at the
	// backend: a push that got through the proxy would land.
	bareRepo(t, root, filepath.Join(f.root, "org", "dep.git"), map[string]string{
		"package.json": `{"name":"dep","version":"1.0.0","main":"index.js"}` + "\n",
		"index.js":     "module.exports = 'dep from the private forge';\n",
	})
	bareRepo(t, root, filepath.Join(f.root, "other", "secret.git"), map[string]string{"SECRET": "other org\n"})
	for _, r := range []string{"org/dep.git", "other/secret.git"} {
		runGit(t, filepath.Join(f.root, r), "config", "http.receivepack", "true")
	}
	cgiH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1"}}
	caPEM, leaf := forgeCA(t)
	f.caFile = filepath.Join(root, "forge-ca.pem")
	if err := os.WriteFile(f.caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		authed := ok && user == forgeUser && pass == forgeSecret
		f.mu.Lock()
		f.seen = append(f.seen, forgeHit{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization") != ""})
		f.mu.Unlock()
		if !authed {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		cgiH.ServeHTTP(w, r)
	}))
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	_, f.port, _ = net.SplitHostPort(f.srv.Listener.Addr().String())
	return f
}

// auth is the git fetch credential a dispatch on acme/app carries for the
// forge (any repository on it).
func (f *fakeForge) auth() sandbox.GitAuth {
	return sandbox.GitAuth{Host: "forge.test:" + f.port, For: "acme/app", Username: forgeUser, Secret: forgeSecret}
}

// forgeCA is a private CA and the localhost leaf it signed.
func forgeCA(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "forge ca"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	lt := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"forge.test", "localhost"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// bareRepo makes a bare repository at bare whose main holds files.
func bareRepo(t *testing.T, root, bare string, files map[string]string) {
	t.Helper()
	work, err := os.MkdirTemp(root, "src-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q", "--bare", "--initial-branch=main", bare)
	runGit(t, root, "init", "-q", "--initial-branch=main", work)
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(work, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "init")
	runGit(t, work, "push", "-q", bare, "main")
}

// hostRewriteEnv is what the test's own (host) git and yarn need to reach
// the forge (at localhost: forge.test resolves only in the proxy): the
// SSH form rewritten, the credential sent, the forge's CA trusted.
func hostRewriteEnv(f *fakeForge, home string) []string {
	env := []string{"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_SSL_CAINFO=" + f.caFile,
		"GIT_CONFIG_GLOBAL=/dev/null", "YARN_CACHE_FOLDER=" + filepath.Join(home, "yarn-cache"),
		"GIT_CONFIG_KEY_0=url.https://localhost:" + f.port + "/.insteadOf", "GIT_CONFIG_VALUE_0=ssh://git@forge.test/"}
	n := 1
	hdr := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(forgeUser+":"+forgeSecret))
	env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=http.extraHeader", n), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, hdr), fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1))
	for _, k := range []string{"PATH", "LANG", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// appOrigin is acme/app's bare repository: main depends on nothing; PR 7
// (branch add-dep, refs/pull/7/head) adds the private dependency by its SSH
// URL (and, when yarn is here, its yarn.lock) — a dependency nothing ever
// fetched for this repository before.
func appOrigin(t *testing.T, root string, f *fakeForge, yarn string) string {
	t.Helper()
	pkg := `{"name":"app","version":"1.0.0","private":true,"dependencies":{"dep":"git+ssh://git@forge.test/org/dep.git"}}` + "\n"
	files := map[string]string{"package.json": pkg}
	if yarn != "" {
		// The lockfile, resolved on the host with the credential.
		src := filepath.Join(root, "lock-src")
		home := filepath.Join(root, "lock-home")
		for _, d := range []string{src, home} {
			os.MkdirAll(d, 0o700)
		}
		os.WriteFile(filepath.Join(src, "package.json"), []byte(pkg), 0o644)
		cmd := exec.Command(yarn, "install", "--non-interactive", "--ignore-scripts", "--no-progress")
		cmd.Dir, cmd.Env = src, hostRewriteEnv(f, home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("host yarn install (lockfile): %v\n%s", err, out)
		}
		lock, err := os.ReadFile(filepath.Join(src, "yarn.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(lock), "git+ssh://git@forge.test/org/dep.git") {
			t.Fatalf("yarn.lock does not resolve the SSH URL:\n%s", lock)
		}
		files["yarn.lock"] = string(lock)
	}
	bare := filepath.Join(root, "app.git")
	bareRepo(t, root, bare, map[string]string{"package.json": `{"name":"app","version":"1.0.0","private":true}` + "\n"})
	work := filepath.Join(root, "app-pr")
	runGit(t, root, "clone", "-q", bare, work)
	runGit(t, work, "checkout", "-q", "-b", "add-dep")
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(work, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "add the private dependency")
	runGit(t, work, "push", "-q", "origin", "add-dep")
	runGit(t, bare, "update-ref", "refs/pull/7/head", "refs/heads/add-dep")
	return bare
}

// gitFetchProbe is what a sandboxed agent runs: every line
// "ok …" or "FAIL …". It fetches the dependency its checkout's
// package.json names, and another repository on the forge.
const gitFetchProbe = `
say() { if [ "$1" = 0 ]; then echo "ok $2"; else echo "FAIL $2"; fi; }
grep -q "git+ssh://git@forge.test/org/dep.git" package.json; say $? "the checkout's package.json names the new dependency"
git ls-remote ssh://git@forge.test/org/dep.git >"$TMPDIR/ls" 2>&1; say $? "ls-remote of the ssh:// URL"
rm -rf "$TMPDIR/dep"; git clone -q git@forge.test:org/dep.git "$TMPDIR/dep" >"$TMPDIR/clone" 2>&1 && test -f "$TMPDIR/dep/index.js"; say $? "clone of the scp-form URL"
git ls-remote ssh://git@forge.test/other/secret.git >/dev/null 2>&1; say $? "ls-remote of another repository on the host"
if [ -d "$TMPDIR/dep/.git" ]; then
  ( cd "$TMPDIR/dep" && git -c user.name=a -c user.email=a@b commit -q --allow-empty -m x && git push -q origin HEAD:refs/heads/pushed ) >"$TMPDIR/push" 2>&1; [ $? != 0 ]; say $? "push is refused"
  grep -q "git push from the sandbox" "$TMPDIR/push"; say $? "push refused by the proxy"
fi
git push -q ssh://git@forge.test/org/dep.git HEAD:refs/heads/pushed2 >"$TMPDIR/push2" 2>&1; [ $? != 0 ]; say $? "push of the checkout is refused"
needle="s3cr3t-""forge"
env | grep -q "$needle"; [ $? != 0 ]; say $? "the secret is not in the environment"
find "$HOME" "$TMPDIR" . -type f -exec grep -l "$needle" {} + 2>/dev/null | grep -q .; [ $? != 0 ]; say $? "the secret is nowhere on disk the agent sees"
command -v ssh >/dev/null && echo "has ssh" || true
`

// TestGitSSHDependencyFetchedInTheSandbox: a pull request adds a private
// dependency by its SSH URL (the v0.102.0 failure: ssh in the jail refused
// its config, the detector killed it as a tunnel tool). A real sandboxed
// agent launch fetches it over HTTPS through the egress proxy, which adds
// the connector's git fetch credential — for any repository on the host,
// nothing learned beforehand — but only because the dispatch already
// granted the launch that very token (by value; GH_TOKEN here). A launch
// granted a different token (an App token while git_fetch is the
// operator's own), or none, gets no git auth: its fetch fails as before.
// A push is refused either way, and the secret never enters the sandbox.
func TestGitSSHDependencyFetchedInTheSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := agentsandbox.Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	t.Cleanup(func() { _ = keycache.RemoveAll(root) })
	forge := newFakeForge(t, root)
	yarn, _ := exec.LookPath("yarn")
	origin := appOrigin(t, root, forge, yarn)
	forge.reset()

	self := buildConductor(t)
	oldSelf := launchSelfExe
	launchSelfExe = func() (string, error) { return self, nil }
	pm := sandbox.NewProxyManager(nil)
	// forge.test (RFC 6761) is the fake forge, on this host's loopback.
	pm.Resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "forge.test" {
			return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
		}
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	var recMu sync.Mutex
	var recs []sandbox.ConnRecord
	pm.OnConn = func(r sandbox.ConnRecord) { recMu.Lock(); recs = append(recs, r); recMu.Unlock() }
	oldU, oldA, oldUS, oldAS, oldSB := EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession, AgentSandbox
	EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession = pm.UnixEndpoint, pm.Endpoint, pm.UnixEndpointSession, pm.EndpointSession
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		launchSelfExe = oldSelf
		EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession, AgentSandbox = oldU, oldA, oldUS, oldAS, oldSB
		pm.Close()
		rel.Close()
	})
	state := filepath.Join(root, "state")
	gp := gitwt.New(state)
	gp.RemoteURL = func(string) string { return origin }
	// The egress allowlist names one unrelated path on the forge's host
	// (and so its loopback address): the git fetches pass on the git
	// credential alone, nothing else on the host does.
	r := &agentsandbox.Resolver{
		Registry: &agentsandbox.Registry{}, StateRoot: filepath.Join(state, "agent-state"), GOOS: runtime.GOOS,
		Egress:  []string{"https://forge.test:" + forge.port + "/elsewhere/"},
		Inspect: []sandbox.InspectHost{{Host: "forge.test", CA: forge.caFile}},
		Env:     os.Environ(),
	}
	AgentSandbox = &SandboxWiring{Resolver: r, Relay: rel.Relay, GitFetch: true}

	// launch runs the probe in a sandboxed agent launch for PR 7, whose
	// dispatch granted granted (env credentials) and carries git.
	launch := func(t *testing.T, id string, granted map[string]string, git []sandbox.GitAuth) string {
		t.Helper()
		req := dispatch.Request{DispatchID: id, Trigger: core.Trigger{TargetTrusted: true, Kind: "pr",
			Target: core.Target{Repo: "acme/app", PR: 7, Number: 7, BaseRef: "main"}, Context: map[string]any{"head_ref": "add-dep"}},
			Credentials: dispatch.Credentials{Env: granted, Git: git}}
		_, wt, err := gp.ProvisionWorktree(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		argv, dir, env, cleanup, err := prepareAgentSandbox(wt, dispatchEnv(granted), []string{"sh", "-c", gitFetchProbe}, withAgent(launchOpts{}, "", &req, false))
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(env, "FORGE_PORT="+forge.port)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	// unauthenticated: the launch's fetches went to the forge with no
	// credential (and failed), its push never reached it, the secret never
	// entered the sandbox.
	unauthenticated := func(t *testing.T, out string) {
		t.Helper()
		t.Logf("probe:\n%s", out)
		for _, l := range []string{"ok the checkout's package.json names the new dependency", "FAIL ls-remote of the ssh:// URL",
			"FAIL clone of the scp-form URL", "FAIL ls-remote of another repository on the host", "ok push of the checkout is refused",
			"ok the secret is not in the environment", "ok the secret is nowhere on disk the agent sees"} {
			if !strings.Contains(out, l+"\n") {
				t.Fatalf("want %q:\n%s", l, out)
			}
		}
		for _, h := range forge.hits() {
			if h.auth {
				t.Fatalf("the credential reached the forge: %+v", h)
			}
			if strings.Contains(h.path, "receive-pack") || strings.Contains(h.query, "receive-pack") {
				t.Fatalf("a push reached the forge: %+v", h)
			}
		}
		recMu.Lock()
		defer recMu.Unlock()
		for _, r := range recs {
			if r.AllowedBy == "git fetch credential" {
				t.Fatalf("a request carried the git fetch credential: %+v", r)
			}
		}
	}
	reset := func() {
		forge.reset()
		recMu.Lock()
		recs = nil
		recMu.Unlock()
	}

	t.Run("a launch with no git fetch credential fails as before", func(t *testing.T) {
		reset()
		unauthenticated(t, launch(t, "nocred", map[string]string{"GH_TOKEN": forgeSecret}, nil))
	})

	t.Run("a launch not granted the token gets no git auth", func(t *testing.T) {
		reset()
		unauthenticated(t, launch(t, "notoken", nil, []sandbox.GitAuth{forge.auth()}))
	})

	t.Run("a launch granted only a different token gets no git auth", func(t *testing.T) {
		// The dispatch's App token, while git_fetch carries the operator's
		// own: not the same token, so not the same reach.
		reset()
		unauthenticated(t, launch(t, "apptoken", map[string]string{"GH_TOKEN": "ghs-app-installation-token"}, []sandbox.GitAuth{forge.auth()}))
	})

	t.Run("a launch granted the token fetches the PR's new private dependency", func(t *testing.T) {
		reset()
		// Named anything: the value is what counts.
		out := launch(t, "held", map[string]string{"OPERATOR_TOKEN": forgeSecret}, []sandbox.GitAuth{forge.auth()})
		t.Logf("probe:\n%s", out)
		if strings.Contains(out, "FAIL") || strings.Count(out, "ok ") != 9 {
			t.Fatalf("probe:\n%s", out)
		}
		if strings.Contains(out, "has ssh") {
			t.Log("(ssh is installed in the sandbox's view, and was never needed)")
		}
		assertForgeHits(t, forge)
		for _, ref := range []string{"pushed", "pushed2"} {
			if _, err := os.Stat(filepath.Join(forge.root, "org", "dep.git", "refs", "heads", ref)); err == nil {
				t.Fatalf("the push (%s) landed", ref)
			}
		}
		recMu.Lock()
		defer recMu.Unlock()
		var credited, other, pushDenied bool
		for _, r := range recs {
			if strings.Contains(r.Reason, forgeSecret) || strings.Contains(r.Path, forgeSecret) {
				t.Fatalf("the secret was recorded: %+v", r)
			}
			credited = credited || (r.Inspected && r.AllowedBy == "git fetch credential" && strings.HasPrefix(r.Path, "/org/dep.git/"))
			other = other || (r.Inspected && r.AllowedBy == "git fetch credential" && strings.HasPrefix(r.Path, "/other/secret.git/"))
			pushDenied = pushDenied || (r.Inspected && r.Outcome == "denied" && strings.Contains(r.Reason, "git push"))
		}
		if !credited || !other || !pushDenied {
			t.Fatalf("credited fetch %v, another repository %v, denied push %v in the audit: %+v", credited, other, pushDenied, recs)
		}
	})
}

// dispatchEnv is granted as the KEY=VALUE environment a dispatch hands its
// launch (which the sandbox strips of credentials).
func dispatchEnv(granted map[string]string) []string {
	return dispatch.Credentials{Env: granted}.EnvList()
}

// assertForgeHits: the forge saw the credential on git fetches only —
// never on a push, never on any other path — and saw it.
func assertForgeHits(t *testing.T, f *fakeForge) {
	t.Helper()
	sawFetch := false
	for _, h := range f.hits() {
		isFetch := strings.HasSuffix(h.path, "/git-upload-pack") || h.query == "service=git-upload-pack"
		sawFetch = sawFetch || (isFetch && h.auth)
		if h.auth && !isFetch {
			t.Errorf("the credential was sent on %s %s?%s", h.method, h.path, h.query)
		}
		if strings.Contains(h.path, "receive-pack") || strings.Contains(h.query, "receive-pack") {
			t.Errorf("a push reached the forge: %s %s?%s", h.method, h.path, h.query)
		}
	}
	if !sawFetch {
		t.Errorf("no credited fetch reached the forge: %+v", f.hits())
	}
}
