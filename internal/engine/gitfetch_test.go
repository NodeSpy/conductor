package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/store"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// gitFetchRig is an engine over a state dir, whose forge instance "i"
// (connection: conn, YAML) declares git_fetch for its read credential.
type gitFetchRig struct {
	t      *testing.T
	dir    string
	reg    *connector.Registry
	sem    *sdk.ConnSemantics
	conn   string
	minted []string
	st     *store.Store
	e      *Engine
}

func newGitFetchRig(t *testing.T, conn string, g sdk.GitFetch) *gitFetchRig {
	r := &gitFetchRig{t: t, dir: t.TempDir(), reg: forgeRegistry(t), conn: conn}
	in, ok := r.reg.Get("i")
	if !ok || in.Decl.Semantics == nil {
		t.Fatal("no forge instance")
	}
	sem := *in.Decl.Semantics
	sem.GitFetch = []sdk.GitFetch{g}
	in.Decl.Semantics = &sem
	r.sem = &sem
	r.restart()
	return r
}

// restart is a daemon restart: a new store from the same state dir, a new
// engine, nothing carried in memory.
func (r *gitFetchRig) restart() {
	if r.st != nil {
		r.st.Close()
	}
	st, err := store.Open(store.Options{StatePath: filepath.Join(r.dir, "s.json"), AuditPath: filepath.Join(r.dir, "a.jsonl"), MaxPRs: 100, AuditMaxSize: 1 << 20})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { st.Close() })
	cfg := baseCfg()
	var cc config.Config
	if err := yaml.Unmarshal([]byte("connectors:\n  i: "+r.conn+"\n"), &cc); err != nil {
		r.t.Fatal(err)
	}
	cfg.ConnectorsMap = cc.ConnectorsMap
	r.st = st
	r.e = New(Options{Config: cfg, Store: st, Dispatch: &fakeDispatcher{}, Notifier: &fakeNotifier{},
		Author: dispatch.Author{Name: "Me"}, Connectors: r.reg,
		InvokeVerb: func(_ context.Context, inst, verb string, opts map[string]any) (map[string]any, error) {
			repo, _ := opts["repo"].(string)
			r.minted = append(r.minted, inst+"."+verb+":"+repo)
			return map[string]any{"token": "tok-" + verb}, nil
		}})
}

// dispatch is trusted work on repo (a PR the platform assigned): its git
// fetch credentials.
func (r *gitFetchRig) dispatch(repo string) []sandbox.GitAuth {
	r.t.Helper()
	tr := core.Trigger{Instance: "i", TargetTrusted: true, Target: core.Target{Repo: repo, Number: 1}, Context: map[string]any{"repo": repo}}
	c, err := r.e.declaredCredentials(context.Background(), tr, "i", r.sem, false)
	if err != nil {
		r.t.Fatal(err)
	}
	return c.Git
}

var forgeFetch = sdk.GitFetch{Credential: "read", Hosts: []string{"forge.example"}, Username: "x-access"}

// A dispatch's credential covers its repository alone until a warm learns
// its dependencies; work for a target the platform did not assign, or with
// no repository, gets none.
func TestGitFetchCredentials(t *testing.T) {
	r := newGitFetchRig(t, "{ use: github }", forgeFetch)
	g := r.dispatch("Org/App")
	if len(g) != 1 || g[0].Host != "forge.example" || g[0].Secret != "tok-read_token" || g[0].Username != "x-access" ||
		g[0].Learn || g[0].For != "Org/App" || !reflect.DeepEqual(g[0].Repos, []string{"/org/app"}) {
		t.Fatalf("git = %+v", g)
	}
	if c, _ := r.e.declaredCredentials(context.Background(), core.Trigger{Instance: "i", Target: core.Target{Repo: "org/app"}}, "i", r.sem, false); len(c.Git) != 0 {
		t.Fatalf("unassigned: %+v", c.Git)
	}
	if c, _ := r.e.declaredCredentials(context.Background(), core.Trigger{Instance: "i", TargetTrusted: true}, "i", r.sem, false); len(c.Git) != 0 {
		t.Fatalf("no repo: %+v", c.Git)
	}
	// A repository no trusted work ran on: nothing minted, for a warm or a
	// hand-off.
	r.minted = nil
	if g := r.e.GitFetchTeach(context.Background(), "evil/repo"); len(g) != 0 || len(r.minted) != 0 {
		t.Fatalf("unseen repo: %+v minted %v", g, r.minted)
	}
	if g := r.e.GitFetchFor(context.Background(), "evil/repo"); len(g) != 0 || len(r.minted) != 0 {
		t.Fatalf("unseen repo: %+v minted %v", g, r.minted)
	}
}

