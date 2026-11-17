//go:build linux || darwin

package controller

import (
	"context"
	"fmt"
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
	"github.com/NodeSpy/conductor/internal/repocache"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestRepoCacheEndToEnd proves the whole loop on the real sandbox with the
// real Go toolchain, a real git origin and the module proxy reached through
// conductor's egress proxy: a Go repository's first agent run misses its
// cache and downloads its modules into its own home; conductor warms the
// default branch and publishes; a second run on a PR branch hits and builds
// with GOPROXY=off — every module from the shared cache — with almost
// nothing of its own in its home, and the shared copy unchanged.
//
// It needs the network (the module proxy): opt in with
// CONDUCTOR_E2E_CACHES=1.
func TestRepoCacheEndToEnd(t *testing.T) {
	if os.Getenv("CONDUCTOR_E2E_CACHES") != "1" {
		t.Skip("set CONDUCTOR_E2E_CACHES=1 (needs the network and the Go toolchain)")
	}
	if why := agentsandbox.Probe(); why != "" {
		t.Skipf("no sandbox here: %s", why)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	t.Cleanup(func() { _ = repocache.RemoveAll(root) })
	origin := goOrigin(t, root)

	// The daemon's pieces, as cmd/conductor wires them.
	self := buildConductor(t)
	oldSelf := launchSelfExe
	launchSelfExe = func() (string, error) { return self, nil }
	pm := sandbox.NewProxyManager(nil)
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
	var logs []string
	var mu sync.Mutex
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		l := fmt.Sprintf(f, a...)
		logs = append(logs, l)
		t.Log(l)
	}
	agentState := filepath.Join(state, "agent-state")
	m := &repocache.Manager{
		Store: &repocache.Store{Dir: filepath.Join(state, "repo-caches")}, Recipes: repocache.Recipes(nil),
		Autodetect: true, RepoFileOn: true, Protected: agentsandbox.ToolStatePaths(), MaxSize: 10 << 30,
		DefaultFile: gp.DefaultBranchFile, Checkout: gp.DefaultBranchCheckout, Warm: RunCacheWarm,
		WarmTimeout: 10 * time.Minute, Logf: logf,
	}
	r := &agentsandbox.Resolver{
		Registry: &agentsandbox.Registry{}, StateRoot: agentState, Egress: []string{"*"}, GOOS: runtime.GOOS,
		Env: os.Environ(),
		Caches: func(req repocache.Request) repocache.Grant {
			g, err := m.Restore(context.Background(), req)
			if err != nil {
				t.Errorf("restore: %v", err)
			}
			return g
		},
	}
	AgentSandbox = &SandboxWiring{Resolver: r, Relay: rel.Relay}

	// An agent run in a PR checkout: build, then report where the modules
	// came from.
	agentRun := func(id string, offline bool) (home, out string, layers []string) {
		t.Helper()
		req := dispatch.Request{DispatchID: id, Trigger: core.Trigger{TargetTrusted: true, Kind: "pr",
			Target: core.Target{Repo: "acme/app", PR: 7, Number: 7, BaseRef: "main"}, Context: map[string]any{"head_ref": "feature"}}}
		_, wt, err := gp.ProvisionWorktree(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		e, err := dispatch.RegisterSandboxWorkspace(wt, "agent:e2e@"+id, "", &req)
		if err != nil {
			t.Fatal(err)
		}
		if e.Repo != "acme/app" {
			t.Fatalf("entry repo %q", e.Repo)
		}
		e.Skill = nil
		pol, err := r.PolicyForEntry(e, "", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		script := `set -e; go build -o /tmp/app . && /tmp/app && go env GOMODCACHE GOCACHE GOFLAGS`
		if offline {
			pol.Env = append(pol.Env, "GOPROXY=off")
		}
		pol.Env = append(pol.Env, "GOTOOLCHAIN=local")
		l, err := agentsandbox.Wrap(pol, []string{"sh", "-c", script}, agentsandbox.Deps{
			SelfExe: launchSelfExe, EgressUnix: EgressProxyUnix, EgressAddr: EgressProxyFor,
			EgressUnixSession: EgressProxyUnixSession, EgressAddrSession: EgressProxyForSession,
			Relay: rel.Relay, LookPath: exec.LookPath, Bins: []string{goBin}, Inherited: os.Environ(),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer l.Cleanup()
		cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
		cmd.Env, cmd.Dir = l.Env, l.Dir
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agent run %s: %v\n%s", id, err, b)
		}
		for _, c := range pol.Caches {
			if strings.HasPrefix(c.Upper, pol.Home+"/") || strings.HasPrefix(c.Work, pol.Home+"/") {
				t.Fatalf("overlay layers in the agent's home: %+v", c)
			}
			layers = append(layers, filepath.Dir(c.Upper))
		}
		return pol.Home, string(b), layers
	}

	// 1. The first run: nothing published, a miss; it fetches its own.
	home1, out1, _ := agentRun("d1", false)
	if !hasLine(logs, "go (autodetect): miss") {
		t.Fatalf("no miss logged: %v", logs)
	}
	used1 := repocache.DiskUsage(home1)
	t.Logf("run 1 (miss): home %s\n%s", repocache.HumanSize(used1), out1)

	// 2. Conductor warms the default branch and publishes.
	if err := m.WarmRepo(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	list := m.Store.List()
	if len(list) != 1 || list[0].Name != "go" {
		t.Fatalf("published %+v", list)
	}
	pub := list[0]
	if _, err := os.Stat(filepath.Join(pub.Dir, "data", "home", "go", "pkg", "mod", "github.com", "google")); err != nil {
		t.Fatalf("the module is not in the published cache: %v", err)
	}
	before := treeDigest(t, pub.Dir)

	// 3. A second run, offline for modules: everything from the cache.
	home2, out2, layers2 := agentRun("d2", true)
	if !hasLine(logs, "go (autodetect): hit") {
		t.Fatalf("no hit logged: %v", logs)
	}
	// The run's disk: its home and its overlay layers (conductor's, beside
	// the store).
	used2 := repocache.DiskUsage(home2)
	for _, l := range layers2 {
		used2 += repocache.DiskUsage(l)
	}
	if len(layers2) == 0 {
		t.Fatal("run 2 has no overlay layers")
	}
	t.Logf("run 2 (hit, GOPROXY=off): home + layers %s (published cache %s)\n%s", repocache.HumanSize(used2), repocache.HumanSize(pub.Size), out2)
	if !strings.Contains(out2, "hello from a cached module") {
		t.Fatalf("run 2 output:\n%s", out2)
	}
	if used2*4 > used1 || used2 > 4<<20 {
		t.Errorf("run 2's home is %s against run 1's %s: the cache was copied, not overlaid", repocache.HumanSize(used2), repocache.HumanSize(used1))
	}
	if after := treeDigest(t, pub.Dir); after != before {
		t.Fatal("the run changed the shared cache")
	}
	if !m.InUse(pub.Dir) {
		t.Fatal("the overlaid cache is not in use while its home exists")
	}
	// The home gone, the cache is free and the run's layers go.
	if err := repocache.RemoveAll(home2); err != nil {
		t.Fatal(err)
	}
	m.Evict()
	if m.InUse(pub.Dir) {
		t.Fatal("still in use after its home went")
	}
	for _, l := range layers2 {
		if _, err := os.Lstat(l); err == nil {
			t.Fatalf("run 2's layers outlived its home: %s", l)
		}
	}
}

func hasLine(logs []string, s string) bool {
	for _, l := range logs {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// treeDigest lists every file under root with its size and mode (not
// .used, which a restore touches).
func treeDigest(t *testing.T, root string) string {
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || filepath.Base(p) == ".used" {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s %d %s %d\n", rel, fi.Size(), fi.Mode(), fi.ModTime().UnixNano())
		return nil
	})
	return b.String()
}

// goOrigin is a bare repository holding a Go module with one dependency on
// main, and a PR branch (refs/pull/7/head) that changes code, not deps.
func goOrigin(t *testing.T, root string) string {
	t.Helper()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare, work := filepath.Join(root, "origin.git"), filepath.Join(root, "src")
	git(root, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(root, "init", "-q", "--initial-branch=main", work)
	files := map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n\nrequire github.com/google/uuid v1.6.0\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/google/uuid\"\n)\n\nfunc main() { _ = uuid.New(); fmt.Println(\"hello from a cached module\") }\n",
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(work, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// go.sum from the module proxy, into a module cache of the test's own.
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = work
	tidy.Env = append(os.Environ(), "GOMODCACHE="+filepath.Join(root, "hostmod"), "GOFLAGS=-modcacherw", "GOTOOLCHAIN=local")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "app")
	git(work, "remote", "add", "origin", bare)
	git(work, "push", "-q", "origin", "main")
	git(work, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("pr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "pr")
	git(work, "push", "-q", "origin", "feature", "feature:refs/pull/7/head")
	return bare
}
