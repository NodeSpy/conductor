package agentsandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
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

func TestGoPrefetchFill(t *testing.T) {
	bin, record := fakeGo(t)
	cache := t.TempDir()
	g := &GoPrefetch{Cache: cache, Go: bin, Proxy: "file:///op/cache/download,https://proxy.golang.org", Env: []string{"PATH=" + os.Getenv("PATH")}}
	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n"), "sub/go.sum": []byte("s\n")}}
	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(record)
	rec := string(b)
	for _, want := range []string{
		"args=mod download\n", "GOMODCACHE=" + cache + "\n", "GOPROXY=file:///op/cache/download,https://proxy.golang.org\n",
		"GOTOOLCHAIN=local\n", "GOVCS=*:off\n", "GOENV=off\n", "GOWORK=off\n", "GOFLAGS=\n", "GOPRIVATE=\n",
		"./go.mod\n", "./sub/go.sum\n",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("the download lacks %q:\n%s", want, rec)
		}
	}
	if strings.Contains(rec, "HOME="+cache) || strings.Contains(rec, "GITHUB_TOKEN") {
		t.Errorf("the download runs in its own temp HOME with none of the daemon's environment:\n%s", rec)
	}
	cwd := strings.TrimPrefix(strings.SplitN(rec, "\n", 2)[0], "cwd=")
	if within(cwd, cache) {
		t.Errorf("the download runs in its own temp dir, not the cache: %s", cwd)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Errorf("the temp dir is removed: %s", cwd)
	}

	// The same module files again: nothing to do.
	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(record); len(b2) != len(b) {
		t.Fatal("an already-prefetched set of module files is downloaded again")
	}

	// A go.work workspace runs in workspace mode; a failure is reported and
	// not recorded as done.
	os.WriteFile(filepath.Join(filepath.Dir(bin), "fail"), nil, 0o644)
	w := &GoModules{Files: map[string][]byte{"go.work": []byte("use ./a\n"), "a/go.mod": []byte("module a\n")}}
	os.Remove(record)
	if err := g.Fill(context.Background(), w); err == nil || !strings.Contains(err.Error(), "boom: no such module") {
		t.Fatalf("a failed download: %v", err)
	}
	if b, _ := os.ReadFile(record); strings.Contains(string(b), "GOWORK=off") {
		t.Fatal("a go.work workspace is downloaded in workspace mode")
	}
	if _, err := os.Stat(filepath.Join(cache, ".conductor-prefetched", w.Digest())); err == nil {
		t.Fatal("a failed download is marked done")
	}

	if err := g.Fill(context.Background(), &GoModules{Files: map[string][]byte{"../x/go.mod": nil}}); err == nil {
		t.Fatal("a module file out of the temp dir is refused")
	}
}

// Every launch sees the cache read-only and runs Go offline.
func TestPolicyGoPrefetch(t *testing.T) {
	cache := t.TempDir()
	r := &Resolver{StateRoot: t.TempDir(), RealHome: t.TempDir(), GOOS: "linux",
		GoPrefetch: &GoPrefetch{Cache: cache}}
	ws := t.TempDir()
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app", ReadOnly: []string{"/base/objects"}, Env: []string{"GIT_AUTHOR_NAME=x"}}, "claude", nil)
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
	if p, _ := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app"}, "claude", nil); contains(p.ReadOnly, cache) || contains(p.Env, "GOPROXY=off") {
		t.Fatalf("prefetch off: %+v", p)
	}
}

