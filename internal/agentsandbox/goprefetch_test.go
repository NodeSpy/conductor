package agentsandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotKeys(m *GoModules) []string {
	var k []string
	for f := range m.Files {
		k = append(k, f)
	}
	sort.Strings(k)
	return k
}

// A go.work workspace: the modules it uses (block and single-line), the
// modules their local replaces point at, never a path out of the workspace
// nor a symlink.
func TestSnapshotGoModulesWorkspace(t *testing.T) {
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{"go.mod": "module secret\n", "id_rsa": "KEY"})
	ws := t.TempDir()
	writeFiles(t, ws, map[string]string{
		"go.work":         "go 1.27.3\n\nuse (\n\t./api // the API\n\t\"./core\"\n\t../" + filepath.Base(outside) + "\n)\n\nuse ./util\n",
		"go.work.sum":     "sums\n",
		"api/go.mod":      "module x/api\n\nreplace x/shared => ../shared\nreplace golang.org/x/oauth2 => github.com/acme/oauth2 v0.0.1\n",
		"api/go.sum":      "api sums\n",
		"core/go.mod":     "module x/core\n\nreplace (\n\tx/gen => ./gen\n\tx/abs => /etc\n)\n",
		"core/gen/go.mod": "module x/gen\n",
		"shared/go.mod":   "module x/shared\n",
		"util/go.mod":     "module x/util\n",
		"unused/go.mod":   "module x/unused\n",
	})
	if err := os.Symlink(filepath.Join(outside, "id_rsa"), filepath.Join(ws, "util", "go.sum")); err != nil {
		t.Fatal(err)
	}
	m, err := SnapshotGoModules(ws)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api/go.mod", "api/go.sum", "core/gen/go.mod", "core/go.mod", "go.work", "go.work.sum", "shared/go.mod", "util/go.mod"}
	if got := snapshotKeys(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot %v, want %v", got, want)
	}
	for _, b := range m.Files {
		if strings.Contains(string(b), "KEY") || strings.Contains(string(b), "module secret") {
			t.Fatal("a file outside the workspace was read")
		}
	}
}

