//go:build linux

package agentsandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/keycache"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// fakePkgTool is a package manager with no network: its cache is
// ~/.cache/fake; on a miss it "downloads" (writes) a package there, on a
// hit it uses what is there.
const fakePkgTool = `#!/bin/sh
set -e
c="$HOME/.cache/fake"
if [ -f "$c/pkg/blob" ]; then
	echo "fake-tool: hit $(cat "$c/pkg/origin")"
else
	mkdir -p "$c/pkg"
	head -c 262144 /dev/zero >"$c/pkg/blob"
	echo "$1" >"$c/pkg/origin"
	echo "fake-tool: miss, filled by $1"
fi
`

type cacheRig struct {
	t     *testing.T
	root  string
	ws    string
	store *keycache.Store
	m     *keycache.Manager
	r     *Resolver
	pm    *sandbox.ProxyManager
	self  func() (string, error)
}

// newCacheRig is a resolver whose launches restore step caches from a
// real store, by overlay (or by copy when overlay is false).
func newCacheRig(t *testing.T, overlay bool, persist string) *cacheRig {
	root := requireSandbox(t)
	g := &cacheRig{t: t, root: root, ws: filepath.Join(root, "ws"), self: selfConductor(t)}
	if err := os.MkdirAll(g.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.ws, "fake-tool"), []byte(fakePkgTool), 0o755); err != nil {
		t.Fatal(err)
	}
	g.store = &keycache.Store{Dir: filepath.Join(root, "caches")}
	if err := os.MkdirAll(g.store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keycache.RemoveAll(g.store.Dir) })
	ov := func() (bool, string) { return false, "copy restore under test" }
	if overlay {
		ok, why := keycache.OverlayUsable(g.store.Dir, filepath.Join(g.store.Dir, ".layers"))
		if !ok {
			t.Skipf("no overlay here: %s", why)
		}
		ov = nil
	}
	g.m = &keycache.Manager{Store: g.store, MaxSize: 1 << 30, MinSize: 64 << 10, MinRuns: 1,
		Protected: ToolStatePaths(), FinishWait: 5 * time.Second, Overlay: ov, Logf: t.Logf}
	lastSweep.Store(time.Now().Unix())
	g.r = &Resolver{Registry: &Registry{}, StateRoot: filepath.Join(root, "st"), GOOS: "linux", Persist: persist,
		RunLive: func(string) bool { return persist != PersistNone },
		Caches: func(req keycache.Request) keycache.Grant {
			gr, err := g.m.Restore(context.Background(), req)
			if err != nil {
				t.Errorf("restore: %v", err)
			}
			return gr
		}}
	g.pm = sandbox.NewProxyManager(nil)
	t.Cleanup(func() { g.pm.Close() })
	return g
}

func (g *cacheRig) policy(identity, dispatch string, key *keycache.Key, from ...keycache.Key) Policy {
	g.t.Helper()
	e := Entry{Workspace: g.ws, Identity: identity, Dispatch: dispatch, Repo: "o/r"}
	if key != nil {
		e.Cache = &CacheBinding{Key: *key, RestoreFrom: from}
	}
	pol, err := g.r.PolicyForEntry(e, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		g.t.Fatal(err)
	}
	pol.Detect, pol.Limits = DetectSpec{Action: DetectOff}, LimitSpec{Off: true}
	return pol
}

