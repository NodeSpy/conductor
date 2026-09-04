package controller

import (
	"context"
	"fmt"
	"net"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// HostArgvPrefix resolves the ssh argv prefix for a named `hosts:` entry:
// append(prefix, hosts.ShellJoin(argv)) (or the output of
// hosts.RemoteCommand/RemoteCommandEnv in place of ShellJoin(argv)) runs argv
// on that host. It is a package-level injectable seam, matching this
// package's existing DI pattern (cliLauncher, deckRunner, acpDialer,
// opencodeDialer): set once by cmd/conductor's wiring from the loaded
// config's `hosts:` block before any controller launches, and stubbed in
// tests. A nil value or an unknown host name is a launch-time error — there
// is no silent local fallback for a controller configured with `host:`.
var HostArgvPrefix func(hostName string) ([]string, error)

// resolveHost picks the host a controller launch runs on: the dispatched
// profile's host wins over the controller's own configured host (a per-agent
// override) — see AgentProfile.Host's doc comment. Only reachable where a
// Spec (and so a profile) is in scope, i.e. NewSession; ResumeSession has no
// profile to consult and uses the controller's host alone.
func resolveHost(controllerHost, profileHost string) string {
	if profileHost != "" {
		return profileHost
	}
	return controllerHost
}

// prepareLaunch adapts a local subprocess launch (argv, working directory,
// identity env) for a possibly-remote controller (cli/acp/agent-deck).
//
// host == "" is the common case: argv/dir are returned unchanged and remote
// is false — callers apply env locally exactly as before this feature
// existed (byte-for-byte the old behavior).
//
// host != "" resolves it via HostArgvPrefix and returns an ssh argv (the
// resolved prefix plus one already-quoted command string built by
// hosts.RemoteCommandEnv: cd <dir> && export K=V; ... && exec argv) with
// remote == true. In that case the caller must launch the returned argv with
// no local working directory and no local env: the original dir is
// meaningless to the local ssh process (it's a path on the remote box), and
// the identity env now rides the ssh channel embedded in the wrapped command
// string instead of the local process's environment table — see
// hosts.RemoteCommandEnv's security note. This is exactly why prepareLaunch
// returns "" for the directory rather than leaving that decision to callers.
func prepareLaunch(host, dir string, env, argv []string) (wrappedArgv []string, localDir string, remote bool, err error) {
	if host == "" {
		return argv, dir, false, nil
	}
	if HostArgvPrefix == nil {
		return nil, "", false, fmt.Errorf("controller: host %q configured but no host resolver is wired", host)
	}
	prefix, err := HostArgvPrefix(host)
	if err != nil {
		return nil, "", false, fmt.Errorf("controller: host %q: %w", host, err)
	}
	remoteCmd := hosts.RemoteCommandEnv(argv, dir, env)
	wrapped := make([]string, 0, len(prefix)+1)
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, remoteCmd)
	return wrapped, "", true, nil
}

// HostDial opens a net.Conn to addr AS SEEN FROM the named host (an
// `ssh -W` stdio forward — see hosts.Client.DialVia). The opencode
// controller's HTTP client uses it to reach a server the remote runtime
// bound to its own 127.0.0.1. Wired by cmd/conductor alongside
// HostArgvPrefix; nil or an unknown host is a launch-time error.
var HostDial func(ctx context.Context, hostName, addr string) (net.Conn, error)
