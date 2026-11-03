package pkgproxy

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
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

func TestRubyGems(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewRubyGems(c) }, up.URL)
	gem := "fake gem"
	up.set("/info/rack-test", "---\n2.0.0 rack:>= 1.3|checksum:"+sha256hex("old")+",ruby:>= 2.2\n2.1.0 |checksum:"+sha256hex(gem)+"\n")
	up.set("/versions", "created_at: x\n---\nrack-test 2.1.0 abc\n")
	if code, body := r.get(t, "/versions"); code != 200 || !strings.Contains(body, "rack-test 2.1.0") {
		t.Fatalf("versions: %d %s", code, body)
	}
	if code, body := r.get(t, "/info/rack-test"); code != 200 || !strings.Contains(body, "2.1.0") {
		t.Fatalf("info: %d %s", code, body)
	}
	// The name has a dash; the version starts at the dash before a digit.
	assertArtifact(t, r, up, "/gems/rack-test-2.1.0.gem", "/gems/rack-test-2.1.0.gem", gem)
	if code, _ := r.get(t, "/gems/rack-test-9.9.9.gem"); code != 404 {
		t.Fatalf("unlisted gem: %d", code)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/info/../versions", "/info/a%2fb", "/gems/../x.gem", "/gems/x.sh", "/quick/Marshal.4.8/../x.gemspec.rz",
		"/api/v1/gems", "/info/.x",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i"})
	if !strings.Contains(files[".bundle/config"], "BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG/") || !strings.Contains(files[".gemrc"], ":sources:") {
		t.Fatalf("home files: %v", files)
	}
}

func TestMaven(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewMaven(c) }, up.URL)
	jar := "fake jar"
	p := "/org/example/lib/1.0/lib-1.0.jar"
	up.set(p+".sha256", sha256hex(jar)+"  lib-1.0.jar\n")
	assertArtifact(t, r, up, p, p, jar)
	// Only a sha1 published.
	pom := "<project/>"
	up.set("/org/example/lib/1.0/lib-1.0.pom.sha1", sha1hex(pom))
	assertArtifact(t, r, up, "/org/example/lib/1.0/lib-1.0.pom", "/org/example/lib/1.0/lib-1.0.pom", pom)
	up.set("/org/example/lib/maven-metadata.xml", "<metadata/>")
	if code, body := r.get(t, "/org/example/lib/maven-metadata.xml"); code != 200 || body != "<metadata/>" {
		t.Fatalf("metadata: %d %s", code, body)
	}
	if code, _ := r.get(t, "/org/example/lib/1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar"); code != 404 {
		t.Fatalf("snapshot served: %d", code)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/org/../etc/passwd", "/org/example/lib/1.0/..%2fx.jar", "/org//lib/1.0/x.jar", "/.m2/x/y/z",
		"/org/example/lib/1.0/a b.jar", "/x.jar",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i"})
	if !strings.Contains(files[".m2/settings.xml"], "<mirrorOf>*</mirrorOf>") || !strings.Contains(files[".gradle/init.d/conductor.gradle"], "MavenArtifactRepository") {
		t.Fatalf("home files: %v", files)
	}
}

func TestNuGet(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewNuGet(c) }, up.URL+"/v3/index.json")
	pkg := "fake nupkg"
	h := sha512.Sum512([]byte(pkg))
	up.set("/v3/index.json", `{"version":"3.0.0","resources":[{"@id":"`+up.URL+`/flat/","@type":"PackageBaseAddress/3.0.0"},{"@id":"`+up.URL+`/reg/","@type":"RegistrationsBaseUrl/3.6.0"}]}`)
	up.set("/reg/newtonsoft.json/13.0.1.json", `{"catalogEntry":"`+up.URL+`/catalog/n.json"}`)
	up.set("/catalog/n.json", `{"packageHash":"`+base64.StdEncoding.EncodeToString(h[:])+`","packageHashAlgorithm":"SHA512"}`)
	up.set("/flat/newtonsoft.json/index.json", `{"versions":["13.0.1"]}`)
	if code, body := r.get(t, "/v3/index.json"); code != 200 || !strings.Contains(body, r.base+"/v3-flatcontainer/") || strings.Contains(body, up.URL) {
		t.Fatalf("service index: %d %s", code, body)
	}
	if code, body := r.get(t, "/v3-flatcontainer/Newtonsoft.Json/index.json"); code != 200 || !strings.Contains(body, "13.0.1") {
		t.Fatalf("versions: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/v3-flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg", "/flat/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg", pkg)
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/v3-flatcontainer/../x/index.json", "/v3-flatcontainer/a/1.0/b.1.0.nupkg", "/v3-flatcontainer/a/../a.nupkg",
		"/v3/registration/x", "/v3-flatcontainer/a/1.0/a.1.0.sh",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i"})
	if c := files[".nuget/NuGet/NuGet.Config"]; !strings.Contains(c, "<clear />") || !strings.Contains(c, "/v3/index.json") {
		t.Fatalf("NuGet.Config: %q", c)
	}
}

