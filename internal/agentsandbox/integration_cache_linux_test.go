//go:build linux

package agentsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/repocache"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// A dispatch's home that a launch used a repository cache in — writing
// through the overlay and leaving read-only directories, as Go does — is
// removed whole when its run ends, by the same remover as any home. The
// shared cache is untouched, and the launch's overlay layers (conductor's,
// beside the store) and its pin end with the launch, not the home.
func TestSandboxCachedHomeIsReclaimed(t *testing.T) {
	root := requireSandbox(t)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &repocache.Store{Dir: filepath.Join(root, "caches")}
	e, err := store.Publish(repocache.Meta{Repo: "o/r", Name: "go", Key: "k1", Paths: []string{"~/go/pkg/mod"}}, func(data string) error {
		d := filepath.Join(data, "home", "go", "pkg", "mod", "dep")
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
	t.Cleanup(func() { _ = repocache.RemoveAll(store.Dir) })
	if ok, why := repocache.OverlayUsable(store.Dir, filepath.Join(store.Dir, ".layers")); !ok {
		t.Skipf("no overlay here: %s", why)
	}
	m := &repocache.Manager{Store: store, MaxSize: 1 << 30,
		Repos: map[string][]repocache.Cache{"o/r": {{Name: "go", Key: "k1", Paths: []string{"~/go/pkg/mod"}, Env: map[string]string{"GOFLAGS": "-modcacherw"}}}}}
	lastSweep.Store(time.Now().Unix())
	r := &Resolver{Registry: &Registry{}, StateRoot: filepath.Join(root, "st"), GOOS: "linux",
		RunLive: func(string) bool { return false },
		Caches: func(req repocache.Request) repocache.Grant {
			g, err := m.Restore(context.Background(), req)
			if err != nil {
				t.Errorf("restore: %v", err)
			}
			return g
		}}
	pol, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@o/r", Dispatch: "rC:fix", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.Caches) != 1 {
		t.Fatalf("caches %+v", pol.Caches)
	}
	pol.Detect, pol.Limits = DetectSpec{Action: DetectOff}, LimitSpec{Off: true}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	script := `set -e
test "$(cat ~/go/pkg/mod/dep/f)" = cached
chmod -R u+w ~/go/pkg/mod
echo run >~/go/pkg/mod/dep/new
rm ~/go/pkg/mod/dep/f
mkdir -p ~/go/pkg/mod/mine/d ~/ro/d
echo x >~/ro/d/f
chmod -R a-w ~/go/pkg/mod/mine ~/ro
echo cached-run-ok`
	l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	out, err := cmd.CombinedOutput()
	l.Cleanup()
	if err != nil || !strings.Contains(string(out), "cached-run-ok") {
		t.Fatalf("the cached launch: %v\n%s", err, out)
	}
	launched(pol.Home)
	// The pin is the launch's, not the home's: once it ended, its layers are
	// gone and the cache is free, though the home is still there.
	if ents, _ := os.ReadDir(filepath.Join(store.Dir, ".layers")); len(ents) != 0 {
		t.Fatalf("the launch's layers outlived it: %v", ents)
	}
	if m.InUse(e.Dir) {
		t.Fatal("the cache is still pinned after its launch ended")
	}
	if !exists(pol.Home) {
		t.Fatal("the home went with the launch")
	}

	if n := r.ReclaimRun("rC"); n < 1 {
		t.Fatalf("reclaimed %d home(s)", n)
	}
	if exists(pol.Home) {
		ents, _ := os.ReadDir(pol.Home)
		t.Fatalf("the home that used a cache is still there: %v", ents)
	}
	if b, err := os.ReadFile(filepath.Join(e.Dir, "data", "home", "go", "pkg", "mod", "dep", "f")); err != nil || string(b) != "cached" {
		t.Fatalf("the shared cache changed: %q %v", b, err)
	}
	m.Evict()
	if len(store.List()) != 1 {
		t.Fatal("the cache was evicted under its cap")
	}
}

// Launches sharing a home (a kept identity home) each keep their own layers:
// one that starts and ends while another runs never touches the other's, and
// the cache stays pinned while either runs.
func TestSandboxLaunchesSharingAHomeKeepTheirCaches(t *testing.T) {
	root := requireSandbox(t)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &repocache.Store{Dir: filepath.Join(root, "caches")}
	e, err := store.Publish(repocache.Meta{Repo: "o/r", Name: "go", Key: "k1", Paths: []string{"~/go/pkg/mod"}}, func(data string) error {
		d := filepath.Join(data, "home", "go", "pkg", "mod", "dep")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "f"), []byte("cached"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repocache.RemoveAll(store.Dir) })
	if ok, why := repocache.OverlayUsable(store.Dir, filepath.Join(store.Dir, ".layers")); !ok {
		t.Skipf("no overlay here: %s", why)
	}
	// No grace: a launch is live only while it holds its lock.
	store.LaunchGrace = time.Nanosecond
	m := &repocache.Manager{Store: store, MaxSize: 1 << 30,
		Repos: map[string][]repocache.Cache{"o/r": {{Name: "go", Key: "k1", Paths: []string{"~/go/pkg/mod"}}}}}
	lastSweep.Store(time.Now().Unix())
	r := &Resolver{Registry: &Registry{}, StateRoot: filepath.Join(root, "st"), GOOS: "linux", Persist: PersistHome,
		RunLive: func(string) bool { return true },
		Caches: func(req repocache.Request) repocache.Grant {
			g, err := m.Restore(context.Background(), req)
			if err != nil {
				t.Errorf("restore: %v", err)
			}
			return g
		}}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	launch := func(dispatch, script string) (*exec.Cmd, *Launch, *strings.Builder, string) {
		pol, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@o/r", Dispatch: dispatch, Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		if len(pol.Caches) != 1 {
			t.Fatalf("caches %+v", pol.Caches)
		}
		pol.Detect, pol.Limits = DetectSpec{Action: DetectOff}, LimitSpec{Off: true}
		l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
			SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
			LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
		})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
		out := &strings.Builder{}
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = l.Dir, l.Env, out, out
		return cmd, l, out, pol.Home
	}
	a, la, outA, home := launch("rA:fix", `set -e
echo a >~/go/pkg/mod/dep/a
touch ~/a-started
until [ -e ~/a-go ]; do sleep 0.05; done
echo a2 >~/go/pkg/mod/dep/a2
test "$(cat ~/go/pkg/mod/dep/a)" = a
test "$(cat ~/go/pkg/mod/dep/f)" = cached
echo A-ok`)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Process.Kill() }()
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
	b, lb, outB, homeB := launch("rB:fix", `set -e
echo b >~/go/pkg/mod/dep/b
test ! -e ~/go/pkg/mod/dep/a
echo B-ok`)
	if homeB != home {
		t.Fatalf("the launches do not share a home: %s, %s", home, homeB)
	}
	if err := b.Run(); err != nil || !strings.Contains(outB.String(), "B-ok") {
		t.Fatalf("launch B: %v\n%s", err, outB)
	}
	lb.Cleanup()
	if !m.InUse(e.Dir) {
		t.Fatal("the cache is not in use while launch A runs")
	}
	m.Evict()
	if err := os.WriteFile(filepath.Join(home, "a-go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err = a.Wait()
	la.Cleanup()
	if err != nil || !strings.Contains(outA.String(), "A-ok") {
		t.Fatalf("launch A after B ended: %v\n%s", err, outA)
	}
	if ents, _ := os.ReadDir(filepath.Join(store.Dir, ".layers")); len(ents) != 0 {
		t.Fatalf("layers outlived their launches: %v", ents)
	}
}
