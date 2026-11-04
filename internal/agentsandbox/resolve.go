package agentsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// Resolver builds launch policies on the daemon side: for a registered
// workspace from its Entry, for any other directory a default policy scoped
// to it. It never builds an unconfined one.
type Resolver struct {
	// Registry is consulted for the cwd's identity (nil: Default).
	Registry *Registry
	// StateRoot holds the identity homes (<state>/agent-state).
	StateRoot string
	// RealHome is the operator's home: settings seed, login files, and a
	// boundary no default workspace may cover.
	RealHome string
	// Protected are further directories a default workspace may be neither
	// inside nor contain: conductor's own state and config.
	Protected []string
	// Egress is the operator's sandbox.egress, added for every agent.
	Egress []string
	// Socket is the conductor skill socket ("" = none served).
	Socket string
	// Mint registers a fresh session token for an identity, bound to root's
	// process tree (rt2 #12) — root is the pid on the other end of the
	// `conductor sandbox exec` control connection that asked for this
	// policy, whose descendants are the sandboxed agent and everything it
	// spawns. ("" + nil error when tokens are not available here.)
	Mint func(id skill.Identity, root skill.ProcessAnchor) (string, error)
	// GitName / GitEmail are the operator's git identity for the agent's
	// commits.
	GitName, GitEmail string
	// Env is the daemon's own environment, for configured model endpoints.
	Env []string
	// GOOS is the platform the launch runs on.
	GOOS string
	// PosixIPC is sandbox.macos_posix_ipc.
	PosixIPC bool
	// ModelCredential is sandbox.model_credential (Policy.ModelCredential).
	ModelCredential string
	// Detect is sandbox.detect (Policy.Detect).
	Detect DetectSpec
	// Limits is sandbox.limits (Policy.Limits).
	Limits LimitSpec
	// Persist is sandbox.persist ("" = PersistNone).
	Persist string
	// Inspect is sandbox.inspect (Policy.Inspect).
	Inspect []sandbox.InspectHost
	// EgressAnyBytes / EgressIdle / EgressUpload / EgressDownload are the
	// egress session settings every policy carries (Policy).
	EgressAnyBytes                           bool
	EgressIdle, EgressUpload, EgressDownload int64
	// ModelUpload / ModelDownload are the model gateway's caps (Policy).
	ModelUpload, ModelDownload int64
	// Allow is sandbox.allow: the base allowance of every launch, joined by
	// each entry's own scope.
	Allow *Allowance
	// Rescope recomputes a workspace's recorded scope from the live config
	// (nil: the recorded scope as is).
	Rescope func(*Scope) *Scope
	// Session resolves a cwd that is NOT in the registry to a live bot
	// session's entry (its identity, its granted skill verbs, its read-only
	// paths), consulted before a default workspace is built. This is how a
	// bot's home — or a hand-off worker — keeps its bot identity and verbs
	// under the sandbox: its session is a PINNED paseo workspace conductor
	// never registered as a worktree, so `conductor sandbox exec` would
	// otherwise resolve it to a verb-less `dir:` identity. nil: no bot
	// sessions on this daemon (the lookup is wired by the bot service).
	Session func(cwd string) (Entry, bool)
}

func (r *Resolver) registry() *Registry {
	if r.Registry != nil {
		return r.Registry
	}
	return Default
}