func TestComposer(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewComposer(c) }, up.URL)
	zip1, zip2 := "fake dist 1", "fake dist 2"
	// Minified: the second entry inherits name and replaces dist; "__unset" drops a key.
	up.set("/p2/acme/widget.json", `{"minified":"composer/2.0","packages":{"acme/widget":[`+
		`{"name":"acme/widget","version":"1.0.0","homepage":"h","dist":{"type":"zip","url":"`+up.URL+`/zip/aaa","reference":"aaa","shasum":"`+sha1hex(zip1)+`"},"source":{"type":"git","url":"https://git.example/x"}},`+
		`{"version":"0.9.0","homepage":"__unset","dist":{"type":"zip","url":"`+up.URL+`/zip/bbb","reference":"bbb","shasum":""}}]}}`)
	if code, body := r.get(t, "/packages.json"); code != 200 || !strings.Contains(body, r.base+"/p2/%package%.json") {
		t.Fatalf("packages.json: %d %s", code, body)
	}
	code, body := r.get(t, "/p2/acme/widget.json")
	if code != 200 || strings.Contains(body, up.URL) || strings.Contains(body, "git.example") || !strings.Contains(body, r.base+"/dists/acme/widget/bbb.zip") ||
		strings.Count(body, `"name":"acme/widget"`) != 2 || strings.Count(body, `"homepage"`) != 1 {
		t.Fatalf("p2 not expanded and rewritten: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/dists/acme/widget/aaa.zip", "/zip/aaa", zip1)
	// No shasum published: cached by URL.
	up.set("/zip/bbb", zip2)
	for i := 0; i < 2; i++ {
		if code, got := r.get(t, "/dists/acme/widget/bbb.zip"); code != 200 || got != zip2 {
			t.Fatalf("unhashed dist: %d %s", code, got)
		}
	}
	if n := up.count("/zip/bbb"); n != 1 {
		t.Fatalf("unhashed dist fetched %d times", n)
	}
	if code, _ := r.get(t, "/dists/acme/widget/ccc.zip"); code != 404 {
		t.Fatalf("unlisted dist: %d", code)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/p2/../x.json", "/p2/acme/../x.json", "/p2/Acme/Widget.json", "/dists/acme/widget/../aaa.zip",
		"/dists/acme/widget/aaa.sh", "/dists/aaa.zip", "/p2/acme.json",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i"})
	if c := files[".composer/config.json"]; !strings.Contains(c, `"packagist.org": false`) || !strings.Contains(c, `"type": "composer"`) {
		t.Fatalf("composer config: %q", c)
	}
}

func TestPub(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewPub(c) }, up.URL)
	tgz := "fake pub archive"
	up.set("/api/packages/http", `{"name":"http","latest":{"version":"1.0.0","archive_url":"`+up.URL+`/a/http-1.0.0.tar.gz"},"versions":[{"version":"1.0.0","archive_url":"`+up.URL+`/a/http-1.0.0.tar.gz","archive_sha256":"`+sha256hex(tgz)+`"}]}`)
	if code, body := r.get(t, "/api/packages/http"); code != 200 || strings.Contains(body, up.URL) || !strings.Contains(body, r.base+"/packages/http/versions/1.0.0.tar.gz") {
		t.Fatalf("listing: %d %s", code, body)
	}
	assertArtifact(t, r, up, "/packages/http/versions/1.0.0.tar.gz", "/a/http-1.0.0.tar.gz", tgz)
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/api/packages/../x", "/api/packages/a-b", "/packages/http/versions/../1.0.0.tar.gz", "/packages/http/versions/1.0.tar.gz",
		"/api/packages/http/versions/1.0.0",
	})
	if env := strings.Join(r.srv.AgentEnv(Requester{Identity: "i"}), "\n"); !strings.Contains(env, "PUB_HOSTED_URL=http://") {
		t.Fatalf("env: %s", env)
	}
}

