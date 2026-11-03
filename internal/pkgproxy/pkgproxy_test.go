package pkgproxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/pkgproxy/pkgproxytest"
)

var hello = pkgproxytest.Module{Path: "example.com/hello", Version: "v1.0.0", Files: map[string]string{
	"go.mod":   "module example.com/hello\n\ngo 1.21\n",
	"hello.go": "package hello\n\nfunc Hello() string { return \"hello from the proxy\" }\n",
}}

var secret = pkgproxytest.Module{Path: "git.internal.test/team/secret", Version: "v0.1.0", Files: map[string]string{
	"go.mod": "module git.internal.test/team/secret\n",
	"s.go":   "package secret\n",
}}

type rig struct {
	w      *pkgproxytest.World
	srv    *Server
	cache  *Cache
	base   string // the agent's GOPROXY
	mu     sync.Mutex
	events []Event
}

func newRig(t *testing.T, max int64, private ...string) *rig {
	t.Helper()
	w := pkgproxytest.New(t, []pkgproxytest.Module{hello}, secret)
	r := &rig{w: w}
	db, err := ParseSumDB(w.GOSUMDB)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r.cache, err = OpenCache(filepath.Join(dir, "cache"), max)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.cache.Close)
	f, err := NewFetcher(w.Upstream.URL, db.URL)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGo(GoConfig{Upstream: w.Upstream.URL, SumDB: db, Private: private, Cache: r.cache, SumDir: filepath.Join(dir, "sumdb"), Fetcher: f})
	if err != nil {
		t.Fatal(err)
	}
	r.srv, err = New(bytes.Repeat([]byte("k"), 32), g)
	if err != nil {
		t.Fatal(err)
	}
	r.srv.OnEvent = func(e Event) {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
	addr, err := r.srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.srv.Close)
	r.base = "http://" + addr + "/" + r.srv.Token(Requester{Identity: "profile:fixer", Dispatch: "d-42"}) + "/go"
	return r
}

func (r *rig) get(t *testing.T, p string) (int, string) {
	t.Helper()
	resp, err := http.Get(r.base + p)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *rig) evs() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// A module is fetched once, verified, cached, and every later request — from
// any agent — is served from the cache; each upstream fetch is audited with
// who asked.
func TestGoModuleIsVerifiedCachedAndAudited(t *testing.T) {
	r := newRig(t, 1<<30)
	if code, body := r.get(t, "/example.com/hello/@v/list"); code != 200 || !strings.Contains(body, "v1.0.0") {
		t.Fatalf("list: %d %q", code, body)
	}
	for _, f := range []string{".info", ".mod", ".zip"} {
		for i := 0; i < 2; i++ {
			code, body := r.get(t, "/example.com/hello/@v/v1.0.0"+f)
			if code != 200 {
				t.Fatalf("%s: %d %s", f, code, body)
			}
		}
		if n := r.w.Hits("/example.com/hello/@v/v1.0.0" + f); n != 1 {
			t.Fatalf("%s fetched upstream %d times, want once", f, n)
		}
	}
	_, z := r.get(t, "/example.com/hello/@v/v1.0.0.zip")
	if z != string(r.w.Zip("example.com/hello", "v1.0.0")) {
		t.Fatal("the zip served is not the upstream's")
	}
	var fetchedZip, servedZip int
	for _, e := range r.evs() {
		if e.Requester != (Requester{Identity: "profile:fixer", Dispatch: "d-42"}) || e.Ecosystem != "go" {
			t.Fatalf("event without its caller: %+v", e)
		}
		if e.Artifact == "zip" {
			servedZip++
			if e.Upstream {
				fetchedZip++
			}
			if e.Module != "example.com/hello" || e.Version != "v1.0.0" {
				t.Fatalf("zip event: %+v", e)
			}
		}
	}
	if fetchedZip != 1 || servedZip != 3 {
		t.Fatalf("zip events: %d fetched, %d served; want 1 and 3", fetchedZip, servedZip)
	}
}

