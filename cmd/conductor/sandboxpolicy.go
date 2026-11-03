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

// capAgentSlice caps every agent launch together (a var: tests never touch
// the user's systemd manager).
var capAgentSlice = agentsandbox.CapAgentSlice

// sandboxResolver is the daemon's policy builder for `conductor sandbox
// exec` (set at boot when the agent sandbox is on; nil = refused).
var sandboxResolver atomic.Pointer[agentsandbox.Resolver]

// sandboxPolicyResponse answers the control socket's sandbox-policy: the
// policy for the caller's cwd and a token minted for this launch alone. The
// socket is 0600 already; the caller's uid is checked too — a policy carries
// a live token.
func sandboxPolicyResponse(conn net.Conn, req controlRequest, log func(string, ...any)) controlResponse {
	uid, pid, credOK := agentsandbox.PeerCred(conn)
	if credOK && int(uid) != os.Getuid() {
		return controlResponse{Error: fmt.Sprintf("sandbox-policy: refused for uid %d", uid)}
	}
	r := sandboxResolver.Load()
	if r == nil {
		return controlResponse{Error: "the agent sandbox is off in this conductor (sandbox.agents: off)"}
	}
	// The minted token is bound to THIS connection's peer process tree (rt2
	// #12): that process — `conductor sandbox exec` — is what execs or forks
	// the sandboxed agent, so every descendant of it (the agent, the
	// `conductor call` subprocesses it spawns, an in-sandbox MCP tool
	// server) is authorized, and nothing outside that tree is, even at the
	// same uid. No peer pid (credOK false: a platform with no SO_PEERCRED) ⇒
	// a zero anchor ⇒ Mint refuses the token outright rather than minting
	// one nothing can ever present.
	var root skill.ProcessAnchor
	if credOK {
		root = skill.AnchorForPID(pid)
	}
	pol, err := r.PolicyFor(req.Cwd, req.Tool, req.Env, req.Scratch, root)
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
		Mint: func(id skill.Identity, root skill.ProcessAnchor) (string, error) {
			b := skill.Active()
			if b == nil {
				return "", nil
			}
			if root.Empty() {
				// No peer pid to anchor to (a platform with no SO_PEERCRED,
				// or a caller with no real connection — tests). Refusing to
				// mint here, rather than silently falling back to uid
				// binding, is rt2 #12's fix: a token this daemon cannot bind
				// to a process tree must not exist at all.
				return "", fmt.Errorf("sandbox token: no process to anchor it to")
			}
			return b.MintSession(id, root, sandboxTokenTTL)
		},
		GitName: name, GitEmail: email,
		Env:             os.Environ(),
		GOOS:            runtime.GOOS,
		PosixIPC:        cfg.Sandbox.PosixIPCAllowed(),
		EgressAnyBytes:  !cfg.Sandbox.EgressTLSRequired(),
		ModelCredential: cfg.Sandbox.ModelCredentialMode(),
		Detect: agentsandbox.DetectSpec{
			Action: cfg.Sandbox.DetectAction(), Interval: cfg.Sandbox.DetectInterval(),
			NoTripwires: !cfg.Sandbox.TripwiresOn(), Allow: cfg.Sandbox.DetectAllow(),
		},
		// A bot's home (or a hand-off worker) runs in a pinned paseo workspace
		// conductor never registers as a worktree, so `conductor sandbox exec`
		// would resolve it to a verb-less dir: identity. This lookup (published
		// by buildBots, which ran earlier) resolves such a cwd to the bot's own
		// identity and granted verbs. nil when no bots are configured.
		Session: botSessionLookup,
	}
	if cfg.Sandbox != nil && cfg.Sandbox.Limits != nil {
		l := cfg.Sandbox.Limits
		r.Limits = agentsandbox.LimitSpec{Off: l.Off, Memory: l.Memory, Pids: l.Pids, Required: l.Required}
	}
	r.Limits.DiskFreeFrac, r.Limits.DiskFreeBytes, r.Limits.DiskOff = cfg.Sandbox.DiskFreeMin()
	r.Persist = cfg.Sandbox.PersistMode()
	if !r.Limits.Off {
		// All agents together: the slice their launch scopes run in.
		if err := capAgentSlice(runtime.GOOS, cfg.Sandbox.AggregateMemory()); err != nil {
			logf("agent sandbox: limits: %v (each launch keeps its own cap)", err)
		}
	}
	idle, up, down := cfg.Sandbox.AgentEgressLimits()
	r.EgressIdle, r.EgressUpload, r.EgressDownload = int64(idle), up, down
	if err := os.MkdirAll(r.StateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("agent sandbox state dir %s: %w", r.StateRoot, err)
	}
	// What a workspace's git files may name: conductor's own checkouts (the
	// gitwt base clones in the state dir, paseo's clone cache), never the
	// home or conductor's state and config.
	agentsandbox.SharedGitDirect = cfg.Sandbox.GitSharedDirect()
	agentsandbox.GitTrust.Home = home
	agentsandbox.GitTrust.Forbidden = r.Protected
	agentsandbox.GitTrust.ObjectRoots = []string{filepath.Join(filepath.Dir(cfg.Store.StateFile), "checkouts"), filepath.Join(home, ".conductor", "checkouts")}
	// The workspace registry survives a restart: a workspace's layout is
	// the one recorded while it was conductor's, never re-read from it.
	if err := agentsandbox.Default.PersistTo(filepath.Join(r.StateRoot, ".workspaces.json")); err != nil {
		logf("agent sandbox: workspace registry: %v (starting empty)", err)
	}
	dispatch.SandboxPrefetch = nil
	if cfg.Sandbox.PrefetchGo() {
		// A dot name no identity home can have (IdentityKey trims it).
		gp, err := agentsandbox.NewGoPrefetch(filepath.Join(r.StateRoot, ".go-modcache"), os.Environ())
		if err != nil {
			return nil, err
		}
		// The download itself runs CONFINED: the same self-exe and
		// egress-proxy wiring every other local launch gets (codeSandboxDeps),
		// so a malicious go.mod/go.sum this box ever snapshots cannot read the
		// daemon's real HOME/credentials or reach a network host beyond
		// gp.Proxy's own. Fill fails closed (skips + logs) if this box cannot
		// realize the jail — it never falls back to an unconfined download.
		gp.Sandbox = codeSandboxDeps()
		gp.Warn = logf
		r.GoPrefetch = gp
		dispatch.SandboxPrefetch = func(dir string) {
			start := time.Now()
			if err := gp.Workspace(context.Background(), dir); err != nil {
				logf("sandbox: go prefetch for %s: %v (the agent's Go runs offline without what is missing)", dir, err)
				return
			}
			if d := time.Since(start); d > 5*time.Second {
				logf("sandbox: go prefetch for %s took %s", dir, d.Round(time.Second))
			}
		}
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
	sandboxResolver.Store(r)
	controller.AgentSandbox = &controller.SandboxWiring{
		Resolver: r, Relay: rel.Relay,
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
		sandboxResolver.Store(nil)
		controller.AgentSandbox = nil
		gitdiff.Confine = nil
		dispatch.PaseoSandboxed = false
		dispatch.PaseoSandboxLog = nil
		dispatch.SandboxPrefetch = nil
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
