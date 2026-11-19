package agentsandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/keycache"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
	"golang.org/x/sys/unix"
)

// A launch of a step with a cache key gets its cache: the hook is asked
// with the launch's own home and workspace and the step's keys, and its
// overlays and ledger lock are the policy's.
func TestPolicyRestoresStepCaches(t *testing.T) {
	ws := t.TempDir()
	var got []keycache.Request
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Caches: func(req keycache.Request) keycache.Grant {
		got = append(got, req)
		return keycache.Grant{
			Mounts: []keycache.Mount{{Target: filepath.Join(req.Home, "go", "pkg", "mod"), Lower: "/store/o~r/go/k/data/home/go/pkg/mod",
				Upper: "/store/.layers/r/s/upper", Work: "/store/.layers/r/s/work"}},
			Lock: "/store/.layers/r/lock",
		}
	}}
	key := keycache.Key{Template: "pr-{{.repo}}", Value: "pr-o/r"}
	from := []keycache.Key{{Template: "{{.repo}}", Value: "o/r"}}
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "d-1", Repo: "o/r",
		Cache: &CacheBinding{Key: key, RestoreFrom: from}}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != key || !slices.Equal(got[0].RestoreFrom, from) || got[0].Home != p.Home ||
		got[0].Workspace != canon(ws) || got[0].Dispatch != "d-1" {
		t.Fatalf("hook asked %+v", got)
	}
	if len(p.Caches) != 1 || p.CacheLock != "/store/.layers/r/lock" {
		t.Fatalf("policy %+v", p)
	}
	// No cache key (a step without cache:, a member, a hand-off): no caches,
	// whatever the repository.
	got = nil
	if _, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:y", Dispatch: "d-2", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{}); err != nil {
		t.Fatal(err)
	}
	// A key but no dispatch to finish it: none either.
	if _, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:z", Cache: &CacheBinding{Key: key}}, "claude", nil, skill.ProcessAnchor{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("asked for caches without a key or dispatch: %+v", got)
	}
}