// Content that does not match the checksum database is neither served nor
// cached: one agent cannot poison what the next is served.
func TestGoTamperedModuleIsRefusedAndNotCached(t *testing.T) {
	r := newRig(t, 1<<30)
	zp := "/example.com/hello/@v/v1.0.0.zip"
	r.w.Tamper(zp, r.w.Zip("example.com/hello", "v1.0.0")[:200])
	if code, body := r.get(t, zp); code == 200 {
		t.Fatalf("a truncated zip was served: %q", body)
	}
	evil := pkgproxytest.BuildZip(t, pkgproxytest.Module{Path: hello.Path, Version: hello.Version, Files: map[string]string{
		"go.mod":   hello.Files["go.mod"],
		"hello.go": "package hello\n\nfunc Hello() string { return \"pwned\" }\n",
	}})
	r.w.Tamper(zp, evil)
	code, body := r.get(t, zp)
	if code != http.StatusBadGateway || !strings.Contains(body, "SECURITY") || !strings.Contains(body, "checksum") {
		t.Fatalf("tampered zip: %d %q", code, body)
	}
	r.w.Tamper("/example.com/hello/@v/v1.0.0.mod", []byte("module example.com/hello\n\nreplace x => y v1.0.0\n"))
	if code, body := r.get(t, "/example.com/hello/@v/v1.0.0.mod"); code != http.StatusBadGateway || !strings.Contains(body, "SECURITY") {
		t.Fatalf("tampered go.mod: %d %q", code, body)
	}
	if _, err := r.cache.Open("go/example.com/hello/@v/v1.0.0.zip"); err == nil {
		t.Fatal("the tampered zip was cached")
	}
	// The real thing, once upstream is honest again.
	r.w.Tamper(zp, nil)
	if code, body := r.get(t, zp); code != 200 || body != string(r.w.Zip("example.com/hello", "v1.0.0")) {
		t.Fatalf("honest zip after a refusal: %d", code)
	}
}

// A module the checksum database does not know (a private one) fails
// clearly — never fetched any other way.
func TestGoUnknownToChecksumDatabaseFailsClearly(t *testing.T) {
	r := newRig(t, 1<<30)
	code, body := r.get(t, "/git.internal.test/team/secret/@v/v0.1.0.zip")
	if code != http.StatusNotFound || !strings.Contains(body, "not in the checksum database") || !strings.Contains(body, "sandbox.packages.go.private") {
		t.Fatalf("unknown module: %d %q", code, body)
	}
	code, body = r.get(t, "/example.com/absent/@v/v1.0.0.mod")
	if code != http.StatusNotFound || !strings.Contains(body, "never direct") {
		t.Fatalf("absent module: %d %q", code, body)
	}
}

// A configured private pattern is served from the upstream without the
// checksum database, and never cached.
func TestGoPrivatePatternSkipsSumDBAndCache(t *testing.T) {
	r := newRig(t, 1<<30, "git.internal.test/*")
	for i := 0; i < 2; i++ {
		if code, body := r.get(t, "/git.internal.test/team/secret/@v/v0.1.0.zip"); code != 200 {
			t.Fatalf("private module: %d %q", code, body)
		}
	}
	if n := r.w.Hits("/git.internal.test/team/secret/@v/v0.1.0.zip"); n != 2 {
		t.Fatalf("private zip fetched %d times; it must never be cached", n)
	}
	for _, p := range r.w.Requests() {
		if strings.Contains(p, "sumdb") {
			t.Fatal("a private module was looked up in the checksum database")
		}
	}
	env := strings.Join(r.srv.AgentEnv(Requester{Identity: "x"}), "\n")
	if !strings.Contains(env, "GONOSUMDB=git.internal.test/*") {
		t.Fatalf("agent env lacks the private pattern:\n%s", env)
	}
}

