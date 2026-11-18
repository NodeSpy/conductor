package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
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
