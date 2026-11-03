//go:build linux || darwin

package agentsandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/pkgproxy"
	"github.com/NodeSpy/conductor/internal/pkgproxy/pkgproxytest"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestSandboxedGoBuildFetchesThroughConductor runs a real `go build` inside
// the real sandbox for a module that needs downloading. The sandbox has no
// network and no Go host on its allowlist: the module arrives through
// conductor's package proxy (relayed in like a loopback model endpoint),
// verified against the checksum database by both the proxy and the agent's
// own go command. The upstream and the checksum database are local fakes.
func TestSandboxedGoBuildFetchesThroughConductor(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	hello := pkgproxytest.Module{Path: "example.com/hello", Version: "v1.0.0", Files: map[string]string{
		"go.mod":   "module example.com/hello\n\ngo 1.21\n",
		"hello.go": "package hello\n\nfunc Hello() string { return \"hello through conductor\" }\n",
	}}
	world := pkgproxytest.New(t, []pkgproxytest.Module{hello})

	root, err := SocketDir("csg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The go module cache is read-only.
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		os.RemoveAll(root)
	})
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws, state, real := filepath.Join(root, "ws"), filepath.Join(root, "st"), filepath.Join(root, "home")
	for _, d := range []string{ws, state, real} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/app\n\ngo 1.21\n\nrequire example.com/hello v1.0.0\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/hello\"\n)\n\nfunc main() { fmt.Println(hello.Hello()) }\n"), 0o600)

	db, err := pkgproxy.ParseSumDB(world.GOSUMDB)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := pkgproxy.OpenCache(filepath.Join(state, ".pkgproxy", "cache"), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	fetch, err := pkgproxy.NewFetcher(world.Upstream.URL, db.URL)
	if err != nil {
		t.Fatal(err)
	}
	goEco, err := pkgproxy.NewGo(pkgproxy.GoConfig{Upstream: world.Upstream.URL, SumDB: db, Cache: cache,
		SumDir: filepath.Join(state, ".pkgproxy", "sumdb"), Fetcher: fetch})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := pkgproxy.New(bytes.Repeat([]byte("e"), 32), goEco)
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

	// sandbox.prefetch: [go] with the proxy on warms the proxy's cache on
	// the host, through the proxy, before the launch.
	warmEnv := srv.AgentEnv(pkgproxy.Requester{Identity: "conductor:prefetch"})
	gw, err := NewGoWarm(filepath.Join(state, ".pkgproxy", "go-prefetched"), os.Environ(), warmEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.Workspace(context.Background(), ws); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if n := world.Hits("/example.com/hello/@v/v1.0.0.zip"); n != 1 {
		t.Fatalf("the warm fetched the zip %d times", n)
	}

	r := &Resolver{StateRoot: filepath.Join(state, "agent-state"), RealHome: real, GOOS: runtime.GOOS, GoPrefetch: gw,
		Packages: func(id, disp string) PackageAccess {
			env, files := srv.AgentConfig(pkgproxy.Requester{Identity: id, Dispatch: disp})
			return PackageAccess{Addr: srv.Addr(), Env: env, HomeFiles: files}
		}}
	pol, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "profile:go-fixer", Dispatch: "d-e2e"}, "claude-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The go command reads its GOROOT (the standard library); a toolchain
	// installed outside the base system (setup-go's on macOS) is shown
	// read-only, like any tool directory an operator adds.
	gr, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	goroot := strings.TrimSpace(string(gr))
	if r, err := filepath.EvalSymlinks(goroot); err == nil {
		goroot = r
	}
	if !visibleInBase(goroot) {
		pol.ReadOnly = append(pol.ReadOnly, goroot)
	}
	for _, h := range pol.Egress {
		if strings.Contains(h, "127.0.0.1") || strings.Contains(h, "golang") {
			t.Fatalf("the module host is on the allowlist: %v", pol.Egress)
		}
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	script := "set -e; go mod tidy; go build -o app .; ./app; " +
		// The upstream itself is unreachable from inside: only conductor is.
		"GOPROXY=" + world.Upstream.URL + " GONOSUMDB='*' go list -m -versions example.com/probedirect >/dev/null 2>&1 || true"
	l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint, Relay: rel.Relay,
		LookPath: exec.LookPath, RealHome: real, Inherited: os.Environ(), Bins: []string{"go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, append(l.Env, "GOTOOLCHAIN=local", "GOMODCACHE="+filepath.Join(pol.Home, "gomod"))
	out, err := cmd.CombinedOutput()
	t.Logf("sandboxed go build:\n%s", out)
	if err != nil {
		t.Fatalf("sandboxed go build failed: %v", err)
	}
	if !strings.Contains(string(out), "hello through conductor") {
		t.Fatal("the built program did not run")
	}
	for _, p := range world.Requests() {
		if strings.Contains(p, "probedirect") {
			t.Fatal("the sandbox reached the upstream directly")
		}
	}
	sum, err := os.ReadFile(filepath.Join(ws, "go.sum"))
	if err != nil || !strings.Contains(string(sum), "example.com/hello v1.0.0 h1:") {
		t.Fatalf("go.sum: %v %q", err, sum)
	}
	mu.Lock()
	defer mu.Unlock()
	var warmed, served bool
	for _, e := range events {
		if e.Artifact != "zip" || e.Module != "example.com/hello" {
			continue
		}
		warmed = warmed || (e.Upstream && e.Requester.Identity == "conductor:prefetch")
		served = served || (!e.Upstream && e.Requester.Dispatch == "d-e2e" && e.Requester.Identity == "profile:go-fixer")
	}
	if !warmed || !served {
		t.Fatalf("want an audited upstream fetch by the prefetch and a cached serve for the dispatch: %+v", events)
	}
	if n := world.Hits("/example.com/hello/@v/v1.0.0.zip"); n != 1 {
		t.Fatalf("the agent's build fetched upstream again (%d zip fetches): one cache when the proxy is on", n)
	}
	for _, kv := range pol.Env {
		if kv == "GOPROXY=off" {
			t.Fatal("a warming prefetch left the agent's Go offline")
		}
	}
}