// hexEntry is a gzipped signed registry entry for one package.
func hexEntry(name string, rels map[string][]byte) string {
	ld := func(num int, b []byte) []byte {
		out := binary.AppendUvarint(nil, uint64(num<<3|2))
		out = binary.AppendUvarint(out, uint64(len(b)))
		return append(out, b...)
	}
	var pkg []byte
	for v, sum := range rels {
		rel := append(ld(1, []byte(v)), ld(2, []byte("inner"))...)
		rel = append(rel, ld(5, sum)...)
		pkg = append(pkg, ld(1, rel)...)
	}
	pkg = append(pkg, ld(2, []byte(name))...)
	signed := append(ld(1, pkg), ld(2, []byte("sig"))...)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(signed)
	zw.Close()
	return buf.String()
}

func TestHex(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewHex(c) }, up.URL)
	tar := "fake hex tarball"
	h := sha256.Sum256([]byte(tar))
	o := sha256.Sum256([]byte("other"))
	entry := hexEntry("jason", map[string][]byte{"1.4.0": h[:], "1.3.0": o[:]})
	up.set("/packages/jason", entry)
	if code, body := r.get(t, "/packages/jason"); code != 200 || body != entry {
		t.Fatalf("registry entry not relayed unchanged: %d", code)
	}
	assertArtifact(t, r, up, "/tarballs/jason-1.4.0.tar", "/tarballs/jason-1.4.0.tar", tar)
	if code, _ := r.get(t, "/tarballs/jason-9.0.0.tar"); code != 404 {
		t.Fatalf("unlisted tarball: %d", code)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/packages/../names", "/packages/Jason", "/tarballs/jason.tar", "/tarballs/../x-1.0.0.tar", "/installs/hex-1.ez",
	})
	if env := strings.Join(r.srv.AgentEnv(Requester{Identity: "i"}), "\n"); !strings.Contains(env, "HEX_MIRROR=http://") {
		t.Fatalf("env: %s", env)
	}
}

func TestCRAN(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewCRAN(c) }, up.URL)
	src := "fake R source"
	m := md5.Sum([]byte(src))
	idx := "Package: other\nVersion: 1.0\nMD5sum: 00\n\nPackage: jsonlite\nVersion: 1.8.8\nMD5sum: " + hex.EncodeToString(m[:]) + "\n\n"
	up.set("/src/contrib/PACKAGES", idx)
	if code, body := r.get(t, "/src/contrib/PACKAGES"); code != 200 || body != idx {
		t.Fatalf("PACKAGES: %d %s", code, body)
	}
	up.set("/src/contrib/jsonlite_1.8.8.tar.gz", "tampered")
	if code, msg := r.get(t, "/src/contrib/jsonlite_1.8.8.tar.gz"); code != http.StatusBadGateway || !strings.Contains(msg, "SECURITY") {
		t.Fatalf("md5 mismatch served: %d %s", code, msg)
	}
	up.set("/src/contrib/jsonlite_1.8.8.tar.gz", src)
	for i := 0; i < 2; i++ {
		if code, got := r.get(t, "/src/contrib/jsonlite_1.8.8.tar.gz"); code != 200 || got != src {
			t.Fatalf("archive: %d %s", code, got)
		}
	}
	if n := up.count("/src/contrib/jsonlite_1.8.8.tar.gz"); n != 2 {
		t.Fatalf("archive fetched %d times", n)
	}
	assertRefusedBeforeUpstream(t, r, up, []string{
		"/src/contrib/../x", "/etc/passwd/x/y", "/src/contrib/x.sh", "/src/contrib/Archive/../../x_1.0.tar.gz", "/bin/x",
	})
	_, files := r.srv.AgentConfig(Requester{Identity: "i"})
	if !strings.Contains(files[".Rprofile"], "options(repos") {
		t.Fatalf("Rprofile: %v", files)
	}
}
