package pkgproxy

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeUp is a scripted upstream: path → body, counting requests.
type fakeUp struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]string
	hits   map[string]int
	all    []string
}

func newFakeUp(t *testing.T) *fakeUp {
	f := &fakeUp{routes: map[string]string{}, hits: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		p := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			p += "?" + r.URL.RawQuery
		}
		f.hits[p]++
		f.all = append(f.all, p)
		body, ok := f.routes[p]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found: "+p, http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUp) set(p, body string) {
	f.mu.Lock()
	f.routes[p] = body
	f.mu.Unlock()
}

func (f *fakeUp) count(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[p]
}

func (f *fakeUp) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.all...)
}

// regRig is one ecosystem behind a live proxy server.
type regRig struct {
	srv    *Server
	cache  *Cache
	base   string
	mu     sync.Mutex
	events []Event
}

func newRegRig(t *testing.T, mk func(RegistryConfig) (Ecosystem, error), upstream string, private ...string) *regRig {
	t.Helper()
	c, err := OpenCache(filepath.Join(t.TempDir(), "cache"), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	f, err := NewFetcher(upstream)
	if err != nil {
		t.Fatal(err)
	}
	e, err := mk(RegistryConfig{Upstream: upstream, Private: private, Cache: c, Fetcher: f})
	if err != nil {
		t.Fatal(err)
	}
	r := &regRig{cache: c}
	r.srv, err = New(bytes.Repeat([]byte("k"), 32), e)
	if err != nil {
		t.Fatal(err)
	}
	r.srv.OnEvent = func(ev Event) { r.mu.Lock(); r.events = append(r.events, ev); r.mu.Unlock() }
	if _, err := r.srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.srv.Close)
	r.base = "http://" + r.srv.Addr() + "/" + r.srv.Token(Requester{Identity: "profile:x", Dispatch: "d-9"}) + "/" + e.Name()
	return r
}

