package agentsandbox

import (
	"fmt"
	"os"
	"path/filepath"

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
	// Mint registers a fresh session token for an identity ("" + nil error
	// when tokens are not available here).
	Mint func(id skill.Identity) (string, error)
	// GitName / GitEmail are the operator's git identity for the agent's
	// commits.
	GitName, GitEmail string
	// Env is the daemon's own environment, for configured model endpoints.
	Env []string
	// GOOS is the platform the launch runs on.
	GOOS string
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
func (r *Resolver) PolicyFor(cwd, tool string, launcherEnv []string, scratchOK bool) (Policy, error) {
	if !filepath.IsAbs(cwd) {
		return Policy{}, fmt.Errorf("agentsandbox: cwd %q is not absolute", cwd)
	}
	if e, ok := r.registry().Lookup(cwd); ok {
		p, err := r.PolicyForEntry(e, tool, launcherEnv)
		// The agent starts where it was started, inside its workspace.
		p.Cwd = canon(cwd)
		return p, err
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
		p, perr := r.PolicyForEntry(e, tool, launcherEnv)
		p.Note = fmt.Sprintf("%v — running in the %s scratch workspace %s instead", err, e.Identity, e.Workspace)
		return p, perr
	}
	return r.PolicyForEntry(DefaultEntry(ws), tool, launcherEnv)
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
	return Entry{Workspace: canon(ws), Identity: id, Skill: &skill.Identity{Agent: "sandbox:scratch"}}, nil
}

// DefaultEntry is the entry for a directory conductor did not create: the
// directory is the identity, its own tool state, and a token that
// authenticates it to conductor without granting any verb.
func DefaultEntry(ws string) Entry {
	return Entry{
		Workspace: ws,
		Identity:  "dir:" + ws,
		Skill:     &skill.Identity{Agent: "sandbox:" + filepath.Base(ws)},
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
// relays, and a freshly minted token.
func (r *Resolver) PolicyForEntry(e Entry, tool string, launcherEnv []string) (Policy, error) {
	if e.Tool != "" {
		tool = e.Tool
	}
	tool = CanonicalTool(tool)
	if e.Identity == "" {
		return Policy{}, fmt.Errorf("agentsandbox: workspace %s has no identity", e.Workspace)
	}
	home, logins, err := PrepareHome(HomeSpec{
		Root: r.StateRoot, Key: e.Identity, Tool: tool, RealHome: r.RealHome,
		GitName: r.GitName, GitEmail: r.GitEmail, GOOS: r.GOOS,
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
	p := Policy{
		Identity: e.Identity, Tool: tool, Workspace: e.Workspace, Home: home, Logins: logins,
		Egress: egress, Loopback: loopback,
		ReadOnly: e.ReadOnly, ReadWrite: e.ReadWrite, Hide: e.Hide, Env: e.Env,
	}
	if r.Socket != "" && e.Skill != nil && r.Mint != nil {
		tok, err := r.Mint(*e.Skill)
		if err != nil {
			return Policy{}, fmt.Errorf("agentsandbox: mint token: %w", err)
		}
		if tok != "" {
			p.Socket, p.Token = r.Socket, tok
		}
	}
	return p, nil
}
