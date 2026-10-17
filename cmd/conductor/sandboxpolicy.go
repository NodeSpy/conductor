package main

import (
	"context"
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
//
// A container policy is prepared here — the identity's container created or
// started, this launch's proxy listener, relays and socket wired into it —
// and release (non-nil then) ends the launch's share: the caller holds it
// until the `sandbox exec` closes the connection.
func sandboxPolicyResponse(ctx context.Context, conn net.Conn, req controlRequest, log func(string, ...any)) (controlResponse, func()) {
	if uid, ok := agentsandbox.PeerUID(conn); ok && int(uid) != os.Getuid() {
		return controlResponse{Error: fmt.Sprintf("sandbox-policy: refused for uid %d", uid)}, nil
	}
	r := sandboxResolver.Load()
	if r == nil {
		return controlResponse{Error: "the agent sandbox is off in this conductor (sandbox.agents: off)"}, nil
	}
	pol, err := r.PolicyFor(req.Cwd, req.Tool, req.Env, req.Scratch)
	if err != nil {
		log("sandbox-policy for %s refused: %v", req.Cwd, err)
		return controlResponse{Error: err.Error()}, nil
	}
	var release func()
	if pol.Container != nil {
		w := controller.AgentSandbox
		if w == nil || w.Containers == nil {
			return controlResponse{Error: "this conductor has no container backend"}, nil
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		release, err = w.Containers.Prepare(pctx, &pol)
		cancel()
		if err != nil {
			log("sandbox-policy for %s refused: %v", req.Cwd, err)
			return controlResponse{Error: err.Error()}, nil
		}
		log("sandbox-policy: %s in %s (identity %s, container %s, egress %d host(s), %d relay(s))", pol.Tool, req.Cwd, pol.Identity, pol.Container.Session.Name, len(pol.Egress), len(pol.Loopback))
		return controlResponse{OK: true, Policy: &pol}, release
	}
	log("sandbox-policy: %s in %s (identity %s, egress %d host(s), %d relay(s))", pol.Tool, req.Cwd, pol.Identity, len(pol.Egress), len(pol.Loopback))
	return controlResponse{OK: true, Policy: &pol}, nil
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
	if p := cfg.Sandbox; p != nil && p.Push != nil {
		// No global git identity on this box: the identity conductor signs
		// as is the one the agent commits as.
		if name == "" {
			name = p.Push.Name
		}
		if email == "" {
			email = p.Push.Email
		}
	}
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
	if s := cfg.Sandbox; s != nil && s.Backend == "container" {
		// Every agent launch with no isolation: of its own — a `sandbox exec`
		// session in any directory included — runs in a container.
		r.Container = &agentsandbox.Container{Persistent: s.Container != nil && s.Container.Persistent}
		if s.Container != nil {
			r.Container.Engine = s.Container.Engine
		}
	}
	if err := os.MkdirAll(r.StateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("agent sandbox state dir %s: %w", r.StateRoot, err)
	}
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		rel.Close()
		return nil, fmt.Errorf("agent sandbox: resolve conductor's own binary: %w", err)
	}
	containers := agentContainers(cfg, r.StateRoot, logf)
	agentsandbox.SetDaemonContainers(containers)
	sandboxResolver.Store(r)
	controller.AgentSandbox = &controller.SandboxWiring{
		Resolver: r, Relay: rel.Relay, Containers: containers,
		// What an agent-deck session's wrapper runs: this daemon's policy.
		Exec: []string{self, "sandbox", "exec", "--socket", controlSockPath(cfg)},
	}
	// conductor's own reads of a sandboxed workspace (the diff preview) run
	// sandboxed too: the agent wrote that checkout's git config.
	gitdiff.Confine = controller.ConfinedGit
	// conductor's paseo dispatches launch through the sandboxed provider
	// entries (`conductor paseo setup`).
	dispatch.PaseoSandboxed = true
	dispatch.PaseoSandboxLog = logf
	if why := agentsandbox.Probe(); why != "" {
		logf("WARNING: the agent sandbox cannot be built on this box (%s) — every agent launch will be REFUSED until it can (or set sandbox.agents: off)", why)
	} else {
		logf("agent sandbox: on (identity homes in %s; conductor socket %q)", r.StateRoot, r.Socket)
	}
	return func() {
		agentsandbox.SetDaemonContainers(nil)
		containers.Close()
		sandboxResolver.Store(nil)
		controller.AgentSandbox = nil
		gitdiff.Confine = nil
		dispatch.PaseoSandboxed = false
		dispatch.PaseoSandboxLog = nil
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
