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
		Packages: func(id, disp string) (string, []string) {
			gotID, gotDisp = id, disp
			return "127.0.0.1:41234", []string{"GOPROXY=http://127.0.0.1:41234/tok/go", "GOSUMDB=sum.golang.org"}
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
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, h := range p.Egress {
		if strings.Contains(h, "golang.org") || strings.Contains(h, "googleapis") {
			t.Errorf("no Go host joins the allowlist: %v", p.Egress)
		}
	}
}
