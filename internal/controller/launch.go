package controller

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// EgressProxyFor resolves the loopback address of conductor's egress proxy
// enforcing exactly the given allowlist (empty/nil = deny everything),
// starting it on first use. A package-level injectable seam like
// HostArgvPrefix: wired once by cmd/conductor from a sandbox.ProxyManager,
// stubbed in tests. nil + a launch that needs an egress policy is a launch
// error — the policy fails closed, never silently unenforced.
var EgressProxyFor func(allow []string) (string, error)

// launchGOOS / launchLookPath feed sandbox.Spec.Check's platform probe —
// package vars so tests can exercise the isolation paths without the wrapper
// binaries (sudo/unshare/docker) installed.
var (
	launchGOOS     = runtime.GOOS
	launchLookPath = exec.LookPath
)

// launchOpts carries the per-dispatch isolation decision to prepareLaunch.
type launchOpts struct {
	iso           *config.IsolationConfig
	agentAuthored bool
}

// launchOptsFor resolves the isolation for one dispatch: the profile's own
// isolation: wins over the runtime's (most-specific wins, like `host:`).
func launchOptsFor(runtimeIso *config.IsolationConfig, req dispatch.Request) launchOpts {
	iso := runtimeIso
	if req.Profile.Isolation != nil {
		iso = req.Profile.Isolation
	}
	return launchOpts{iso: iso, agentAuthored: req.AgentAuthored}
}

// resume returns the opts a profile-less relaunch (ResumeSession, session
// follow-ups) runs under: the runtime's own isolation, never weaker — but
// with no request in scope the agent-authored flag cannot be re-derived, so
// callers that kept the original opts should reuse those instead.
func resumeOpts(runtimeIso *config.IsolationConfig) launchOpts {
	return launchOpts{iso: runtimeIso}
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
	allow, useProxy := spec.ProxyPolicy()
	if !useProxy && opt.agentAuthored && (spec == nil || !spec.Deny) {
		// Deny-by-default: an agent-authored dispatch with no explicit
		// network policy routes through the deny-all proxy (audited).
		allow, useProxy = nil, true
	}

	if host == "" {
		if useProxy {
			if EgressProxyFor == nil {
				return nil, "", nil, false, fmt.Errorf("controller: launch needs an egress proxy (isolation network policy, or agent-authored deny-by-default) but none is wired")
			}
			addr, perr := EgressProxyFor(allow)
			if perr != nil {
				return nil, "", nil, false, fmt.Errorf("controller: egress proxy: %w", perr)
			}
			env = append(append([]string(nil), env...), sandbox.ProxyEnv(addr)...)
		}
		if err := spec.Check(launchGOOS, launchLookPath); err != nil {
			return nil, "", nil, false, err
		}
		wrapped, werr := spec.WrapLocal(argv, dir, envKeys(env))
		if werr != nil {
			return nil, "", nil, false, werr
		}
		return wrapped, dir, env, false, nil
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

// envKeys extracts the KEY halves of KEY=VALUE pairs (for the container
// mode's `-e KEY` pass-through).
func envKeys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
			out = append(out, k)
		}
	}
	return out
}