// PolicyFor resolves the policy for a launch in cwd of tool. launcherEnv is
// the launching process's environment (its configured model endpoint, e.g.
// ANTHROPIC_BASE_URL, joins the allowlist or the relays). A cwd conductor
// registered gets its entry's identity; any other directory a default
// policy: identity = the directory, the model endpoints only.
//
// scratchOK: the launch is not an agent session in a directory of the
// operator's choosing (a launcher's own utility process — paseo listing a
// provider's models from its daemon's cwd). A cwd that may not be a
// workspace (the home, /, conductor's state) then gets the tool's scratch
// workspace instead of a refusal.
//
// root anchors any token this policy mints to the process tree that asked
// for it (rt2 #12) — the pid on the other end of the control connection
// (zero value: the caller has no such peer to anchor to, e.g. tests; Mint
// then refuses per MintSession's own contract).
func (r *Resolver) PolicyFor(cwd, tool string, launcherEnv []string, scratchOK bool, root skill.ProcessAnchor) (Policy, error) {
	if !filepath.IsAbs(cwd) {
		return Policy{}, fmt.Errorf("agentsandbox: cwd %q is not absolute", cwd)
	}
	if e, ok := r.registry().Lookup(cwd); ok {
		return r.policyForCwd(e, cwd, tool, launcherEnv, root)
	}
	// A bot session's cwd is not registered as a worktree (it is a pinned
	// paseo workspace): resolve it to the bot's entry before falling to a
	// default, so the launch keeps its bot identity and verbs.
	if r.Session != nil {
		if e, ok := r.Session(cwd); ok {
			if e.Workspace == "" {
				e.Workspace = canon(cwd)
			}
			return r.policyForCwd(e, cwd, tool, launcherEnv, root)
		}
	}
	ws := canon(cwd)
	if err := r.defaultAllowed(ws); err != nil {
		if !scratchOK {
			return Policy{}, err
		}
		e, serr := r.scratchEntry(CanonicalTool(tool))
		if serr != nil {
			return Policy{}, serr
		}
		p, perr := r.PolicyForEntry(e, tool, launcherEnv, root)
		p.Note = fmt.Sprintf("%v — running in the %s scratch workspace %s instead", err, e.Identity, e.Workspace)
		return p, perr
	}
	return r.PolicyForEntry(DefaultEntry(ws), tool, launcherEnv, root)
}

// policyForCwd builds the policy for an entry whose workspace is the launch's
// own cwd — a registered workspace, or a live bot session. The agent starts
// where it was started, inside its workspace.
func (r *Resolver) policyForCwd(e Entry, cwd, tool string, launcherEnv []string, root skill.ProcessAnchor) (Policy, error) {
	p, err := r.PolicyForEntry(e, tool, launcherEnv, root)
	p.Cwd = canon(cwd)
	if err == nil && p.Tool == "claude-code" && e.Dispatch != "" && p.Cwd != p.Workspace {
		err = trustClaudeWorkspace(p.Home, p.Cwd)
	}
	return p, err
}

// scratchEntry is a tool's scratch workspace: an empty directory of its
// own under the identity homes, for a launch with no workspace to show.
func (r *Resolver) scratchEntry(tool string) (Entry, error) {
	if tool == "" {
		tool = "agent"
	}
	id := "scratch:" + tool
	ws := filepath.Join(r.StateRoot, IdentityKey(id), "work")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return Entry{}, err
	}
	// A scratch launch is a launcher's own utility process: no requester.
	return Entry{Workspace: canon(ws), Identity: id, Skill: &skill.Identity{Agent: "sandbox:scratch"}}, nil
}

// DefaultEntry is the entry for a directory conductor did not create: the
// directory is the identity, its own tool state, and a token that
// authenticates it to conductor without granting any verb. Its requester
// is the operator: an agent started in a directory of the operator's own,
// not by any trigger (conductor's own launches register their workspaces).
func DefaultEntry(ws string) Entry {
	return Entry{
		Workspace: ws,
		Identity:  "dir:" + ws,
		Skill:     &skill.Identity{Agent: "sandbox:" + filepath.Base(ws), Requester: skill.RequesterOperator},
	}
}

// defaultAllowed refuses a default workspace that would show the operator's
// secrets or conductor's own state: the home itself (and anything above it),
// or a directory inside / containing conductor's state or config.
func (r *Resolver) defaultAllowed(ws string) error {
	if ws == string(filepath.Separator) {
		return fmt.Errorf("agentsandbox: refusing to sandbox an agent with / as its workspace")
	}
	if r.RealHome != "" && within(canon(r.RealHome), ws) {
		return fmt.Errorf("agentsandbox: refusing %s as a workspace: it contains your home directory (its keys and logins would be in the sandbox) — start the agent in a project directory", ws)
	}
	for _, p := range r.Protected {
		if p == "" {
			continue
		}
		cp := canon(p)
		if within(ws, cp) || within(cp, ws) {
			return fmt.Errorf("agentsandbox: refusing %s as a workspace: it overlaps conductor's own state or config (%s)", ws, p)
		}
	}
	return nil
}

