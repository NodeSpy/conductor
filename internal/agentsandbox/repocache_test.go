package agentsandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/repocache"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// A workspace conductor checked out of a repository gets its caches: the
// hook is asked with the launch's own home and workspace, and its overlays
// and environment are the policy's.
func TestPolicyRestoresRepoCaches(t *testing.T) {
	ws := t.TempDir()
	var got []repocache.Request
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Caches: func(req repocache.Request) repocache.Grant {
		got = append(got, req)
		return repocache.Grant{
			Mounts: []repocache.Mount{{Target: filepath.Join(req.Home, "go", "pkg", "mod"), Lower: "/store/o~r/go/k/data/home/go/pkg/mod",
				Upper: "/store/.layers/r/s/upper", Work: "/store/.layers/r/s/work"}},
			Env:  []string{"GOFLAGS=-modcacherw"},
			Lock: "/store/.layers/r/lock",
		}
	}}
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "d-1", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != "o/r" || got[0].Home != p.Home || got[0].Workspace != canon(ws) || got[0].Run != "d-1" {
		t.Fatalf("hook asked %+v", got)
	}
	if len(p.Caches) != 1 || !slices.Contains(p.Env, "GOFLAGS=-modcacherw") || p.CacheLock != "/store/.layers/r/lock" {
		t.Fatalf("policy %+v", p)
	}
	// No repository (a member, a dir: workspace, an artifact): no caches.
	got = nil
	if _, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:y", Dispatch: "d-2"}, "claude", nil, skill.ProcessAnchor{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("asked for caches without a repository")
	}
}

// An overlay's upper and work directories are paths mount(2) follows a
// symlink in: a policy never takes them from anywhere the agent can write —
// its home, its workspace, or any directory it is granted read-write.
func TestValidateRefusesStrayCacheMounts(t *testing.T) {
	f := newPolicyFixture(t)
	ok := repocache.Mount{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/s/u", Work: "/store/.layers/r/s/w"}
	rw, ro := f.pol.ReadWrite[0], f.pol.ReadOnly[0]
	f.pol.CacheLock = "/store/.layers/r/lock"
	f.pol.Caches = []repocache.Mount{ok}
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
	for name, m := range map[string]repocache.Mount{
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
		f.pol.Caches = []repocache.Mount{m}
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
	f.pol.Caches = []repocache.Mount{{Target: ok.Target, Lower: ok.Lower,
		Upper: filepath.Join(store, ".layers", "r", "s"), Work: filepath.Join(store, ".layers", "r", "w")}}
	if err := f.pol.Validate(); err != nil {
		t.Fatal(err)
	}
	f.pol.ReadOnly = append(f.pol.ReadOnly, roLink)
	if err := f.pol.Validate(); err == nil {
		t.Error("layers under a symlinked read-only grant validated")
	}
	f.pol.ReadOnly = f.pol.ReadOnly[:len(f.pol.ReadOnly)-1]

	f.pol.Caches, f.pol.CacheLock = []repocache.Mount{ok}, ""
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
	f.pol.Caches = []repocache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/u", Work: "/store/.layers/r/w"}}
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
		Caches: func(req repocache.Request) repocache.Grant {
			return repocache.Grant{Lock: "/store/.layers/r/lock", Mounts: []repocache.Mount{
				{Target: filepath.Join(req.Home, "go"), Lower: filepath.Join(ws, "x"), Upper: "/store/.layers/r/a/u", Work: "/store/.layers/r/a/w"},
				{Target: filepath.Join(req.Home, "npm"), Lower: "/store/n", Upper: "/store/.layers/r/b/u", Work: "/store/.layers/r/b/w"},
			}}
		}}
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "d-1", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
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
	m := repocache.Mount{Target: filepath.Join(f.pol.Home, "go", "pkg", "mod"), Lower: "/store/o~r/go/k/data/home/go/pkg/mod",
		Upper: filepath.Join(launch, "s", "upper"), Work: filepath.Join(launch, "s", "work")}
	f.pol.Caches, f.pol.CacheLock = []repocache.Mount{m}, filepath.Join(launch, "lock")
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	// The launch's layers are its own: gone when it ends.
	defer func() {
		l.Cleanup()
		if _, err := os.Lstat(launch); err == nil {
			t.Error("the launch's layers outlived it")
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
	f.pol.Caches = []repocache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x",
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
	store := &repocache.Store{Dir: t.TempDir()}
	if _, err := store.Publish(repocache.Meta{Repo: "o/r", Name: "go", Key: "k"}, func(data string) error {
		for _, d := range []string{"go/pkg/mod/dep", ".cache/go-build/ab"} {
			if err := os.MkdirAll(filepath.Join(data, "home", d), 0o755); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &repocache.Manager{Store: store, Overlay: func() (bool, string) { return true, "" },
		Repos: map[string][]repocache.Cache{"o/r": {{Name: "go", Key: "k", Paths: []string{"~/go/pkg/mod", "~/.cache/go-build"}}}}}
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Persist: PersistCache,
		Caches: func(req repocache.Request) repocache.Grant { g, _ := m.Restore(context.Background(), req); return g }}
	p, err := r.PolicyForEntry(Entry{Workspace: t.TempDir(), Identity: "agent:x", Dispatch: "d-1", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(p.Home, ".cache")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skipf("persist: cache did not link ~/.cache here (%v)", err)
	}
	if len(p.Caches) != 1 || !strings.HasSuffix(p.Caches[0].Target, "/go/pkg/mod") {
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
	f.pol.Caches = []repocache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: "/store/.layers/r/u", Work: "/store/.layers/r/w"}}
	f.pol.CacheLock = "/store/.layers/r/lock"
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "/store/x") {
		t.Fatal("the store was allowed in the profile")
	}
}

// The warm step is an agent with less: no conductor socket or token, no
// model endpoint, no login — the operator's egress only.
func TestWarmPolicy(t *testing.T) {
	r := &Resolver{StateRoot: t.TempDir(), Socket: "/s", Egress: []string{"*"}, ModelCredential: CredGateway,
		Mint: func(skill.Identity, skill.ProcessAnchor) (string, error) { t.Fatal("minted"); return "", nil }}
	ws, home := t.TempDir(), t.TempDir()
	p := r.WarmPolicy(ws, home, "cache-warm:o/r")
	if p.Socket != "" || p.Token != "" || len(p.Logins) != 0 || len(p.ModelHosts) != 0 || len(p.Loopback) != 0 {
		t.Fatalf("warm policy carries more than an agent's sandbox: %+v", p)
	}
	if !slices.Equal(p.Egress, []string{"*"}) || p.Workspace != ws || p.Home != home || p.Tool != "" {
		t.Fatalf("%+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	env := envMap(AgentEnv([]string{"PATH=/usr/bin", "GITHUB_TOKEN=x", "ANTHROPIC_API_KEY=k", "OPENAI_API_KEY=o"}, p))
	for _, k := range []string{"GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s reaches the warm step", k)
		}
	}
}

func TestToolStatePathsAreProtected(t *testing.T) {
	ps := ToolStatePaths()
	for _, want := range []string{".claude", ".codex"} {
		if !slices.Contains(ps, want) {
			t.Errorf("%s missing from %v", want, ps)
		}
	}
	for _, p := range ps {
		if _, err := repocache.ParsePath("~/"+p, ps); err == nil {
			t.Errorf("~/%s may be cached", p)
		}
	}
}
