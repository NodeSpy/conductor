package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A connection's git_fetch makes its read credential a git fetch
// credential for the declared hosts, scoped to the repositories its repos
// render to for the work's repository — for an assigned target only — and
// a cache warm of a repository such work ran on mints one through the
// same instance; any other repository gets none.
func TestGitFetchCredentials(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	in, ok := e.connectors.Get("i")
	if !ok || in.Decl.Semantics == nil {
		t.Fatal("no forge instance")
	}
	var minted []string
	e.invokeVerb = func(_ context.Context, inst, verb string, opts map[string]any) (map[string]any, error) {
		r, _ := opts["repo"].(string)
		minted = append(minted, inst+"."+verb+":"+r)
		return map[string]any{"token": "tok-" + verb}, nil
	}
	sem := *in.Decl.Semantics
	sem.GitFetch = []sdk.GitFetch{{Credential: "read", Hosts: []string{"forge.example", "forge.example:8443"}, Username: "x-access",
		Repos: []string{"{{.repo_owner}}/", "{{.repo}}-docs"}}}
	in.Decl.Semantics = &sem
	tr := core.Trigger{Instance: "i", TargetTrusted: true, Target: core.Target{Repo: "org/app", Number: 1}, Context: map[string]any{"repo": "org/app"}}
	c, err := e.declaredCredentials(context.Background(), tr, "i", &sem, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Git) != 2 || c.Git[0].Host != "forge.example" || c.Git[1].Host != "forge.example:8443" {
		t.Fatalf("git = %+v", c.Git)
	}
	if g := c.Git[0]; g.Secret != "tok-read_token" || g.Username != "x-access" || !reflect.DeepEqual(g.Repos, []string{"/org/", "/org/app-docs"}) {
		t.Fatalf("git[0] = %+v", g)
	}
	// Only the credential git_fetch names.
	if c.Env["GH_TOKEN"] == "" {
		t.Fatalf("env = %v", c.Env)
	}
	// An unassigned target: nothing.
	if c, _ := e.declaredCredentials(context.Background(), core.Trigger{Instance: "i", Target: core.Target{Repo: "org/app"}}, "i", &sem, false); len(c.Git) != 0 {
		t.Fatalf("unassigned: %+v", c.Git)
	}
	// Work with no repository: nothing.
	if c, _ := e.declaredCredentials(context.Background(), core.Trigger{Instance: "i", TargetTrusted: true}, "i", &sem, false); len(c.Git) != 0 {
		t.Fatalf("no repo: %+v", c.Git)
	}
	// A warm of org/app mints through i, for org/app; another repository
	// gets nothing and mints nothing.
	minted = nil
	if g := e.GitFetchFor(context.Background(), "org/app"); len(g) != 2 || g[0].Secret != "tok-read_token" || !reflect.DeepEqual(g[0].Repos, []string{"/org/", "/org/app-docs"}) {
		t.Fatalf("warm: %+v", g)
	}
	if !reflect.DeepEqual(minted, []string{"i.read_token:org/app"}) {
		t.Fatalf("warm minted %v", minted)
	}
	minted = nil
	if g := e.GitFetchFor(context.Background(), "evil/repo"); len(g) != 0 || len(minted) != 0 {
		t.Fatalf("unseen repo: %+v minted %v", g, minted)
	}
}

func TestRenderRepoScope(t *testing.T) {
	for _, c := range []struct{ tmpl, repo, want string }{
		{"{{.repo}}", "o/r", "/o/r"},
		{"{{.repo_owner}}/", "o/r", "/o/"},
		{"{{.repo_owner}}/", "g/sub/r", "/g/sub/"},
		{"{{.repo_owner}}/", "r", ""},
		{"{{.repo}}/../x", "o/r", ""},
		{"{{.nope}}", "o/r", ""},
		{"/{{.repo}}", "o/r", "/o/r"},
		{"{{.repo}}%2f", "o/r", ""},
	} {
		if got := renderRepoScope(c.tmpl, c.repo); got != c.want {
			t.Errorf("%q over %q = %q, want %q", c.tmpl, c.repo, got, c.want)
		}
	}
}

// A daemon restart (a new engine over the same state dir) keeps a
// repository's git fetch instance: a cache warm right after it mints the
// credential exactly as before, with no dispatch in between. The state
// holds names only, never the credential.
func TestGitFetchSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	open := func() *store.Store {
		st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl"), MaxPRs: 100, AuditMaxSize: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	reg := forgeRegistry(t)
	in, _ := reg.Get("i")
	sem := *in.Decl.Semantics
	sem.GitFetch = []sdk.GitFetch{{Credential: "read", Hosts: []string{"forge.example"}, Username: "x-access", Repos: []string{"{{.repo}}"}}}
	in.Decl.Semantics = &sem
	var minted []string
	verbs := func(_ context.Context, inst, verb string, opts map[string]any) (map[string]any, error) {
		r, _ := opts["repo"].(string)
		minted = append(minted, inst+"."+verb+":"+r)
		return map[string]any{"token": "tok-" + verb}, nil
	}
	engine := func(st *store.Store) *Engine {
		return New(Options{Config: baseCfg(), Store: st, Dispatch: &fakeDispatcher{}, Notifier: &fakeNotifier{},
			Author: dispatch.Author{Name: "Me"}, InvokeVerb: verbs, Connectors: reg})
	}

	st1 := open()
	e1 := engine(st1)
	tr := core.Trigger{Instance: "i", TargetTrusted: true, Target: core.Target{Repo: "org/app", Number: 1}, Context: map[string]any{"repo": "org/app"}}
	if c, err := e1.declaredCredentials(context.Background(), tr, "i", &sem, false); err != nil || len(c.Git) != 1 {
		t.Fatalf("dispatch: %+v %v", c.Git, err)
	}
	st1.Close()
	b, err := os.ReadFile(filepath.Join(dir, "git-fetch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tok-") {
		t.Fatalf("the state holds a credential: %s", b)
	}

	// The restart: a new store from disk, a new engine, no dispatch.
	st2 := open()
	defer st2.Close()
	e2 := engine(st2)
	minted = nil
	g := e2.GitFetchFor(context.Background(), "org/app")
	if len(g) != 1 || g[0].Secret != "tok-read_token" || !reflect.DeepEqual(g[0].Repos, []string{"/org/app"}) {
		t.Fatalf("warm after restart: %+v", g)
	}
	if !reflect.DeepEqual(minted, []string{"i.read_token:org/app"}) {
		t.Fatalf("minted %v", minted)
	}
	if g := e2.GitFetchFor(context.Background(), "org/other"); len(g) != 0 {
		t.Fatalf("a repository no trusted work ran on: %+v", g)
	}
}
