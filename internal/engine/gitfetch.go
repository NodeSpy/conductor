package engine

import (
	"context"
	"strings"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/store"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Git fetch credentials (the connection's git_fetch, sandbox/gitauth.go):
// a declared read credential that also authenticates the agent's git
// fetches on its hosts, applied by the sandbox's egress proxy — never
// handed to the agent.

// gitFetchAuth is credential name's git fetch credentials for work on
// repo, carrying val. The repositories in scope render over the repo
// alone; work with no repository gets none.
func (e *Engine) gitFetchAuth(sem *sdk.ConnSemantics, name, instance, repo, val string) []sandbox.GitAuth {
	if sem == nil || repo == "" || val == "" {
		return nil
	}
	var out []sandbox.GitAuth
	for _, g := range sem.GitFetch {
		if g.Credential != name {
			continue
		}
		repos := g.Repos
		if len(repos) == 0 {
			repos = []string{"{{.repo}}"}
		}
		var scope []string
		for _, r := range repos {
			if p := renderRepoScope(r, repo); p != "" {
				scope = append(scope, p)
			}
		}
		if len(scope) == 0 {
			continue
		}
		for _, h := range g.Hosts {
			a := sandbox.GitAuth{Host: h, Repos: scope, Username: g.Username, Secret: val}
			if err := a.Validate(); err != nil {
				e.log("git fetch credential %s on %s: %v", name, instance, err)
				continue
			}
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		e.recordGitFetch(repo, instance)
	}
	return out
}

// gitFetchStore persists the repo → instance map across restarts.
// *store.Store satisfies it.
type gitFetchStore interface {
	PutGitFetchInstance(repo, instance string) error
	GitFetchInstance(repo string) (string, bool)
}

var _ gitFetchStore = (*store.Store)(nil)

// recordGitFetch notes that instance's git fetch credential covered
// trusted work on repo: a later warm of repo — after a restart too —
// mints through it.
func (e *Engine) recordGitFetch(repo, instance string) {
	e.gitRepos.Store(repo, instance)
	if gs, ok := e.store.(gitFetchStore); ok {
		if err := gs.PutGitFetchInstance(repo, instance); err != nil {
			e.log("git fetch: record %s for %s: %v", instance, repo, err)
		}
	}
}

// gitFetchInstance is the instance recorded for repo, this run or before.
func (e *Engine) gitFetchInstance(repo string) (string, bool) {
	if v, ok := e.gitRepos.Load(repo); ok {
		return v.(string), true
	}
	if gs, ok := e.store.(gitFetchStore); ok {
		return gs.GitFetchInstance(repo)
	}
	return "", false
}

// renderRepoScope renders one git_fetch repos entry for repo as a path
// prefix on the host ("" when it renders to nothing usable: an empty
// value, a dot segment).
func renderRepoScope(tmpl, repo string) string {
	owner := ""
	if i := strings.LastIndex(repo, "/"); i > 0 {
		owner = repo[:i]
	}
	vars := map[string]any{"repo": repo, "repo_owner": owner}
	s, _ := core.DeclaredArgs(map[string]string{"r": tmpl}, vars)["r"].(string)
	s = strings.Trim(strings.TrimSpace(s), " ")
	dir := strings.HasSuffix(s, "/")
	s = strings.Trim(s, "/")
	if s == "" {
		return ""
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\%?#") {
			return ""
		}
	}
	s = "/" + s
	if dir {
		s += "/"
	}
	return s
}

// GitFetchFor mints the git fetch credentials for a cache warm of repo:
// through the connector instance that last dispatched trusted work on
// repo with a git fetch credential, as recorded in the store (none before
// such work — a repository the platform never assigned to it gets nothing).
func (e *Engine) GitFetchFor(ctx context.Context, repo string) []sandbox.GitAuth {
	instance, ok := e.gitFetchInstance(repo)
	if !ok || e.connectors == nil {
		return nil
	}
	in, ok := e.connectors.Get(instance)
	if !ok || in.Decl == nil || in.Decl.Semantics == nil || !in.Enabled {
		return nil
	}
	sem := in.Decl.Semantics
	// The trigger only carries the repo to the mint verb's args: the
	// repository's trust was decided by the dispatch that recorded it.
	t := core.Trigger{Instance: instance, Target: core.Target{Repo: repo}}
	var out []sandbox.GitAuth
	for _, cr := range sem.Credentials {
		used := false
		for _, g := range sem.GitFetch {
			used = used || g.Credential == cr.Name
		}
		if !used {
			continue
		}
		val, err := e.mint(ctx, t, instance, cr, true)
		if err != nil {
			e.log("cache warm %s: git fetch credential %s: %v", repo, cr.Name, err)
			continue
		}
		out = append(out, e.gitFetchAuth(sem, cr.Name, instance, repo, val)...)
	}
	return out
}