// The package proxy reaches every policy: its address relayed into the
// sandbox, the agent's env pointing at it with the identity and dispatch.
func TestPolicyPackageProxy(t *testing.T) {
	var gotID, gotDisp string
	r := &Resolver{StateRoot: t.TempDir(), RealHome: t.TempDir(), GOOS: "linux",
		Packages: func(id, disp string) PackageAccess {
			gotID, gotDisp = id, disp
			return PackageAccess{Addr: "127.0.0.1:41234", Env: []string{"GOPROXY=http://127.0.0.1:41234/tok/go", "GOSUMDB=sum.golang.org"},
				HomeFiles: map[string]string{".cargo/config.toml": "[source.crates-io]\n", ".npmrc": "registry=x\n"}}
		}}
	p, err := r.PolicyForEntry(Entry{Workspace: t.TempDir(), Identity: "profile:fixer", Dispatch: "d-1"}, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != "profile:fixer" || gotDisp != "d-1" {
		t.Fatalf("caller %q %q", gotID, gotDisp)
	}
	if !contains(p.Loopback, "127.0.0.1:41234") || !contains(p.Env, "GOPROXY=http://127.0.0.1:41234/tok/go") {
		t.Fatalf("policy: loopback %v env %v", p.Loopback, p.Env)
	}
	if b, err := os.ReadFile(filepath.Join(p.Home, ".cargo", "config.toml")); err != nil || string(b) != "[source.crates-io]\n" {
		t.Fatalf("home file: %q %v", b, err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, h := range p.Egress {
		if strings.Contains(h, "golang.org") || strings.Contains(h, "googleapis") {
			t.Errorf("no Go host joins the allowlist: %v", p.Egress)
		}
	}
}

// Home files land inside the identity home only: a symlink the agent
// planted there cannot redirect a write out of it, an agent's edit is
// overwritten, and a path out of the home is refused.
func TestWriteHomeFiles(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".cargo")); err != nil {
		t.Fatal(err)
	}
	if err := WriteHomeFiles(home, map[string]string{".cargo/config.toml": "x"}); err == nil {
		t.Fatal("wrote through a symlink out of the home")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("wrote outside the home: %v", ents)
	}
	os.Remove(filepath.Join(home, ".cargo"))
	if err := os.WriteFile(filepath.Join(outside, "target"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(home, ".npmrc")); err != nil {
		t.Fatal(err)
	}
	if err := WriteHomeFiles(home, map[string]string{".npmrc": "registry=proxy\n", ".m2/settings.xml": "<s/>"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "target")); string(b) != "keep" {
		t.Fatalf("a symlink's target was written: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(home, ".npmrc")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf(".npmrc is not a regular file now: %v %v", fi, err)
	}
	os.WriteFile(filepath.Join(home, ".m2", "settings.xml"), []byte("agent edit"), 0o600)
	if err := WriteHomeFiles(home, map[string]string{".m2/settings.xml": "<s/>"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".m2", "settings.xml")); string(b) != "<s/>" {
		t.Fatalf("agent edit kept: %q", b)
	}
	for _, bad := range []string{"../x", "/etc/x", "a/../../x", "./a"} {
		if err := WriteHomeFiles(home, map[string]string{bad: "x"}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if ents, _ := os.ReadDir(home); len(ents) != 2 {
		t.Fatalf("stray files in the home: %v", ents)
	}
}

// With the package proxy on, prefetch warms the proxy: the download runs
// with the proxy's GOPROXY/GOSUMDB (never a default upstream) into a
// throwaway module cache, records its marker apart from any module cache,
// and leaves the agent's policy to the proxy.
func TestGoPrefetchWarm(t *testing.T) {
	bin, record := fakeGo(t)
	markers := filepath.Join(t.TempDir(), "markers")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	g, err := NewGoWarm(markers, []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=secret", "GOPROXY=https://evil.test"},
		[]string{"GOPROXY=http://127.0.0.1:9/tok/go", "GOSUMDB=sum.golang.org", "GONOSUMDB="})
	if err != nil {
		t.Fatal(err)
	}
	m := &GoModules{Files: map[string][]byte{"go.mod": []byte("module x\n")}}
	if err := g.Fill(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(record)
	rec := string(b)
	for _, want := range []string{"GOPROXY=http://127.0.0.1:9/tok/go\n", "GOSUMDB=sum.golang.org\n", "GOFLAGS=-modcacherw\n", "GOVCS=*:off\n"} {
		if !strings.Contains(rec, want) {
			t.Errorf("the warm lacks %q:\n%s", want, rec)
		}
	}
	if strings.Contains(rec, "GOPROXY=off") || strings.Contains(rec, "evil.test") || strings.Contains(rec, "GITHUB_TOKEN") {
		t.Errorf("the warm's GOPROXY is the proxy's alone:\n%s", rec)
	}
	mc := ""
	for _, l := range strings.Split(rec, "\n") {
		if v, ok := strings.CutPrefix(l, "GOMODCACHE="); ok {
			mc = v
		}
	}
	if mc == "" || within(mc, markers) {
		t.Fatalf("the warm's module cache is its own: %q", mc)
	}
	if _, err := os.Stat(mc); err == nil {
		t.Fatalf("the throwaway module cache is removed: %s", mc)
	}
	if _, err := os.Stat(filepath.Join(markers, ".conductor-prefetched", m.Digest())); err != nil {
		t.Fatal("the warm is not recorded")
	}

	// No proxy env at all: GOPROXY stays off, never a default upstream.
	g2, err := NewGoWarm(filepath.Join(t.TempDir(), "m"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(record)
	g2.Go = bin
	_ = g2.Fill(context.Background(), m)
	if b, _ := os.ReadFile(record); !strings.Contains(string(b), "GOPROXY=off\n") {
		t.Fatalf("a warm without the proxy's env must not fetch:\n%s", b)
	}

	// The agent's policy: no read-only cache, Go not offline.
	r := &Resolver{StateRoot: t.TempDir(), RealHome: t.TempDir(), GOOS: "linux", GoPrefetch: g}
	p, err := r.PolicyForEntry(Entry{Workspace: t.TempDir(), Identity: "agent:x"}, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if contains(p.ReadOnly, markers) || contains(p.Env, "GOPROXY=off") || contains(p.Env, "GOMODCACHE="+markers) {
		t.Fatalf("a warming prefetch shaped the policy: %+v", p)
	}
}

// Lockfiles are read from the checkout itself (bounded depth), never
// through a symlink, never from dependency trees or dot directories.
func TestSnapshotLockfiles(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("package-lock.json", "root")
	write("web/app/yarn.lock", "deep")
	write("a/b/c/d/Cargo.lock", "too deep")
	write("node_modules/x/package-lock.json", "installed")
	write(".git/Cargo.lock", "dot")
	write("requirements-dev.txt", "reqs")
	write("README.md", "no")
	os.WriteFile(filepath.Join(outside, "Cargo.lock"), []byte("secret"), 0o600)
	os.Symlink(filepath.Join(outside, "Cargo.lock"), filepath.Join(ws, "Cargo.lock"))
	os.Symlink(outside, filepath.Join(ws, "linked"))
	got, err := SnapshotLockfiles(ws, []string{"package-lock.json", "yarn.lock", "Cargo.lock", "requirements*.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "package-lock.json,requirements-dev.txt,web/app/yarn.lock" || string(got["web/app/yarn.lock"]) != "deep" {
		t.Fatalf("snapshot: %v", keys)
	}
}
