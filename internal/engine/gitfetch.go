package engine

import (
	"context"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/store"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Git fetch credentials (the connection's git_fetch, sandbox/gitauth.go):
// a declared read credential that also authenticates the agent's git
// fetches on its host, applied by the sandbox's egress proxy — never handed
// to the agent.
//
// The plugin declares the host and the credential; the scope is
// conductor's, LEARNED:
//
//   - A cache warm of a repository — always its default branch, with the
//     default branch's recipe — may fetch any repository on the host the
//     credential can read. The proxy hears each one the forge served, and
//     when the warm succeeds that set replaces what the recipe's previous
//     warm learned (a dependency it no longer fetched drops off).
//   - Every other launch (a dispatch on a PR branch, a review, a fixer, a
//     `conductor sandbox exec` hand-off) gets the repository itself plus
//     its learned dependencies, and nothing a PR branch names: a PR that
//     adds a new private dependency fetches it once the default branch has
//     it and the next warm learns it.

// gitFetchStore persists the repo → instance map and the learned sets
// across restarts. *store.Store satisfies it.
type gitFetchStore interface {
	PutGitFetchInstance(repo, instance string) error
	GitFetchInstance(repo string) (string, bool)
	PutGitLearned(repo, instance, recipe string, fetched []string) error
	GitLearned(repo, instance string) []string
}

var _ gitFetchStore = (*store.Store)(nil)

// gitMem is the in-memory fallback for an engine whose store keeps none.
type gitMem struct {
	mu       sync.Mutex
	instance map[string]string
	learned  map[string]map[string][]string
}

func (e *Engine) gitStore() gitFetchStore {
	if gs, ok := e.store.(gitFetchStore); ok {
		return gs
	}
	return nil
}

// gitKey is repo as the git fetch state keys it: forges name repositories
// case-insensitively.
func gitKey(repo string) string { return strings.ToLower(strings.TrimSpace(repo)) }

// gitFetchAuth is credential name's git fetch credentials for trusted work
// on repo, carrying val: the repository and its learned dependencies.
// Work with no repository gets none.
func (e *Engine) gitFetchAuth(sem *sdk.ConnSemantics, name, instance, repo, val string) []sandbox.GitAuth {
	out := e.gitAuths(sem, name, instance, repo, val, false)
	if len(out) > 0 {
		e.recordGitFetch(repo, instance)
	}
	return out
}

// gitAuths builds the credentials; learn: a default-branch warm's, for any
// repository on the host.
func (e *Engine) gitAuths(sem *sdk.ConnSemantics, name, instance, repo, val string, learn bool) []sandbox.GitAuth {
	if sem == nil || repo == "" || val == "" || strings.Contains(repo, "..") {
		return nil
	}
	learned := e.gitLearned(repo, instance)
	var out []sandbox.GitAuth
	for _, g := range sem.GitFetch {
		if g.Credential != name {
			continue
		}
		for _, h := range e.gitFetchHosts(instance, g) {
			a := sandbox.GitAuth{Host: h, For: repo, Username: g.Username, Secret: val, Learn: learn}
			if !learn {
				a.Repos = append([]string{"/" + gitKey(repo)}, learnedOn(learned, h)...)
			}
			if err := a.Validate(); err != nil {
				e.log("git fetch credential %s on %s: %v", name, instance, err)
				continue
			}
			out = append(out, a)
		}
	}
	return out
}

// learnedOn is the learned entries ("host/path") on host, as paths.
func learnedOn(learned []string, host string) []string {
	var out []string
	pre := strings.ToLower(host) + "/"
	for _, l := range learned {
		if p, ok := strings.CutPrefix(l, pre); ok && p != "" {
			out = append(out, "/"+p)
		}
	}
	return out
}

// gitFetchHosts is g's hosts for instance: the host of the instance's
// HostFrom setting when it sets one, else the declared Hosts.
func (e *Engine) gitFetchHosts(instance string, g sdk.GitFetch) []string {
	if g.HostFrom == "" {
		return g.Hosts
	}
	v, _ := e.connectionSetting(instance, g.HostFrom).(string)
	if strings.TrimSpace(v) == "" {
		return g.Hosts
	}
	h := apiGitHost(v)
	if h == "" {
		e.log("git fetch on %s: %s %q names no host: no git fetch credential", instance, g.HostFrom, v)
		return nil
	}
	return []string{h}
}

// apiGitHost is the git host of an API base URL: its host (with :port when
// not 443), less a leading "api." label when the API is at the host's root.
func apiGitHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if p := strings.Trim(u.EscapedPath(), "/"); p == "" {
		if rest, ok := strings.CutPrefix(host, "api."); ok && strings.Contains(rest, ".") {
			host = rest
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return net.JoinHostPort(host, port)
	}
	return host
}

// connectionSetting is a top-level setting of instance's connection block
// as configured (nil: unset, or no such instance).
func (e *Engine) connectionSetting(instance, key string) any {
	if e.cfg == nil {
		return nil
	}
	ref, ok := e.cfg.ConnectorsMap[instance]
	if !ok {
		return nil
	}
	var raw map[string]any
	if ref.Decode(&raw) != nil {
		return nil
	}
	return raw[key]
}

// recordGitFetch notes that instance's git fetch credential covered
// trusted work on repo: a later warm or hand-off of repo — after a restart
// too — mints through it.
func (e *Engine) recordGitFetch(repo, instance string) {
	k := gitKey(repo)
	if gs := e.gitStore(); gs != nil {
		if err := gs.PutGitFetchInstance(k, instance); err != nil {
			e.log("git fetch: record %s for %s: %v", instance, repo, err)
		}
		return
	}
	e.gitMem.mu.Lock()
	defer e.gitMem.mu.Unlock()
	if e.gitMem.instance == nil {
		e.gitMem.instance = map[string]string{}
	}
	e.gitMem.instance[k] = instance
}

// gitFetchInstance is the instance recorded for repo.
func (e *Engine) gitFetchInstance(repo string) (string, bool) {
	k := gitKey(repo)
	if gs := e.gitStore(); gs != nil {
		return gs.GitFetchInstance(k)
	}
	e.gitMem.mu.Lock()
	defer e.gitMem.mu.Unlock()
	v, ok := e.gitMem.instance[k]
	return v, ok
}

// gitLearned is repo's learned dependencies through instance.
func (e *Engine) gitLearned(repo, instance string) []string {
	k := gitKey(repo)
	if gs := e.gitStore(); gs != nil {
		return gs.GitLearned(k, instance)
	}
	e.gitMem.mu.Lock()
	defer e.gitMem.mu.Unlock()
	var out []string
	for _, l := range e.gitMem.learned[k+"\x00"+instance] {
		out = append(out, l...)
	}
	sort.Strings(out)
	return out
}

// GitLearned records what a successful default-branch warm of repo's
// recipe fetched with the credential ("host/path" each), replacing what
// that recipe's previous warm learned. Only RunCacheWarm calls it.
func (e *Engine) GitLearned(repo, recipe string, fetched []string) {
	instance, ok := e.gitFetchInstance(repo)
	if !ok {
		return
	}
	k := gitKey(repo)
	var clean []string
	for _, f := range fetched {
		f = strings.ToLower(f)
		if host, p, ok := strings.Cut(f, "/"); ok && host != "" && p != "" && path.Clean("/"+p) == "/"+p && strings.TrimSuffix(p, "/") != k {
			clean = append(clean, host+"/"+p)
		}
	}
	if gs := e.gitStore(); gs != nil {
		if err := gs.PutGitLearned(k, instance, recipe, clean); err != nil {
			e.log("git fetch: record what the %s warm of %s learned: %v", recipe, repo, err)
		}
	} else {
		e.gitMem.mu.Lock()
		if e.gitMem.learned == nil {
			e.gitMem.learned = map[string]map[string][]string{}
		}
		m := e.gitMem.learned[k+"\x00"+instance]
		if m == nil {
			m = map[string][]string{}
			e.gitMem.learned[k+"\x00"+instance] = m
		}
		m[recipe] = clean
		e.gitMem.mu.Unlock()
	}
	if len(clean) > 0 {
		e.log("git fetch: %s learned %d dependenc(ies) from its default branch's %s warm: %s", repo, len(clean), recipe, strings.Join(clean, ", "))
	}
}

// GitFetchFor mints the git fetch credentials for a launch on repo outside
// a dispatch (a `conductor sandbox exec` hand-off in a workspace conductor
// checked out of it): the repository and its learned dependencies.
func (e *Engine) GitFetchFor(ctx context.Context, repo string) []sandbox.GitAuth {
	return e.gitFetchMint(ctx, repo, false)
}

// GitFetchWarm mints the git fetch credentials for a default-branch cache
// warm of repo: for any repository on the host (the warm learns which).
func (e *Engine) GitFetchWarm(ctx context.Context, repo string) []sandbox.GitAuth {
	return e.gitFetchMint(ctx, repo, true)
}

// gitFetchMint mints through the connector instance that last dispatched
// trusted work on repo with a git fetch credential — none before such
// work, and none once the instance no longer covers repo.
func (e *Engine) gitFetchMint(ctx context.Context, repo string, learn bool) []sandbox.GitAuth {
	instance, ok := e.gitFetchInstance(repo)
	if !ok || e.connectors == nil {
		return nil
	}
	in, ok := e.connectors.Get(instance)
	if !ok || in.Decl == nil || in.Decl.Semantics == nil || !in.Enabled {
		return nil
	}
	sem := in.Decl.Semantics
	if !e.instanceCovers(instance, sem, repo) {
		e.log("git fetch: %s no longer covers %s: no git fetch credential", instance, repo)
		return nil
	}
	// The trigger only carries the repo to the mint verb's args: the
	// repository's trust was decided by the dispatch that recorded it, and
	// is re-checked against the instance's scope above.
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
			e.log("git fetch for %s: credential %s: %v", repo, cr.Name, err)
			continue
		}
		out = append(out, e.gitAuths(sem, cr.Name, instance, repo, val, learn)...)
	}
	return out
}

// instanceCovers re-checks, at each use, that instance still covers repo:
// when its connection declares an operator-chosen repo scope (semantics
// scope, dimension repo) and the instance sets that option, repo must match
// one of its patterns (case-insensitively, as the forge names repos).
func (e *Engine) instanceCovers(instance string, sem *sdk.ConnSemantics, repo string) bool {
	if sem.Scope == nil || sem.Scope.Dimension != "repo" || sem.Scope.Option == "" {
		return true
	}
	list, ok := e.connectionSetting(instance, sem.Scope.Option).([]any)
	if !ok || len(list) == 0 {
		return true
	}
	r := strings.ToLower(repo)
	for _, p := range list {
		s, _ := p.(string)
		if m, _ := path.Match(strings.ToLower(s), r); m {
			return true
		}
	}
	return false
}
