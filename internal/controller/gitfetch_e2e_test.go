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
	"slices"
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

// auth is the launch's git fetch credential for the forge: org's
// repositories only.
// auth is a launch's credential for acme/app, scoped to repos.
func (f *fakeForge) auth(repos ...string) sandbox.GitAuth {
	return sandbox.GitAuth{Host: "forge.test:" + f.port, Repos: repos, For: "acme/app", Username: forgeUser, Secret: forgeSecret}
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

// appOrigin is acme/app's bare repository: a package.json depending on
// org/dep by its SSH URL (and, when yarn is here, its yarn.lock).
func appOrigin(t *testing.T, root string, f *fakeForge, yarn string) string {
	t.Helper()
	files := map[string]string{
		"package.json": `{"name":"app","version":"1.0.0","private":true,"dependencies":{"dep":"git+ssh://git@forge.test/org/dep.git"}}` + "\n",
	}
	if yarn != "" {
		// The lockfile, resolved on the host with the credential.
		src := filepath.Join(root, "lock-src")
		home := filepath.Join(root, "lock-home")
		for _, d := range []string{src, home} {
			os.MkdirAll(d, 0o700)
		}
		os.WriteFile(filepath.Join(src, "package.json"), []byte(files["package.json"]), 0o644)
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
	bareRepo(t, root, bare, files)
	runGit(t, bare, "update-ref", "refs/pull/7/head", "refs/heads/main")
	return bare
}

// gitFetchProbe is what a sandboxed agent runs: every line
// "ok …" or "FAIL …".
const gitFetchProbe = `
say() { if [ "$1" = 0 ]; then echo "ok $2"; else echo "FAIL $2"; fi; }
git ls-remote ssh://git@forge.test/org/dep.git >"$TMPDIR/ls" 2>&1; say $? "ls-remote of the ssh:// URL"
rm -rf "$TMPDIR/dep"; git clone -q git@forge.test:org/dep.git "$TMPDIR/dep" >"$TMPDIR/clone" 2>&1 && test -f "$TMPDIR/dep/index.js"; say $? "clone of the scp-form URL"
git ls-remote ssh://git@forge.test/other/secret.git >/dev/null 2>&1; [ $? != 0 ]; say $? "another owner's repository is not fetched"
git ls-remote https://forge.test:$FORGE_PORT/org/../other/secret.git >/dev/null 2>&1; [ $? != 0 ]; say $? "a dot-segment path does not reach another owner"
( cd "$TMPDIR/dep" && git -c user.name=a -c user.email=a@b commit -q --allow-empty -m x && git push -q origin HEAD:refs/heads/pushed ) >"$TMPDIR/push" 2>&1; [ $? != 0 ]; say $? "push is refused"
grep -q "git push from the sandbox" "$TMPDIR/push"; say $? "push refused by the proxy"
needle="s3cr3t-""forge"
env | grep -q "$needle"; [ $? != 0 ]; say $? "the secret is not in the environment"
find "$HOME" "$TMPDIR" . -type f -exec grep -l "$needle" {} + 2>/dev/null | grep -q .; [ $? != 0 ]; say $? "the secret is nowhere on disk the agent sees"
command -v ssh >/dev/null && echo "has ssh" || true
`

// teachProbe is a teaching launch: a default-branch run of a teach: step
// whose tool fetches the private dependency and fills its cache.
const teachProbe = `
rm -rf "$TMPDIR/dep"
if git clone -q git@forge.test:org/dep.git "$TMPDIR/dep" >"$TMPDIR/clone" 2>&1 && test -f "$TMPDIR/dep/index.js"; then echo "ok teaching clone"; else echo "FAIL teaching clone"; cat "$TMPDIR/clone"; fi
mkdir -p ~/.cache/fake && cp -r "$TMPDIR/dep" ~/.cache/fake/dep && head -c 262144 /dev/zero >~/.cache/fake/blob
needle="s3cr3t-""forge"
env | grep -q "$needle"; [ $? != 0 ]; if [ $? = 0 ]; then echo "ok the secret is not in the environment"; else echo "FAIL the secret is in the environment"; fi
`

// TestGitSSHDependencyFetchedInTheSandbox: a repository whose package.json
// names a private dependency by its SSH URL (the v0.102.0 failure: ssh in
// the jail refused its config, the detector killed it as a tunnel tool).
// With the forge connector's git fetch credential, a real sandboxed agent
// launch fetches it over HTTPS through the egress proxy, which adds the
// credential to fetches of the repository and its learned dependencies
// only and refuses a push; the secret is never inside the sandbox. Without
// one, the same launch fails as before. A dependency is learned only by a
// teaching launch — conductor's own default-branch checkout running a step
// that declared cache: { teach: true } — whose step succeeded and
// published; a PR's launch never teaches, a failed step teaches nothing.
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
	oldU, oldA, oldUS, oldAS, oldSB, oldTG, oldTL, oldOD := EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession, AgentSandbox, TeachGitAuth, TeachGitLearned, dispatch.CheckoutOnDefault
	EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession = pm.UnixEndpoint, pm.Endpoint, pm.UnixEndpointSession, pm.EndpointSession
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		launchSelfExe = oldSelf
		EgressProxyUnix, EgressProxyFor, EgressProxyUnixSession, EgressProxyForSession, AgentSandbox, TeachGitAuth, TeachGitLearned, dispatch.CheckoutOnDefault = oldU, oldA, oldUS, oldAS, oldSB, oldTG, oldTL, oldOD
		pm.Close()
		rel.Close()
	})
	state := filepath.Join(root, "state")
	gp := gitwt.New(state)
	gp.RemoteURL = func(string) string { return origin }
	dispatch.CheckoutOnDefault = gp.OnDefault
	var logMu sync.Mutex
	var logs []string
	logf := func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		l := fmt.Sprintf(f, a...)
		logs = append(logs, l)
		t.Log(l)
	}
	m := &keycache.Manager{
		Store: &keycache.Store{Dir: filepath.Join(state, "repo-caches")}, Protected: agentsandbox.ToolStatePaths(),
		MaxSize: 10 << 30, MinSize: 64 << 10, MinRuns: 1, FinishWait: 5 * time.Second, Logf: logf,
	}
	// The egress allowlist names one unrelated path on the forge's host
	// (and so its loopback address): the git fetches pass on the git
	// credential alone, nothing else on the host does.
	r := &agentsandbox.Resolver{
		Registry: &agentsandbox.Registry{}, StateRoot: filepath.Join(state, "agent-state"), GOOS: runtime.GOOS,
		Egress:  []string{"https://forge.test:" + forge.port + "/elsewhere/"},
		Inspect: []sandbox.InspectHost{{Host: "forge.test", CA: forge.caFile}},
		Env:     os.Environ(),
		Caches: func(req keycache.Request) keycache.Grant {
			g, err := m.Restore(context.Background(), req)
			if err != nil {
				t.Errorf("restore: %v", err)
			}
			return g
		},
	}
	AgentSandbox = &SandboxWiring{Resolver: r, Relay: rel.Relay, GitFetch: true}

	// The key a default-branch teach: step publishes and PR steps restore
	// from; a PR step's own key.
	shared := keycache.Key{Template: "{{.repo}}", Value: "acme/app"}
	prKey := keycache.Key{Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-acme/app-7"}
	prReq := func(id string, git []sandbox.GitAuth, cache *dispatch.CacheKeys) dispatch.Request {
		return dispatch.Request{DispatchID: id, Trigger: core.Trigger{TargetTrusted: true, Kind: "pr",
			Target: core.Target{Repo: "acme/app", PR: 7, Number: 7, BaseRef: "main"}, Context: map[string]any{"head_ref": "main"}},
			Credentials: dispatch.Credentials{Git: git}, Cache: cache}
	}
	defaultReq := func(id string) dispatch.Request {
		req := dispatch.Request{DispatchID: id, Trigger: core.Trigger{TargetTrusted: true, Kind: "push",
			Target: core.Target{Repo: "acme/app", BaseRef: "main"}},
			Cache: &dispatch.CacheKeys{Key: shared, Teach: true}}
		req.Action.Checkout = "branch-off"
		return req
	}
	launch := func(t *testing.T, req dispatch.Request, script string) string {
		t.Helper()
		_, wt, err := gp.ProvisionWorktree(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		argv, dir, env, cleanup, err := prepareAgentSandbox(wt, nil, []string{"sh", "-c", script}, withAgent(launchOpts{}, "", &req, false))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(env, "FORGE_PORT="+forge.port)
		out, _ := cmd.CombinedOutput()
		cleanup()
		return string(out)
	}
	// finish is the step's end (cmd/conductor finishStepCache): publish,
	// then hand what a teaching launch fetched over — or drop it.
	finish := func(t *testing.T, id string, ok bool) *keycache.Published {
		t.Helper()
		p, err := m.Finish(context.Background(), id, ok)
		if err != nil {
			t.Fatal(err)
		}
		TaughtGit(id, p != nil)
		return p
	}

	// TeachGitLearned is how a teaching launch whose step succeeded hands
	// over what it fetched; nothing else may call it.
	var learnMu sync.Mutex
	var learnCalls [][]string
	TeachGitLearned = func(repo, key string, fetched []string) {
		learnMu.Lock()
		defer learnMu.Unlock()
		if repo != "acme/app" || key != "cache:"+shared.ID() {
			t.Errorf("learned for %s %s", repo, key)
		}
		learnCalls = append(learnCalls, fetched)
	}
	learnedCalls := func() [][]string {
		learnMu.Lock()
		defer learnMu.Unlock()
		return append([][]string(nil), learnCalls...)
	}
	var teachAsked []string
	TeachGitAuth = func(_ context.Context, repo string) []sandbox.GitAuth {
		teachAsked = append(teachAsked, repo)
		return []sandbox.GitAuth{{Host: "forge.test:" + forge.port, Learn: true, For: "acme/app", Username: forgeUser, Secret: forgeSecret}}
	}
	withheld := func(t *testing.T, repo string) bool {
		recMu.Lock()
		defer recMu.Unlock()
		for _, r := range recs {
			if strings.Contains(r.Reason, "git credential withheld for "+repo+": not a learned dependency of acme/app; it is learned when a default-branch run of a step with cache: { teach: true } fetches it") {
				return true
			}
		}
		return false
	}

	t.Run("agent launch without a git fetch credential fails as before", func(t *testing.T) {
		out := launch(t, prReq("nocred", nil, nil), gitFetchProbe)
		t.Logf("probe:\n%s", out)
		if !strings.Contains(out, "FAIL ls-remote of the ssh:// URL") || !strings.Contains(out, "FAIL clone of the scp-form URL") ||
			strings.Contains(out, "FAIL the secret") || strings.Contains(out, "FAIL another owner") {
			t.Fatalf("the ssh:// dependency was fetched with no credential:\n%s", out)
		}
	})

	t.Run("a PR launch gets no credential for a dependency not learned, and never teaches", func(t *testing.T) {
		// Its step's key is even one that restores from the shared key; it
		// is a PR's checkout, so it does not teach.
		out := launch(t, prReq("unlearned", []sandbox.GitAuth{forge.auth("/acme/app")}, &dispatch.CacheKeys{Key: prKey, RestoreFrom: []keycache.Key{shared}}), gitFetchProbe)
		t.Logf("probe:\n%s", out)
		if !strings.Contains(out, "FAIL ls-remote of the ssh:// URL") || !strings.Contains(out, "FAIL clone of the scp-form URL") ||
			strings.Contains(out, "FAIL the secret") || strings.Contains(out, "FAIL another owner") {
			t.Fatalf("an unlearned dependency was fetched:\n%s", out)
		}
		for _, h := range forge.hits() {
			if h.auth {
				t.Fatalf("the credential reached the forge: %+v", h)
			}
		}
		if !withheld(t, "org/dep") {
			t.Fatal("no record says why the credential was withheld")
		}
		finish(t, "unlearned", true)
		if n := len(learnedCalls()); n != 0 || len(teachAsked) != 0 {
			t.Fatalf("a PR launch taught %d time(s) (teaching credential asked for %v)", n, teachAsked)
		}
	})

	t.Run("a teaching launch whose step fails teaches nothing", func(t *testing.T) {
		forge.reset()
		out := launch(t, defaultReq("teach-fails"), teachProbe)
		t.Logf("probe:\n%s", out)
		if !strings.Contains(out, "ok teaching clone") {
			t.Fatalf("the teaching launch did not fetch the dependency:\n%s", out)
		}
		if p := finish(t, "teach-fails", false); p != nil {
			t.Fatal("a failed step published")
		}
		if n := len(learnedCalls()); n != 0 {
			t.Fatalf("a failed step taught %d time(s)", n)
		}
		if l := m.Store.List(); len(l) != 0 {
			t.Fatalf("published %+v", l)
		}
	})

	// The default branch's teaching launch learns org/dep.
	learned := []string{"forge.test:" + forge.port + "/org/dep"}
	t.Run("a teaching launch whose step succeeds learns the dependency", func(t *testing.T) {
		forge.reset()
		out := launch(t, defaultReq("teach"), teachProbe)
		t.Logf("probe:\n%s", out)
		if strings.Contains(out, "FAIL") || !strings.Contains(out, "ok teaching clone") {
			t.Fatalf("the teaching launch:\n%s", out)
		}
		p := finish(t, "teach", true)
		if p == nil || p.Key.ID() != shared.ID() {
			t.Fatalf("published %+v (logs %v)", p, logs)
		}
		found := false
		_ = filepath.Walk(p.Entry.Dir, func(q string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && filepath.Base(q) == "index.js" {
				b, _ := os.ReadFile(q)
				found = found || strings.Contains(string(b), "dep from the private forge")
			}
			if err == nil && !fi.IsDir() && fi.Size() < 1<<20 {
				if b, _ := os.ReadFile(q); strings.Contains(string(b), forgeSecret) {
					t.Errorf("the secret is in the published cache: %s", q)
				}
			}
			return nil
		})
		if !found {
			t.Fatal("the private dependency is not in the published cache")
		}
		assertForgeHits(t, forge)
		calls := learnedCalls()
		if len(calls) != 1 {
			t.Fatalf("learned %d time(s): %v", len(calls), calls)
		}
		got := slices.Compact(slices.Sorted(slices.Values(calls[0])))
		if !slices.Equal(got, learned) {
			t.Fatalf("learned %v, want %v", got, learned)
		}
	})

	t.Run("agent launch fetches the learned dependency", func(t *testing.T) {
		var repos []string
		for _, l := range learned {
			_, p, _ := strings.Cut(l, "/")
			repos = append(repos, "/"+p)
		}
		before := len(learnedCalls())
		forge.reset()
		out := launch(t, prReq("cred", []sandbox.GitAuth{forge.auth(append([]string{"/acme/app"}, repos...)...)}, nil), gitFetchProbe)
		t.Logf("probe:\n%s", out)
		if strings.Contains(out, "FAIL") || strings.Count(out, "ok ") != 8 {
			t.Fatalf("probe:\n%s", out)
		}
		if strings.Contains(out, "has ssh") {
			t.Log("(ssh is installed in the sandbox's view, and was never needed)")
		}
		assertForgeHits(t, forge)
		if _, err := os.Stat(filepath.Join(forge.root, "org", "dep.git", "refs", "heads", "pushed")); err == nil {
			t.Fatal("the push landed")
		}
		if n := len(learnedCalls()); n != before {
			t.Fatal("a PR launch taught")
		}
		recMu.Lock()
		defer recMu.Unlock()
		var credited, pushDenied bool
		for _, r := range recs {
			if strings.Contains(r.Reason, forgeSecret) || strings.Contains(r.Path, forgeSecret) {
				t.Fatalf("the secret was recorded: %+v", r)
			}
			credited = credited || (r.Inspected && r.AllowedBy == "git fetch credential" && strings.HasPrefix(r.Path, "/org/dep.git/"))
			pushDenied = pushDenied || (r.Inspected && r.Outcome == "denied" && strings.Contains(r.Reason, "git push"))
		}
		if !credited || !pushDenied {
			t.Fatalf("credited fetch %v, denied push %v in the audit: %+v", credited, pushDenied, recs)
		}
	})
}

// assertForgeHits: the forge saw the credential on git fetches of org's
// repositories only — never on another owner's, never on a push, never on
// any other path.
func assertForgeHits(t *testing.T, f *fakeForge) {
	t.Helper()
	sawFetch := false
	for _, h := range f.hits() {
		isFetch := strings.HasPrefix(h.path, "/org/") && (strings.HasSuffix(h.path, "/git-upload-pack") || h.query == "service=git-upload-pack")
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
