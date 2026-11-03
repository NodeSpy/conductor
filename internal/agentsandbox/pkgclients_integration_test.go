//go:build linux || darwin

package agentsandbox

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/pkgproxy"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The package clients' end-to-end tests: a real client (npm, pip, uv,
// cargo, bundler, gradle, dotnet) installs one package inside the real
// sandbox, through conductor's package proxy, from a local fake registry.
// Each asserts the package came through the proxy (an audited upstream
// fetch for the dispatch), that the fake registry is not reachable from
// inside directly, and that the real public registry is not either.
//
// They run only with CONDUCTOR_PKG_CLIENTS set: "all" runs every client
// found on PATH, a comma list names clients that must be there (a missing
// one fails rather than skips).

type fakeRegistry struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	seen  []string
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{files: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.Path)
		b, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRegistry) set(p string, b []byte) { f.mu.Lock(); f.files[p] = b; f.mu.Unlock() }

func (f *fakeRegistry) saw(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.seen {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

type clientCase struct {
	name string
	bins []string // must be on PATH (the first is the client)
	eco  func(pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error)
	// upstream is the ecosystem's upstream URL given the fake's base.
	upstream func(base string) string
	// setup fills the fake registry and the workspace; it may add read-only
	// paths and env the toolchain needs.
	setup func(t *testing.T, f *fakeRegistry, ws string) (ro, env []string)
	// script installs and uses the package; its output must hold want.
	script string
	want   string
	// public is the real registry, unreachable from inside.
	public string
}

func TestSandboxedPackageClients(t *testing.T) {
	sel := os.Getenv("CONDUCTOR_PKG_CLIENTS")
	if sel == "" {
		t.Skip("set CONDUCTOR_PKG_CLIENTS (all, or a comma list) to run the package-client e2e tests")
	}
	if why := Probe(); why != "" {
		t.Fatalf("the agent sandbox cannot be built here: %s", why)
	}
	required := map[string]bool{}
	if sel != "all" {
		for _, c := range strings.Split(sel, ",") {
			required[strings.TrimSpace(c)] = true
		}
	}
	self := selfConductor(t) // built once, for every client
	for _, c := range clientCases() {
		t.Run(c.name, func(t *testing.T) {
			if sel != "all" && !required[c.name] {
				t.Skip("not selected")
			}
			for _, b := range c.bins {
				if _, err := exec.LookPath(b); err != nil {
					if required[c.name] {
						t.Fatalf("%s is required here and not on PATH", b)
					}
					t.Skipf("no %s on PATH", b)
				}
			}
			runClientCase(t, c, self)
		})
	}
}

func runClientCase(t *testing.T, c clientCase, self func() (string, error)) {
	f := newFakeRegistry(t)
	root, err := SocketDir("pkc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		os.RemoveAll(root)
	})
	ws, state, real := filepath.Join(root, "ws"), filepath.Join(root, "st"), filepath.Join(root, "home")
	for _, d := range []string{ws, state, real} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ro, extraEnv := c.setup(t, f, ws)

	cache, err := pkgproxy.OpenCache(filepath.Join(state, ".pkgproxy", "cache"), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	up := c.upstream(f.URL)
	fetch, err := pkgproxy.NewFetcher(up)
	if err != nil {
		t.Fatal(err)
	}
	eco, err := c.eco(pkgproxy.RegistryConfig{Upstream: up, Cache: cache, Fetcher: fetch})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := pkgproxy.New(bytes.Repeat([]byte("e"), 32), eco)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []pkgproxy.Event
	srv.OnEvent = func(e pkgproxy.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }
	if _, err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	r := &Resolver{StateRoot: filepath.Join(state, "agent-state"), RealHome: real, GOOS: runtime.GOOS,
		Packages: func(id, disp string) PackageAccess {
			env, files := srv.AgentConfig(pkgproxy.Requester{Identity: id, Dispatch: disp})
			return PackageAccess{Addr: srv.Addr(), Env: env, HomeFiles: files}
		}}
	pol, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "profile:" + c.name, Dispatch: "d-" + c.name}, "claude-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Each client's toolchain is shown read-only where the base system does
	// not show it already (a runner's tool cache, a user-installed SDK).
	var path []string
	for _, b := range c.bins {
		p, _ := exec.LookPath(b)
		// PATH names the tool where the operator's does (a launcher script
		// finds its install from there); its install is shown read-only.
		if d := filepath.Dir(p); !contains(path, d) {
			path = append(path, d)
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
		if inst := filepath.Dir(filepath.Dir(p)); !visibleInBase(inst) && !contains(pol.ReadOnly, inst) {
			pol.ReadOnly = append(pol.ReadOnly, inst)
		}
	}
	pol.ReadOnly = append(pol.ReadOnly, ro...)
	pol.Env = append(append([]string(nil), pol.Env...), "PATH="+strings.Join(append(path, "/usr/local/bin", "/usr/bin", "/bin"), ":"))
	pol.Env = append(pol.Env, extraEnv...)

	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	script := "set -e\n" + c.script + "\n" +
		"set +e\n" +
		// The fake registry itself is unreachable from inside: only conductor is.
		"curl -s --max-time 5 -o /dev/null " + f.URL + "/probe-direct\n" +
		// So is the real one.
		"curl -sf --max-time 10 -o /dev/null " + c.public + " && echo DIRECT-REACHED\n" +
		"exit 0\n"
	l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
		SelfExe: self, EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint, Relay: rel.Relay,
		LookPath: exec.LookPath, RealHome: real, Inherited: os.Environ(), Bins: append(append([]string(nil), c.bins...), "curl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	out, err := cmd.CombinedOutput()
	t.Logf("sandboxed %s:\n%s", c.name, out)
	if err != nil {
		t.Fatalf("sandboxed %s failed: %v", c.name, err)
	}
	if !strings.Contains(string(out), c.want) {
		t.Fatalf("the installed package did not run (want %q)", c.want)
	}
	if strings.Contains(string(out), "DIRECT-REACHED") {
		t.Fatalf("the sandbox reached %s directly", c.public)
	}
	if f.saw("probe-direct") {
		t.Fatal("the sandbox reached the fake registry directly")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		if e.Artifact == "artifact" && e.Upstream && e.Status == 200 && e.Requester.Dispatch == "d-"+c.name {
			return
		}
	}
	t.Fatalf("no audited upstream artifact fetch for the dispatch: %+v", events)
}

// Archive builders for the fixtures.

func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range sortedNames(files) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(files[name]))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range sortedNames(files) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[name]))
	}
	zw.Close()
	return buf.Bytes()
}

func sortedNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func hexSum(h []byte) string { return hex.EncodeToString(h) }

func writeWS(t *testing.T, ws string, files map[string]string) {
	for name, body := range files {
		p := filepath.Join(ws, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func clientCases() []clientCase {
	same := func(base string) string { return base }
	return []clientCase{
		{
			name: "npm", bins: []string{"npm", "node"},
			eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewNPM(c) },
			upstream: same,
			setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
				tgz := tarGz(t, map[string]string{
					"package/package.json": `{"name":"fakepkg","version":"1.0.0","main":"index.js"}`,
					"package/index.js":     `module.exports = "npm via conductor";`,
				})
				s5 := sha512.Sum512(tgz)
				s1 := sha1.Sum(tgz)
				f.set("/fakepkg/-/fakepkg-1.0.0.tgz", tgz)
				f.set("/fakepkg", []byte(`{"name":"fakepkg","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"fakepkg","version":"1.0.0","dist":{"tarball":"`+
					f.URL+`/fakepkg/-/fakepkg-1.0.0.tgz","integrity":"sha512-`+base64.StdEncoding.EncodeToString(s5[:])+`","shasum":"`+hexSum(s1[:])+`"}}}}`))
				writeWS(t, ws, map[string]string{"package.json": `{"name":"app","version":"0.0.0","private":true}`})
				return nil, nil
			},
			script: "npm install --no-audit --no-fund fakepkg@1.0.0\nnode -e 'console.log(require(\"fakepkg\"))'",
			want:   "npm via conductor", public: "https://registry.npmjs.org/",
		},
		pyCase("pip", []string{"python3"}, "python3 -m pip install --no-deps --target ./t fakepkg==1.0"),
		pyCase("uv", []string{"uv", "python3"}, "uv pip install --no-deps --target ./t fakepkg==1.0"),
		{
			name: "cargo", bins: []string{"cargo", "rustc"},
			eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewCargo(c) },
			upstream: same,
			setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
				crate := tarGz(t, map[string]string{
					"fakecrate-1.0.0/Cargo.toml": "[package]\nname = \"fakecrate\"\nversion = \"1.0.0\"\nedition = \"2021\"\n",
					"fakecrate-1.0.0/src/lib.rs": "pub fn hi() -> &'static str { \"cargo via conductor\" }\n",
				})
				sum := sha256.Sum256(crate)
				f.set("/config.json", []byte(`{"dl":"`+f.URL+`/crates/{crate}/{crate}-{version}.crate"}`))
				f.set("/fa/ke/fakecrate", []byte(`{"name":"fakecrate","vers":"1.0.0","deps":[],"cksum":"`+hexSum(sum[:])+`","features":{},"yanked":false}`+"\n"))
				f.set("/crates/fakecrate/fakecrate-1.0.0.crate", crate)
				writeWS(t, ws, map[string]string{
					"Cargo.toml":  "[package]\nname = \"app\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nfakecrate = \"=1.0.0\"\n",
					"src/main.rs": "fn main() { println!(\"{}\", fakecrate::hi()); }\n",
				})
				// A rustup install: its toolchain, read-only, run directly
				// (rustup's proxies look for it under the real HOME).
				var stderr bytes.Buffer
				cmd := exec.Command("rustc", "--print", "sysroot")
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("rustc --print sysroot: %v\n%s", err, stderr.Bytes())
				}
				sys := strings.TrimSpace(string(out))
				if visibleInBase(sys) {
					return nil, nil
				}
				return []string{sys}, []string{"PATH=" + filepath.Join(sys, "bin") + ":/usr/bin:/bin"}
			},
			script: "cargo run -q",
			want:   "cargo via conductor", public: "https://index.crates.io/config.json",
		},
		{
			name: "bundler", bins: []string{"bundle", "ruby", "gem"},
			eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewRubyGems(c) },
			upstream: same,
			setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
				gem := buildGem(t)
				sum := sha256.Sum256(gem)
				info := "---\n1.0.0 |checksum:" + hexSum(sum[:]) + "\n"
				im := md5.Sum([]byte(info))
				f.set("/gems/fakegem-1.0.0.gem", gem)
				f.set("/info/fakegem", []byte(info))
				f.set("/versions", []byte("created_at: 2024-01-01T00:00:00Z\n---\nfakegem 1.0.0 "+hexSum(im[:])+"\n"))
				f.set("/names", []byte("---\nfakegem\n"))
				writeWS(t, ws, map[string]string{"Gemfile": "source \"https://rubygems.org\"\ngem \"fakegem\", \"1.0.0\"\n"})
				env := []string{"BUNDLE_PATH=vendor/bundle", "BUNDLE_APP_CONFIG=.bundle-app"}
				// Ruby 3.4's bundler unescapes a proxy URL's credentials
				// with a CGI it did not load (the sandbox's egress proxy URL
				// carries one); loading cgi first works around it there.
				if out, _ := exec.Command("ruby", "-e", "print RUBY_VERSION").Output(); strings.HasPrefix(string(out), "3.4") {
					env = append(env, "RUBYOPT=-rcgi")
				}
				return nil, env
			},
			script: "bundle install --quiet\nbundle exec ruby -e 'require \"fakegem\"; puts Fakegem::X'",
			want:   "gem via conductor", public: "https://rubygems.org/versions",
		},
		{
			name: "gradle", bins: []string{"gradle", "java"},
			eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewMaven(c) },
			upstream: same,
			setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
				jar := zipOf(t, map[string]string{"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n", "fake.txt": "gradle via conductor"})
				pom := `<?xml version="1.0"?><project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>fake</artifactId><version>1.0</version></project>`
				js, ps := sha256.Sum256(jar), sha256.Sum256([]byte(pom))
				d := "/com/example/fake/1.0/fake-1.0"
				f.set(d+".jar", jar)
				f.set(d+".jar.sha256", []byte(hexSum(js[:])))
				f.set(d+".pom", []byte(pom))
				f.set(d+".pom.sha256", []byte(hexSum(ps[:])))
				writeWS(t, ws, map[string]string{
					"settings.gradle": "rootProject.name = 'app'\n",
					"build.gradle": "configurations { fetch }\nrepositories { mavenCentral() }\ndependencies { fetch 'com.example:fake:1.0' }\n" +
						"tasks.register('fetchIt') { def files = configurations.fetch; doLast { files.each { println new java.util.zip.ZipFile(it).with { z -> z.getInputStream(z.getEntry('fake.txt')).text } } } }\n",
				})
				var ro, env []string
				if jh := os.Getenv("JAVA_HOME"); jh != "" {
					env = append(env, "JAVA_HOME="+jh)
					if !visibleInBase(jh) {
						ro = append(ro, jh)
					}
				}
				return ro, env
			},
			script: "gradle -q --no-daemon fetchIt",
			want:   "gradle via conductor", public: "https://repo.maven.apache.org/maven2/",
		},
		{
			name: "dotnet", bins: []string{"dotnet"},
			eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewNuGet(c) },
			upstream: func(base string) string { return base + "/v3/index.json" },
			setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
				nupkg := zipOf(t, map[string]string{
					"Fake.Pkg.nuspec":  `<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>Fake.Pkg</id><version>1.0.0</version><authors>t</authors><description>t</description></metadata></package>`,
					"content/fake.txt": "dotnet via conductor",
				})
				h := sha512.Sum512(nupkg)
				f.set("/v3/index.json", []byte(`{"version":"3.0.0","resources":[{"@id":"`+f.URL+`/flat/","@type":"PackageBaseAddress/3.0.0"},{"@id":"`+f.URL+`/reg/","@type":"RegistrationsBaseUrl/3.6.0"}]}`))
				f.set("/flat/fake.pkg/index.json", []byte(`{"versions":["1.0.0"]}`))
				f.set("/flat/fake.pkg/1.0.0/fake.pkg.1.0.0.nupkg", nupkg)
				f.set("/reg/fake.pkg/1.0.0.json", []byte(`{"catalogEntry":"`+f.URL+`/catalog/fake.pkg.1.0.0.json"}`))
				f.set("/catalog/fake.pkg.1.0.0.json", []byte(`{"packageHash":"`+base64.StdEncoding.EncodeToString(h[:])+`","packageHashAlgorithm":"SHA512"}`))
				// The SDK's own framework: its reference pack ships with it,
				// so the one package restored is the fixture's.
				v, err := exec.Command("dotnet", "--version").Output()
				if err != nil {
					t.Fatal(err)
				}
				major, _, _ := strings.Cut(strings.TrimSpace(string(v)), ".")
				writeWS(t, ws, map[string]string{"app.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net` + major + `.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup><ItemGroup><PackageReference Include="Fake.Pkg" Version="1.0.0" /></ItemGroup></Project>`})
				var ro, env []string
				if dr := os.Getenv("DOTNET_ROOT"); dr != "" {
					env = append(env, "DOTNET_ROOT="+dr)
					if !visibleInBase(dr) {
						ro = append(ro, dr)
					}
				}
				return ro, env
			},
			script: "dotnet restore --nologo -v q\ncat \"$HOME/.nuget/packages/fake.pkg/1.0.0/content/fake.txt\"; echo",
			want:   "dotnet via conductor", public: "https://api.nuget.org/v3/index.json",
		},
	}
}