func (r *regRig) get(t *testing.T, p string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", r.base+p, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *regRig) evs() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func sha256hex(b string) string { h := sha256.Sum256([]byte(b)); return hex.EncodeToString(h[:]) }
func sha1hex(b string) string   { h := sha1.Sum([]byte(b)); return hex.EncodeToString(h[:]) }
func sri512(b string) string {
	h := sha512.Sum512([]byte(b))
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

// assertArtifact: served once from upstream, then from the cache; a
// tampered copy is refused and not cached; the event names the requester.
func assertArtifact(t *testing.T, r *regRig, up *fakeUp, reqPath, upPath, body string) {
	t.Helper()
	up.set(upPath, "tampered"+body)
	if code, msg := r.get(t, reqPath); code != http.StatusBadGateway || !strings.Contains(msg, "SECURITY") {
		t.Fatalf("tampered %s: %d %q", reqPath, code, msg)
	}
	up.set(upPath, body)
	for i := 0; i < 2; i++ {
		if code, got := r.get(t, reqPath); code != 200 || got != body {
			t.Fatalf("%s: %d %q", reqPath, code, got)
		}
	}
	if n := up.count(upPath); n != 2 {
		t.Fatalf("%s fetched %d times upstream; want the refused one and one fill", upPath, n)
	}
	var seen bool
	for _, e := range r.evs() {
		if e.Artifact == "artifact" && e.Upstream && e.Status == 200 && e.Requester.Dispatch == "d-9" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no audited upstream fetch: %+v", r.evs())
	}
}

// assertRefusedBeforeUpstream: none of paths is served or reaches upstream.
func assertRefusedBeforeUpstream(t *testing.T, r *regRig, up *fakeUp, paths []string) {
	t.Helper()
	before := len(up.requests())
	for _, p := range paths {
		if code, _ := r.get(t, p); code == 200 {
			t.Errorf("%s: served", p)
		}
	}
	if got := up.requests()[before:]; len(got) != 0 {
		t.Fatalf("invalid requests reached upstream: %q", got)
	}
}

func TestNPM(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewNPM(c) }, up.URL)
	tgz := "fake tarball bytes"
	pk := `{"name":"left-pad","versions":{"1.0.0":{"name":"left-pad","version":"1.0.0","dist":{"tarball":"` + up.URL + `/left-pad/-/left-pad-1.0.0.tgz","integrity":"` + sri512(tgz) + `","shasum":"` + sha1hex(tgz) + `"}}}}`
	up.set("/left-pad", pk)
	code, body := r.get(t, "/left-pad", "Accept", "application/vnd.npm.install-v1+json")
	if code != 200 || !strings.Contains(body, r.base+"/left-pad/-/left-pad-1.0.0.tgz") || strings.Contains(body, up.URL) {
		t.Fatalf("packument not rewritten: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/left-pad/-/left-pad-1.0.0.tgz", "/left-pad/-/left-pad-1.0.0.tgz", tgz)

	// A scoped package, legacy sha1 only.
	sc := "scoped bytes"
	up.set("/@acme%2fwidget", `{"versions":{"2.0.0":{"dist":{"tarball":"`+up.URL+`/@acme/widget/-/widget-2.0.0.tgz","shasum":"`+sha1hex(sc)+`"}}}}`)
	if code, body := r.get(t, "/@acme%2fwidget"); code != 200 || !strings.Contains(body, r.base+"/@acme/widget/-/widget-2.0.0.tgz") {
		t.Fatalf("scoped packument: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/@acme/widget/-/widget-2.0.0.tgz", "/@acme/widget/-/widget-2.0.0.tgz", sc)

	// A tarball the packument does not list is never fetched.
	if code, _ := r.get(t, "/left-pad/-/left-pad-9.9.9.tgz"); code != 404 {
		t.Fatalf("unlisted tarball: %d", code)
	}
	// A packument pointing a tarball at an http host that is not the
	// upstream is refused (metadata-derived URLs must be https).
	up.set("/evil", `{"versions":{"1.0.0":{"dist":{"tarball":"http://198.51.100.9/evil-1.0.0.tgz","integrity":"`+sri512("x")+`"}}}}`)
	if code, msg := r.get(t, "/evil/-/evil-1.0.0.tgz"); code != http.StatusBadGateway || !strings.Contains(msg, "non-https") {
		t.Fatalf("http tarball host: %d %s", code, msg)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/../etc/passwd", "/%2e%2e/x", "/left-pad/-/..%2f..%2fx.tgz", "/left-pad/-/x.sh",
		"/@acme/../../x", "/.hidden", "/_x", "/a b", "/http:%2f%2fevil.test",
		"/" + strings.Repeat("a", 215),
	})
	env := strings.Join(r.srv.AgentEnv(Requester{Identity: "i"}), "\n")
	for _, want := range []string{"npm_config_registry=http://", "YARN_NPM_REGISTRY_SERVER=http://", "npm_config_audit=false"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
}

func TestPyPI(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewPyPI(c) }, up.URL)
	whl := "fake wheel"
	sdist := "fake sdist"
	up.set("/simple/my-pkg/", `{"meta":{"api-version":"1.1"},"name":"my-pkg","files":[`+
		`{"filename":"my_pkg-1.0-py3-none-any.whl","url":"`+up.URL+`/packages/aa/my_pkg-1.0-py3-none-any.whl","hashes":{"sha256":"`+sha256hex(whl)+`"},"requires-python":">=3.8","core-metadata":{"sha256":"x"}},`+
		`{"filename":"my_pkg-1.0.tar.gz","url":"../../packages/bb/my_pkg-1.0.tar.gz","hashes":{"sha256":"`+sha256hex(sdist)+`"},"yanked":"broken"},`+
		`{"filename":"../escape.whl","url":"`+up.URL+`/x","hashes":{}}]}`)
	code, body := r.get(t, "/simple/My_Pkg/", "Accept", "application/vnd.pypi.simple.v1+json")
	if code != 200 || !strings.Contains(body, r.base+"/files/my-pkg/my_pkg-1.0-py3-none-any.whl") || strings.Contains(body, up.URL) || strings.Contains(body, "core-metadata") || strings.Contains(body, "escape") {
		t.Fatalf("simple JSON: %d %s", code, body)
	}
	code, body = r.get(t, "/simple/my-pkg/")
	if code != 200 || !strings.Contains(body, `#sha256=`+sha256hex(whl)) || !strings.Contains(body, `data-requires-python="&gt;=3.8"`) || !strings.Contains(body, `data-yanked="broken"`) {
		t.Fatalf("simple HTML: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/files/my-pkg/my_pkg-1.0-py3-none-any.whl", "/packages/aa/my_pkg-1.0-py3-none-any.whl", whl)
	// A relative file URL resolves against the upstream's own listing.
	assertArtifact(t, r, up, "/files/my-pkg/my_pkg-1.0.tar.gz", "/packages/bb/my_pkg-1.0.tar.gz", sdist)
	up.set("/pypi/my-pkg/json", `{"info":{"name":"my-pkg"},"urls":[{"filename":"my_pkg-1.0.tar.gz","url":"`+up.URL+`/packages/bb/my_pkg-1.0.tar.gz"}],"releases":{"1.0":[{"filename":"my_pkg-1.0.tar.gz","url":"`+up.URL+`/x"}]}}`)
	if code, body := r.get(t, "/pypi/my-pkg/json"); code != 200 || strings.Contains(body, up.URL) || !strings.Contains(body, r.base+"/files/my-pkg/my_pkg-1.0.tar.gz") {
		t.Fatalf("JSON API: %d %s", code, body)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/simple/../x/", "/simple/a%2fb/", "/files/my-pkg/../../etc/passwd", "/files/my-pkg/x.sh",
		"/files/my-pkg/sub%2fx.whl", "/simple/", "/pypi/x/../json", "/simple/-bad/",
	})
	env := strings.Join(r.srv.AgentEnv(Requester{Identity: "i"}), "\n")
	if !strings.Contains(env, "PIP_INDEX_URL=http://") || !strings.Contains(env, "UV_DEFAULT_INDEX=http://") {
		t.Fatalf("env: %s", env)
	}
}

