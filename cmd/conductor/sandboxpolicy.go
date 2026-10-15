package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitdiff"
	"github.com/NodeSpy/conductor/internal/skill"
)

// sandboxResolver is the daemon's policy builder for `conductor sandbox
// exec` (set at boot when the agent sandbox is on; nil = refused).
var sandboxResolver atomic.Pointer[agentsandbox.Resolver]

// sandboxPolicyResponse answers the control socket's sandbox-policy: the
// policy for the caller's cwd and a token minted for this launch alone. The
// socket is 0600 already; the caller's uid is checked too — a policy carries
// a live token.
func sandboxPolicyResponse(conn net.Conn, req controlRequest, log func(string, ...any)) controlResponse {
	if uid, ok := agentsandbox.PeerUID(conn); ok && int(uid) != os.Getuid() {
		return controlResponse{Error: fmt.Sprintf("sandbox-policy: refused for uid %d", uid)}
	}
	r := sandboxResolver.Load()
	if r == nil {
		return controlResponse{Error: "the agent sandbox is off in this conductor (sandbox.agents: off)"}
	}
	pol, err := r.PolicyFor(req.Cwd, req.Tool, req.Env, req.Scratch)
	if err != nil {
		log("sandbox-policy for %s refused: %v", req.Cwd, err)
		return controlResponse{Error: err.Error()}
	}
	log("sandbox-policy: %s in %s (identity %s, egress %d host(s), %d relay(s))", pol.Tool, req.Cwd, pol.Identity, len(pol.Egress), len(pol.Loopback))
	return controlResponse{OK: true, Policy: &pol}
}

// sandboxTokenTTL bounds a `conductor sandbox exec` launch's token: a paseo
// session can run for hours, and each relaunch (resume) mints a new one.
const sandboxTokenTTL = 12 * time.Hour

// wireAgentSandbox turns the agent sandbox on for this daemon: the policy
// builder `conductor sandbox exec` asks (sandbox-policy), and the
// controller's wrap of every local cli/acp agent launch. cfgFile is the
// config path (its directory is never a workspace).
func wireAgentSandbox(cfg *config.Config, cfgFile string, logf func(string, ...any)) (func(), error) {
	if !cfg.AgentSandboxOn() {
		sandboxResolver.Store(nil)
		controller.AgentSandbox = nil
		dispatch.PaseoSandboxed = false
		logf("WARNING: sandbox.agents: off — agents run UNSANDBOXED, with this daemon's view of the machine and your credentials")
		return func() {}, nil
	}
	home, _ := os.UserHomeDir()
	var egress []string
	if cfg.Sandbox != nil {
		egress = cfg.Sandbox.Egress
	}
	name, email := gitGlobalIdentity()
	r := &agentsandbox.Resolver{
		StateRoot: cfg.SandboxStateDir(),
		RealHome:  home,
		Protected: []string{filepath.Dir(cfg.Store.StateFile), filepath.Dir(cfgFile), config.StateDir()},
		Egress:    egress,
		Socket:    strings.TrimPrefix(dispatch.LocalSkillEndpoint(), "unix://"),
		Mint: func(id skill.Identity) (string, error) {
			b := skill.Active()
			if b == nil {
				return "", nil
			}
			return b.MintSession(id, uint32(os.Getuid()), sandboxTokenTTL)
		},
		GitName: name, GitEmail: email,
		Env:  os.Environ(),
		GOOS: runtime.GOOS,
	}
	if err := os.MkdirAll(r.StateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("agent sandbox state dir %s: %w", r.StateRoot, err)
	}
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		return nil, err
	}
	sandboxResolver.Store(r)
	controller.AgentSandbox = &controller.SandboxWiring{Resolver: r, Relay: rel.Relay}
	// conductor's own reads of a sandboxed workspace (the diff preview) run
	// sandboxed too: the agent wrote that checkout's git config.
	gitdiff.Confine = controller.ConfinedGit
	// conductor's paseo dispatches launch through the sandboxed provider
	// entries (`conductor paseo setup`).
	dispatch.PaseoSandboxed = true
	if why := agentsandbox.Probe(); why != "" {
		logf("WARNING: the agent sandbox cannot be built on this box (%s) — every agent launch will be REFUSED until it can (or set sandbox.agents: off)", why)
	} else {
		logf("agent sandbox: on (identity homes in %s; conductor socket %q)", r.StateRoot, r.Socket)
	}
	return func() {
		sandboxResolver.Store(nil)
		controller.AgentSandbox = nil
		gitdiff.Confine = nil
		dispatch.PaseoSandboxed = false
		rel.Close()
	}, nil
}

// gitGlobalIdentity is the operator's git identity from their own (global)
// config: the identity a sandboxed agent's commits carry.
func gitGlobalIdentity() (name, email string) {
	get := func(k string) string {
		out, err := exec.Command("git", "config", "--global", "--get", k).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}