func pyCase(name string, bins []string, install string) clientCase {
	return clientCase{
		name: name, bins: bins,
		eco:      func(c pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) { return pkgproxy.NewPyPI(c) },
		upstream: func(base string) string { return base },
		setup: func(t *testing.T, f *fakeRegistry, ws string) ([]string, []string) {
			files := map[string]string{
				"fakepkg/__init__.py":                 "X = \"python via conductor\"\n",
				"fakepkg-1.0.dist-info/METADATA":      "Metadata-Version: 2.1\nName: fakepkg\nVersion: 1.0\n",
				"fakepkg-1.0.dist-info/WHEEL":         "Wheel-Version: 1.0\nGenerator: conductor-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
				"fakepkg-1.0.dist-info/top_level.txt": "fakepkg\n",
			}
			var record strings.Builder
			for _, n := range sortedNames(files) {
				h := sha256.Sum256([]byte(files[n]))
				fmt.Fprintf(&record, "%s,sha256=%s,%d\n", n, base64.RawURLEncoding.EncodeToString(h[:]), len(files[n]))
			}
			record.WriteString("fakepkg-1.0.dist-info/RECORD,,\n")
			files["fakepkg-1.0.dist-info/RECORD"] = record.String()
			whl := zipOf(t, files)
			sum := sha256.Sum256(whl)
			f.set("/packages/fakepkg-1.0-py3-none-any.whl", whl)
			f.set("/simple/fakepkg/", []byte(`{"meta":{"api-version":"1.1"},"name":"fakepkg","files":[{"filename":"fakepkg-1.0-py3-none-any.whl","url":"`+
				f.URL+`/packages/fakepkg-1.0-py3-none-any.whl","hashes":{"sha256":"`+hexSum(sum[:])+`"}}]}`))
			return nil, []string{"PIP_BREAK_SYSTEM_PACKAGES=1", "PIP_NO_INPUT=1"}
		},
		script: install + "\nPYTHONPATH=./t python3 -c 'import fakepkg; print(fakepkg.X)'",
		want:   "python via conductor", public: "https://pypi.org/simple/",
	}
}

// buildGem builds the fixture gem with the host's gem (a test fixture, in a
// temp dir with a temp HOME).
func buildGem(t *testing.T) []byte {
	dir := t.TempDir()
	writeWS(t, dir, map[string]string{
		"fakegem.gemspec": "Gem::Specification.new do |s|\n  s.name = 'fakegem'\n  s.version = '1.0.0'\n  s.summary = 't'\n  s.authors = ['t']\n  s.files = ['lib/fakegem.rb']\n  s.license = 'MIT'\nend\n",
		"lib/fakegem.rb":  "module Fakegem\n  X = 'gem via conductor'\nend\n",
	})
	cmd := exec.Command("gem", "build", "fakegem.gemspec")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gem build: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "fakegem-1.0.0.gem"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
