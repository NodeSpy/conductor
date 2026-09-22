package controller

import (
	"fmt"
	"os"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// EgressProxyFor resolves the loopback address of conductor's egress proxy
// enforcing exactly the given allowlist (empty/nil = deny everything),
// starting it on first use, plus a fresh per-dispatch client credential the
// proxy requires (#36 iso-review M9). A package-level injectable seam like
// HostArgvPrefix: wired once by cmd/conductor from a sandbox.ProxyManager
// (Endpoint), stubbed in tests. nil + a launch that needs an egress policy
// is a launch error — the policy fails closed, never silently unenforced.
var EgressProxyFor func(allow []string) (addr, cred string, revoke func(), err error)

// EgressProxyUnix resolves the UNIX-socket endpoint of the proxy enforcing
// the given allowlist — the daemon-side end of the ENFORCED egress path
// (deny+allowlist under namespace/container, #36 iso-review C1) — plus a
// fresh per-dispatch credential. Wired from sandbox.ProxyManager.UnixEndpoint;
// nil + an enforced-egress launch fails closed.
var EgressProxyUnix func(allow []string) (sock, cred string, revoke func(), err error)

// launchSelfExe resolves conductor's own binary (re-executed inside the
// sandbox as the forwarder); a var for tests.
var launchSelfExe = os.Executable

// DaemonMaskPaths are the daemon's own sensitive paths (state dir, config
// dir) hidden by DEFAULT inside every namespace-mode sandbox (#36 iso-review
// H7) — a namespace shares the daemon's uid, so without masking the agent
// could read the store, audit trail, and secrets env. `privileged: true` on
// the isolation block is the explicit opt-out. Wired once at boot by
// cmd/conductor.
var DaemonMaskPaths []string

// launchOpts carries the per-dispatch isolation decision to prepareLaunch.
type launchOpts struct {
	iso           *config.IsolationConfig
	agentAuthored bool
	// onEgressCred, if set, receives a revoke func for each per-dispatch egress
	// credential minted for this launch. The caller wires it to the launch's
	// own session end (ctx cancel / cleanup func) so the credential does not
	// outlive the dispatch on the host-wide loopback proxy (#36 iso-review
	// round 2, item 4). nil = no revocation collected (tests / no-egress paths).
	onEgressCred func(revoke func())
}

// withEgressRevoke installs an onEgressCred sink on opt and returns it
// alongside a revoke func that retires every per-dispatch egress credential
// minted for this launch, exactly once. Callers wire the returned revoke to
// their own session end — a ctx cancel for ctx-scoped launches, or the
// cleanup func for background sessions — so a credential does not outlive the
// dispatch on conductor's host-wide loopback proxy (#36 iso-review round 2,
// item 4). Safe to call unconditionally: with no egress policy nothing is
// collected and revoke is a no-op.
func withEgressRevoke(opt launchOpts) (launchOpts, func()) {
	var mu sync.Mutex
	var revokes []func()
	opt.onEgressCred = func(rev func()) {
		mu.Lock()
		revokes = append(revokes, rev)
		mu.Unlock()
	}
	var once sync.Once
	return opt, func() {
		once.Do(func() {
			mu.Lock()
			rs := revokes
			revokes = nil
			mu.Unlock()
			for _, r := range rs {
				r()
			}
		})
	}
}

// launchOptsFor resolves the isolation for one dispatch: the profile's own
// isolation: wins over the runtime's (most-specific wins, like `host:`).
func launchOptsFor(runtimeIso *config.IsolationConfig, req dispatch.Request) launchOpts {
	iso := runtimeIso
	if req.Step.Isolation != nil {
		iso = req.Step.Isolation
	}
	return launchOpts{iso: iso, agentAuthored: req.AgentAuthored}
}

// resume returns the opts a profile-less relaunch (ResumeSession, session
// follow-ups) runs under: the runtime's own isolation, never weaker, plus
// the ORIGINAL dispatch's agent-authored provenance replayed from the
// persisted session ref (#36 iso-review H5) — a resumed agent-authored
// session keeps its deny-by-default egress across restarts.
func resumeOpts(runtimeIso *config.IsolationConfig, agentAuthored bool) launchOpts {
	return launchOpts{iso: runtimeIso, agentAuthored: agentAuthored}
}

// prepareLaunch adapts a local subprocess launch (argv, working directory,
// identity env) for a possibly-remote, possibly-isolated controller
// (cli/acp/opencode/agent-deck).
//
// Local (host == ""): the returned argv is the (possibly sandbox-wrapped)
// invocation, localDir the working directory, and localEnv the environment
// the caller must apply — the input env plus, when an egress policy governs
// this launch, the HTTP(S)_PROXY variables routing traffic through
// conductor's egress proxy (#36 §15). The egress policy is the isolation's
// own network: block; an agent-authored dispatch (#36 §11) with no explicit
// network policy gets the deny-all proxy — deny by default.
//
// Remote (host != ""): the argv is an ssh launch (HostArgvPrefix + one
// already-quoted remote command carrying dir and env — see
// hosts.RemoteCommandEnv), wrapped in the isolation's remote prefix when one
// is configured. localDir and localEnv return empty: the original dir is a
// remote path and the env rides the ssh channel inside the command string.
// The egress proxy is loopback-only and never applies to a remote launch
// (config validation rejects a remote egress allowlist; the remote box's own
// `hosts:` isolation is the wall there).
func prepareLaunch(host, dir string, env, argv []string, opt launchOpts) (wrappedArgv []string, localDir string, localEnv []string, remote bool, err error) {
	spec := sandbox.FromConfig(opt.iso)

	if host == "" {
		wrapSpec := spec
		if _, useProxy := spec.ProxyPolicy(); !useProxy && opt.agentAuthored && (spec == nil || !spec.Deny) {
			// Deny-by-default: an agent-authored dispatch with no explicit
			// network policy routes through the deny-all proxy (audited). A
			// synthetic spec carrying HasEgress with an empty allowlist makes
			// WrapLocalCommand's own ProxyPolicy() call resolve to exactly
			// that — an empty-allowlist advisory proxy — while every other
			// field (Mode, Privileged, …) passes through unchanged so the
			// rest of the wrap (namespace, masks, …) behaves exactly as the
			// operator configured it.
			derived := &sandbox.Spec{}
			if spec != nil {
				cp := *spec
				derived = &cp
			}
			derived.HasEgress, derived.Egress = true, nil
			wrapSpec = derived
		}
		deps := sandbox.LocalWrapDeps{
			SelfExe:    launchSelfExe,
			MaskPaths:  DaemonMaskPaths,
			EgressAddr: EgressProxyFor,
			EgressUnix: EgressProxyUnix,
		}
		wrapped, wrappedEnv, cleanup, werr := sandbox.WrapLocalCommand(wrapSpec, argv, dir, env, deps)
		if werr != nil {
			return nil, "", nil, false, werr
		}
		if opt.onEgressCred != nil {
			opt.onEgressCred(cleanup)
		}
		return wrapped, dir, wrappedEnv, false, nil
	}

	if HostArgvPrefix == nil {
		return nil, "", nil, false, fmt.Errorf("controller: host %q configured but no host resolver is wired", host)
	}
	prefix, err := HostArgvPrefix(host)
	if err != nil {
		return nil, "", nil, false, fmt.Errorf("controller: host %q: %w", host, err)
	}
	remoteCmd := hosts.RemoteCommandEnv(argv, dir, env)
	if remoteCmd, err = spec.WrapRemote(remoteCmd); err != nil {
		return nil, "", nil, false, err
	}
	wrapped := make([]string, 0, len(prefix)+1)
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, remoteCmd)
	return wrapped, "", nil, true, nil
}