// Every agent-chosen part of a request is a module path or version checked
// against the module grammar; anything else is refused before upstream is
// asked anything — no traversal, no other host, no other file.
func TestGoRequestPathsAreValidatedBeforeAnyFetch(t *testing.T) {
	r := newRig(t, 1<<30)
	bad := []string{
		"/../../../etc/passwd/@v/v1.0.0.zip",
		"/example.com/%2e%2e/%2e%2e/@v/v1.0.0.zip",
		"/example.com/hello/@v/..%2f..%2fx.zip",
		"/example.com/hello/@v/v1.0.0%2f..%2f..%2fx.zip",
		"/Example.com/hello/@v/v1.0.0.zip", // not canonically escaped
		"/example.com/hello/@v/v1.0.zip",   // not canonical
		"/example.com/hello/@v/latest.zip",
		"/example.com/hello/@v/v1.0.0.tar",
		"/example.com/hello/@v/v1.0.0.info/x",
		"/http:/evil.test/x/@v/list",
		"/evil.test:8080/x/@v/list",
		"/@v/list",
		"/example.com/hello",
		"/sumdb/sum.golang.org/lookup/x",
		"/sumdb/sum.test/tile/../../x",
		"/sumdb/sum.test/lookup/example.com/hello@v1.0.0/../../x",
		"/sumdb/evil.test/latest",
		"/sumdb/sum.test/tile/8/0/../../../x",
		"/sumdb/sum.test/lookup/example.com/hello@v1.0",
		"/sumdb/sum.test/lookup/x",
		"/sumdb/sum.test/whatever",
		"/example.com/hello/v2/@v/v1.0.0.zip",  // major version not matching the path
		"/example.com/hello/@v/v2.0.0.zip",     // v2 needs /v2 or +incompatible
		"/example.com/hello/@v/feature/x.info", // a query is one name, no separators
		"/example.com/hello/@v/" + strings.Repeat("a", 200) + ".info",
		"/example.com/hello/@v/-x.info",
	}
	before := len(r.w.Requests())
	for _, p := range bad {
		if code, _ := r.get(t, p); code == 200 {
			t.Errorf("%s: served", p)
		}
	}
	if got := r.w.Requests()[before:]; len(got) != 0 {
		t.Fatalf("invalid requests reached upstream: %q", got)
	}
	if n := r.w.SumDBRequests(); n != 0 {
		t.Fatalf("invalid requests reached the checksum database %d times", n)
	}
	ents, _ := os.ReadDir(r.cache.Dir())
	for _, e := range ents {
		if e.Name() != ".tmp" && e.Name() != "go" {
			t.Fatalf("cache holds %s", e.Name())
		}
	}
}

