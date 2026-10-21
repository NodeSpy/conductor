package agentsandbox

import (
	"path/filepath"
	"sync"

	"github.com/NodeSpy/conductor/internal/skill"
)

// Entry is a workspace conductor created for an agent, and who works in it.
// `conductor sandbox exec` finds the identity for its cwd here (Lookup);
// the push verbs find where a dispatch's work goes (ForDispatch).
//
// Whoever creates an agent's workspace registers it: a dispatch's runtime
// when it launches into its worktree, a paseo dispatch when it creates its
// agent, a member (#178) for its own workspace. An unregistered directory
// still runs sandboxed — under a default policy scoped to that directory
// (Resolver.PolicyFor).
type Entry struct {
	// Workspace is the directory; a cwd inside it resolves to this entry.
	Workspace string
	// Identity names who works here: its tool state (HOME) is keyed by it,
	// so every launch with the same identity — a resumed session included —
	// sees the same memory.
	Identity string
	// Tool is the canonical agent CLI when the registrant knows it ("" =
	// whatever the launch says).
	Tool string
	// Model is the model the launch runs ("provider/model" for opencode;
	// "" = the tool's default): a multi-provider tool's allowlist follows it.
	Model string
	// Dispatch is the dispatch id the workspace serves ("" for a member).
	Dispatch string
	// Skill is what a fresh token for a launch here stands for — the
	// dispatch's (or member's) real provenance and skill policy. nil: no
	// token is minted.
	Skill *skill.Identity
	// Egress adds allowlist entries beyond the model endpoints and the
	// operator's sandbox.egress.
	Egress []string
	// ReadOnly / ReadWrite are further paths the workspace needs (GitPaths).
	ReadOnly  []string
	ReadWrite []string
	Hide      []string
	// Env are extra variables for the agent (its git identity).
	Env []string
	// Push is where conductor pushes this workspace's work (nil: nowhere).
	Push *PushTarget
}

// PushTarget is where conductor — never the agent — pushes a workspace.
type PushTarget struct {
	// Repo is owner/name.
	Repo string
	// Branch is the dispatch branch: the ONLY ref a push may update
	// (refs/heads/<Branch>).
	Branch string
	// GitDir is the trusted git dir conductor's git runs in: the base
	// clone's .git for a per-dispatch clone, a linked worktree's common dir.
	// Never the agent-writable clone itself.
	GitDir string
	// Remote is the URL pushed to, read from trusted config when the
	// workspace was provisioned.
	Remote string
	// HeadDir is a linked worktree's own git dir, recorded when the
	// workspace was registered ("" for a per-dispatch clone).
	HeadDir string
}

// Registry maps workspaces to entries. The zero value is ready to use.
type Registry struct {
	mu   sync.Mutex
	byWS map[string]Entry
}

// Default is the daemon's registry.
var Default = &Registry{}

// Register records e and returns the func that removes it (idempotent).
func (r *Registry) Register(e Entry) func() {
	ws := canon(e.Workspace)
	e.Workspace = ws
	r.mu.Lock()
	if r.byWS == nil {
		r.byWS = map[string]Entry{}
	}
	r.byWS[ws] = e
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if cur, ok := r.byWS[ws]; ok && cur.Identity == e.Identity && cur.Dispatch == e.Dispatch {
				delete(r.byWS, ws)
			}
			r.mu.Unlock()
		})
	}
}

// Forget removes the entry registered for exactly ws.
func (r *Registry) Forget(ws string) {
	c := canon(ws)
	r.mu.Lock()
	delete(r.byWS, c)
	delete(r.byWS, filepath.Clean(ws))
	r.mu.Unlock()
}

// Lookup returns the entry whose workspace is cwd or contains it (the
// deepest, when workspaces nest).
func (r *Registry) Lookup(cwd string) (Entry, bool) {
	c := canon(cwd)
	r.mu.Lock()
	defer r.mu.Unlock()
	var best Entry
	found := false
	for ws, e := range r.byWS {
		if within(c, ws) && (!found || len(ws) > len(best.Workspace)) {
			best, found = e, true
		}
	}
	return best, found
}

// ForDispatch returns the entry registered for a dispatch id.
func (r *Registry) ForDispatch(id string) (Entry, bool) {
	if id == "" {
		return Entry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.byWS {
		if e.Dispatch == id {
			return e, true
		}
	}
	return Entry{}, false
}

// canon is a path's canonical form for matching: absolute, clean, symlinks
// resolved where it exists (macOS's /var → /private/var, a symlinked home).
func canon(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	// Resolve the longest prefix that exists: a path not created yet under
	// a symlinked dir (macOS /var) still matches its workspace.
	rest := ""
	for dir := p; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