// An overlay's upper and work directories are paths mount(2) follows a
// symlink in: a policy never takes them from anywhere the agent can write —
// its home, its workspace, or any directory it is granted read-write.
func TestValidateRefusesStrayCacheMounts(t *testing.T) {
	f := newPolicyFixture(t)
	ok := keycache.Mount{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/s/u", Work: "/store/.layers/r/s/w"}
	rw, ro := f.pol.ReadWrite[0], f.pol.ReadOnly[0]
	f.pol.CacheLock = "/store/.layers/r/lock"
	f.pol.Caches = []keycache.Mount{ok}
	if err := f.pol.Validate(); err != nil {
		t.Fatal(err)
	}
	// A grant reached through a symlink is the grant.
	if err := os.MkdirAll(filepath.Join(rw, "l"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(rw, link); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]keycache.Mount{
		"target outside":    {Target: "/etc", Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"target is home":    {Target: f.pol.Home, Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"target is ws":      {Target: f.pol.Workspace, Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"upper in home":     {Target: ok.Target, Lower: ok.Lower, Upper: filepath.Join(f.pol.Home, ".conductor-cache-layers", "s", "u"), Work: ok.Work},
		"work in home":      {Target: ok.Target, Lower: ok.Lower, Upper: ok.Upper, Work: filepath.Join(f.pol.Home, "w")},
		"upper in ws":       {Target: ok.Target, Lower: ok.Lower, Upper: filepath.Join(f.pol.Workspace, "u"), Work: ok.Work},
		"upper is home":     {Target: ok.Target, Lower: ok.Lower, Upper: f.pol.Home, Work: ok.Work},
		"upper in a rw dir": {Target: ok.Target, Lower: ok.Lower, Upper: filepath.Join(rw, "u"), Work: ok.Work},
		"work above a rw":   {Target: ok.Target, Lower: ok.Lower, Upper: ok.Upper, Work: filepath.Dir(rw)},
		"upper relative":    {Target: ok.Target, Lower: ok.Lower, Upper: "store/u", Work: ok.Work},
		"upper unclean":     {Target: ok.Target, Lower: ok.Lower, Upper: "/store/.layers/../x", Work: ok.Work},
		"upper is work":     {Target: ok.Target, Lower: ok.Lower, Upper: ok.Upper, Work: ok.Upper},
		"lower in home":     {Target: ok.Target, Lower: filepath.Join(f.pol.Home, "l"), Upper: ok.Upper, Work: ok.Work},
		"lower in ws":       {Target: ok.Target, Lower: filepath.Join(f.pol.Workspace, "l"), Upper: ok.Upper, Work: ok.Work},
		"lower relative":    {Target: ok.Target, Lower: "store/x", Upper: ok.Upper, Work: ok.Work},
		"upper not its own": {Target: ok.Target, Lower: ok.Lower, Upper: "/store/.layers/other/u", Work: ok.Work},
		"upper in a ro dir": {Target: ok.Target, Lower: ok.Lower, Upper: filepath.Join(ro, "u"), Work: ok.Work},
		"lower in a rw dir": {Target: ok.Target, Lower: filepath.Join(rw, "l"), Upper: ok.Upper, Work: ok.Work},
		"lower via a link":  {Target: ok.Target, Lower: filepath.Join(link, "l"), Upper: ok.Upper, Work: ok.Work},
	} {
		f.pol.Caches = []keycache.Mount{m}
		if err := f.pol.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	// A read-only grant that is a link to the store shows the layers.
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, ".layers", "r", "s"), 0o700); err != nil {
		t.Fatal(err)
	}
	roLink := filepath.Join(t.TempDir(), "ro-link")
	if err := os.Symlink(store, roLink); err != nil {
		t.Fatal(err)
	}
	f.pol.CacheLock = filepath.Join(store, ".layers", "r", "lock")
	f.pol.Caches = []keycache.Mount{{Target: ok.Target, Lower: ok.Lower,
		Upper: filepath.Join(store, ".layers", "r", "s"), Work: filepath.Join(store, ".layers", "r", "w")}}
	if err := f.pol.Validate(); err != nil {
		t.Fatal(err)
	}
	f.pol.ReadOnly = append(f.pol.ReadOnly, roLink)
	if err := f.pol.Validate(); err == nil {
		t.Error("layers under a symlinked read-only grant validated")
	}
	f.pol.ReadOnly = f.pol.ReadOnly[:len(f.pol.ReadOnly)-1]

	f.pol.Caches, f.pol.CacheLock = []keycache.Mount{ok}, ""
	if err := f.pol.Validate(); err == nil || !strings.Contains(err.Error(), "cache lock") {
		t.Errorf("caches without a launch lock: %v", err)
	}
}

// The store's filesystem is watched by the low-disk guard: a launch's
// writes over a cache land there.
func TestDiskGuardWatchesTheStore(t *testing.T) {
	f := newPolicyFixture(t)
	if slices.Contains(f.pol.diskPaths(), "/store/.layers/r") {
		t.Fatal("a launch without caches watches a store")
	}
	f.pol.Caches = []keycache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/u", Work: "/store/.layers/r/w"}}
	f.pol.CacheLock = "/store/.layers/r/lock"
	if got := f.pol.diskPaths(); !slices.Contains(got, "/store/.layers/r") || !slices.Contains(got, f.pol.Home) || !slices.Contains(got, f.pol.Workspace) {
		t.Fatalf("disk paths %v", got)
	}
}

// A cache mount the policy would refuse (here a lower in the workspace) is
// dropped, logged, at resolve: the launch runs without it.
func TestResolverDropsACacheItWouldRefuse(t *testing.T) {
	ws := t.TempDir()
	var logs []string
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(),
		Log: func(f string, a ...any) { logs = append(logs, f) },
		Caches: func(req keycache.Request) keycache.Grant {
			return keycache.Grant{Lock: "/store/.layers/r/lock", Mounts: []keycache.Mount{
				{Target: filepath.Join(req.Home, "go"), Lower: filepath.Join(ws, "x"), Upper: "/store/.layers/r/a/u", Work: "/store/.layers/r/a/w"},
				{Target: filepath.Join(req.Home, "npm"), Lower: "/store/n", Upper: "/store/.layers/r/b/u", Work: "/store/.layers/r/b/w"},
			}}
		}}
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "d-1",
		Cache: &CacheBinding{Key: keycache.Key{Template: "k", Value: "k"}}}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatalf("a launch failed for a cache: %v", err)
	}
	if len(p.Caches) != 1 || p.Caches[0].Lower != "/store/n" {
		t.Fatalf("caches %+v", p.Caches)
	}
	if len(logs) == 0 {
		t.Error("the dropped cache was not logged")
	}
}

func TestWrapLinuxMountsCacheOverlays(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	launch := filepath.Join(t.TempDir(), ".layers", "r")
	if err := os.MkdirAll(filepath.Join(launch, "s"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launch, "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := keycache.Mount{Target: filepath.Join(f.pol.Home, ".cache", "fake"), Lower: "/store/keys/k/v1/data/.cache/fake",
		Upper: filepath.Join(launch, "s", "upper"), Work: filepath.Join(launch, "s", "work")}
	f.pol.Caches, f.pol.CacheLock = []keycache.Mount{m}, filepath.Join(launch, "lock")
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	// The launch holds its ledger lock while it runs; when it ends it marks
	// itself ended and lets go, its layers left for its step's end to
	// publish from.
	if _, ok := tryFlock(filepath.Join(launch, "lock")); ok {
		t.Fatal("the launch does not hold its ledger lock")
	}
	defer func() {
		l.Cleanup()
		if _, err := os.Lstat(filepath.Join(launch, "ended")); err != nil {
			t.Error("the launch did not mark itself ended")
		}
		if release, ok := tryFlock(filepath.Join(launch, "lock")); !ok {
			t.Error("the launch kept its ledger lock")
		} else {
			release()
		}
	}()
	bs := jailOf(t, l.Argv)
	var ov *sandbox.BindMount
	for i := range bs {
		if bs[i].Overlay != nil {
			ov = &bs[i]
		}
		if bs[i].Path == m.Lower || strings.HasPrefix(bs[i].Path, "/store") {
			t.Errorf("the store is bound into the jail: %+v", bs[i])
		}
	}
	if ov == nil || ov.Path != m.Target || *ov.Overlay != (sandbox.Overlay{Lower: m.Lower, Upper: m.Upper, Work: m.Work}) {
		t.Fatalf("overlay bind %+v in %+v", ov, bs)
	}
}

// A launch that comes after its layers were swept (its lock is gone) runs
// without its caches rather than failing.
func TestWrapWithoutItsLayersRunsUncached(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	gone := filepath.Join(t.TempDir(), ".layers", "r")
	f.pol.Caches = []keycache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x",
		Upper: filepath.Join(gone, "s", "upper"), Work: filepath.Join(gone, "s", "work")}}
	f.pol.CacheLock = filepath.Join(gone, "lock")
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatalf("a launch failed for want of its caches: %v", err)
	}
	defer l.Cleanup()
	for _, b := range jailOf(t, l.Argv) {
		if b.Overlay != nil {
			t.Fatalf("mounted a swept cache: %+v", b)
		}
	}
}