// No go.work: the root module alone (and its local replaces); no go.mod at
// all is no snapshot.
func TestSnapshotGoModulesSingleModule(t *testing.T) {
	ws := t.TempDir()
	writeFiles(t, ws, map[string]string{"go.mod": "module x\n", "go.sum": "s\n", "sub/go.mod": "module x/sub\n"})
	m, err := SnapshotGoModules(ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotKeys(m); !reflect.DeepEqual(got, []string{"go.mod", "go.sum"}) {
		t.Fatalf("snapshot %v", got)
	}
	if m, err := SnapshotGoModules(t.TempDir()); err != nil || m != nil {
		t.Fatalf("no go.mod: %+v %v", m, err)
	}
	link := t.TempDir()
	os.Symlink(filepath.Join(ws, "go.mod"), filepath.Join(link, "go.mod"))
	if m, err := SnapshotGoModules(link); err != nil || m != nil {
		t.Fatalf("a symlinked go.mod is no module: %+v %v", m, err)
	}
}

// fakeGo is a go binary that records its working directory, arguments and
// environment, and fails when FAKE_GO_FAIL names a file that exists.
func fakeGo(t *testing.T) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	bin = filepath.Join(dir, "go")
	script := "#!/bin/sh\n" +
		"{ echo \"cwd=$(pwd)\"; echo \"args=$*\"; env; echo '--files'; find . -type f | sort; } >> " + record + "\n" +
		"[ -e " + filepath.Join(dir, "fail") + " ] && { echo 'boom: no such module' >&2; exit 1; }\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

// TestGoPrefetchPlan covers the PURE logic Fill wraps in confinement: the
// snapshot staged on disk, the argv/env `go mod download` runs with, and the
// workspace-vs-single-module GOWORK switch — none of it needs a real
// namespace/privilege, so this stays a fast unit test. Confinement itself
// (that the download really CANNOT reach outside its allow-list) is proven
// by the Linux integration test in goprefetch_jail_linux_test.go.
func TestGoPrefetchPlan(t *testing.T) {
	cache := t.TempDir()
	g := &GoPrefetch{Cache: cache, Go: "/usr/bin/go", Proxy: "https://proxy.golang.org", Env: []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=leak-me-not"}}
	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n"), "sub/go.sum": []byte("s\n")}}

	p, err := g.plan(m)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(p.tmp)

	src := filepath.Join(p.tmp, "src")
	if got := strings.Join(p.argv, " "); got != g.Go+" -C "+src+" mod download" {
		t.Fatalf("argv = %q", got)
	}
	if got := strings.Join(p.goroot, " "); got != g.Go+" -C "+src+" env GOROOT" {
		t.Fatalf("goroot argv = %q", got)
	}
	if p.dir != p.tmp {
		t.Fatalf("dir = %q, want the scratch root %q (the jail binds/chdirs into it; -C points at src inside it)", p.dir, p.tmp)
	}
	for _, want := range []string{
		"GOMODCACHE=" + cache, "GOPROXY=https://proxy.golang.org",
		"GOTOOLCHAIN=auto", "GOVCS=*:off", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPRIVATE=",
		"HOME=" + p.tmp,
	} {
		if !contains(p.env, want) {
			t.Errorf("env lacks %q: %v", want, p.env)
		}
	}
	for _, kv := range p.env {
		// A toolchain is only fetched verified: the checksum database stays
		// at its default.
		if strings.HasPrefix(kv, "GOSUMDB=") || kv == "GOTOOLCHAIN=local" {
			t.Errorf("the download must not set %s", kv)
		}
	}
	if contains(p.env, "GITHUB_TOKEN=leak-me-not") == false {
		t.Error("g.Env must still reach the plan (an operator variable the download explicitly keeps)")
	}
	for _, rel := range []string{"go.mod", "sub/go.sum"} {
		if _, err := os.Stat(filepath.Join(src, filepath.FromSlash(rel))); err != nil {
			t.Errorf("staged file missing: %s: %v", rel, err)
		}
	}

	// A go.work workspace plans in workspace mode (no GOWORK=off).
	w := &GoModules{Files: map[string][]byte{"go.work": []byte("use ./a\n"), "a/go.mod": []byte("module a\n")}}
	pw, err := g.plan(w)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(pw.tmp)
	if contains(pw.env, "GOWORK=off") {
		t.Error("a go.work workspace must not plan with GOWORK=off")
	}

	if _, err := g.plan(&GoModules{Files: map[string][]byte{"../x/go.mod": nil}}); err == nil {
		t.Fatal("a module file out of the temp dir is refused")
	}
}

// Fill never falls back to an unconfined download: with no Sandbox deps
// wired (the zero value — this box's goprefetch has no self-exe/egress
// wiring), it logs and returns nil (the agent runs Go offline without these
// modules) instead of ever running `go mod download` bare. fakeGo would
// record a run if Fill executed it at all.
func TestGoPrefetchFillSkipsWhenUnconfinable(t *testing.T) {
	bin, record := fakeGo(t)
	cache := t.TempDir()
	var warned string
	g := &GoPrefetch{Cache: cache, Go: bin, Proxy: "https://proxy.golang.org",
		Env:  []string{"PATH=" + os.Getenv("PATH")},
		Warn: func(format string, args ...any) { warned = fmt.Sprintf(format, args...) }}
	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n")}}

	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatalf("an unconfinable download must be skipped, not errored: %v", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatal("the download ran UNCONFINED — it must never fall back to a bare host download")
	}
	if warned == "" || !strings.Contains(warned, "cannot confine") {
		t.Fatalf("expected a log line naming the skip, got %q", warned)
	}
	if _, err := os.Stat(filepath.Join(cache, ".conductor-prefetched-v2", m.Digest())); err == nil {
		t.Fatal("a skipped download must not be marked done — a later box that CAN confine must still try")
	}
}

// Fill routes through sandbox.WrapLocalCommand (Confine, namespace mode,
// enforced egress to the GOPROXY hosts) rather than reading g.Sandbox and
// silently doing nothing: with a self-exe wired but no egress-proxy minter
// (the other half WrapLocalCommand needs for the enforced allowlist),
// WrapLocalCommand's own fail-closed check names exactly that missing wiring
// — proof the call was actually reached, without needing a real privileged
// jail to exercise it.
func TestGoPrefetchFillRoutesThroughConfinement(t *testing.T) {
	cache := t.TempDir()
	var warned string
	g := &GoPrefetch{Cache: cache, Go: "/usr/bin/go", Proxy: "https://proxy.golang.org",
		Sandbox: sandbox.LocalWrapDeps{SelfExe: func() (string, error) { return "/conductor", nil }},
		Warn:    func(format string, args ...any) { warned = fmt.Sprintf(format, args...) }}
	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n")}}

	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatalf("an unconfinable download must be skipped, not errored: %v", err)
	}
	if !strings.Contains(warned, "cannot confine") || !strings.Contains(warned, "proxy's unix endpoint") {
		t.Fatalf("expected WrapLocalCommand's own enforced-egress refusal to surface through the skip warning: %q", warned)
	}
}

// NewGoPrefetch never layers the operator's own $GOMODCACHE onto GOPROXY as
// a file:// source (rt2 #11): this ONE cache is shared by every identity, so
// pulling whatever the operator's own module cache holds into it would leak
// private modules across identities. Regression for the removed lines.
func TestNewGoPrefetchNoFileProxyLayer(t *testing.T) {
	opHome := t.TempDir()
	download := filepath.Join(opHome, "pkg", "mod", "cache", "download")
	if err := os.MkdirAll(download, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "shared-cache")
	g, err := NewGoPrefetch(cache, []string{"PATH=" + os.Getenv("PATH"), "GOPATH=" + filepath.Join(opHome, "pkg")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g.Proxy, "file://") {
		t.Fatalf("GOPROXY must never carry a file:// layer onto the operator's own module cache, got %q", g.Proxy)
	}
	if g.Proxy != "https://proxy.golang.org" {
		t.Fatalf("GOPROXY = %q, want the plain public default", g.Proxy)
	}
}

// goProxyHosts names only the reachable proxy hosts — never "direct"/"off",
// and comma/pipe separators are read the same way (we only need which hosts
// might be dialed, not the fallback semantics between them).
func TestGoProxyHosts(t *testing.T) {
	cases := []struct {
		proxy string
		want  []string
	}{
		{"https://proxy.golang.org", []string{"proxy.golang.org:443"}},
		{"off", nil},
		{"direct", nil},
		{"https://a.example.com|https://b.example.com,direct", []string{"a.example.com:443", "b.example.com:443"}},
		{"http://mirror.internal:8080", []string{"mirror.internal:8080"}},
	}
	for _, c := range cases {
		got := goProxyHosts(c.proxy)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("goProxyHosts(%q) = %v, want %v", c.proxy, got, c.want)
		}
	}
}

// Every launch sees the cache read-only and runs Go offline.
func TestPolicyGoPrefetch(t *testing.T) {
	cache := t.TempDir()
	r := &Resolver{StateRoot: t.TempDir(), RealHome: t.TempDir(), GOOS: "linux",
		GoPrefetch: &GoPrefetch{Cache: cache}}
	ws := t.TempDir()
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app", ReadOnly: []string{"/base/objects"}, Env: []string{"GIT_AUTHOR_NAME=x"}}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(p.ReadOnly, cache) || !contains(p.ReadOnly, "/base/objects") {
		t.Fatalf("read-only: %v", p.ReadOnly)
	}
	for _, kv := range []string{"GOMODCACHE=" + cache, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GIT_AUTHOR_NAME=x"} {
		if !contains(p.Env, kv) {
			t.Errorf("env lacks %s: %v", kv, p.Env)
		}
	}
	for _, h := range p.Egress {
		if strings.Contains(h, "golang.org") || strings.Contains(h, "googleapis") {
			t.Errorf("no Go host joins the allowlist: %v", p.Egress)
		}
	}
	r.GoPrefetch = nil
	if p, _ := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app"}, "claude", nil, skill.ProcessAnchor{}); contains(p.ReadOnly, cache) || contains(p.Env, "GOPROXY=off") {
		t.Fatalf("prefetch off: %+v", p)
	}
}

