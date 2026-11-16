package agentsandbox

import (
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
// hook is asked with the launch's own home and workspace, and its overlays,
// environment and hidden layers dir are the policy's.
func TestPolicyRestoresRepoCaches(t *testing.T) {
	ws := t.TempDir()
	var got []repocache.Request
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Caches: func(req repocache.Request) repocache.Grant {
		got = append(got, req)
		layers := filepath.Join(req.Home, repocache.LayersDir)
		return repocache.Grant{
			Mounts: []repocache.Mount{{Target: filepath.Join(req.Home, "go", "pkg", "mod"), Lower: "/store/o~r/go/k/data/home/go/pkg/mod",
				Upper: filepath.Join(layers, "s", "upper"), Work: filepath.Join(layers, "s", "work")}},
			Env:  []string{"GOFLAGS=-modcacherw"},
			Hide: []string{layers},
		}
	}}
	p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "d-1", Repo: "o/r"}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != "o/r" || got[0].Home != p.Home || got[0].Workspace != canon(ws) || got[0].Run != "d-1" {
		t.Fatalf("hook asked %+v", got)
	}
	if len(p.Caches) != 1 || !slices.Contains(p.Env, "GOFLAGS=-modcacherw") || !slices.Contains(p.Hide, filepath.Join(p.Home, repocache.LayersDir)) {
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

// A home a cached launch used keeps its layers hidden, cache or not.
func TestLayersDirAlwaysHidden(t *testing.T) {
	ws := t.TempDir()
	r := &Resolver{Registry: &Registry{}, StateRoot: t.TempDir(), Persist: PersistHome}
	e := Entry{Workspace: ws, Identity: "agent:z"}
	p, err := r.PolicyForEntry(e, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.Hide, filepath.Join(p.Home, repocache.LayersDir)) {
		t.Fatal("hid a layers dir that does not exist")
	}
	if err := os.MkdirAll(filepath.Join(p.Home, repocache.LayersDir), 0o700); err != nil {
		t.Fatal(err)
	}
	p, _ = r.PolicyForEntry(e, "claude", nil, skill.ProcessAnchor{})
	if !slices.Contains(p.Hide, filepath.Join(p.Home, repocache.LayersDir)) {
		t.Fatalf("layers dir shown: %v", p.Hide)
	}
}

func TestValidateRefusesStrayCacheMounts(t *testing.T) {
	f := newPolicyFixture(t)
	layers := filepath.Join(f.pol.Home, repocache.LayersDir)
	ok := repocache.Mount{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: filepath.Join(layers, "s", "u"), Work: filepath.Join(layers, "s", "w")}
	f.pol.Caches = []repocache.Mount{ok}
	if err := f.pol.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]repocache.Mount{
		"target outside": {Target: "/etc", Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"target is home": {Target: f.pol.Home, Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"target is ws":   {Target: f.pol.Workspace, Lower: ok.Lower, Upper: ok.Upper, Work: ok.Work},
		"upper outside":  {Target: ok.Target, Lower: ok.Lower, Upper: "/store/x", Work: ok.Work},
		"work in home":   {Target: ok.Target, Lower: ok.Lower, Upper: ok.Upper, Work: filepath.Join(f.pol.Home, "w")},
		"lower in home":  {Target: ok.Target, Lower: filepath.Join(f.pol.Home, "l"), Upper: ok.Upper, Work: ok.Work},
		"lower in ws":    {Target: ok.Target, Lower: filepath.Join(f.pol.Workspace, "l"), Upper: ok.Upper, Work: ok.Work},
		"lower relative": {Target: ok.Target, Lower: "store/x", Upper: ok.Upper, Work: ok.Work},
	} {
		f.pol.Caches = []repocache.Mount{m}
		if err := f.pol.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

func TestWrapLinuxMountsCacheOverlays(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	layers := filepath.Join(f.pol.Home, repocache.LayersDir)
	m := repocache.Mount{Target: filepath.Join(f.pol.Home, "go", "pkg", "mod"), Lower: "/store/o~r/go/k/data/home/go/pkg/mod",
		Upper: filepath.Join(layers, "s", "upper"), Work: filepath.Join(layers, "s", "work")}
	f.pol.Caches = []repocache.Mount{m}
	f.pol.Hide = append(f.pol.Hide, layers)
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
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
	if !hasBind(bs, sandbox.BindMount{Path: layers, Tmpfs: true}) {
		t.Fatal("the layers dir is not hidden")
	}
}

func TestWrapDarwinIgnoresCacheOverlays(t *testing.T) {
	withPlatform(t, "darwin")
	f := newPolicyFixture(t)
	layers := filepath.Join(f.pol.Home, repocache.LayersDir)
	f.pol.Caches = []repocache.Mount{{Target: filepath.Join(f.pol.Home, "go"), Lower: "/store/x", Upper: filepath.Join(layers, "u"), Work: filepath.Join(layers, "w")}}
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