// The proxy authenticates its caller; a request without a valid token is
// refused, and a forged token does not verify.
func TestTokenIsRequired(t *testing.T) {
	r := newRig(t, 1<<30)
	addr := r.srv.Addr()
	for _, p := range []string{
		"/go/example.com/hello/@v/list",
		"/x.y/go/example.com/hello/@v/list",
		"/" + strings.Replace(r.srv.Token(Requester{Identity: "a"}), "a", "b", 1) + "/go/example.com/hello/@v/list",
	} {
		resp, err := http.Get("http://" + addr + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	resp, err := http.Post(r.base+"/example.com/hello/@v/list", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	other, _ := New(bytes.Repeat([]byte("z"), 32))
	if _, ok := r.srv.caller(other.Token(Requester{Identity: "a"})); ok {
		t.Fatal("a token signed with another key verified")
	}
	if c, ok := r.srv.caller(r.srv.Token(Requester{Identity: "dir:/w/x", Dispatch: "d"})); !ok || c.Identity != "dir:/w/x" || c.Dispatch != "d" {
		t.Fatalf("token round trip: %+v %v", c, ok)
	}
}

// Agents asking for the same module at once cause one upstream fetch.
func TestConcurrentRequestsFetchOnce(t *testing.T) {
	r := newRig(t, 1<<30)
	r.w.Delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(r.base + "/example.com/hello/@v/v1.0.0.zip")
			if err != nil {
				errs <- err.Error()
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !bytes.Equal(b, r.w.Zip("example.com/hello", "v1.0.0")) {
				errs <- resp.Status
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if n := r.w.Hits("/example.com/hello/@v/v1.0.0.zip"); n != 1 {
		t.Fatalf("16 concurrent requests fetched upstream %d times", n)
	}
}

// The checksum database is proxied for the agent's own go command — only the
// configured one, only well-formed paths.
func TestSumDBProxy(t *testing.T) {
	r := newRig(t, 1<<30)
	if code, _ := r.get(t, "/sumdb/sum.test/supported"); code != 200 {
		t.Fatalf("supported: %d", code)
	}
	if code, _ := r.get(t, "/sumdb/sum.golang.org/supported"); code != 404 {
		t.Fatalf("an unconfigured database must not be proxied: %d", code)
	}
	if code, body := r.get(t, "/sumdb/sum.test/lookup/example.com/hello@v1.0.0"); code != 200 || !strings.Contains(body, "example.com/hello v1.0.0 h1:") {
		t.Fatalf("lookup: %d %q", code, body)
	}
	if code, _ := r.get(t, "/sumdb/sum.test/latest"); code != 200 {
		t.Fatalf("latest: %d", code)
	}
}

// Upstream unreachable: a clear failure, nothing fetched any other way.
func TestUpstreamDownFailsClearly(t *testing.T) {
	r := newRig(t, 1<<30)
	r.w.Upstream.Close()
	code, body := r.get(t, "/example.com/hello/@v/v1.0.0.mod")
	if code != http.StatusBadGateway || !strings.Contains(body, "unreachable") {
		t.Fatalf("upstream down: %d %q", code, body)
	}
}

// A redirect is followed only to https and only to a public address — a
// compromised upstream cannot point conductor at its own network.
func TestFetcherRedirectGuards(t *testing.T) {
	r := newRig(t, 1<<30)
	zp := "/example.com/hello/@v/v1.0.0.zip"
	r.w.Redirect(zp, "http://198.51.100.7/blob.zip")
	if code, body := r.get(t, zp); code != http.StatusBadGateway || !strings.Contains(body, "non-https") {
		t.Fatalf("http redirect: %d %q", code, body)
	}
	r.w.Redirect("/example.com/hello/@v/v1.0.0.mod", r.w.Upstream.URL+"/example.com/hello/@v/v1.0.0.mod")
	if code, body := r.get(t, "/example.com/hello/@v/v1.0.0.mod"); code != http.StatusBadGateway || !strings.Contains(body, "redirected more than") {
		t.Fatalf("redirect loop: %d %q", code, body)
	}
	f, err := NewFetcher("https://proxy.example")
	if err != nil {
		t.Fatal(err)
	}
	f.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	}
	if _, err := f.dial(context.Background(), "tcp", "storage.example:443"); err != errBlockedAddr {
		t.Fatalf("redirect target resolving inside: %v", err)
	}
}

// The cache stays within its bound, evicting the least recently used.
func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c, err := OpenCache(t.TempDir(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	put := func(k string) {
		if _, err := c.Fill(k, func(f *os.File) error { _, err := f.Write(make([]byte, 300)); return err }); err != nil {
			t.Fatal(err)
		}
	}
	put("go/a")
	put("go/b")
	put("go/c")
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(c.Dir(), "go", "a"), old, old)
	if f, err := c.Open("go/b"); err == nil { // b is used: newest
		f.Close()
	}
	put("go/d")
	if c.Size() > 1000 {
		t.Fatalf("cache %d bytes over its 1000 bound", c.Size())
	}
	if _, err := c.Open("go/a"); err == nil {
		t.Fatal("the least recently used artifact survived eviction")
	}
	for _, k := range []string{"go/b", "go/d"} {
		if f, err := c.Open(k); err != nil {
			t.Fatalf("%s evicted: %v", k, err)
		} else {
			f.Close()
		}
	}
	for _, k := range []string{"../x", "go/../../x", "/etc/passwd", "go//x", "go/./x", ".tmp/x", "go/x/.."} {
		if _, err := c.Fill(k, func(*os.File) error { return nil }); err == nil {
			t.Errorf("cache key %q accepted", k)
		}
	}
}

// The agent's go is pointed at conductor only, verification on.
func TestGoAgentEnv(t *testing.T) {
	r := newRig(t, 1<<30)
	env := map[string]string{}
	for _, kv := range r.srv.AgentEnv(Requester{Identity: "i"}) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if p := env["GOPROXY"]; !strings.HasPrefix(p, "http://"+r.srv.Addr()+"/") || strings.ContainsAny(p, ",|") {
		t.Fatalf("GOPROXY %q must be conductor alone (no fallback)", p)
	}
	if env["GOSUMDB"] != r.w.VKey {
		t.Fatalf("GOSUMDB %q", env["GOSUMDB"])
	}
	for _, k := range []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE"} {
		if v, ok := env[k]; !ok || v != "" {
			t.Fatalf("%s must be set empty, got %q (present %v)", k, v, ok)
		}
	}
	db, _ := ParseSumDB("sum.golang.org")
	if db.GoEnv != "sum.golang.org" || db.URL != "https://sum.golang.org" || !strings.HasPrefix(db.Key, "sum.golang.org+") {
		t.Fatalf("default sumdb: %+v", db)
	}
	for _, s := range []string{"off", "off+x", "", "a b c", "unknown.example"} {
		if _, err := ParseSumDB(s); err == nil {
			t.Errorf("sumdb %q accepted", s)
		}
	}
}

// The proxy listens on loopback only — it is reached through the sandbox's
// relay, never from the network.
func TestListenIsLoopbackOnly(t *testing.T) {
	s, _ := New(bytes.Repeat([]byte("k"), 32))
	for _, a := range []string{"0.0.0.0:0", "[::]:0", "localhost:0", ":0"} {
		if _, err := s.Listen(a); err == nil {
			s.Close()
			t.Fatalf("listened on %s", a)
		}
	}
}