// toolchainCache lays out a toolchain the way Go unpacks one into a module
// cache, and returns its GOROOT.
func toolchainCache(t *testing.T, cache, version string) string {
	t.Helper()
	goroot := filepath.Join(cache, "golang.org", "toolchain@v0.0.1-"+version+".linux-amd64")
	if err := os.MkdirAll(filepath.Join(goroot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return goroot
}

// A launch's PATH starts with the toolchain its workspace's module files
// selected — only one the prefetch put in the cache — and the agent's Go
// otherwise stays as it was: offline, GOSUMDB off, GOTOOLCHAIN=local.
func TestPolicyGoPrefetchToolchainFirstOnPath(t *testing.T) {
	cache := t.TempDir()
	g := &GoPrefetch{Cache: cache}
	r := &Resolver{StateRoot: t.TempDir(), RealHome: t.TempDir(), GOOS: "linux", GoPrefetch: g}
	ws, other := t.TempDir(), t.TempDir()
	goroot := toolchainCache(t, cache, "go1.99.0")
	if err := g.recordToolchain(ws, goroot); err != nil {
		t.Fatal(err)
	}
	policy := func(ws string) Policy {
		t.Helper()
		p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app"}, "claude", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := policy(ws)
	bin := filepath.Join(goroot, "bin")
	if !reflect.DeepEqual(p.PathFirst, []string{bin}) {
		t.Fatalf("path first: %v", p.PathFirst)
	}
	env := AgentEnv([]string{"PATH=/usr/bin:/bin"}, p)
	if !contains(env, "PATH="+bin+string(os.PathListSeparator)+"/usr/bin:/bin") {
		t.Fatalf("the toolchain is not first on PATH: %v", env)
	}
	for _, kv := range []string{"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"} {
		if !contains(env, kv) {
			t.Errorf("env lacks %s: %v", kv, env)
		}
	}
	// Per workspace: another one gets the host's go.
	if p := policy(other); len(p.PathFirst) != 0 {
		t.Fatalf("another workspace got a toolchain: %v", p.PathFirst)
	}

	// Only a toolchain in the cache: the host's own GOROOT (the host's go is
	// new enough) or anything else records nothing.
	for _, gr := range []string{"/usr/lib/go", filepath.Join(cache, "example.com", "m@v1.0.0"), filepath.Join(cache, "..", "golang.org", "toolchain@v0.0.1-go1.99.0.linux-amd64"), ""} {
		if err := g.recordToolchain(ws, gr); err != nil {
			t.Fatal(err)
		}
		if p := policy(ws); len(p.PathFirst) != 0 {
			t.Errorf("GOROOT %q put %v on PATH", gr, p.PathFirst)
		}
	}
	// A record naming a path out of the cache is ignored.
	g.recordToolchain(ws, goroot)
	os.WriteFile(g.toolchainRecord(ws), []byte("../golang.org/toolchain@v0.0.1-go1.99.0.linux-amd64"), 0o644)
	if bin := g.ToolchainBin(ws); bin != "" {
		t.Errorf("a record out of the cache gave %q", bin)
	}
	// A toolchain whose go is not an executable file is not used.
	g.recordToolchain(ws, goroot)
	os.Remove(filepath.Join(bin, "go"))
	os.Symlink("/bin/sh", filepath.Join(bin, "go"))
	if got := g.ToolchainBin(ws); got != "" {
		t.Errorf("a symlinked go gave %q", got)
	}
}

// No PathFirst, no change to PATH; no inherited PATH, PathFirst alone.
func TestAgentEnvPathFirst(t *testing.T) {
	p := Policy{Home: "/h"}
	if env := AgentEnv([]string{"PATH=/usr/bin"}, p); !contains(env, "PATH=/usr/bin") {
		t.Fatalf("PATH changed: %v", env)
	}
	p.PathFirst = []string{"/tc/bin"}
	env := AgentEnv(nil, p)
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			n++
		}
	}
	if !contains(env, "PATH=/tc/bin") || n != 1 {
		t.Fatalf("PATH: %v", env)
	}
}