func (g *cacheRig) wrap(pol Policy, script string) (*exec.Cmd, *Launch) {
	g.t.Helper()
	l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
		SelfExe: g.self, EgressUnix: g.pm.UnixEndpoint, EgressAddr: g.pm.Endpoint,
		LookPath: exec.LookPath, RealHome: filepath.Join(g.root, "real"), Inherited: os.Environ(),
	})
	if err != nil {
		g.t.Fatal(err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	return cmd, l
}

// run launches a step's agent in the jail and returns its policy and
// output; the launch has ended (its step not yet).
func (g *cacheRig) run(identity, dispatch, script string, key *keycache.Key, from ...keycache.Key) (Policy, string) {
	g.t.Helper()
	pol := g.policy(identity, dispatch, key, from...)
	cmd, l := g.wrap(pol, script)
	out, err := cmd.CombinedOutput()
	l.Cleanup()
	if err != nil {
		g.t.Fatalf("%s: %v\n%s", dispatch, err, out)
	}
	return pol, string(out)
}

// The end-to-end shape, in a real jail with no network: two runs of a step
// share a key. The first misses, its tool fills ~/.cache/fake, its step
// succeeds and publishes; the second is shown what the first filled — by
// overlay, and by copy — and its tool hits. A run of another key misses.
func TestSandboxStepCacheSecondRunRestores(t *testing.T) {
	for _, overlay := range []bool{true, false} {
		t.Run(fmt.Sprint("overlay=", overlay), func(t *testing.T) {
			g := newCacheRig(t, overlay, PersistNone)
			key := keycache.Key{Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-o/r-1"}
			pol, out := g.run("agent:fixer@o/r", "r1:fix", "./fake-tool run1", &key)
			if len(pol.Caches) != 0 || !strings.Contains(out, "miss, filled by run1") {
				t.Fatalf("the first run: %+v\n%s", pol.Caches, out)
			}
			p, err := g.m.Finish(context.Background(), "r1:fix", true)
			if err != nil || p == nil {
				t.Fatalf("the first run's step published nothing: %v", err)
			}
			if got := strings.Join(p.Entry.Paths, ","); got != ".cache/fake" {
				t.Fatalf("published %s", got)
			}
			pol, out = g.run("agent:fixer@o/r", "r2:fix", "./fake-tool run2", &key)
			if !strings.Contains(out, "fake-tool: hit run1") {
				t.Fatalf("the second run did not restore what the first filled: %+v\n%s", pol.Caches, out)
			}
			if overlay != (len(pol.Caches) == 1) {
				t.Fatalf("restore mounts %+v (overlay %v)", pol.Caches, overlay)
			}
			if _, err := g.m.Finish(context.Background(), "r2:fix", true); err != nil {
				t.Fatal(err)
			}
			if ents, _ := os.ReadDir(filepath.Join(g.store.Dir, ".layers")); len(ents) != 0 {
				t.Fatalf("layers outlived their steps: %v", ents)
			}
			other := keycache.Key{Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-o/r-2"}
			if _, out = g.run("agent:fixer@o/r", "r3:fix", "./fake-tool run3", &other); !strings.Contains(out, "miss") {
				t.Fatalf("another key restored this one's cache:\n%s", out)
			}
		})
	}
}

// A run restored from a shared (restore_from) key changes what it was
// shown; its step publishes to its own key, and the shared copy is as it
// was.
func TestSandboxRestoreFromIsReadOnly(t *testing.T) {
	g := newCacheRig(t, true, PersistNone)
	shared := keycache.Key{Template: "{{.repo}}", Value: "o/r"}
	pr := keycache.Key{Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-o/r-9"}
	g.run("agent:warm@o/r", "w:warm", "./fake-tool default-branch", &shared)
	if p, err := g.m.Finish(context.Background(), "w:warm", true); err != nil || p == nil {
		t.Fatalf("the shared key was not published: %v", err)
	}
	before, _ := g.store.Lookup(shared.ID())
	_, out := g.run("agent:fixer@o/r", "f:fix", `set -e
./fake-tool pr
echo pr >~/.cache/fake/pkg/origin
rm ~/.cache/fake/pkg/blob
echo pr-ok`, &pr, shared)
	if !strings.Contains(out, "hit default-branch") || !strings.Contains(out, "pr-ok") {
		t.Fatalf("the PR's run:\n%s", out)
	}
	p, err := g.m.Finish(context.Background(), "f:fix", true)
	if err != nil || p == nil || p.Key.ID() != pr.ID() {
		t.Fatalf("published %+v %v", p, err)
	}
	after, _ := g.store.Lookup(shared.ID())
	if after.Version != before.Version {
		t.Fatal("the PR's run published to the shared key")
	}
	data := filepath.Join(before.Dir, "data", ".cache", "fake", "pkg")
	if b, err := os.ReadFile(filepath.Join(data, "origin")); err != nil || string(b) != "default-branch\n" {
		t.Fatalf("the shared copy changed: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(data, "blob")); err != nil {
		t.Fatal("the PR's delete reached the shared copy")
	}
	pdata := filepath.Join(p.Entry.Dir, "data", ".cache", "fake", "pkg")
	if b, _ := os.ReadFile(filepath.Join(pdata, "origin")); string(b) != "pr\n" {
		t.Fatalf("the PR's key lacks its write: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(pdata, "blob")); err == nil {
		t.Fatal("the PR's key has the file its run deleted")
	}
}

// A dispatch's home that a launch used a cache in — writing through the
// overlay and leaving read-only directories, as Go does — is removed whole
// when its run ends, by the same remover as any home. The shared cache is
// untouched; the launch's layers (conductor's, beside the store) wait for
// its step, then go.
func TestSandboxCachedHomeIsReclaimed(t *testing.T) {
	g := newCacheRig(t, true, PersistNone)
	key := keycache.Key{Template: "k", Value: "k1"}
	e, err := g.store.Publish(key, []string{".cache/fake"}, func(data string) error {
		d := filepath.Join(data, ".cache", "fake", "dep")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte("cached"), 0o444); err != nil {
			return err
		}
		return os.Chmod(d, 0o555)
	})
	if err != nil {
		t.Fatal(err)
	}
	// No grace: a launch is live only while it holds its lock.
	g.store.LaunchGrace = time.Nanosecond
	pol, out := g.run("agent:fixer@o/r", "rC:fix", `set -e
test "$(cat ~/.cache/fake/dep/f)" = cached
chmod -R u+w ~/.cache/fake
echo run >~/.cache/fake/dep/new
rm ~/.cache/fake/dep/f
mkdir -p ~/.cache/fake/mine/d ~/ro/d
echo x >~/ro/d/f
chmod -R a-w ~/.cache/fake/mine ~/ro
echo cached-run-ok`, &key)
	if len(pol.Caches) != 1 || !strings.Contains(out, "cached-run-ok") {
		t.Fatalf("the cached launch: %+v\n%s", pol.Caches, out)
	}
	launched(pol.Home)
	if g.m.InUse(e.Dir) {
		t.Fatal("the cache is still in use after its launch ended")
	}
	if !exists(pol.Home) {
		t.Fatal("the home went with the launch")
	}
	// The step failed: nothing published, its layers go.
	if p, err := g.m.Finish(context.Background(), "rC:fix", false); err != nil || p != nil {
		t.Fatalf("a failed step published %+v %v", p, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(g.store.Dir, ".layers")); len(ents) != 0 {
		t.Fatalf("the launch's layers outlived its step: %v", ents)
	}
	if n := g.r.ReclaimRun("rC"); n < 1 {
		t.Fatalf("reclaimed %d home(s)", n)
	}
	if exists(pol.Home) {
		ents, _ := os.ReadDir(pol.Home)
		t.Fatalf("the home that used a cache is still there: %v", ents)
	}
	if b, err := os.ReadFile(filepath.Join(e.Dir, "data", ".cache", "fake", "dep", "f")); err != nil || string(b) != "cached" {
		t.Fatalf("the shared cache changed: %q %v", b, err)
	}
	g.m.Evict()
	if len(g.store.List()) != 1 {
		t.Fatal("the cache was evicted under its cap")
	}
}

// Launches sharing a home (a kept identity home) each keep their own layers:
// one that starts and ends while another runs never touches the other's, and
// the cache stays in use while either runs.
func TestSandboxLaunchesSharingAHomeKeepTheirCaches(t *testing.T) {
	g := newCacheRig(t, true, PersistHome)
	key := keycache.Key{Template: "k", Value: "k1"}
	e, err := g.store.Publish(key, []string{".cache/fake"}, func(data string) error {
		d := filepath.Join(data, ".cache", "fake", "dep")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "f"), []byte("cached"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	// No grace: a launch is live only while it holds its lock.
	g.store.LaunchGrace = time.Nanosecond
	polA := g.policy("agent:fixer@o/r", "rA:fix", &key)
	a, la := g.wrap(polA, `set -e
echo a >~/.cache/fake/dep/a
touch ~/a-started
until [ -e ~/a-go ]; do sleep 0.05; done
echo a2 >~/.cache/fake/dep/a2
test "$(cat ~/.cache/fake/dep/a)" = a
test "$(cat ~/.cache/fake/dep/f)" = cached
echo A-ok`)
	outA := &strings.Builder{}
	a.Stdout, a.Stderr = outA, outA
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Process.Kill() }()
	home := polA.Home
	started := false
	for i := 0; i < 600 && !started; i++ {
		started = exists(filepath.Join(home, "a-started"))
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		_ = a.Process.Kill()
		_ = a.Wait()
		t.Fatalf("launch A never started:\n%s", outA)
	}
	polB, outB := g.run("agent:fixer@o/r", "rB:fix", `set -e
echo b >~/.cache/fake/dep/b
test ! -e ~/.cache/fake/dep/a
echo B-ok`, &key)
	if polB.Home != home {
		t.Fatalf("the launches do not share a home: %s, %s", home, polB.Home)
	}
	if !strings.Contains(outB, "B-ok") {
		t.Fatalf("launch B:\n%s", outB)
	}
	if _, err := g.m.Finish(context.Background(), "rB:fix", false); err != nil {
		t.Fatal(err)
	}
	if !g.m.InUse(e.Dir) {
		t.Fatal("the cache is not in use while launch A runs")
	}
	g.m.Evict()
	if err := os.WriteFile(filepath.Join(home, "a-go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err = a.Wait()
	la.Cleanup()
	if err != nil || !strings.Contains(outA.String(), "A-ok") {
		t.Fatalf("launch A after B ended: %v\n%s", err, outA)
	}
	if _, err := g.m.Finish(context.Background(), "rA:fix", false); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(g.store.Dir, ".layers")); len(ents) != 0 {
		t.Fatalf("layers outlived their launches: %v", ents)
	}
}

// A kept home (persist: home) whose first launch missed and filled the
// cache itself gets it on its next launch once its step published — the
// home's own files under the target stay its own — and an agent planting a
// file in place of the target does not turn it off.
func TestSandboxKeptHomeGetsTheCacheAfterAMiss(t *testing.T) {
	g := newCacheRig(t, true, PersistHome)
	key := keycache.Key{Template: "k", Value: "k1"}
	// 1. A miss: the launch fills the cache itself; its step publishes.
	pol, _ := g.run("bot:o/r", "r1:fix", "./fake-tool r1", &key)
	if len(pol.Caches) != 0 {
		t.Fatal("restored from an empty store")
	}
	if p, err := g.m.Finish(context.Background(), "r1:fix", true); err != nil || p == nil {
		t.Fatalf("not published: %v", err)
	}
	// 2. The next launch in the kept home is shown the published copy.
	pol, out := g.run("bot:o/r", "r2:fix", `./fake-tool r2; touch ~/.cache/fake/x`, &key)
	if len(pol.Caches) != 1 || !strings.Contains(out, "hit r1") {
		t.Fatalf("the kept home's next launch has no cache: %+v\n%s", pol.Caches, out)
	}
	if _, err := g.m.Finish(context.Background(), "r2:fix", false); err != nil {
		t.Fatal(err)
	}
	// 3. A file planted in place of the target does not turn it off.
	g.run("bot:o/r", "r3:fix", `rm -rf ~/.cache.old; mv ~/.cache ~/.cache.old; mkdir -p ~/.cache; echo x >~/.cache/fake`, nil)
	pol, out = g.run("bot:o/r", "r4:fix", `./fake-tool r4`, &key)
	if len(pol.Caches) != 1 || !strings.Contains(out, "hit r1") {
		t.Fatalf("a planted file turned the cache off: %+v\n%s", pol.Caches, out)
	}
}