// LEARNED SCOPE. A default-branch warm's credential is for any repository
// on the host; what it fetched becomes the repository's learned
// dependencies — the scope of every other launch (a PR dispatch, a hand-off).
// A later warm that no longer fetches one drops it; another recipe's stay;
// a restart keeps them; nothing but a warm adds one.
func TestGitFetchLearnedScope(t *testing.T) {
	r := newGitFetchRig(t, "{ use: github }", forgeFetch)
	scope := func(g []sandbox.GitAuth) []string {
		t.Helper()
		if len(g) != 1 {
			t.Fatalf("credentials %+v", g)
		}
		return g[0].Repos
	}
	// A PR launch before any warm: the repository alone — an org's other
	// repository is not in it.
	if s := scope(r.dispatch("org/app")); !reflect.DeepEqual(s, []string{"/org/app"}) {
		t.Fatalf("before a warm: %v", s)
	}
	// The warm: any repository on the host.
	w := r.e.GitFetchTeach(context.Background(), "org/app")
	if len(w) != 1 || !w[0].Learn || len(w[0].Repos) != 0 || w[0].Secret != "tok-read_token" {
		t.Fatalf("warm: %+v", w)
	}
	r.e.GitLearned("org/app", "yarn", []string{"forge.example/org/dep", "forge.example/Other/Lib", "forge.example/org/app", "elsewhere.example/x/y", "forge.example/../etc"})
	r.e.GitLearned("org/app", "go", []string{"forge.example/org/gomod"})
	want := []string{"/org/app", "/org/dep", "/org/gomod", "/other/lib"}
	if s := scope(r.dispatch("org/app")); !reflect.DeepEqual(s, want) {
		t.Fatalf("after the warms: %v, want %v", s, want)
	}
	if s := scope(r.e.GitFetchFor(context.Background(), "org/app")); !reflect.DeepEqual(s, want) {
		t.Fatalf("a hand-off: %v, want %v", s, want)
	}
	// Another repository's launch learns nothing from it.
	if s := scope(r.dispatch("org/other")); !reflect.DeepEqual(s, []string{"/org/other"}) {
		t.Fatalf("another repository: %v", s)
	}
	// A restart keeps them.
	r.restart()
	if s := scope(r.e.GitFetchFor(context.Background(), "org/app")); !reflect.DeepEqual(s, want) {
		t.Fatalf("after a restart: %v, want %v", s, want)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, "git-learned.json"))
	if !strings.Contains(string(b), "org/dep") || strings.Contains(string(b), "tok-") {
		t.Fatalf("git-learned.json: %s", b)
	}
	// The yarn warm no longer fetches org/dep: it drops off; go's stays.
	r.e.GitLearned("org/app", "yarn", []string{"forge.example/other/lib"})
	if s := scope(r.dispatch("org/app")); !reflect.DeepEqual(s, []string{"/org/app", "/org/gomod", "/other/lib"}) {
		t.Fatalf("after yarn dropped org/dep: %v", s)
	}
	r.e.GitLearned("org/app", "yarn", nil)
	r.e.GitLearned("org/app", "go", nil)
	if s := scope(r.dispatch("org/app")); !reflect.DeepEqual(s, []string{"/org/app"}) {
		t.Fatalf("nothing learned: %v", s)
	}
}