// With a kept identity cache dir (persist: cache, ~/.cache a link to it), a
// cache path through ~/.cache is not restored — the policy never names a
// target behind a link, which the jail would refuse — and the rest are.
func TestCachesWithAPersistedCacheDir(t *testing.T) {
	store := &keycache.Store{Dir: t.TempDir()}
	key := keycache.Key{Template: "k", Value: "k"}
	if _, err := store.Publish(key, []string{"go", ".cache/go-build"}, func(data string) error {
		for _, d := range []string{"go/pkg/mod/dep", ".cache/go-build/ab"} {
			if err := os.MkdirAll(filepath.Join(data, d), 0o755); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &keycache.Manager{Store: store, Overlay: func() (bool, string) { return true, "" }}
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Persist: PersistCache,
		Caches: func(req keycache.Request) keycache.Grant { g, _ := m.Restore(context.Background(), req); return g }}
	p, err := r.PolicyForEntry(Entry{Workspace: t.TempDir(), Identity: "agent:x", Dispatch: "d-1", Cache: &CacheBinding{Key: key}}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(p.Home, ".cache")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skipf("persist: cache did not link ~/.cache here (%v)", err)
	}
	if len(p.Caches) != 1 || !strings.HasSuffix(p.Caches[0].Target, "/go") {
		t.Fatalf("caches %+v", p.Caches)
	}
	for _, c := range p.Caches {
		rel, _ := filepath.Rel(p.Home, c.Target)
		cur := p.Home
		for _, part := range strings.Split(rel, "/") {
			cur = filepath.Join(cur, part)
			if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("a cache target is behind a link: %s", cur)
			}
		}
	}
}

func TestWrapDarwinIgnoresCacheOverlays(t *testing.T) {
	withPlatform(t, "darwin")
	f := newPolicyFixture(t)
	f.pol.Caches = []keycache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/u", Work: "/store/.layers/r/w"}}
	f.pol.CacheLock = "/store/.layers/r/lock"
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "/store/x") {
		t.Fatal("the store was allowed in the profile")
	}
}

func TestToolStatePathsAreProtected(t *testing.T) {
	ps := ToolStatePaths()
	// The agent's own install and logs too: an auto-update across two runs
	// must never get the agent's binary learned and overlaid.
	for _, want := range []string{".claude", ".codex", ".local/share/claude", ".cache/claude-cli-nodejs"} {
		if !slices.Contains(ps, want) {
			t.Errorf("%s missing from %v", want, ps)
		}
	}
	for _, p := range ps {
		if keycache.CheckHomePath(p, ps) {
			t.Errorf("~/%s may be cached", p)
		}
	}
}

// tryFlock takes p's flock exclusively without waiting, as another process
// would (the ledger's locks are flocks on their files).
func tryFlock(p string) (func(), bool) {
	f, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}