// PolicyForEntry builds the policy for a launch in e's workspace: the
// identity home prepared (created, seeded once), the egress allowlist and
// relays, and a freshly minted token bound to root's process tree (root may
// be the zero value when the caller mints its token some other way —
// internal/controller/agentsandbox.go's prepareAgentSandbox passes
// e.Skill == nil instead, so Mint is never reached on that path at all).
func (r *Resolver) PolicyForEntry(e Entry, tool string, launcherEnv []string, root skill.ProcessAnchor) (Policy, error) {
	if e.Tool != "" {
		tool = e.Tool
	}
	tool = CanonicalTool(tool)
	if e.Identity == "" {
		return Policy{}, fmt.Errorf("agentsandbox: workspace %s has no identity", e.Workspace)
	}
	r.maybeSweep()
	scope := e.Scope
	if r.Rescope != nil {
		// What the scope's config says now, not what it said when the
		// workspace was recorded.
		scope = r.Rescope(scope)
	}
	persist := r.Persist
	if scope != nil && scope.Persist != "" {
		persist = scope.Persist
	}
	homeRoot, homeKey := r.homeFor(e, persist)
	home, logins, err := PrepareHome(HomeSpec{
		Root: homeRoot, Key: homeKey, Tool: tool, RealHome: r.RealHome,
		GitName: r.GitName, GitEmail: r.GitEmail, GOOS: r.GOOS,
		ShareLogins: r.ModelCredential == CredLoginShare,
	})
	if err != nil {
		return Policy{}, err
	}
	env := append(append([]string(nil), r.Env...), launcherEnv...)
	hosts, loopback := ModelRouteFor(tool, e.Model, r.RealHome, env)
	egress := append([]string(nil), hosts...)
	for _, list := range [][]string{r.Egress, e.Egress} {
		for _, h := range list {
			egress = appendUnique(egress, h)
		}
	}
	if tool == "claude-code" && e.Dispatch != "" {
		// A dispatch's workspace, conductor's own: trusted in this home.
		if err := trustClaudeWorkspace(home, canon(e.Workspace)); err != nil {
			return Policy{}, fmt.Errorf("agentsandbox: trust the workspace in %s: %w", home, err)
		}
	}
	var modelHosts []string
	for _, h := range hosts {
		if !slices.Contains(r.Egress, h) && !slices.Contains(e.Egress, h) {
			modelHosts = append(modelHosts, h)
		}
	}
	p := Policy{
		ModelCredential: r.ModelCredential, ModelHosts: modelHosts, Detect: r.Detect, Limits: r.Limits,
		Identity: e.Identity, Tool: tool, Workspace: e.Workspace, Home: home, Logins: logins,
		Egress: egress, Loopback: loopback,
		ReadOnly: e.ReadOnly, ReadWrite: e.ReadWrite, Hide: e.Hide, Env: e.Env,
		GitCommon: e.GitCommon, GitWorktree: e.GitWorktree,
		PosixIPC: r.PosixIPC,
		Run:      e.Dispatch, EgressAnyBytes: r.EgressAnyBytes, EgressIdle: r.EgressIdle,
		EgressUpload: r.EgressUpload, EgressDownload: r.EgressDownload,
		ModelUpload: r.ModelUpload, ModelDownload: r.ModelDownload,
	}
	p.ModelRoute = hosts
	for _, h := range r.Inspect {
		p.Inspect = appendInspect(p.Inspect, h)
	}
	applyScope(&p, r.Allow, scope)
	if cache, err := r.persistCache(e, home, persist); err != nil {
		return Policy{}, fmt.Errorf("agentsandbox: identity cache: %w", err)
	} else if cache != "" {
		p.ReadWrite = append(append([]string(nil), p.ReadWrite...), cache)
	}
	if r.Socket != "" && e.Skill != nil && r.Mint != nil {
		id := *e.Skill
		if id.Sandbox == nil {
			// The token stands for this sandbox too: the browser keys on it.
			id.Sandbox = &skill.SandboxBinding{
				Identity: e.Identity, Workspace: e.Workspace, Egress: e.Egress,
				Deny: append(append([]string(nil), e.ReadOnly...), e.Hide...),
			}
		}
		tok, err := r.Mint(id, root)
		if err != nil {
			return Policy{}, fmt.Errorf("agentsandbox: mint token: %w", err)
		}
		if tok != "" {
			p.Socket, p.Token = r.Socket, tok
		}
	}
	return p, nil
}