// A warm right after a restart mints exactly as before: the repository's
// instance is kept in the state dir.
func TestGitFetchSurvivesRestart(t *testing.T) {
	r := newGitFetchRig(t, "{ use: github }", forgeFetch)
	if len(r.dispatch("org/app")) != 1 {
		t.Fatal("no dispatch credential")
	}
	r.restart()
	r.minted = nil
	g := r.e.GitFetchTeach(context.Background(), "org/app")
	if len(g) != 1 || g[0].Secret != "tok-read_token" || !g[0].Learn {
		t.Fatalf("warm after restart: %+v", g)
	}
	if !reflect.DeepEqual(r.minted, []string{"i.read_token:org/app"}) {
		t.Fatalf("minted %v", r.minted)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, "git-fetch.json"))
	if strings.Contains(string(b), "tok-") {
		t.Fatalf("the state holds a credential: %s", b)
	}
}

// host_from: an instance whose API base is a GitHub Enterprise Server's
// (or a data-residency api.<sub>.ghe.com) fetches from that host, never the
// declared default; one with none set keeps the declared host.
func TestGitFetchHostFrom(t *testing.T) {
	g := sdk.GitFetch{Credential: "read", Hosts: []string{"github.com"}, HostFrom: "api_base", Username: "x-access-token"}
	for conn, want := range map[string]string{
		"{ use: github }": "github.com",
		"{ use: github, api_base: 'https://api.github.com' }":     "github.com",
		"{ use: github, api_base: 'https://ghes.corp/api/v3' }":   "ghes.corp",
		"{ use: github, api_base: 'https://api.acme.ghe.com/' }":  "acme.ghe.com",
		"{ use: github, api_base: 'https://api.corp/api/v3' }":    "api.corp",
		"{ use: github, api_base: 'https://ghes.corp:8443/api' }": "ghes.corp:8443",
	} {
		r := newGitFetchRig(t, conn, g)
		got := r.dispatch("org/app")
		if len(got) != 1 || got[0].Host != want {
			t.Errorf("%s: %+v, want %s", conn, got, want)
		}
	}
	for _, conn := range []string{
		"{ use: github, api_base: 'http://ghes.corp/api/v3' }",
		"{ use: github, api_base: 'https://u:p@ghes.corp/api/v3' }",
		"{ use: github, api_base: 'not a url' }",
	} {
		if got := newGitFetchRig(t, conn, g).dispatch("org/app"); len(got) != 0 {
			t.Errorf("%s: %+v, want none", conn, got)
		}
	}
}

// The instance's own repo scope is re-checked at every use: a repository
// removed from it gets no credential for a warm or a hand-off.
func TestGitFetchRechecksInstanceScope(t *testing.T) {
	r := newGitFetchRig(t, "{ use: github, repos: ['Org/*'] }", forgeFetch)
	if len(r.dispatch("org/app")) != 1 {
		t.Fatal("no dispatch credential")
	}
	if g := r.e.GitFetchFor(context.Background(), "org/app"); len(g) != 1 {
		t.Fatalf("in scope (case-insensitively): %+v", g)
	}
	r.conn = "{ use: github, repos: ['other/*'] }"
	r.restart()
	r.minted = nil
	if g := r.e.GitFetchTeach(context.Background(), "org/app"); len(g) != 0 || len(r.minted) != 0 {
		t.Fatalf("warm of a repository the instance dropped: %+v minted %v", g, r.minted)
	}
	if g := r.e.GitFetchFor(context.Background(), "org/app"); len(g) != 0 {
		t.Fatalf("hand-off of a repository the instance dropped: %+v", g)
	}
}

func TestAPIGitHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.github.com":          "github.com",
		"https://API.GitHub.com/":         "github.com",
		"https://ghe.corp/api/v3":         "ghe.corp",
		"https://api.x.ghe.com":           "x.ghe.com",
		"https://api.corp/api/v3":         "api.corp",
		"https://api.corp.example/api/v3": "api.corp.example",
		"https://api.localhost":           "api.localhost",
		"https://ghe.corp:443/api/v3":     "ghe.corp",
		"https://ghe.corp:8443/api/v3":    "ghe.corp:8443",
		"http://ghe.corp/api/v3":          "",
		"https://user:pw@ghe.corp/api/v3": "",
		"ghe.corp":                        "",
		"https:///api":                    "",
	} {
		if got := apiGitHost(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