func TestCargo(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewCargo(c) }, up.URL)
	crate := "fake crate"
	up.set("/config.json", `{"dl":"`+up.URL+`/crates/{crate}/{crate}-{version}.crate"}`)
	up.set("/se/rd/serde", `{"name":"serde","vers":"1.0.0","cksum":"`+sha256hex(crate)+`"}`+"\n"+`{"name":"serde","vers":"1.0.1","cksum":"`+sha256hex("other")+`"}`+"\n")
	if code, body := r.get(t, "/index/config.json"); code != 200 || !strings.Contains(body, r.base+"/api/v1/crates") {
		t.Fatalf("config.json: %d %s", code, body)
	}
	if code, body := r.get(t, "/index/se/rd/serde"); code != 200 || !strings.Contains(body, `"vers":"1.0.1"`) {
		t.Fatalf("index: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/api/v1/crates/serde/1.0.0/download", "/crates/serde/serde-1.0.0.crate", crate)
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/index/xx/yy/serde", "/index/../config.json", "/index/se/rd/../../x", "/api/v1/crates/serde/../download",
		"/api/v1/crates/serde/1.0/download", "/api/v1/crates/se%2frde/1.0.0/download", "/index/1/ab",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i", Dispatch: "d"})
	cfg := files[".cargo/config.toml"]
	if !strings.Contains(cfg, `replace-with = "conductor"`) || !strings.Contains(cfg, "sparse+http://") {
		t.Fatalf("cargo config: %q", cfg)
	}
	if tok := r.srv.Token(Requester{Identity: "i", Dispatch: "d"}); strings.Contains(cfg, tok) {
		t.Fatal("a HOME file carries a dispatch's token; it must name only the identity")
	}
}
